package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// atFolder is a folder a session ran in: not a temporary one, which I4 keeps out.
func atFolder(parts ...string) string {
	return filepath.Join(append([]string{string(filepath.Separator) + "work"}, parts...)...)
}

// atSession is a live session of a fake program; what it is sent is kept in got, keyed by session.
func atSession(via, id, cwd string, program bool, used int64, got map[string][]string, fail error) liveTarget {
	return liveTarget{via: via, id: id, cwd: cwd, program: program, used: time.Unix(used, 0), send: func(text string) error {
		got[id] = append(got[id], text)
		return fail
	}}
}

// atPrograms stands in for every deliverer for one test, and serves the folder tree "acme" with no roles.
func atPrograms(t *testing.T, ds ...deliverer) {
	t.Helper()
	smKeepCfg(t)
	cfg.Scope, cfg.Roles = "acme", nil
	old := deliverers
	deliverers = ds
	t.Cleanup(func() { deliverers = old })
}

func atLive(sessions ...liveTarget) func() ([]liveTarget, error) {
	return func() ([]liveTarget, error) { return sessions, nil }
}

// SPEC A5: task add records the session that ran it: the id its program gives the command, otherwise the S1 rule; the
// words that asked stay apart. The alarm wakes that session when it qualifies, not the one used last, and only it.
// When it is gone, the alarm wakes the qualifying session used last. The message is A4's, with the marker the claim
// recorded.
func TestAlarmWakesSessionThatAddedTask(t *testing.T) {
	got := map[string][]string{}
	acme := atFolder("acme")
	atPrograms(t,
		deliverer{name: "fa", sessionVar: "TRACE_TEST_FA_SESSION", live: atLive(atSession("fa", "s-last", acme, false, 300, got, nil))},
		deliverer{name: "fb", sessionVar: "TRACE_TEST_FB_SESSION", live: atLive(atSession("fb", "s-asker", acme, false, 100, got, nil))})
	db, _ := alarmStore(t)
	rzExec(t, db, `insert into session(id, host, role) values ('s-typed', 'claude-code', 'main')`)
	rzExec(t, db, `insert into event(session_id, seq, ts, speaker, kind, text) values ('s-typed', 1, '2026-11-11T09:00:00Z', 'user', 'prompt', 'remind me to call Bob')`)
	var out strings.Builder
	t.Setenv("TRACE_TEST_FB_SESSION", "s-asker")
	if err := taskAdd(db, newTask{text: "call Bob", due: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}, &out); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRACE_TEST_FB_SESSION", "")
	t.Setenv("TRACE_TEST_FA_SESSION", "s-closed")
	taskAdd(db, newTask{text: "renew the domain", due: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}, &out)
	if got := mutStr(t, db, "select group_concat(id || ':' || coalesce(added_session, '-') || ':' || asked_event, ' ') from task"); got != "1:s-asker:1 2:s-closed:1" {
		t.Fatalf("recorded sessions: %s", got)
	}
	alarmAt(t, "2026-11-12T09:00:00Z")
	if err := fireDue(db); err != nil {
		t.Fatal(err)
	}
	if len(got["s-asker"]) != 1 || len(got["s-last"]) != 1 {
		t.Fatalf("delivered %v: task 1 to the session that added it, task 2 (its session gone) to the one used last", got)
	}
	marker := mutStr(t, db, "select marker from task where id = 1")
	if want := alarmMessage(1, "2026-11-12T09:00:00Z", "call Bob", "remind me to call Bob", marker); got["s-asker"][0] != want {
		t.Errorf("sent\n%s\nwant\n%s", got["s-asker"][0], want)
	}
	if !regexp.MustCompile(`^trace-alarm:1:[0-9a-f]{16}$`).MatchString(marker) || !strings.HasSuffix(got["s-last"][0], mutStr(t, db, "select marker from task where id = 2")) {
		t.Errorf("markers: %s, and task 2's message %q", marker, got["s-last"][0])
	}
	if s := mutStr(t, db, "select group_concat(id || ':' || fire_state || ':' || fire_session, ' ') from task"); s != "1:delivered:s-asker 2:delivered:s-last" {
		t.Errorf("fires: %s", s)
	}
}

// SPEC A5: without an id from its program, task add takes the S1 rule: the recent session whose last event runs
// `trace task add`, else the newest recent one.
func TestTaskAddSessionFromS1Rule(t *testing.T) {
	atPrograms(t)
	db, _ := alarmStore(t)
	now := time.Now().UTC()
	for i, s := range []struct{ id, last string }{{"s-old", "tool_call Bash trace task add"}, {"s-asker", `tool_call Bash {"command":"trace.exe task add -text x"}`}, {"s-new", "reply hi"}} {
		rzExec(t, db, `insert into session(id, host, role, ended_at) values (?, 'codex', 'main', ?)`, s.id, now.Add(time.Duration(i-10)*time.Second).Format(time.RFC3339))
		kind, text, _ := strings.Cut(s.last, " ")
		rzExec(t, db, `insert into event(session_id, seq, ts, speaker, kind, text) values (?, 1, ?, 'assistant', ?, ?)`, s.id, now.Format(time.RFC3339), kind, text)
	}
	rzExec(t, db, `update session set ended_at = '2020-01-01T00:00:00Z' where id = 's-old'`)
	if got := callerSession(db); got != "s-asker" {
		t.Errorf("caller %q, want the session whose last call ran task add", got)
	}
	rzExec(t, db, `update event set text = 'Bash ls' where session_id = 's-asker'`)
	if got := callerSession(db); got != "s-new" {
		t.Errorf("caller %q, want the newest recent session", got)
	}
}

