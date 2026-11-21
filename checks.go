package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
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
	Run    string `json:"run"`    // shell command
	Before bool   `json:"before"` // run before the tool call and stop it on failure (SPEC C3), not after
}

// applies says whether the check follows this tool call: On matches the tool's name (empty: every tool) and Match
// the file it wrote or the command it ran. A pattern that does not compile is the error; callers then ignore ok.
func (c check) applies(tool, target string) (ok bool, err error) {
	on, err := regexp.Compile(`^(?:` + c.On + `)$`)
	if err != nil {
		return
	}
	match, err := regexp.Compile(c.Match)
	if err != nil {
		return
	}
	return (c.On == "" || on.MatchString(tool)) && match.MatchString(target), nil
}

// checkLimit is the time all the checks of one tool call share, under the hook's 60 seconds (SPEC C2).
var checkLimit = 45 * time.Second

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
func runChecks(db *sql.DB, in hookInput, cs []check, shell shellFunc) string {
	failed, notes := checkFindings(db, in, cs, shell)
	// Claude Code swaps hook text over 10,000 characters for a file path and a preview the agent may never open.
	return clip(strings.Join(append(notes, failed...), "\n\n"), 9000)
}

// checkFindings runs every check that matches this tool call and logs each run: failed are the checks that failed,
// notes those that could not judge (misconfigured, or out of time), which never stop a call (SPEC C3).
func checkFindings(db *sql.DB, in hookInput, cs []check, shell shellFunc) (failed, notes []string) {
	target := toolTarget(in.ToolInput)
	ctx, cancel := context.WithTimeout(context.Background(), checkLimit)
	defer cancel()
	for _, c := range cs {
		applies, err := c.applies(in.ToolName, target)
		if err != nil {
			notes = append(notes, fmt.Sprintf("TRACE check %q is misconfigured: %v", c.Name, err))
			continue
		}
		if !applies {
			continue
		}
		if ctx.Err() != nil {
			notes = append(notes, fmt.Sprintf("TRACE check %q was not run: the checks' %s for this tool call ran out.", c.Name, checkLimit))
			continue
		}
		if text, cut := runCheck(ctx, db, in, c, target, shell); cut {
			notes = append(notes, fmt.Sprintf("TRACE check %q was not run: the checks' %s for this tool call ran out.", c.Name, checkLimit))
		} else if text != "" {
			failed = append(failed, text)
		}
	}
	return failed, notes
}

// runCheck runs one check and logs it: the failure text, or "" when it passed; cut when a check before the call was
// stopped by the limit, which gives no verdict, so the call goes on.
func runCheck(ctx context.Context, db *sql.DB, in hookInput, c check, target string, shell shellFunc) (failure string, cut bool) {
	text, err := shell(ctx, in.Cwd, c.Run, "TRACE_TOOL="+in.ToolName, "TRACE_TARGET="+target) // SPEC C4
	if db != nil {
		db.Exec("insert into check_run(ts, session_id, name, tool, target, ok, output) values (?, ?, ?, ?, ?, ?, ?)",
			time.Now().UTC().Format(time.RFC3339), in.SessionID, c.Name, in.ToolName, redacted(target), err == nil, redacted(clip(text, 4000)))
	}
	switch {
	case err == nil:
		return "", false
	case c.Before && ctx.Err() != nil:
		return "", true
	}
	return checkFailure(c, in.ToolName, err, text), false
}

// checkFailure is what the agent reads about a failed check: after the call, fix it; before, the call did not run.
func checkFailure(c check, tool string, err error, text string) string {
	how := fmt.Sprintf("failed after %s (%v). Fix this before going on", tool, err)
	if c.Before {
		how = fmt.Sprintf("failed, so %s did not run (%v). Do what it asks, or ask the user", tool, err)
	}
	return fmt.Sprintf("TRACE check %q %s:\n%s", c.Name, how, clip(strings.TrimSpace(text), 2000))
}

// runShell runs one check command in dir with the platform's shell, until ctx ends. Then shellCommand's Cancel
// ends every process the command started; WaitDelay stops the wait for a pipe a stray child still holds.
func runShell(ctx context.Context, dir, command string, env ...string) (string, error) {
	cmd := shellCommand(ctx, command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 2 * time.Second
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// anyCheckApplies says whether a check would run (or report itself misconfigured) for this tool call, so the common
// call opens no database and starts no process.
func anyCheckApplies(in hookInput, cs []check) bool {
	target := toolTarget(in.ToolInput)
	for _, c := range cs {
		if ok, err := c.applies(in.ToolName, target); ok || err != nil {
			return true
		}
	}
	return false
}

// ruleHook answers PostToolUse and UserPromptSubmit: the tasks due (a prompt only), the failed checks, then the rule
// card when it is due (reinject.go), as one output under Claude Code's 10,000-character cap. Checks run in the session's
// own folder.
func ruleHook(in hookInput, role, host, dbPath string, checks bool, w io.Writer) error {
	store := &lazyStore{path: dbPath}
	defer store.close()
	var parts []string
	if text := dueTasks(in, host, store); text != "" {
		parts = append(parts, text)
	}
	if after := checksWhen(cfg.Checks, false); checks && in.Event == "PostToolUse" && anyCheckApplies(in, after) {
		// In the session's own folder (SPEC G6), not a repository root above it.
		if text := runChecks(store.open(), in, after, runShell); text != "" {
			parts = append(parts, text)
		}
	}
	if text := promptNet(in, store); text != "" {
		parts = append(parts, text)
	}
	if text := dueCard(in, role, host, store); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return nil
	}
	return contextOut(w, in.Event, clip(strings.Join(parts, "\n\n"), 9500))
}

// dueCard is the rule card when enough new context has come in since it was last shown, else "". Subagents get no
// card: their payload names the parent's transcript, so its size would be the parent's.
func dueCard(in hookInput, role, host string, store *lazyStore) string {
	if cfg.Reinject == nil || in.AgentID != "" {
		return ""
	}
	ctx := contextTokens(in.Transcript, host)
	if ctx <= 0 {
		return ""
	}
	d := store.open()
	if d == nil {
		return ""
	}
	text, _ := sessionCard(d, in.Cwd, in.Transcript, role, host)
	out, _, err := reinjection(d, in.SessionID, ctx, text, reinjectFactor(), time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace: re-injection:", err)
	}
	return out
}

// lazyStore opens the store the first time it is needed, so a tool call that needs nothing opens nothing; nil when
// it cannot be opened (the hook then runs without it).
type lazyStore struct {
	path string
	db   *sql.DB
}

func (s *lazyStore) open() *sql.DB {
	if s.db == nil {
		d, err := openDB(s.path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "trace: running without the store:", err)
			return nil
		}
		s.db = d
	}
	return s.db
}

func (s *lazyStore) close() {
	if s.db != nil {
		s.db.Close()
	}
}
