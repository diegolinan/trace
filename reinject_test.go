package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The sources are the files Claude Code loads, imports included and code skipped; MEMORY.md comes from the
// configured auto-memory folder; a worktree's memory folder is derived from the main repository.
func TestClaudeSources(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	write(filepath.Join(repo, "CLAUDE.md"), "@MANIFEST.md\nsee @docs/a.md too\n```\n@NOT.md\n```\n")
	write(filepath.Join(repo, "MANIFEST.md"), "manifest @CLAUDE.md") // a loop is read once
	write(filepath.Join(repo, "docs", "a.md"), "a rule")
	write(filepath.Join(repo, "NOT.md"), "inside code")
	write(filepath.Join(repo, "CLAUDE.local.md"), "local")
	write(filepath.Join(home, ".claude", "CLAUDE.md"), "user")
	mem := filepath.Join(home, "mem")
	write(filepath.Join(repo, ".claude", "settings.local.json"), `{"autoMemoryDirectory":"`+filepath.ToSlash(mem)+`"}`)
	write(filepath.Join(mem, "MEMORY.md"), "- [x](x.md)\n@NOTANIMPORT.md")
	write(filepath.Join(mem, "NOTANIMPORT.md"), "no")
	var got []string
	for _, s := range claudeSources(filepath.Join(repo, "docs"), home) {
		got = append(got, filepath.Base(s.label))
	}
	if strings.Join(got, " ") != "CLAUDE.md CLAUDE.md MANIFEST.md a.md CLAUDE.local.md MEMORY.md" {
		t.Fatalf("sources: %v", got)
	}
	// A linked worktree: the .git file points into the main repository.
	wt := t.TempDir()
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.ToSlash(repo)+"/.git/worktrees/wt\n")
	if root, main := gitRoots(filepath.Join(wt)); root != wt || filepath.Clean(main) != filepath.Clean(repo) {
		t.Fatalf("worktree roots: %s %s", root, main)
	}
}

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
	if n := contextTokens(p); n != 132 {
		t.Fatalf("context %d, want 132", n)
	}
	if contextTokens(filepath.Join(t.TempDir(), "none")) != 0 {
		t.Fatal("a missing transcript is 0")
	}
}
