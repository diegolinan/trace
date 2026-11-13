package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// check is one project rule a command can verify. After a tool call whose name matches On and whose file path or
// command matches Match, TRACE runs Run in the session's folder; a non-zero exit is a violation, and its output is
// shown to the agent at once. A rule in context is still broken when nothing checks it (queue Q50, 2026-11-13: B
// wrote files the contract forbids with the contract loaded), so the checks come first and their log is the baseline.
type check struct {
	Name  string `json:"name"`
	On    string `json:"on"`    // tool-name regexp, as in Claude Code's hook matchers; empty: every tool
	Match string `json:"match"` // regexp on the tool's file path or command; empty: any
	Run   string `json:"run"`   // shell command
}

const checkTimeout = 45 * time.Second

// checkMatcher is the PostToolUse matcher for the configured checks: empty when there are none, so no hook is
// installed and no tool call starts a process.
func checkMatcher(cs []check) string {
	var ons []string
	for _, c := range cs {
		if c.On == "" {
			return "*"
		}
		ons = append(ons, c.On)
	}
	return strings.Join(ons, "|")
}

// toolTarget is what Match is tested against: the file a tool wrote or the command it ran.
func toolTarget(raw json.RawMessage) string {
	var in map[string]any
	json.Unmarshal(raw, &in)
	var parts []string
	for _, k := range []string{"file_path", "path", "notebook_path", "command"} {
		if s, ok := in[k].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// runChecks runs every check that matches this tool call, logs each run, and returns the text the agent sees:
// empty when all passed. A check whose patterns do not compile is reported, never skipped silently.
func runChecks(db *sql.DB, in hookInput, cs []check, shell func(dir, cmd string) (string, error)) string {
	target := toolTarget(in.ToolInput)
	var out []string
	for _, c := range cs {
		on, err1 := regexp.Compile(`^(?:` + c.On + `)$`)
		match, err2 := regexp.Compile(c.Match)
		if err1 != nil || err2 != nil {
			out = append(out, fmt.Sprintf("TRACE check %q is misconfigured: %v %v", c.Name, err1, err2))
			continue
		}
		if c.On != "" && !on.MatchString(in.ToolName) || !match.MatchString(target) {
			continue
		}
		text, err := shell(in.Cwd, c.Run)
		ok := err == nil
		if db != nil {
			db.Exec("insert into check_run(ts, session_id, name, tool, target, ok, output) values (?, ?, ?, ?, ?, ?, ?)",
				time.Now().UTC().Format(time.RFC3339), in.SessionID, c.Name, in.ToolName, target, ok, clip(text, 4000))
		}
		if !ok {
			out = append(out, fmt.Sprintf("TRACE check %q failed after %s (%v). Fix this before going on:\n%s", c.Name, in.ToolName, err, clip(strings.TrimSpace(text), 2000)))
		}
	}
	// Claude Code swaps hook text over 10,000 characters for a file path and a preview the agent may never open.
	return clip(strings.Join(out, "\n\n"), 9000)
}

// runShell runs one check command in dir, with the platform's shell and a time limit.
func runShell(dir, command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// anyCheckApplies says whether a check would run (or report itself misconfigured) for this tool call, so the common
// call opens no database and starts no process.
func anyCheckApplies(in hookInput, cs []check) bool {
	target := toolTarget(in.ToolInput)
	for _, c := range cs {
		on, err1 := regexp.Compile(`^(?:` + c.On + `)$`)
		match, err2 := regexp.Compile(c.Match)
		if err1 != nil || err2 != nil || (c.On == "" || on.MatchString(in.ToolName)) && match.MatchString(target) {
			return true
		}
	}
	return false
}

// ruleHook answers PostToolUse and UserPromptSubmit: the failed checks first, then the rule card when it is due
// (reinject.go), as one output under Claude Code's 10,000-character cap. Checks run from the root of the session's
// git checkout, so a project's command works after the agent cd's into a subfolder.
func ruleHook(in hookInput, role, dbPath string, w io.Writer) error {
	var db *sql.DB
	open := func() *sql.DB {
		if db == nil {
			d, err := openDB(dbPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, "trace: running without the store:", err)
				return nil
			}
			db = d
		}
		return db
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	var parts []string
	if in.Event == "PostToolUse" && anyCheckApplies(in, cfg.Checks) {
		ci := in
		if root, err := exec.Command("git", "-C", in.Cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
			ci.Cwd = strings.TrimSpace(string(root))
		}
		if text := runChecks(open(), ci, cfg.Checks, runShell); text != "" {
			parts = append(parts, text)
		}
	}
	// Subagents get no card: their payload names the parent's transcript, so its size would be the parent's.
	if cfg.Reinject != nil && in.AgentID == "" {
		if ctx := contextTokens(in.Transcript); ctx > 0 {
			if d := open(); d != nil {
				text, _ := sessionCard(d, in.Cwd, role)
				out, _, err := reinjection(d, in.SessionID, ctx, text, cfg.Reinject.factor(), time.Now())
				if err != nil {
					fmt.Fprintln(os.Stderr, "trace: re-injection:", err)
				}
				if out != "" {
					parts = append(parts, out)
				}
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": in.Event, "additionalContext": clip(strings.Join(parts, "\n\n"), 9500)}})
}
