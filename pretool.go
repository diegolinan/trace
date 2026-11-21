package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// shellFunc runs one check command in dir with env added to the process's own.
type shellFunc func(ctx context.Context, dir, cmd string, env ...string) (string, error)

// actionTools are the Claude Code tools that change something, where PreToolUse listens by default (SPEC C5).
const actionTools = "Bash|Edit|Write|MultiEdit|NotebookEdit"

// checksWhen is the checks that run before the tool call (before) or after it.
func checksWhen(cs []check, before bool) []check {
	var out []check
	for _, c := range cs {
		if c.Before == before {
			out = append(out, c)
		}
	}
	return out
}

// preMatcher is PreToolUse's matcher: the tools that change something and those the checks before name.
func preMatcher(cs []check) string {
	if m := checkMatcher(checksWhen(cs, true)); m != "" {
		if m == "*" {
			return m
		}
		return actionTools + "|" + m
	}
	return actionTools
}

// preToolHook answers Claude Code's PreToolUse (SPEC C3, D11): a failing check before the call denies it; otherwise the
// rules about what the call touches, and any check that could not judge, reach the agent as context.
func preToolHook(in hookInput, role, dbPath string, cwdIn bool, w io.Writer) error {
	store := &lazyStore{path: dbPath}
	defer store.close()
	var denied, notes []string
	if cs := checksWhen(cfg.Checks, true); cwdIn && anyCheckApplies(in, cs) {
		denied, notes = checkFindings(store.open(), in, cs, runShell)
	}
	if about := rulesAbout(store.open(), toolTarget(in.ToolInput), role, in.Cwd); about != "" {
		notes = append(notes, about)
	}
	if len(denied) > 0 {
		return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": clip(strings.Join(append(denied, notes...), "\n\n"), 9500)}})
	}
	return contextOut(w, "PreToolUse", clip(strings.Join(notes, "\n\n"), 9500))
}

// rulesAbout is each record in force whose -about matches target, in the block's line, or "" (SPEC D11).
func rulesAbout(db *sql.DB, target, role, cwd string) string {
	if db == nil || target == "" {
		return ""
	}
	recs, err := ruleRecords(db, role, cwd)
	if err != nil {
		return ""
	}
	var lines []string
	for _, r := range recs {
		if re, err := regexp.Compile("(?i)" + r.about); r.about != "" && err == nil && re.MatchString(target) {
			lines = append(lines, ruleLine(db, r))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("What %s said about what you are about to do (recorded rules and decisions):\n%s", userName(), strings.Join(lines, "\n"))
}
