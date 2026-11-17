package main

import (
	"strings"
	"testing"
)

// ghCriteria is a task's conditions as n:kind:words:met, in order; kind is after, needs or then.
func ghCriteria(t *testing.T, root string, task int) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select coalesce(group_concat(n || ':' || case when after_id is not null then 'after'
		when done_when is not null then 'then' else 'needs' end || ':' || coalesce(done_when, needs, after_id) || ':' || (met_at is not null), ';'), '')
		from (select * from task_condition where task_id = ? order by n)`, task)
}

// SPEC T8: -then repeats and comes after the conditions of ready in one numbering; task then takes the next number;
// criteria never show as waiting on and never make a task not ready.
func TestThenCriteriaStored(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá mandar el CV")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "send the CV", "-then", "Given the CV, when sent, then a PDF arrives", "-after", "1", "-needs", "the address", "-then", "b")
	if out := tkTrace(t, root, "task", "then", "2", "later one"); !strings.Contains(out, "task 2: criterion 5 added") {
		t.Errorf("task then prints %q", out)
	}
	want := "1:after:1:0;2:needs:the address:0;3:then:Given the CV, when sent, then a PDF arrives:0;4:then:b:0;5:then:later one:0"
	if got := ghCriteria(t, root, 2); got != want {
		t.Errorf("criteria stored as\n%q\nwant\n%q", got, want)
	}
	tkTrace(t, root, "task", "add", "-text", "only criteria", "-then", "c")
	if out, _, code := rdRun(t, root, "task", "start", "3"); code != 0 || !strings.Contains(out, "task 3 is ready") {
		t.Errorf("a task with only criteria: exit %d, %q; want ready", code, out)
	}
	if list := tkTrace(t, root, "task", "list"); strings.Contains(list, "PDF") || strings.Contains(list, `"c"`) {
		t.Errorf("criteria shown as waiting on:\n%s", list)
	}
}

// SPEC T8: an empty criterion, and task then on an unknown or closed task or with no words, are refused and store nothing.
func TestTaskThenRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "open one", "-then", "a")
	tkTrace(t, root, "task", "add", "-text", "closed one")
	tkTrace(t, root, "task", "drop", "2")
	if _, errOut, code := rdRun(t, root, "task", "add", "-text", "y", "-then", " "); code == 0 || !strings.Contains(errOut, "nothing stored") {
		t.Errorf("empty -then: exit %d, %q", code, errOut)
	}
	for _, args := range [][]string{{"9", "x"}, {"2", "x"}, {"1", " "}, {"1"}} {
		if _, errOut, code := rdRun(t, root, append([]string{"task", "then"}, args...)...); code == 0 || errOut == "" {
			t.Errorf("task then %v: exit %d, %q; want a refusal", args, code, errOut)
		}
	}
	db := tkStore(t, root)
	if n := mutStr(t, db, "select count(*) from task"); n != "2" {
		t.Errorf("%s tasks, want 2", n)
	}
	if n := mutStr(t, db, "select count(*) from task_condition"); n != "1" {
		t.Errorf("%s criteria, want 1", n)
	}
	db.Close()
	if _, errOut, _ := rdRun(t, root, "task", "then", "2", "x"); !strings.Contains(errOut, "task 2 is not an open task; nothing stored") {
		t.Errorf("task then on a closed task: %q", errOut)
	}
}

// SPEC T9: task done refuses while a criterion is unmet, naming each, and changes nothing; met ones are not named;
// task drop closes whatever the criteria.
func TestDoneRefusedUntilThenMet(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "send it", "-needs", "the address", "-then", "a", "-then", "b")
	_, errOut, code := rdRun(t, root, "task", "done", "1")
	if code != 1 || !strings.Contains(errOut, `then 2: "a"`) || !strings.Contains(errOut, `then 3: "b"`) || strings.Contains(errOut, "address") {
		t.Errorf("done with criteria unmet: exit %d, %q", code, errOut)
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task where closed_at is null"); n != "1" {
		t.Errorf("the refused done closed the task")
	}
	tkTrace(t, root, "task", "met", "1", "2")
	if _, errOut, _ := rdRun(t, root, "task", "done", "1"); strings.Contains(errOut, `then 2:`) || !strings.Contains(errOut, `then 3: "b"`) {
		t.Errorf("after meeting criterion 2: %q", errOut)
	}
	tkTrace(t, root, "task", "met", "1", "3")
	if out := tkTrace(t, root, "task", "done", "1"); !strings.Contains(out, "task 1 closed: done") {
		t.Errorf("every criterion met, done prints %q", out)
	}
	tkTrace(t, root, "task", "add", "-text", "drop it", "-then", "never")
	if out := tkTrace(t, root, "task", "drop", "2"); !strings.Contains(out, "task 2 closed: drop") {
		t.Errorf("drop with a criterion unmet prints %q", out)
	}
}

// SPEC T9: task start prints the unmet criteria after its verdict, ready or not; met ones are left out.
func TestTaskStartShowsDoneWhen(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "send it", "-then", "a", "-then", "b")
	tkTrace(t, root, "task", "add", "-text", "later", "-needs", "x", "-then", "c")
	if out, _, code := rdRun(t, root, "task", "start", "1"); code != 0 || !strings.Contains(out, "task 1 is ready\ndone when: then 1: \"a\"; then 2: \"b\"") {
		t.Errorf("start 1: exit %d, %q", code, out)
	}
	if out, _, code := rdRun(t, root, "task", "start", "2"); code != 3 || !strings.Contains(out, `done when: then 2: "c"`) {
		t.Errorf("start 2: exit %d, %q", code, out)
	}
	tkTrace(t, root, "task", "met", "1", "1")
	if out, _, _ := rdRun(t, root, "task", "start", "1"); strings.Contains(out, `then 1:`) || !strings.Contains(out, `done when: then 2: "b"`) {
		t.Errorf("start 1 after meeting criterion 1: %q", out)
	}
	tkTrace(t, root, "task", "met", "1", "2")
	if out, _, _ := rdRun(t, root, "task", "start", "1"); strings.Contains(out, "done when") {
		t.Errorf("start 1 with every criterion met: %q", out)
	}
}

// SPEC T9: the start block tells the agent about -then, task then, and that task done needs every criterion met.
func TestStartBlockTellsAboutCriteria(t *testing.T) {
	var b strings.Builder
	db := rzDB(t)
	if err := taskSection(db, &b, "the user"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-then", "Given / When / Then", "task then", "every criterion"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("start block lacks %q:\n%s", want, b.String())
		}
	}
}
