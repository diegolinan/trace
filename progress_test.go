package main

import (
	"strings"
	"testing"
)

// pgStarted is every task as id:started (1 or 0), in id order.
func pgStarted(t *testing.T, root string) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select coalesce(group_concat(id || ':' || (started_at is not null), ';'), '') from (select * from task order by id)`)
}

// SPEC T16: task start on a ready task puts it in progress with when and the session; a task not ready stays to do,
// and starting one already in progress keeps when it started.
func TestTaskStartPutsInProgress(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second", "-after", "1")
	if out := tkTrace(t, root, "task", "start", "1"); !strings.Contains(out, "task 1 is ready; now in progress") {
		t.Errorf("start 1 prints %q", out)
	}
	if _, _, code := rdRun(t, root, "task", "start", "2"); code != 3 {
		t.Errorf("start 2: exit %d, want 3", code)
	}
	if got := pgStarted(t, root); got != "1:1;2:0" {
		t.Errorf("started %q, want 1:1;2:0", got)
	}
	db := tkStore(t, root)
	if _, err := db.Exec(`update task set started_at = '2026-11-01T10:00:00Z' where id = 1`); err != nil {
		t.Fatal(err)
	}
	tkTrace(t, root, "task", "start", "1")
	if got := mutStr(t, db, `select started_at || ':' || started_session from task where id = 1`); got != "2026-11-01T10:00:00Z:s1" {
		t.Errorf("started %q, want the first start kept, by s1", got)
	}
}

// SPEC T16: task stop puts a task in progress back to to do and records when and the session; a task not in progress,
// closed or unknown is refused.
func TestTaskStop(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "first")
	tkTrace(t, root, "task", "add", "-text", "second")
	tkTrace(t, root, "task", "start", "1")
	if out := tkTrace(t, root, "task", "stop", "1"); !strings.Contains(out, "task 1: to do") {
		t.Errorf("stop prints %q", out)
	}
	for _, args := range [][]string{{"task", "stop", "1"}, {"task", "stop", "2"}, {"task", "stop", "9"}, {"task", "stop"}} {
		lvRefused(t, root, args...)
	}
	if got := pgStarted(t, root); got != "1:0;2:0" {
		t.Errorf("started %q", got)
	}
	if got := mutStr(t, tkStore(t, root), `select (stopped_at is not null) || ':' || stopped_session from task where id = 1`); got != "1:s1" {
		t.Errorf("stop recorded %q", got)
	}
}

// SPEC T16: without a sprint one story is in progress at a time; the tasks under it and plain tasks are not limited.
func TestOneStoryInProgress(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "story A", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "story B", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "under A", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "a reminder")
	tkTrace(t, root, "task", "start", "1")
	_, errOut, code := rdRun(t, root, "task", "start", "2")
	if code != 1 || !strings.Contains(errOut, "task 1 (story, in progress) is in progress: finish it, stop it (task stop 1) or ask the user first; nothing changed") {
		t.Errorf("a second story: exit %d, %q", code, errOut)
	}
	tkTrace(t, root, "task", "start", "3")
	tkTrace(t, root, "task", "start", "4")
	if got := pgStarted(t, root); got != "1:1;2:0;3:1;4:1" {
		t.Errorf("started %q", got)
	}
	tkTrace(t, root, "task", "stop", "1")
	tkTrace(t, root, "task", "start", "2")
	tkTrace(t, root, "task", "start", "2") // the one in progress is not in its own way
	if got := pgStarted(t, root); got != "1:0;2:1;3:1;4:1" {
		t.Errorf("started %q", got)
	}
}

// SPEC T16: task list, the start block and the alarm's message show in progress in the tag.
func TestInProgressShown(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "call Ana", "-at", "2026-11-12T10:00:00Z")
	tkTrace(t, root, "task", "add", "-text", "not started")
	tkTrace(t, root, "task", "start", "1")
	tkTrace(t, root, "task", "start", "2")
	want := []string{"- task 1 (story, 3 points, in progress)", "- task 2 (in progress)", "- task 3"}
	if got := ptListed(t, root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("list:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	db := tkStore(t, root)
	var block strings.Builder
	if err := taskSection(db, &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.String(), "- task 1 (story, 3 points, in progress),") {
		t.Errorf("start block:\n%s", block.String())
	}
	due, err := toFire(db, "2026-11-12T10:00:00Z")
	if err != nil || len(due) != 1 {
		t.Fatalf("due %v, %v", due, err)
	}
	if msg := alarmMessage(due[0], "trace-alarm:2:0123456789abcdef", 0); !strings.Contains(msg, "TRACE alarm: task 2 (in progress)") {
		t.Errorf("alarm message:\n%s", msg)
	}
}

// SPEC T16: task sizes measures a story started from its start, one never started from when it was added.
func TestSizesFromStart(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "started", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "start", "1")
	tkTrace(t, root, "task", "done", "1")
	tkTrace(t, root, "task", "add", "-text", "never started", "-level", "story", "-points", "5")
	tkTrace(t, root, "task", "done", "2")
	db := tkStore(t, root)
	for _, s := range []string{
		`update task set created_at = '2026-11-10T08:00:00Z', started_at = '2026-11-10T10:00:00Z', closed_at = '2026-11-10T11:00:00Z' where id = 1`,
		`update task set created_at = '2026-11-09T08:00:00Z', closed_at = '2026-11-09T11:00:00Z' where id = 2`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.TrimRight(tkTrace(t, root, "task", "sizes"), "\n")
	want := "- task 1 (story, 3 points): \"started\", 1h0m0s from started to done\n- task 2 (story, 5 points): \"never started\", 3h0m0s from added to done"
	if got != want {
		t.Errorf("sizes:\n%s\nwant\n%s", got, want)
	}
}

// SPEC T16 and the board's scope: the start block tells the agent about task start and task stop, and that most
// conversations put nothing on the board.
func TestStartBlockSaysMostTalkIsNoTask(t *testing.T) {
	var b strings.Builder
	if err := taskSection(rzDB(t), &b, "the user"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"task stop", "one story in progress", "Most conversations put nothing on the board"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("start block lacks %q:\n%s", want, b.String())
		}
	}
}
