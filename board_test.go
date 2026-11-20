package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC T24: each open task in its column with what it waits on and its criteria, recently closed ones apart, every
// text escaped, and the page opened.
func TestTaskBoardPage(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "acordate de llamar al banco")))
	tkTrace(t, root, "task", "add", "-level", "epic", "-text", "Launch")
	tkTrace(t, root, "task", "add", "-level", "story", "-under", "1", "-kind", "bug", "-points", "3", "-text", "fix <script>alert(1)</script>", "-then", "the page loads")
	tkTrace(t, root, "task", "add", "-text", "waits for Diego", "-needs", "Diego's ok", "-needs", "the credit")
	tkTrace(t, root, "task", "add", "-text", "plain to do")
	tkTrace(t, root, "task", "add", "-text", "already finished")
	tkTrace(t, root, "task", "start", "2")
	tkTrace(t, root, "task", "done", "5")
	tkTrace(t, root, "task", "add", "-text", "closed long ago")
	tkTrace(t, root, "task", "add", "-text", "closed last week")
	tkTrace(t, root, "task", "drop", "6")
	tkTrace(t, root, "task", "drop", "7")
	db := tkStore(t, root)
	ago := func(days int) string {
		return clockNow().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	}
	db.Exec("update task set closed_at = ? where id = 6", ago(15))
	db.Exec("update task set closed_at = ? where id = 7", ago(13))
	var opened string
	saved := openPage
	openPage = func(p string) error { opened = p; return nil }
	defer func() { openPage = saved }()
	out := filepath.Join(t.TempDir(), "board.html")
	var w strings.Builder
	if err := taskBoard(db, out, &w); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	col := func(name string) string { // the column's html, up to the next column
		i := strings.Index(page, `data-col="`+name+`"`)
		if i < 0 {
			t.Fatalf("no %s column:\n%s", name, page)
		}
		rest := page[i+1:]
		if j := strings.Index(rest, `data-col="`); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	if c := col("progress"); !strings.Contains(c, "fix &lt;script&gt;") || !strings.Contains(c, "the page loads") || !strings.Contains(c, "Launch") || !strings.Contains(c, "bug") {
		t.Errorf("in progress: %s", c)
	}
	if c := col("waiting"); !strings.Contains(c, "waits for Diego") || !strings.Contains(c, "Diego&#39;s ok") || !strings.Contains(c, "the credit") {
		t.Errorf("waiting: %s", c)
	}
	if c := col("todo"); !strings.Contains(c, "plain to do") || !strings.Contains(c, "Launch") || strings.Contains(c, "waits for Diego") {
		t.Errorf("to do: %s", c)
	}
	if c := col("closed"); !strings.Contains(c, "already finished") || !strings.Contains(c, "done") || !strings.Contains(c, "closed last week") || strings.Contains(c, "closed long ago") {
		t.Errorf("closed: %s", c)
	}
	if strings.Contains(page, "<script>alert") {
		t.Error("a task's words ran as markup")
	}
	if !strings.Contains(page, "acordate de llamar al banco") {
		t.Error("the asking words are missing")
	}
	if opened != out || !strings.Contains(w.String(), out) {
		t.Errorf("opened %q, printed %q", opened, w.String())
	}
}
