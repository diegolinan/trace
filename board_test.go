package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC T24: one grid by state with counts, the epic as context and filter, short cards with their markers, the sprint,
// the detail with every condition and criterion, the 14-day window newest first, escaping, fonts, and the page opened.
func TestTaskBoardPage(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "acordate de llamar al banco")))
	tkTrace(t, root, "task", "add", "-level", "epic", "-text", "Launch")
	tkTrace(t, root, "task", "add", "-level", "story", "-under", "1", "-kind", "bug", "-points", "3", "-text", "fix <script>alert(1)</script>", "-then", "the page loads", "-then", "the cards fit")
	tkTrace(t, root, "task", "add", "-text", "waits for Diego", "-needs", "Diego's ok", "-needs", "the credit")
	tkTrace(t, root, "task", "add", "-level", "story", "-text", "use **bold** and `code`")
	tkTrace(t, root, "task", "add", "-text", "already finished")
	tkTrace(t, root, "task", "add", "-text", "closed long ago")
	tkTrace(t, root, "task", "add", "-text", "closed last week")
	tkTrace(t, root, "task", "start", "2")
	tkTrace(t, root, "task", "met", "2", "1")
	tkTrace(t, root, "task", "done", "5")
	tkTrace(t, root, "task", "drop", "6")
	tkTrace(t, root, "task", "drop", "7")
	if _, errOut, code := rdRun(t, root, "task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "para el viernes", "4"); code != 0 {
		t.Fatalf("sprint: %s", errOut)
	}
	tkTrace(t, root, "task", "add", "-text", "pay the rent")
	db := tkStore(t, root)
	ago := func(days int) string {
		return clockNow().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	}
	db.Exec("update task set closed_at = ? where id = 6", ago(15))
	db.Exec("update task set closed_at = ? where id = 7", ago(13))
	db.Exec("update task set due = ?, zone = 'UTC-3' where id = 9", ago(1))
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
	block := func(start, end string) string { // the html from start up to the next end marker
		i := strings.Index(page, start)
		if i < 0 {
			t.Fatalf("no %s:\n%s", start, page)
		}
		rest := page[i+len(start):]
		if j := strings.Index(rest, end); j >= 0 {
			rest = rest[:j]
		}
		return rest
	}
	col := func(name string) string { return block(` data-col="`+name+`" data-count=`, `</section>`) }
	card := func(id string) string { return block(`data-open="d`+id+`"`, `</button>`) }
	if c := col("todo"); !strings.HasPrefix(c, `"3"`) || !strings.Contains(c, `data-open="d4"`) || !strings.Contains(c, `data-open="d8"`) || !strings.Contains(c, `data-open="d9"`) {
		t.Errorf("to do: %s", c)
	}
	if c := col("waiting"); !strings.HasPrefix(c, `"1"`) || !strings.Contains(c, `data-open="d3"`) {
		t.Errorf("waiting: %s", c)
	}
	if c := col("progress"); !strings.HasPrefix(c, `"1"`) || !strings.Contains(c, `data-open="d2"`) {
		t.Errorf("in progress: %s", c)
	}
	c := col("closed")
	if !strings.HasPrefix(c, `"2"`) || strings.Contains(c, "closed long ago") || strings.Index(c, "already finished") > strings.Index(c, "closed last week") {
		t.Errorf("closed, newest first: %s", c)
	}
	if strings.Contains(page, `class="card" data-open="d1"`) || !strings.Contains(page, `<option value="1">Launch</option>`) || !strings.Contains(page, `<option value="none">No epic</option>`) {
		t.Error("the epic is a card, or not a filter")
	}
	for id, want := range map[string][]string{
		"2": {"Launch", "#2", "bug", "3 pts", "fix &lt;script&gt;", "1/2 criteria", `data-epic="1"`, "progress"},
		"3": {"Waits: Diego&#39;s ok", "+1 more", `data-epic="none"`, "waiting"},
		"4": {"<strong>bold</strong>", "<code>code</code>", "No criteria", "nocrit"},
		"5": {"done"},
		"7": {"dropped"},
		"9": {"Overdue, not told", "overdue"},
	} {
		got := card(id)
		for _, s := range want {
			if !strings.Contains(got, s) {
				t.Errorf("card #%s lacks %q: %s", id, s, got)
			}
		}
	}
	for flag, n := range map[string]string{"progress": "1", "waiting": "1", "nocrit": "1", "overdue": "1"} {
		if !strings.Contains(page, `data-filter="`+flag+`" data-n="`+n+`"`) {
			t.Errorf("count of %s is not %s", flag, n)
		}
	}
	if s := block(`<div class="sprint">`, `</div>`); !strings.Contains(s, "para el viernes") || !strings.Contains(s, "2026") {
		t.Errorf("sprint: %s", s)
	}
	if d := block(`data-detail="2"`, "</dialog>"); !strings.Contains(d, "Launch") || !strings.Contains(d, "1 of 2 met") || !strings.Contains(d, "the page loads") || !strings.Contains(d, "Met") || !strings.Contains(d, "the cards fit") || !strings.Contains(d, "Open") {
		t.Errorf("detail of #2: %s", d)
	}
	if d := block(`data-detail="3"`, "</dialog>"); !strings.Contains(d, "Diego&#39;s ok") || !strings.Contains(d, "the credit") || !strings.Contains(d, "acordate de llamar al banco") {
		t.Errorf("detail of #3: %s", d)
	}
	if d := block(`data-detail="8"`, "</dialog>"); !strings.Contains(d, "added by an agent on its own") {
		t.Errorf("detail of #8: %s", d)
	}
	if d := block(`data-detail="1"`, "</dialog>"); !strings.Contains(d, "#2") {
		t.Errorf("the epic's detail lacks its children: %s", d)
	}
	if strings.Contains(page, "<script>alert") {
		t.Error("a task's words ran as markup")
	}
	for _, s := range []string{`>Closed, last 14 days<`, `aria-pressed="false">Overdue, not told<b>1</b>`, `class="col shown" data-col="progress"`, `font-family: "Die Grotesk"; font-weight: 500`} {
		if !strings.Contains(page, s) {
			t.Errorf("page lacks %s", s)
		}
	}
	if d := block(`data-detail="4"`, "</dialog>"); !strings.Contains(d, "Sprint: para el viernes") || strings.Contains(block(`data-detail="2"`, "</dialog>"), "Sprint:") {
		t.Errorf("sprint in the detail: %s", d)
	}
	if !strings.Contains(page, `id="theme"`) || !strings.Contains(page, `:root[data-theme=dark]`) {
		t.Error("no light or dark pick")
	}
	if !strings.Contains(page, "Snapshot of") || !strings.Contains(page, "data:font/woff2;base64,") {
		t.Error("no snapshot time or no embedded font")
	}
	if opened != out || !strings.Contains(w.String(), out) {
		t.Errorf("opened %q, printed %q", opened, w.String())
	}
}

// SPEC T24: one unmet condition is enough to wait; with nothing in progress, a phone starts on to do.
func TestTaskBoardStart(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "hola")))
	tkTrace(t, root, "task", "add", "-text", "needs one thing", "-needs", "the ok")
	saved := openPage
	openPage = func(string) error { return nil }
	defer func() { openPage = saved }()
	out := filepath.Join(t.TempDir(), "board.html")
	if err := taskBoard(tkStore(t, root), out, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	page := string(b)
	if !strings.Contains(page, `class="col shown" data-col="todo" data-count="0"`) || !strings.Contains(page, `data-col="waiting" data-count="1"`) {
		t.Errorf("a phone does not start on to do, or one condition does not wait")
	}
}

// SPEC T24: pasted-content tags leave the asking words; what was pasted is named, not shown whole.
func TestCleanAsked(t *testing.T) {
	in := "board porque tengo este tambien:\n\n<pasted_content id=\"e178\">\nLiquid AI just shipped 6 new models\n</pasted_content id=\"e178\">"
	if got := cleanAsked(in); got != "board porque tengo este tambien: [pasted text]" {
		t.Errorf("got %q", got)
	}
}
