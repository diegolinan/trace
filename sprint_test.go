package main

import (
	"strings"
	"testing"
)

// spStories adds open stories with the given points, one per text, and returns the root.
func spRoot(t *testing.T) string {
	t.Helper()
	root := tkRoot(t, mutLines(mutUser("u1", 0, "para el viernes, el CV y LinkedIn")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "LinkedIn", "-level", "story", "-points", "5")
	tkTrace(t, root, "task", "add", "-text", "outside", "-level", "story", "-points", "2")
	tkTrace(t, root, "task", "add", "-text", "a plain task")
	return root
}

// spInSprint is every task as id:sprint ("-" for none), in id order.
func spInSprint(t *testing.T, root string) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select group_concat(id || ':' || coalesce(sprint_id, '-'), ';') from (select * from task order by id)`)
}

// SPEC T18: task sprint stores the sprint's end, goal and stories, and adds its review as a task due at the end.
func TestSprintOpen(t *testing.T) {
	root := spRoot(t)
	out, errOut, code := rdRun(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "para el viernes", "1", "2")
	if code != 0 || !strings.Contains(out, "sprint 1 open: 2 stories; its review is task 5, due ") || strings.Contains(errOut, "alarm did not start") {
		t.Errorf("sprint: exit %d, %q, %q", code, out, errOut)
	}
	if got := spInSprint(t, root); got != "1:1;2:1;3:-;4:-;5:-" {
		t.Errorf("in sprint %q", got)
	}
	db := tkStore(t, root)
	if got := mutStr(t, db, `select goal || ':' || review_task || ':' || (closed_at is null) from sprint`); got != "para el viernes:5:1" {
		t.Errorf("sprint row %q", got)
	}
	if got := mutStr(t, db, `select text || ':' || due || ':' || self from task where id = 5`); got != "review the sprint: para el viernes:2026-12-04T18:00:00Z:1" {
		t.Errorf("review task %q", got)
	}
	tkTrace(t, root, "task", "sprint", "add", "3")
	tkTrace(t, root, "task", "sprint", "out", "1")
	if got := spInSprint(t, root); got != "1:-;2:1;3:1;4:-;5:-" {
		t.Errorf("after add and out %q", got)
	}
}

// SPEC T18: a sprint needs an end, a goal and open stories; one is open at a time; add, out and close refuse what
// they cannot do; refusals change nothing.
func TestSprintRefusals(t *testing.T) {
	root := spRoot(t)
	tkTrace(t, root, "task", "add", "-text", "closed", "-level", "story")
	tkTrace(t, root, "task", "drop", "5")
	for _, args := range [][]string{
		{"task", "sprint", "-text", "goal", "1"},                                // no end
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "1"},                  // no goal
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal"},      // no story
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "4"}, // not a story
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "5"}, // closed
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "x"}, // not an id
		{"task", "sprint", "add", "1"},                                          // no sprint open
		{"task", "sprint", "close"},                                             // no sprint open
	} {
		lvRefused(t, root, args...)
	}
	if _, errOut, code := rdRun(t, root, "task", "sprint"); code != 1 || !strings.Contains(errOut, "a sprint needs its end (-at); nothing stored") {
		t.Errorf("sprint alone: exit %d, %q", code, errOut)
	}
	if n := mutStr(t, tkStore(t, root), `select count(*) from sprint`); n != "0" {
		t.Fatalf("%s sprints after refusals", n)
	}
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "1")
	for _, args := range [][]string{
		{"task", "sprint", "-at", "2026-12-05T18:00:00Z", "-text", "another", "2"}, // one open at a time
		{"task", "sprint", "add", "4"},                                             // not a story
		{"task", "sprint", "add", "1"},                                             // already in it
		{"task", "sprint", "out", "2"},                                             // not in it
		{"task", "sprint", "out"},                                                  // no id
		{"task", "sprint", "what", "1"},                                            // not a sprint command
	} {
		lvRefused(t, root, args...)
	}
	if _, errOut, _ := rdRun(t, root, "task", "sprint", "-at", "2026-12-05T18:00:00Z", "-text", "another", "2"); !strings.Contains(errOut, `sprint 1 is open ("goal"); close it first (task sprint close); nothing stored`) {
		t.Errorf("a second sprint: %q", errOut)
	}
	if got := spInSprint(t, root); got != "1:1;2:-;3:-;4:-;5:-;6:-" {
		t.Errorf("in sprint %q", got)
	}
}

// SPEC T18: inside a sprint its stories are not held to one in progress; outside it, kanban's one story still holds.
func TestSprintLiftsWIP(t *testing.T) {
	root := spRoot(t)
	tkTrace(t, root, "task", "add", "-text", "outside too", "-level", "story")
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "1", "2")
	tkTrace(t, root, "task", "start", "1")
	tkTrace(t, root, "task", "start", "2")
	tkTrace(t, root, "task", "start", "3")
	if _, errOut, code := rdRun(t, root, "task", "start", "5"); code != 1 || !strings.Contains(errOut, "task 3 (story, 2 points, in progress) is in progress") {
		t.Errorf("a second story outside the sprint: exit %d, %q", code, errOut)
	}
	if got := pgStarted(t, root); got != "1:1;2:1;3:1;4:0;5:0;6:0" {
		t.Errorf("started %q", got)
	}
}

// SPEC T18: the start block shows the open sprint: goal, end, stories and points done and left.
func TestSprintShown(t *testing.T) {
	root := spRoot(t)
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "para el viernes", "1", "2")
	tkTrace(t, root, "task", "done", "1")
	var block strings.Builder
	if err := taskSection(tkStore(t, root), &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	if b := block.String(); !strings.Contains(b, "task sprint -at") || !strings.Contains(b, "task sprint close") {
		t.Errorf("start block does not tell about sprints:\n%s", b)
	}
	if b := block.String(); !strings.Contains(b, `### Sprint: "para el viernes", until `) || !strings.Contains(b, "1 of 2 stories done, 3 of 8 points; left: task 2") {
		t.Errorf("start block:\n%s", b)
	}
}

