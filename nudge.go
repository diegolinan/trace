package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// commitRe is a command that runs git commit, git -C <dir> commit included (SPEC T23). TRACE reads the text; it runs
// no git (G1).
var commitRe = regexp.MustCompile(`\bgit(\s+-C\s+("[^"]*"|'[^']*'|\S+))?\s+commit\b`)

// commitNudge counts the session's commits and, from the second while no task is in progress, tells the agent to put
// the work on the board (SPEC T23). It is "" for anything else, a subagent's call, or no store.
func commitNudge(db *sql.DB, in hookInput) string {
	if db == nil || in.ToolName != "Bash" || in.AgentID != "" || in.SessionID == "" || !commitRe.MatchString(toolTarget(in.ToolInput)) {
		return ""
	}
	var n int
	if err := db.QueryRow(`insert into commit_count(session_id, commits) values (?, 1)
		on conflict(session_id) do update set commits = commits + 1 returning commits`, in.SessionID).Scan(&n); err != nil || n < 2 {
		return ""
	}
	var open int
	db.QueryRow("select count(*) from task where closed_at is null and started_at is not null").Scan(&open)
	if open > 0 {
		return ""
	}
	return fmt.Sprintf("TRACE: this session has made %d commits with no task in progress on the board. If this work has several steps or will outlast the conversation, add it (%s) and run %s; if it ends here, go on.",
		n, strings.TrimSpace(traceCmd("task", ` add -text "<what>"`)), strings.TrimSpace(traceCmd("task", " start <id>")))
}

// commitLine is the hook's part of SPEC T23: a Claude Code session's commit only, so no other call opens the store.
func commitLine(in hookInput, host string, store *lazyStore) string {
	if in.Event != "PostToolUse" || host != "claude-code" || in.ToolName != "Bash" || !commitRe.MatchString(toolTarget(in.ToolInput)) {
		return ""
	}
	return commitNudge(store.open(), in)
}

// claudeHooks are the optional Claude Code hooks: what checks and the rule card need, and always PostToolUse on Bash
// for the commit count (SPEC T23). Codex keeps optionalHooks alone.
func claudeHooks(c config) map[string]string {
	m := optionalHooks(c)
	switch mt := m["PostToolUse"]; {
	case mt == "":
		m["PostToolUse"] = "Bash"
	case mt != "*" && !regexp.MustCompile(`(^|\|)Bash($|\|)`).MatchString(mt):
		m["PostToolUse"] = mt + "|Bash"
	}
	return m
}
