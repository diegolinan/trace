package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shStore is a store with task 1 delivered into session s1 and not told, and its path; the hook's import is a no-op.
func shStore(t *testing.T) (*sql.DB, string) {
	t.Helper()
	smKeepCfg(t)
	cfg.Scope, cfg.Roles = "", nil
	old := spawnImport
	spawnImport = func([]string, string) error { return nil }
	t.Cleanup(func() { spawnImport = old })
	db, dbPath := alarmStore(t)
	rzExec(t, db, `insert into task(text, due, created_at, announced_at, fire_state, fire_session, marker) values
		('call Bob', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', '2026-11-12T09:00:00Z', 'delivered', 's1', 'trace-alarm:1:00112233aabbccdd')`)
	return db, dbPath
}

// shStop runs the Stop hook as host calls it and returns its stdout and the reason it holds the turn with ("" when it
// lets the turn end).
func shStop(t *testing.T, dbPath, host string, in hookInput) (out, held string) {
	t.Helper()
	in.Event, in.Cwd = "Stop", filepath.Dir(dbPath)
	var w bytes.Buffer
	err := hook(in, nil, dbPath, host, "", func(*sql.DB, string) error { return nil }, &w)
	var h holdTurn
	if errors.As(err, &h) {
		held = h.reason
	} else if err != nil {
		t.Fatal(err)
	}
	return w.String(), held
}

// SPEC A8: while a task delivered into a session is not told, that session's stop hook keeps the turn going and names
// the task, with its marker (A9): Claude Code by exit 2 with the reason on stderr, Codex by decision "block" with a
// reason. task told ends it, and so do task done and task drop. Another session's stop is not held.
func TestStopHookHoldsUntilTold(t *testing.T) {
	db, dbPath := shStore(t)
	rzExec(t, db, `insert into task(text, due, created_at, announced_at, fire_state, fire_session) values
		('renew the domain', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', '2026-11-12T09:00:00Z', 'delivered', 's1'),
		('failed one', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', '2026-11-12T09:00:00Z', 'failed', 's1')`)
	out, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1"})
	if out != "" || !strings.Contains(held, "task 1") || !strings.Contains(held, `"call Bob"`) || !strings.Contains(held, "trace-alarm:1:00112233aabbccdd") ||
		!strings.Contains(held, "task 2") || strings.Contains(held, "task 3") || !strings.Contains(held, "task told") {
		t.Errorf("Claude Code stop: stdout %q, held with %q", out, held)
	}
	out, held = shStop(t, dbPath, "codex", hookInput{SessionID: "s1"})
	var decision struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &decision); err != nil || held != "" || decision.Decision != "block" || !strings.Contains(decision.Reason, "trace-alarm:1:00112233aabbccdd") {
		t.Errorf("Codex stop: stdout %q, held %q", out, held)
	}
	if _, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s2"}); held != "" {
		t.Errorf("another session's stop was held: %q", held)
	}
	rzExec(t, db, "update task set holds = 0")
	var w bytes.Buffer
	if err := taskTold(db, []string{"told", "1"}, &w); err != nil || w.String() != "task 1: told\n" {
		t.Fatalf("task told: %v, %q", err, w.String())
	}
	if err := taskClose(db, []string{"done", "2"}, &w); err != nil {
		t.Fatal(err)
	}
	if _, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1"}); held != "" {
		t.Errorf("held after task told and task done: %q", held)
	}
	for _, args := range [][]string{{"told", "1"}, {"told", "2"}, {"told", "99"}} { // told, closed, unknown
		if err := taskTold(db, args, &w); err == nil || !strings.Contains(err.Error(), "task "+args[1]+" is not") {
			t.Errorf("task told %s: %v", args[1], err)
		}
	}
	rzExec(t, db, `insert into task(text, created_at) values ('not fired', '2026-11-11T09:00:00Z')`)
	if err := taskTold(db, []string{"told", "3"}, &w); err != nil { // late: its delivery failed
		t.Errorf("a late task: %v", err)
	}
	if err := taskTold(db, []string{"told", "4"}, &w); err == nil {
		t.Error("a task never fired was told")
	}
	if err := taskTold(db, []string{"told"}, &w); !errors.As(err, new(usageError)) {
		t.Errorf("task told without an id: %v", err)
	}
}

// SPEC A8: when the hook already held the turn (stop_hook_active), it lets the turn end and counts no hold.
func TestStopHookDoesNotLoop(t *testing.T) {
	db, dbPath := shStore(t)
	for _, host := range []string{"claude-code", "codex"} {
		if out, held := shStop(t, dbPath, host, hookInput{SessionID: "s1", StopActive: true}); out != "" || held != "" {
			t.Errorf("%s: a stop already held was held again: %q %q", host, out, held)
		}
	}
	if n := mutStr(t, db, "select holds from task"); n != "0" {
		t.Errorf("holds %s, want 0", n)
	}
}