// SPEC T18: at its end the sprint's review fires like any task.
func TestSprintEndFires(t *testing.T) {
	root := spRoot(t)
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "para el viernes", "1")
	due, err := toFire(tkStore(t, root), "2026-12-04T18:00:00Z")
	if err != nil || len(due) != 1 || due[0].id != 5 || !strings.Contains(due[0].text, "review the sprint: para el viernes") {
		t.Errorf("due %+v, %v", due, err)
	}
}

// SPEC T18: task sprint close refuses while a story in it is open, naming each, so the agent asks the user about each;
// then it closes the sprint and its review, and a new sprint can open.
func TestSprintClose(t *testing.T) {
	root := spRoot(t)
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "goal", "1", "2")
	tkTrace(t, root, "task", "done", "1")
	_, errOut, code := rdRun(t, root, "task", "sprint", "close")
	if code != 1 || !strings.Contains(errOut, `sprint 1 has open stories: task 2 (story): "LinkedIn"; ask the user about each one: done, back to the backlog (task sprint out <id>) or into the next sprint; nothing changed`) {
		t.Errorf("close with a story open: exit %d, %q", code, errOut)
	}
	tkTrace(t, root, "task", "sprint", "out", "2")
	if out := tkTrace(t, root, "task", "sprint", "close"); !strings.Contains(out, "sprint 1 closed: 1 of 1 stories done, 3 of 3 points") {
		t.Errorf("close prints %q", out)
	}
	db := tkStore(t, root)
	if got := mutStr(t, db, `select (closed_at is not null) || ':' || closed_session from sprint`); got != "1:s1" {
		t.Errorf("sprint closed %q", got)
	}
	if got := mutStr(t, db, `select closed_how from task where id = 5`); got != "done" {
		t.Errorf("review closed %q", got)
	}
	tkTrace(t, root, "task", "sprint", "-at", "2026-12-11T18:00:00Z", "-text", "next", "2")
}
