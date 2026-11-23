package main

import (
	"fmt"
	"strings"
	"testing"
)

// ptTasks is every task as id:kind:points ("-" for none), in id order.
func ptTasks(t *testing.T, root string) string {
	t.Helper()
	return mutStr(t, tkStore(t, root), `select coalesce(group_concat(id || ':' || coalesce(kind, '-') || ':' || coalesce(points, '-'), ';'), '')
		from (select * from task order by id)`)
}

// ptListed is task list's lines cut before " ago", the task and its tag.
func ptListed(t *testing.T, root string) []string {
	t.Helper()
	var heads []string
	for _, line := range strings.Split(strings.TrimRight(tkTrace(t, root, "task", "list"), "\n"), "\n") {
		head := line[:strings.Index(line, " ago")]
		heads = append(heads, strings.TrimSpace(head[:strings.LastIndex(head, ", ")]))
	}
	return heads
}

// SPEC T14: -kind sets a story's kind, story when not given, and task kind changes it on an open story.
func TestTaskKinds(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "login fails", "-level", "story", "-kind", "bug")
	tkTrace(t, root, "task", "add", "-text", "update Go", "-level", "story", "-kind", "chore")
	tkTrace(t, root, "task", "add", "-text", "which ATS", "-level", "story", "-kind", "spike")
	tkTrace(t, root, "task", "add", "-text", "no access", "-level", "story", "-kind", "impediment")
	if out := tkTrace(t, root, "task", "kind", "1", "spike"); !strings.Contains(out, "task 1: spike") {
		t.Errorf("task kind prints %q", out)
	}
	tkTrace(t, root, "task", "kind", "2", "story")
	if got, want := ptTasks(t, root), "1:spike:-;2:-:-;3:chore:-;4:spike:-;5:impediment:-"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
}

// SPEC T14: a kind on any level but story, any other word, and task kind on a closed or unknown task are refused and
// change nothing.
func TestTaskKindRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "a story", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "a task")
	tkTrace(t, root, "task", "add", "-text", "closed", "-level", "story")
	tkTrace(t, root, "task", "drop", "3")
	for _, args := range [][]string{
		{"task", "add", "-text", "x", "-kind", "bug"},                     // a task
		{"task", "add", "-text", "x", "-level", "epic", "-kind", "bug"},   // an epic
		{"task", "add", "-text", "x", "-level", "story", "-kind", "saga"}, // not a kind
		{"task", "kind", "2", "bug"},                                      // a task
		{"task", "kind", "1", "saga"},                                     // not a kind
		{"task", "kind", "1", "spike"},                                    // it has points
		{"task", "kind", "3", "bug"},                                      // closed
		{"task", "kind", "9", "bug"},                                      // unknown
		{"task", "kind", "1"},                                             // no kind
	} {
		lvRefused(t, root, args...)
	}
	if _, errOut, _ := rdRun(t, root, "task", "add", "-text", "x", "-level", "story", "-kind", "saga"); !strings.Contains(errOut, `"saga" is not a kind: story, bug, chore, spike or impediment; nothing stored`) {
		t.Errorf("not a kind: %q", errOut)
	}
	if _, errOut, _ := rdRun(t, root, "task", "kind", "2", "bug"); !strings.Contains(errOut, "only a story has a kind; nothing changed") {
		t.Errorf("a kind on a task: %q", errOut)
	}
	if got, want := ptTasks(t, root), "1:-:3;2:-:-;3:-:-"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
}

// SPEC T15: -points sets a story's, a bug's or a chore's points on the Fibonacci scale, and task points changes them.
func TestTaskPoints(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "login fails", "-level", "story", "-kind", "bug", "-points", "1")
	tkTrace(t, root, "task", "add", "-text", "update Go", "-level", "story", "-kind", "chore", "-points", "13")
	if out := tkTrace(t, root, "task", "points", "1", "5"); !strings.Contains(out, "task 1: 5 points") {
		t.Errorf("task points prints %q", out)
	}
	if got, want := ptTasks(t, root), "1:-:5;2:bug:1;3:chore:13"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
	want := []string{"- task 1 (story, 5 points)", "- task 2 (story, 1 point)", "- task 3 (story, 13 points)"}
	if got := ptListed(t, root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("list:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// SPEC T15: points off the scale, on any level but story, on a spike or an impediment, and a change that would leave
// them there, are refused and change nothing.
func TestTaskPointsRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "a story", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "a spike", "-level", "story", "-kind", "spike")
	tkTrace(t, root, "task", "add", "-text", "a task")
	for _, args := range [][]string{
		{"task", "add", "-text", "x", "-level", "story", "-points", "4"},                        // off the scale
		{"task", "add", "-text", "x", "-level", "story", "-points", "0"},                        // off the scale
		{"task", "add", "-text", "x", "-level", "story", "-points", "three"},                    // not a number
		{"task", "add", "-text", "x", "-points", "3"},                                           // a task
		{"task", "add", "-text", "x", "-level", "epic", "-points", "3"},                         // an epic
		{"task", "add", "-text", "x", "-level", "story", "-kind", "spike", "-points", "2"},      // a spike
		{"task", "add", "-text", "x", "-level", "story", "-kind", "impediment", "-points", "2"}, // an impediment
		{"task", "points", "2", "3"},                                                            // a spike
		{"task", "points", "3", "3"},                                                            // a task
		{"task", "points", "1", "21"},                                                           // off the scale
		{"task", "points", "9", "3"},                                                            // unknown
		{"task", "level", "1", "task"},                                                          // would leave points on a task
	} {
		lvRefused(t, root, args...)
	}
	if _, errOut, _ := rdRun(t, root, "task", "add", "-text", "x", "-level", "story", "-points", "4"); !strings.Contains(errOut, "4 is not a point: 1, 2, 3, 5, 8 or 13; nothing stored") {
		t.Errorf("off the scale: %q", errOut)
	}
	if _, errOut, _ := rdRun(t, root, "task", "points", "2", "3"); !strings.Contains(errOut, "a spike has no points: bound it by its time (-at); nothing changed") {
		t.Errorf("points on a spike: %q", errOut)
	}
	if _, errOut, _ := rdRun(t, root, "task", "add", "-text", "x", "-points", "3"); !strings.Contains(errOut, "only a story carries points; nothing stored") {
		t.Errorf("points on a task: %q", errOut)
	}
	if got, want := ptTasks(t, root), "1:-:3;2:spike:-;3:-:-"; got != want {
		t.Errorf("tasks %q, want %q", got, want)
	}
	if got := lvTasks(t, root); got != "1:story:0;2:story:0;3:task:0" {
		t.Errorf("levels %q", got)
	}
}

