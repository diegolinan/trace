package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC K2: a hook call that writes to the agent logs its characters; stats shows them per event, in total and since
// the session's last compaction.
func TestStatsShowsTraceContext(t *testing.T) {
	root := ruleRoot(t)
	payload, _ := json.Marshal(map[string]string{"hook_event_name": "SubagentStart", "session_id": "s1", "cwd": root,
		"transcript_path": filepath.Join(root, "claude", "projects", "C--work-acme", "s1.jsonl")})
	out, errOut, code := runTrace(t, root, string(payload), nil, "hook")
	if code != 0 || !strings.Contains(out, "helper agent") {
		t.Fatalf("hook: exit %d %q %q", code, out, errOut)
	}
	db := tkStore(t, root)
	if got := mutStr(t, db, `select event || ' ' || (chars = ?) from hook_out where session_id = 's1'`, len([]rune(out))); got != "SubagentStart 1" {
		t.Fatalf("logged %q", got)
	}
	for i, r := range []struct {
		event string
		chars int
	}{{"SessionStart:startup", 1000}, {"PostToolUse", 500}, {"SessionStart:compact", 300}, {"PostToolUse", 200}} {
		rzExec(t, db, `insert into hook_out(ts, session_id, event, chars) values (?, 's2', ?, ?)`, fmt.Sprintf("2026-11-21T10:0%d:00Z", i), r.event, r.chars)
	}
	stats, _, _ := runTrace(t, root, "", nil, "stats")
	if !strings.Contains(stats, "s2  4  2000  500") || !strings.Contains(stats, "s2  PostToolUse  2  700") || !strings.Contains(stats, "since_last_compaction") {
		t.Fatalf("stats:\n%s", stats)
	}
	if got := mutStr(t, db, `select count(*) from hook_out where session_id = 's1'`); got != "1" {
		t.Fatalf("a hook that wrote nothing was logged too: %s rows", got)
	}
}
