package main

import (
	"strings"
	"testing"
)

// rdRun runs trace as session s1 and returns stdout, stderr and the exit code.
func rdRun(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	return runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, args...)
}

// rdConditions is a task's conditions as n:after:needs:met, in order.
func rdConditions(t *testing.T, root string, task int) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select coalesce(group_concat(n || ':' || coalesce(after_id, '') || ':' || coalesce(needs, '') || ':' ||
		(met_at is not null), ';'), '') from (select * from task_condition where task_id = ? order by n)`, task)
}

// SPEC T5: -after and -needs repeat, get one fixed numbering in the order given, and a task with none is ready.
func TestTaskAddConditions(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá todo esto")))
	tkTrace(t, root, "task", "add", "-text", "write the article")
	tkTrace(t, root, "task", "add", "-text", "publish it", "-after", "1", "-needs", "Ana's reply", "-needs", "budget OK")
	if got := rdConditions(t, root, 2); got != "1:1::0;2::Ana's reply:0;3::budget OK:0" {
		t.Errorf("conditions stored as %q", got)
	}
	if got := rdConditions(t, root, 1); got != "" {
		t.Errorf("a task with no condition has %q", got)
	}
}

// SPEC T5: -after an unknown or dropped task is refused and nothing is stored; -after a task done is stored as met.
func TestTaskAddAfterRefusedOrMet(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "dropped one")
	tkTrace(t, root, "task", "add", "-text", "done one")
	tkTrace(t, root, "task", "drop", "1")
	tkTrace(t, root, "task", "done", "2")
	for _, after := range []string{"99", "1", "x"} {
		if _, errOut, code := rdRun(t, root, "task", "add", "-text", "y", "-after", after); code == 0 || !strings.Contains(errOut, "nothing stored") {
			t.Errorf("-after %s: exit %d, stderr %q; want a refusal", after, code, errOut)
		}
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task"); n != "2" {
		t.Errorf("%s tasks after the refused adds, want 2", n)
	}
	tkTrace(t, root, "task", "add", "-text", "after the done one", "-after", "2")
	if got := rdConditions(t, root, 3); got != "1:2::1" {
		t.Errorf("-after a task done stored as %q, want it met", got)
	}
}

// SPEC T6: task met meets a -needs and records when, the session and the user's newest typed message in it.
func TestNeedsMetRecordsTheAsk(t *testing.T) {
	asking := mutLines(mutUser("u1", 0, "anotá publicarlo cuando responda Ana"))
	root := tkRoot(t, asking)
	tkTrace(t, root, "task", "add", "-text", "publish it", "-needs", "Ana's reply")
	tkWrite(t, root, "s1.jsonl", asking+mutLine(mutUser("u2", 5, "Ana respondió, dale")))
	if out := tkTrace(t, root, "task", "met", "1", "1"); !strings.Contains(out, "task 1: condition 1 met") {
		t.Errorf("met prints %q", out)
	}
	got := mutStr(t, tkStore(t, root), `select (c.met_at is not null) || ' ' || c.met_session || ' ' || e.text from task_condition c
		join event e on e.id = c.met_event where c.task_id = 1 and c.n = 1`)
	if got != "1 s1 Ana respondió, dale" {
		t.Errorf("met recorded %q", got)
	}
}

// SPEC T6: an unknown task or n, a closed task, a condition already met and an -after whose task is open are refused
// and change nothing.
func TestTaskMetRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1", "-needs", "a reply")
	tkTrace(t, root, "task", "add", "-text", "closed", "-needs", "x")
	tkTrace(t, root, "task", "drop", "3")
	tkTrace(t, root, "task", "met", "2", "2")
	before := rdConditions(t, root, 2) + "|" + rdConditions(t, root, 3)
	for _, args := range [][]string{{"9", "1"}, {"2", "9"}, {"2", "x"}, {"3", "1"}, {"2", "2"}, {"2", "1"}, {"2"}} {
		if _, errOut, code := rdRun(t, root, append([]string{"task", "met"}, args...)...); code == 0 || errOut == "" {
			t.Errorf("task met %v: exit %d, stderr %q; want a refusal", args, code, errOut)
		}
	}
	if after := rdConditions(t, root, 2) + "|" + rdConditions(t, root, 3); after != before {
		t.Errorf("refused mets changed the conditions: %q, was %q", after, before)
	}
}

// SPEC T6, T7: dropping a task names the tasks that wait on it; they show it as dropped until the user waives it.
func TestDroppedAfterNamedAndWaivable(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1")
	if out := tkTrace(t, root, "task", "drop", "1"); !strings.Contains(out, "task 2 waits on it") {
		t.Errorf("drop does not name the waiting task: %q", out)
	}
	if list := tkTrace(t, root, "task", "list"); !strings.Contains(list, "waiting on: after task 1 (dropped)") {
		t.Errorf("list:\n%s", list)
	}
	tkTrace(t, root, "task", "met", "2", "1")
	if list := tkTrace(t, root, "task", "list"); strings.Contains(list, "waiting on") {
		t.Errorf("a waived condition still shows:\n%s", list)
	}
}

// SPEC T7: list, start block and prompt hook show the unmet conditions, at most three, with a task that is itself
// waiting marked; the start block tells the agent about the commands.
func TestWaitingOnShownEverywhere(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first", "-needs", "a decision")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1", "-needs", "Ana's reply", "-needs", "c", "-needs", strings.Repeat("long ", 100))
	list := tkTrace(t, root, "task", "list")
	if !strings.Contains(list, `second (`) || !strings.Contains(list, `waiting on: after task 1 (open, itself waiting); need 2: "Ana's reply"; need 3: "c"; and 1 more`) {
		t.Errorf("list:\n%s", list)
	}
	if !strings.Contains(list, `waiting on: need 1: "a decision"`) {
		t.Errorf("list does not show the first task's need:\n%s", list)
	}
	db := tkStore(t, root)
	var block strings.Builder
	if err := taskSection(db, &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	if err := taskHelp(&block, "Ana"); err != nil { // the guide is trace task help's (SPEC B1)
		t.Fatal(err)
	}
	for _, want := range []string{`waiting on: need 1: "a decision"`, "-after", "-needs", "task met", "task start"} {
		if !strings.Contains(block.String(), want) {
			t.Errorf("the start block lacks %q:\n%s", want, block.String())
		}
	}
	if long := waitingOn(db, 2, 9); !strings.Contains(long, `need 4: "long long`) || len(long) > 600 {
		t.Errorf("a long need is not clipped: %d characters", len(long))
	}
	rzExec(t, db, "update task set due = '2026-11-12T10:00:00Z' where id = 2")
	alarmAt(t, "2026-11-12T10:00:00Z")
	lines, err := nameUntold(db, "s9")
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], "waiting on: after task 1") {
		t.Errorf("prompt hook lines %q, %v", lines, err)
	}
}

