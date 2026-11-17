package main

import (
	"strings"
	"testing"
)

// lvTasks is every task as id:level:parent (0 for none), in id order.
func lvTasks(t *testing.T, root string) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select coalesce(group_concat(id || ':' || level || ':' || coalesce(parent_id, 0), ';'), '')
		from (select * from task order by id)`)
}

// lvRefused runs a command that must be refused: exit not 0 and something on stderr.
func lvRefused(t *testing.T, root string, args ...string) {
	t.Helper()
	if _, errOut, code := rdRun(t, root, args...); code == 0 || errOut == "" {
		t.Errorf("%v: exit %d, %q; want a refusal", args, code, errOut)
	}
}

// SPEC T10: -level sets the level, a task is a task without it, any other word is refused and stores nothing.
func TestTaskLevels(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "get a job", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "call Ana")
	lvRefused(t, root, "task", "add", "-text", "x", "-level", "saga")
	if got, want := lvTasks(t, root), "1:epic:0;2:story:0;3:task:0"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
}

// SPEC T10: task level changes an open task's level unless that breaks T11; a closed or unknown task is refused.
func TestTaskRelevel(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "loose", "-level", "task")
	if out := tkTrace(t, root, "task", "level", "1", "story"); !strings.Contains(out, "task 1: story") {
		t.Errorf("task level prints %q", out)
	}
	tkTrace(t, root, "task", "add", "-text", "child", "-under", "1")
	lvRefused(t, root, "task", "level", "1", "epic") // its child, a task, would sit two levels down
	lvRefused(t, root, "task", "level", "2", "story") // its parent, a story, would be at its own level
	lvRefused(t, root, "task", "level", "1", "saga")
	lvRefused(t, root, "task", "level", "9", "story")
	tkTrace(t, root, "task", "add", "-text", "closed")
	tkTrace(t, root, "task", "drop", "3")
	lvRefused(t, root, "task", "level", "3", "story")
	if got, want := lvTasks(t, root), "1:story:0;2:task:1;3:task:0"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
}

// SPEC T11: -under puts a task one level below an open parent, taking that level when -level is not given.
func TestTaskUnder(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "get a job", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "the CV", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "write it", "-level", "task", "-under", "2")
	tkTrace(t, root, "task", "add", "-text", "the dates", "-under", "3")
	if got, want := lvTasks(t, root), "1:epic:0;2:story:1;3:task:2;4:subtask:3"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
}

// SPEC T11: an unknown or closed parent, a parent at the wrong level, an epic with a parent, a subtask without one
// and a parent under a subtask are refused and store nothing.
func TestTaskUnderRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "epic", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "closed", "-level", "epic")
	tkTrace(t, root, "task", "drop", "2")
	tkTrace(t, root, "task", "add", "-text", "a task")
	tkTrace(t, root, "task", "add", "-text", "a subtask", "-under", "3")
	for _, args := range [][]string{
		{"-under", "9"},                    // unknown
		{"-under", "2"},                    // closed
		{"-level", "task", "-under", "1"},  // a task under an epic
		{"-level", "epic", "-under", "1"},  // an epic with a parent
		{"-level", "subtask"},              // a subtask without one
		{"-under", "4"},                    // nothing goes under a subtask
		{"-level", "story", "-under", "3"}, // a story under a task
	} {
		lvRefused(t, root, append([]string{"task", "add", "-text", "x"}, args...)...)
	}
	if _, errOut, _ := rdRun(t, root, "task", "add", "-text", "x", "-level", "task", "-under", "1"); !strings.Contains(errOut, "task goes under story, not under epic; nothing stored") {
		t.Errorf("a task under an epic: %q", errOut)
	}
	if _, errOut, _ := rdRun(t, root, "task", "add", "-text", "x", "-under", "4"); !strings.Contains(errOut, "subtask goes under task, not under subtask") {
		t.Errorf("under a subtask: %q", errOut)
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task"); n != "4" {
		t.Errorf("%s tasks, want 4", n)
	}
}

// SPEC T11: task under moves an open task under another open parent, or out with none (not a subtask), and records
// when and the session; refusals change nothing.
func TestTaskMoveUnder(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "story A", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "story B", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "loose")
	tkTrace(t, root, "task", "add", "-text", "under A", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "a subtask", "-under", "4")
	if out := tkTrace(t, root, "task", "under", "3", "1"); !strings.Contains(out, "task 3: under task 1") {
		t.Errorf("task under prints %q", out)
	}
	tkTrace(t, root, "task", "under", "4", "2")
	if out := tkTrace(t, root, "task", "under", "3", "none"); !strings.Contains(out, "task 3: under none") {
		t.Errorf("task under none prints %q", out)
	}
	lvRefused(t, root, "task", "under", "5", "none") // a subtask needs a parent
	lvRefused(t, root, "task", "under", "1", "2")    // a story under a story
	lvRefused(t, root, "task", "under", "4", "4")    // under itself
	lvRefused(t, root, "task", "under", "9", "1")
	lvRefused(t, root, "task", "under", "4")
	if got, want := lvTasks(t, root), "1:story:0;2:story:0;3:task:0;4:task:2;5:subtask:4"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task where moved_at is not null"); n != "2" {
		t.Errorf("%s tasks with a move recorded, want 2", n)
	}
}

// SPEC T12: done and drop refuse while a child is open, naming each open child, and change nothing.
func TestParentCloseRefusedWithOpenChildren(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "write it", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "send it", "-under", "1")
	tkTrace(t, root, "task", "done", "2")
	for _, how := range []string{"done", "drop"} {
		_, errOut, code := rdRun(t, root, "task", how, "1")
		if code != 1 || !strings.Contains(errOut, `task 3 (task): "send it"`) || strings.Contains(errOut, "write it") {
			t.Errorf("%s with an open child: exit %d, %q", how, code, errOut)
		}
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task where closed_at is null"); n != "2" {
		t.Errorf("%s open tasks, want 2", n)
	}
	tkTrace(t, root, "task", "drop", "3")
	if out := tkTrace(t, root, "task", "done", "1"); !strings.Contains(out, "task 1 closed: done") {
		t.Errorf("no open child left, done prints %q", out)
	}
}

// SPEC T13: task list prints the open tasks as a tree, children indented under their parent, with their level.
func TestTaskListTree(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "get a job", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "loose one")
	tkTrace(t, root, "task", "add", "-text", "the CV", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "write it", "-under", "3")
	tkTrace(t, root, "task", "add", "-text", "LinkedIn", "-under", "1")
	var tasks []string
	for _, line := range strings.Split(strings.TrimRight(tkTrace(t, root, "task", "list"), "\n"), "\n") {
		head := line[:strings.Index(line, " ago")]
		tasks = append(tasks, head[:strings.LastIndex(head, ", ")])
	}
	want := []string{"- task 1 (epic)", "  - task 3 (story, in epic 1)", "    - task 4 (task, in story 3)", "  - task 5 (story, in epic 1)", "- task 2"}
	if strings.Join(tasks, "\n") != strings.Join(want, "\n") {
		t.Errorf("list:\n%s\nwant\n%s", strings.Join(tasks, "\n"), strings.Join(want, "\n"))
	}
}

// SPEC T13: the start block, the prompt hook, the alarm's message and task start name the level and the parent;
// an open parent never makes a child not ready.
func TestLevelShownEverywhere(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story", "-needs", "a decision")
	tkTrace(t, root, "task", "add", "-text", "write it", "-under", "1", "-at", "2026-11-12T10:00:00Z")
	const tag = "task 2 (task, in story 1)"
	if out, _, code := rdRun(t, root, "task", "start", "2"); code != 0 || !strings.Contains(out, tag+" is ready") {
		t.Errorf("start 2: exit %d, %q", code, out)
	}
	db := tkStore(t, root)
	var block strings.Builder
	if err := taskSection(db, &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block.String(), "- "+tag+",") {
		t.Errorf("start block:\n%s", block.String())
	}
	due, err := toFire(db, "2026-11-12T10:00:00Z")
	if err != nil || len(due) != 1 {
		t.Fatalf("due %v, %v", due, err)
	}
	if msg := alarmMessage(due[0], "trace-alarm:2:0123456789abcdef", 0); !strings.Contains(msg, "TRACE alarm: "+tag) {
		t.Errorf("alarm message:\n%s", msg)
	}
	alarmAt(t, "2026-11-12T10:00:00Z")
	if lines, err := nameUntold(db, "s9"); err != nil || len(lines) != 1 || !strings.Contains(lines[0], tag) {
		t.Errorf("prompt hook lines %q, %v", lines, err)
	}
}

// SPEC T13: the start block tells the agent about levels and that they are never named to the user.
func TestStartBlockTellsAboutLevels(t *testing.T) {
	var b strings.Builder
	if err := taskSection(rzDB(t), &b, "the user"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-level", "-under", "task under", "task level", "scrum master", "never name"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("start block lacks %q:\n%s", want, b.String())
		}
	}
}
