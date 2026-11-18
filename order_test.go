package main

import (
	"strings"
	"testing"
)

// orListed is task list's task ids and indents, as "  - task 3" lines, without tags, ages or words.
func orListed(t *testing.T, root string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimRight(tkTrace(t, root, "task", "list"), "\n"), "\n") {
		head := line[:strings.Index(line, " ago")]
		head = head[:strings.LastIndex(head, ", ")]
		if i := strings.Index(head, " ("); i >= 0 {
			head = head[:i]
		}
		out = append(out, head)
	}
	return strings.Join(out, "\n")
}

// SPEC T17: a task is added last among the open tasks under the same parent, and task order moves it to place n
// there, shifting the rest; the list shows that order.
func TestTaskOrder(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "one")
	tkTrace(t, root, "task", "add", "-text", "two")
	tkTrace(t, root, "task", "add", "-text", "three")
	if out := tkTrace(t, root, "task", "order", "3", "1"); !strings.Contains(out, "task 3: 1 of 3") {
		t.Errorf("task order prints %q", out)
	}
	if got, want := orListed(t, root), "- task 3\n- task 1\n- task 2"; got != want {
		t.Errorf("list:\n%s\nwant\n%s", got, want)
	}
	tkTrace(t, root, "task", "order", "3", "3")
	tkTrace(t, root, "task", "add", "-text", "four")
	if got, want := orListed(t, root), "- task 1\n- task 2\n- task 3\n- task 4"; got != want {
		t.Errorf("list:\n%s\nwant\n%s", got, want)
	}
	tkTrace(t, root, "task", "order", "4", "2")
	if got, want := orListed(t, root), "- task 1\n- task 4\n- task 2\n- task 3"; got != want {
		t.Errorf("list:\n%s\nwant\n%s", got, want)
	}
}

// SPEC T17: the order is among the open tasks under the same parent only; closed ones do not count.
func TestListInOrder(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "get a job", "-level", "epic")
	tkTrace(t, root, "task", "add", "-text", "the CV", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "LinkedIn", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "closed", "-under", "1")
	tkTrace(t, root, "task", "add", "-text", "loose")
	tkTrace(t, root, "task", "drop", "4")
	if out := tkTrace(t, root, "task", "order", "3", "1"); !strings.Contains(out, "task 3: 1 of 2") {
		t.Errorf("task order prints %q", out)
	}
	tkTrace(t, root, "task", "order", "5", "1")
	if got, want := orListed(t, root), "- task 5\n- task 1\n  - task 3\n  - task 2"; got != want {
		t.Errorf("list:\n%s\nwant\n%s", got, want)
	}
	var block strings.Builder
	if err := taskSection(tkStore(t, root), &block, "Ana"); err != nil {
		t.Fatal(err)
	}
	b := block.String()
	if !strings.Contains(b, "task order <id> <n>") || !strings.Contains(b, "never naming the order") {
		t.Errorf("start block does not tell about the order:\n%s", b)
	}
	if !strings.Contains(b, "### Open tasks (most important first, as a tree)\n- task 5, ") ||
		!(strings.Index(b, "- task 1 ") < strings.Index(b, "\n  - task 3 ") && strings.Index(b, "\n  - task 3 ") < strings.Index(b, "\n  - task 2 ")) {
		t.Errorf("start block:\n%s", b)
	}
}

// SPEC T17: task order refuses a closed or unknown task and a place that is not 1 to the number of open tasks there,
// and changes nothing.
func TestTaskOrderRefusals(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "one")
	tkTrace(t, root, "task", "add", "-text", "two")
	tkTrace(t, root, "task", "add", "-text", "closed")
	tkTrace(t, root, "task", "drop", "3")
	for _, args := range [][]string{
		{"task", "order", "1", "0"}, {"task", "order", "1", "3"}, {"task", "order", "1", "first"},
		{"task", "order", "3", "1"}, {"task", "order", "9", "1"}, {"task", "order", "1"},
	} {
		lvRefused(t, root, args...)
	}
	for args, says := range map[string]string{
		"1 3": "place 3 is not 1 to 2, the open tasks there; nothing changed",
		"1 0": "place 0 is not 1 to 2, the open tasks there; nothing changed",
		"9 1": "task 9 is not an open task; nothing changed",
	} {
		if _, errOut, code := rdRun(t, root, append([]string{"task", "order"}, strings.Fields(args)...)...); code != 1 || !strings.Contains(errOut, says) {
			t.Errorf("order %s: exit %d, %q", args, code, errOut)
		}
	}
	if got, want := orListed(t, root), "- task 1\n- task 2"; got != want {
		t.Errorf("list:\n%s\nwant\n%s", got, want)
	}
}