// SPEC T15: an epic's points are the sum of its stories', open or done; a dropped story does not count.
func TestEpicPointsSum(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "get a job", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "the CV", "-under", "1", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "LinkedIn", "-under", "1", "-points", "5")
	tkTrace(t, root, "task", "add", "-text", "done one", "-under", "1", "-points", "2")
	tkTrace(t, root, "task", "add", "-text", "dropped one", "-under", "1", "-points", "8")
	tkTrace(t, root, "task", "add", "-text", "an empty epic", "-level", "epic")
	tkTrace(t, root, "task", "done", "4")
	tkTrace(t, root, "task", "drop", "5")
	want := []string{"- task 1 (epic, 10 points)", "- task 2 (story, 3 points, in epic 1)", "- task 3 (story, 5 points, in epic 1)", "- task 6 (epic)"}
	if got := ptListed(t, root); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("list:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if out, _, _ := rdRun(t, root, "task", "start", "2"); !strings.Contains(out, "task 2 (story, 3 points, in epic 1) is ready") {
		t.Errorf("start 2 prints %q", out)
	}
}

// SPEC T15: task sizes prints the last 20 stories closed as done with points, newest first, each with its points, its
// kind and how long it took from added to done; nothing else.
func TestTaskSizes(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	if out := tkTrace(t, root, "task", "sizes"); !strings.Contains(out, "No story closed as done with points yet.") {
		t.Errorf("no sizes prints %q", out)
	}
	tkTrace(t, root, "task", "add", "-text", "the CV", "-level", "story", "-points", "3")
	tkTrace(t, root, "task", "add", "-text", "login fails", "-level", "story", "-kind", "bug", "-points", "5")
	tkTrace(t, root, "task", "add", "-text", "no points", "-level", "story")
	tkTrace(t, root, "task", "add", "-text", "dropped", "-level", "story", "-points", "8")
	tkTrace(t, root, "task", "add", "-text", "open", "-level", "story", "-points", "2")
	for _, id := range []string{"1", "2", "3"} {
		tkTrace(t, root, "task", "done", id)
	}
	tkTrace(t, root, "task", "drop", "4")
	db := tkStore(t, root)
	for _, s := range []string{
		`update task set created_at = '2026-11-10T10:00:00Z', closed_at = '2026-11-10T12:30:00Z' where id = 1`,
		`update task set created_at = '2026-11-11T10:00:00Z', closed_at = '2026-11-12T10:00:00Z' where id = 2`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.TrimRight(tkTrace(t, root, "task", "sizes"), "\n")
	want := "- task 2 (bug, 5 points): \"login fails\", 24h0m0s from added to done\n- task 1 (story, 3 points): \"the CV\", 2h30m0s from added to done"
	if got != want {
		t.Errorf("sizes:\n%s\nwant\n%s", got, want)
	}
	for i := 0; i < 20; i++ {
		if _, err := db.Exec(`insert into task(text, created_at, closed_at, closed_how, level, points) values (?, '2026-11-13T10:00:00Z', ?, 'done', 'story', 2)`,
			fmt.Sprintf("later %d", i), fmt.Sprintf("2026-11-13T11:%02d:00Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimRight(tkTrace(t, root, "task", "sizes"), "\n"), "\n")
	if len(lines) != 20 || !strings.Contains(lines[0], `"later 19"`) || strings.Contains(strings.Join(lines, "\n"), "login fails") {
		t.Errorf("sizes, 22 done:\n%s", strings.Join(lines, "\n"))
	}
}

// SPEC T15, B1: trace task help tells the agent about kinds and points, and that it never says them to the user.
func TestStartBlockTellsAboutPoints(t *testing.T) {
	var b strings.Builder
	if err := taskHelp(&b, "the user"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-kind", "-points", "task kind", "task points", "task sizes", "split", "never say kinds or points"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("start block lacks %q:\n%s", want, b.String())
		}
	}
}
