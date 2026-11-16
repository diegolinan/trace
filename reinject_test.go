package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The card is whole when it fits; over the budget only marked blocks go in, and the note names the files without.
func TestCard(t *testing.T) {
	srcs := []source{{"A.md", "rule one"}, {"B.md", "intro\n" + rulesOpen + "\nnever push\n" + rulesClose + "\nmore"}}
	text, note := card(srcs, "### What the user refused\n- rule: siempre X\n", 1000)
	if !strings.Contains(text, "### From A.md\nrule one") || !strings.Contains(text, "intro") || !strings.Contains(text, "siempre X") || note != "" {
		t.Fatalf("whole card: %q %q", text, note)
	}
	text, note = card(srcs, "", 40)
	if strings.Contains(text, "rule one") || !strings.Contains(text, "### From B.md\nnever push") || strings.Contains(text, "intro") || !strings.Contains(note, "no marked rules in: A.md") {
		t.Fatalf("over budget: %q %q", text, note)
	}
	if text, _ := card(nil, "", 100); text != "" {
		t.Fatalf("nothing to say must give no card: %q", text)
	}
}

// The card comes back after factor × its tokens of new context, its new lines when it changes, and the count
// restarts when the context shrinks (a compaction).
func TestReinjection(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cardText := strings.Repeat("r", 400) // 100 tokens; factor 7 → every 700 tokens
	now := time.Now()
	step := func(ctx int, text string) (string, string) {
		out, reason, err := reinjection(db, "s", ctx, text, 7, now)
		if err != nil {
			t.Fatal(err)
		}
		return out, reason
	}
	if out, r := step(10000, cardText); out != "" || r != "start" {
		t.Fatalf("first call: baseline only, got %q %q", out, r)
	}
	if out, r := step(10600, cardText); out != "" || r != "" {
		t.Fatalf("under the threshold: %q %q", out, r)
	}
	if out, r := step(10700, cardText); out != cardText || r != "threshold" {
		t.Fatalf("at the threshold: %q", r)
	}
	if out, _ := step(11000, cardText); out != "" {
		t.Fatal("the count restarts at the injection")
	}
	changed := cardText + "\nnew rule"
	if out, r := step(11001, changed); r != "change" || !strings.HasSuffix(out, "\nnew rule") || strings.Contains(out, "rrrr") {
		t.Fatalf("change: only the new line, got %q %q", out, r)
	}
	if out, r := step(3000, changed); out != "" || r != "reset" {
		t.Fatalf("compaction: %q %q", out, r)
	}
	if out, _ := step(3800, changed); out == "" {
		t.Fatal("after a reset the count starts from the new size")
	}
	if n := count(t, db, "select count(*) from injection where session_id = 's'"); n != 5 {
		t.Fatalf("records %d, want 5 (start, threshold, change, reset, threshold; none for the quiet calls)", n)
	}
}

func TestContextTokens(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte(`{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":100,"cache_creation_input_tokens":30}}}
{"type":"user","message":{"content":"x","usage":{"input_tokens":999}}}
`), 0o644)
	if n := contextTokens(p, "claude-code"); n != 132 {
		t.Fatalf("context %d, want 132", n)
	}
	if contextTokens(filepath.Join(t.TempDir(), "none"), "claude-code") != 0 {
		t.Fatal("a missing transcript is 0")
	}
}

// Parallel tool calls fire their hooks at once, each with its own connection: only one of them injects.
func TestReinjectionParallel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("r", 400)
	if _, _, err := reinjection(db, "s", 1000, text, 7, time.Now()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	outs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			d, err := openDB(path)
			if err != nil {
				outs <- "error: " + err.Error()
				return
			}
			defer d.Close()
			out, _, err := reinjection(d, "s", 5000, text, 7, time.Now())
			if err != nil {
				out = "error: " + err.Error()
			}
			outs <- out
		}()
	}
	injected := 0
	for i := 0; i < 8; i++ {
		switch o := <-outs; {
		case strings.HasPrefix(o, "error"):
			t.Fatal(o)
		case o != "":
			injected++
		}
	}
	if injected != 1 {
		t.Fatalf("%d of 8 parallel hooks injected, want 1", injected)
	}
}
