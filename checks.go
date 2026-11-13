package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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

// checkHook answers PostToolUse: the failed checks, as context for the agent's next step. Checks run from the root
// of the session's git checkout, so a project's command works after the agent cd's into a subfolder.
func checkHook(db *sql.DB, in hookInput, w io.Writer) error {
	if root, err := exec.Command("git", "-C", in.Cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		in.Cwd = strings.TrimSpace(string(root))
	}
	text := runChecks(db, in, cfg.Checks, runShell)
	if text == "" {
		return nil
	}
	return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PostToolUse", "additionalContext": text}})
}