// SPEC A5: a session started by a program, one in an excluded folder, and one with a role this memory does not speak
// to never qualify, even when it added the task or was used last.
func TestAlarmSkipsProgramAndExcludedSessions(t *testing.T) {
	got := map[string][]string{}
	atPrograms(t, deliverer{name: "fa", live: atLive(
		atSession("fa", "s-worker", atFolder("acme"), true, 500, got, nil),
		atSession("fa", "s-scratch", atFolder("acme", "scratch"), false, 400, got, nil),
		atSession("fa", "s-temp", atFolder("acme", "Temp", "x"), false, 300, got, nil),
		atSession("fa", "s-user", atFolder("acme"), false, 100, got, nil))})
	db, _ := alarmStore(t)
	rzExec(t, db, `insert into task(text, due, created_at, added_session) values ('a', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', 's-worker'),
		('b', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', 's-scratch'), ('c', '2026-11-13T09:00:00Z', '2026-11-11T09:00:00Z', 's-user')`)
	alarmAt(t, "2026-11-12T09:00:00Z")
	if err := fireDue(db); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(len(got), len(got["s-user"])) != "1 2" {
		t.Errorf("delivered %v, want both tasks to s-user only", got)
	}
	cfg.Roles = []roleRule{{Name: "main", Marker: "ROLE.md", Contains: "role: main"}} // no folder holds the marker
	alarmAt(t, "2026-11-13T09:00:00Z")
	if err := fireDue(db); err != nil {
		t.Fatal(err)
	}
	if s := mutStr(t, db, "select fire_state || ':' || fire_session || ':' || fire_reason from task where id = 3"); s != "failed::no live session qualifies" || len(got["s-user"]) != 2 {
		t.Errorf("a role this memory does not speak to: %s, delivered %v", s, got)
	}
}

// SPEC A5: the alarm never wakes a session outside the folder tree this TRACE serves, even the only one open and the
// one that added the task. A program that cannot list its sessions says why in the failure, under its name.
func TestAlarmNeverWakesOutsideTree(t *testing.T) {
	got := map[string][]string{}
	atPrograms(t, deliverer{name: "fa", live: atLive(atSession("fa", "s-other", atFolder("other"), false, 100, got, nil))},
		deliverer{name: "fb", live: func() ([]liveTarget, error) { return nil, errors.New("daemon not running") }})
	db, _ := alarmStore(t)
	rzExec(t, db, `insert into task(text, due, created_at, added_session) values ('a', '2026-11-12T09:00:00Z', '2026-11-11T09:00:00Z', 's-other')`)
	asked := 0
	deliverers = append(deliverers, deliverer{name: "fc", live: func() ([]liveTarget, error) { asked++; return nil, nil }})
	alarmAt(t, "2026-11-12T08:59:00Z")
	if err := fireDue(db); err != nil || asked != 0 {
		t.Fatalf("with nothing due: %v, programs asked %d times", err, asked)
	}
	alarmAt(t, "2026-11-12T09:00:00Z")
	if err := fireDue(db); err != nil {
		t.Fatal(err)
	}
	if s := mutStr(t, db, "select fire_state || ':' || fire_session || ':' || fire_reason || ':' || (marker is not null) from task"); s != "failed::fb: daemon not running; no live session qualifies:1" || len(got) != 0 {
		t.Errorf("outside the tree: %s, delivered %v", s, got)
	}
}

// SPEC A7: a task fired and not told is late in every start block until told or closed, whatever went wrong: a
// delivery that failed, no session to wake. Each failure is recorded with its reason, and trace stats shows each
// deliverer's last one, under its name.
func TestUntoldTaskShowsLateAndFailureIsReported(t *testing.T) {
	got := map[string][]string{}
	failing := errors.New("inbox gone")
	atPrograms(t, deliverer{name: "fa", live: func() ([]liveTarget, error) {
		return []liveTarget{atSession("fa", "s-a", atFolder("acme"), false, 100, got, failing)}, nil
	}})
	db, dbPath := alarmStore(t)
	for i, day := range []string{"12", "13", "14"} {
		rzExec(t, db, `insert into task(text, due, created_at) values (?, ?, '2026-11-11T09:00:00Z')`, fmt.Sprintf("task %d", i+1), "2026-11-"+day+"T09:00:00Z")
	}
	alarmAt(t, "2026-11-12T09:00:00Z")
	fireDue(db)
	deliverers = append(deliverers, deliverer{name: "fb", live: func() ([]liveTarget, error) { return nil, errors.New("daemon not running") }})
	cfg.Scope = "elsewhere"
	alarmAt(t, "2026-11-13T09:00:00Z")
	fireDue(db)
	cfg.Scope, failing = "acme", errors.New("the inbox key does not match")
	alarmAt(t, "2026-11-14T09:00:00Z")
	fireDue(db)
	var block strings.Builder
	if err := taskSection(db, &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	late := block.String()[strings.Index(block.String(), "### Late"):]
	for _, task := range []string{"task 1,", "task 2,", "task 3,"} {
		if !strings.Contains(late, task) {
			t.Errorf("%s is not late in the start block:\n%s", task, block.String())
		}
	}
	db.Close()
	out, _, code := runTrace(t, t.TempDir(), "", nil, "stats", "-db", dbPath)
	want := "-- alarm, each deliverer's last failure\n" +
		"fa  2026-11-14 09:00:00 +0000 UTC  fa: the inbox key does not match\n" + // the driver reads a time as one
		"fb  2026-11-13 09:00:00 +0000 UTC  fb: daemon not running; no live session qualifies\n"
	if code != 0 || !strings.Contains(out, want) {
		t.Errorf("stats (exit %d):\n%s\nwant\n%s", code, out, want)
	}
}