// SPEC T7, A4: a waiting task with a time still fires, and its message says what it waits on.
func TestAlarmMessageSaysWaitingOn(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "deploy", "-at", "2026-11-12T10:00:00Z", "-needs", "the green light")
	db := tkStore(t, root)
	due, err := toFire(db, "2026-11-12T10:00:00Z")
	if err != nil || len(due) != 1 {
		t.Fatalf("due %v, %v", due, err)
	}
	if msg := alarmMessage(due[0], "trace-alarm:1:0123456789abcdef", 0); !strings.Contains(msg, `waiting on: need 1: "the green light"`) {
		t.Errorf("the alarm's message does not say what the task waits on:\n%s", msg)
	}
}

// SPEC T7: task start says ready (exit 0) or what is missing (exit 3), records nothing; unknown or closed fails (1).
func TestTaskStartChecks(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1")
	tkTrace(t, root, "task", "add", "-text", "third")
	tkTrace(t, root, "task", "drop", "3")
	if out, _, code := rdRun(t, root, "task", "start", "1"); code != 0 || !strings.Contains(out, "task 1 is ready") {
		t.Errorf("start 1: exit %d, %q", code, out)
	}
	if out, _, code := rdRun(t, root, "task", "start", "2"); code != 3 || !strings.Contains(out, "waiting on: after task 1 (open)") {
		t.Errorf("start 2: exit %d, %q; want exit 3 and what it waits on", code, out)
	}
	for _, id := range []string{"3", "9"} {
		if _, _, code := rdRun(t, root, "task", "start", id); code != 1 {
			t.Errorf("start %s: exit %d, want 1", id, code)
		}
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task where closed_at is null and (told_at is not null or announced_at is not null)"); n != "0" {
		t.Errorf("start recorded something on %s tasks", n)
	}
}

// SPEC T5, T6, T7: closing the task an -after names as done meets the condition; refusals name the task and the
// condition they refuse; task start shows every unmet condition; the cut at the limit is exact.
func TestConditionsMetOnDoneAndShownExactly(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1", "-needs", "a", "-needs", "b")
	tkTrace(t, root, "task", "add", "-text", "third", "-needs", "x", "-needs", "y", "-needs", "z")
	for args, says := range map[string]string{"9 1": "task 9 has no open condition 1", "2 7": "task 2 has no open condition 7",
		"2 1": "condition 1 of task 2 waits on task 1, still open"} {
		if _, errOut, _ := rdRun(t, root, append([]string{"task", "met"}, strings.Fields(args)...)...); !strings.Contains(errOut, says) {
			t.Errorf("task met %s: stderr %q, want %q", args, errOut, says)
		}
	}
	if _, errOut, _ := rdRun(t, root, "task", "start", "9"); !strings.Contains(errOut, "task 9 is not an open task") {
		t.Errorf("start 9: %q", errOut)
	}
	if out, _, _ := rdRun(t, root, "task", "start", "3"); !strings.Contains(out, `need 1: "x"; need 2: "y"; need 3: "z"`) || strings.Contains(out, "more") {
		t.Errorf("start shows all conditions, uncut: %q", out)
	}
	db := tkStore(t, root)
	if got := waitingOn(db, 3, 3); strings.Contains(got, "more") {
		t.Errorf("three conditions cut at three: %q", got)
	}
	if got := waitingOn(db, 3, 1); got != `waiting on: need 1: "x"; and 2 more` {
		t.Errorf("cut at one: %q", got)
	}
	db.Close()
	tkTrace(t, root, "task", "done", "1")
	if out, _, _ := rdRun(t, root, "task", "start", "2"); strings.Contains(out, "after task 1") || !strings.Contains(out, `need 2: "a"`) {
		t.Errorf("after task 1 was done: %q", out)
	}
}