// SPEC A8: only the main session's stop holds, never a subagent's: a payload carrying an agent_id lets it end, and a
// subagent's own stop event is not a Stop.
func TestStopHookIgnoresSubagents(t *testing.T) {
	_, dbPath := shStore(t)
	if out, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1", AgentID: "a1"}); out != "" || held != "" {
		t.Errorf("a subagent's stop was held: %q %q", out, held)
	}
	var w bytes.Buffer
	err := hook(hookInput{Event: "SubagentStop", SessionID: "s1", Cwd: filepath.Dir(dbPath)}, nil, dbPath, "claude-code", "", nil, &w)
	if err != nil || w.Len() != 0 {
		t.Errorf("SubagentStop: %v, %q", err, w.String())
	}
}

// SPEC A8: after two holds for one task the hook stops holding and leaves the task to A7: still untold, so late.
func TestStopHookGivesUpAfterTwoHolds(t *testing.T) {
	db, dbPath := shStore(t)
	for i, want := range []bool{true, true, false} {
		if _, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1"}); (held != "") != want {
			t.Errorf("stop %d held %q, want held %v", i+1, held, want)
		}
	}
	if s := mutStr(t, db, "select holds || ':' || coalesce(told_at, 'untold') from task"); s != "2:untold" {
		t.Errorf("after the third stop: %s", s)
	}
}

// SPEC A8: the hook waits for the store at most one second; a store it cannot use lets the turn end.
func TestStopHookLetsTurnEndOnStoreError(t *testing.T) {
	db, dbPath := shStore(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("update task set text = text"); err != nil { // holds the write lock
		t.Fatal(err)
	}
	start := time.Now()
	if _, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1"}); held != "" || time.Since(start) > 3*time.Second {
		t.Errorf("a locked store: held %q after %s", held, time.Since(start))
	}
	tx.Rollback()
	if _, held := shStop(t, filepath.Join(t.TempDir(), "none.db"), "claude-code", hookInput{SessionID: "s1"}); held != "" {
		t.Errorf("no store: held %q", held)
	}
	rzExec(t, db, "alter table task drop column holds")
	if _, held := shStop(t, dbPath, "claude-code", hookInput{SessionID: "s1"}); held != "" {
		t.Errorf("a store from before the alarm: held %q", held)
	}
}

// SPEC A8, the installed command: Claude Code runs `trace hook` as its Stop hook, and a hold reaches it as exit 2 with
// the reason on stderr; a stop with nothing to hold ends with 0 and prints nothing.
func TestStopHookExitsTwoForClaudeCode(t *testing.T) {
	db, dbPath := shStore(t)
	db.Close()
	payload := func(sid string) string {
		b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": sid, "cwd": filepath.Dir(dbPath), "stop_hook_active": false})
		return string(b)
	}
	root := t.TempDir()
	out, errOut, code := runTrace(t, root, payload("s1"), nil, "hook", "-db", dbPath)
	if code != 2 || out != "" || !strings.Contains(errOut, "trace-alarm:1:00112233aabbccdd") {
		t.Errorf("held stop: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if out, errOut, code := runTrace(t, root, payload("s2"), nil, "hook", "-db", dbPath); code != 0 || out != "" || errOut != "" {
		t.Errorf("a stop with nothing to hold: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// SPEC A8: install already adds the Stop hook for both programs, in a form a hold gets through: Claude Code runs the
// binary itself (exit 2 and stderr reach it), Codex's PowerShell wrapper passes stdout and exits with trace's code.
func TestInstallAddsTheStopHookThatHolds(t *testing.T) {
	settings := map[string]any{}
	setHooks(settings, "/x/trace", "/h", nil, false)
	stop, _ := settings["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 || !strings.Contains(mustJSON(stop), `"command":"/x/trace","timeout":10`) {
		t.Errorf("Claude Code's Stop hook: %s", mustJSON(stop))
	}
	found := false
	for _, ev := range codexHookEvents(config{}) {
		found = found || ev.name == "Stop"
	}
	if !found || !strings.HasSuffix(psScript("/x/trace", "/h"), "; exit $LASTEXITCODE") {
		t.Errorf("Codex's Stop hook: registered %v, script %s", found, psScript("/x/trace", "/h"))
	}
}

// SPEC A8, the command: `trace task told <id>` ends the hold for the next stop.
func TestTaskToldCommand(t *testing.T) {
	db, dbPath := shStore(t)
	db.Close()
	root := t.TempDir()
	if out, errOut, code := runTrace(t, root, "", nil, "task", "told", "-db", dbPath, "1"); code != 0 || out != "task 1: told\n" {
		t.Fatalf("task told: exit %d, %q %q", code, out, errOut)
	}
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s1", "cwd": filepath.Dir(dbPath)})
	if _, errOut, code := runTrace(t, root, string(b), nil, "hook", "-db", dbPath); code != 0 || errOut != "" {
		t.Errorf("a stop after task told: exit %d, stderr %q", code, errOut)
	}
}
