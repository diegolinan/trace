package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// riLine is one transcript or rollout line: v as JSON, ended by a newline (a line without one is still being written).
func riLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func riWrite(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func riDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// A factor or budget under one is still the user's setting; only zero or less falls back to the defaults.
func TestReinjectLimitsKeepSmallSettings(t *testing.T) {
	if f, b := (&reinjectCfg{Factor: 0.5, Budget: 1}).limits(); f != 0.5 || b != 1 {
		t.Fatalf("limits %v, %d; want 0.5, 1", f, b)
	}
}

// riClaudeRecord is a Claude Code attachment record of type typ listing files (path, content) as loaded.
func riClaudeRecord(t *testing.T, typ string, files ...[2]string) string {
	fs := []map[string]string{}
	for _, f := range files {
		fs = append(fs, map[string]string{"path": f[0], "type": "Project", "content": f[1]})
	}
	return riLine(t, map[string]any{"type": "attachment", "source": "instructions", "attachment": map[string]any{"type": typ, "files": fs}})
}

// SPEC G8: the card's files are the last instructions record's, a record with one file included; a later line that is
// not such a record, even one that names "instructions", leaves them as they were.
func TestClaudeSourcesIgnoreLinesThatAreNotARecord(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone", "CLAUDE.md") // not on disk: the recorded text is the card's
	base := riClaudeRecord(t, "instructions", [2]string{gone, "rule as loaded"})
	other := filepath.Join(dir, "other", "CLAUDE.md")
	for name, later := range map[string]string{
		"no later line":       "",
		"a user line":         riLine(t, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hola"}}),
		"another attachment":  riClaudeRecord(t, "nested_memory", [2]string{other, "decoy text"}),
		"a record of no file": riClaudeRecord(t, "instructions"),
		"broken json":         `{"type":"attachment","attachment":{"type":"instructions","files":[` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			tr := riWrite(t, d, "t.jsonl", base, later)
			got := claudeSources(riDB(t, d), tr)
			if len(got) != 1 || got[0].label != filepath.ToSlash(gone) || got[0].text != "rule as loaded" {
				t.Fatalf("sources %+v, want only %s with its recorded text", got, gone)
			}
		})
	}
}

// riCodexMessage is a rollout response_item of payload type ptype from role whose text is text.
func riCodexMessage(t *testing.T, outer, ptype, role, text string) string {
	return riLine(t, map[string]any{"type": outer, "payload": map[string]any{"type": ptype, "role": role,
		"content": []any{map[string]any{"type": "input_text", "text": text}}}})
}

func riAgentsText(rule string) string {
	return "# AGENTS.md instructions for D:/p\n\n<INSTRUCTIONS>\n" + rule + "\n</INSTRUCTIONS>"
}

// The card's Codex text is the last AGENTS.md message the user role got; a later line with the same text that is
// not that message (another kind of item, another payload, another role) or a user message that only mentions
// AGENTS.md leaves it as it was.
func TestCodexSourcesIgnoreLinesThatAreNotTheMessage(t *testing.T) {
	base := riCodexMessage(t, "response_item", "message", "user", riAgentsText("base rule"))
	decoy := riAgentsText("decoy rule")
	for name, later := range map[string]string{
		"no later line":        "",
		"an event":             riCodexMessage(t, "event_msg", "message", "user", decoy),
		"a reasoning item":     riCodexMessage(t, "response_item", "reasoning", "user", decoy),
		"the developer's":      riCodexMessage(t, "response_item", "message", "developer", decoy),
		"a mention in prose":   riCodexMessage(t, "response_item", "message", "user", "read the AGENTS.md instructions for this repo first"),
		"a broken json line":   `{"type":"response_item","payload":{"text":"AGENTS.md instructions for x"` + "\n",
		"an assistant message": riCodexMessage(t, "response_item", "message", "assistant", decoy),
	} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			rollout := riWrite(t, d, "rollout.jsonl", base, later)
			got := codexSources(riDB(t, d), rollout)
			if len(got) != 1 || got[0].text != "base rule" {
				t.Fatalf("sources %+v, want only the base rule", got)
			}
		})
	}
}

// Each call reads only what the host appended since the last one: with nothing new, no line is read again and the
// stored record is the answer.
func TestLastRecordReadsOnlyWhatWasAppended(t *testing.T) {
	d := t.TempDir()
	p := riWrite(t, d, "rollout.jsonl", "keep one\n", "skip\n", "keep two\n")
	db := riDB(t, d)
	seen := 0
	match := func(line []byte) (string, bool) {
		seen++
		s := strings.TrimSpace(string(line))
		return s, strings.HasPrefix(s, "keep")
	}
	if got := lastRecord(db, "codex_instructions", "rollout", "text", p, match); got != "keep two" || seen != 3 {
		t.Fatalf("first read %q after %d lines, want %q after 3", got, seen, "keep two")
	}
	seen = 0
	if got := lastRecord(db, "codex_instructions", "rollout", "text", p, match); got != "keep two" || seen != 0 {
		t.Fatalf("second read %q after %d lines, want %q after none", got, seen, "keep two")
	}
}

// riTokenCount is a Codex token_count event whose last call held total tokens.
func riTokenCount(t *testing.T, total int) string {
	return riLine(t, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count",
		"info": map[string]any{"last_token_usage": map[string]any{"total_tokens": total}}}})
}

// Codex's context is the last token_count with a positive total, the rollout's first line included.
func TestCodexContextTokens(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		want  int
	}{
		{"the only line", []string{riTokenCount(t, 4321)}, 4321},
		{"a later total of zero is skipped", []string{riTokenCount(t, 5000), riTokenCount(t, 0)}, 5000},
		{"a later total of one counts", []string{riTokenCount(t, 5000), riTokenCount(t, 1)}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := riWrite(t, t.TempDir(), "rollout.jsonl", c.lines...)
			if n := contextTokens(p, "codex"); n != c.want {
				t.Fatalf("context %d, want %d", n, c.want)
			}
		})
	}
}

// riOverNote is the note of a card whose files hold total characters, over budget.
func riOverNote(total, budget int) string {
	return fmt.Sprintf("the instruction files hold %d characters, over the card's %d, so only text between %s and %s goes in", total, budget, rulesOpen, rulesClose)
}

// Files that fill the budget exactly still go in whole, unmarked text included; only the card's own heading pushes
// it over, so it is cut and the note says only that.
func TestCardFilesExactlyAtBudgetGoInWhole(t *testing.T) {
	text := "plain prose rule with no markers at all"
	got, note := card([]source{{"A.md", text}}, "", len(text))
	if !strings.Contains(got, "### From A.md\nplain prose") || note != "the card was cut at the budget" {
		t.Fatalf("card %q, note %q", got, note)
	}
}

// Over the budget, a block at the very start of a file goes in.
func TestCardBlockAtFileStart(t *testing.T) {
	src := rulesOpen + "\nkeep this rule\n" + rulesClose + "\n" + strings.Repeat("prose ", 40)
	got, note := card([]source{{"A.md", src}}, "", 100)
	if !strings.Contains(got, "### From A.md\nkeep this rule\n") || note != riOverNote(len(src), 100) {
		t.Fatalf("card %q, note %q", got, note)
	}
}

// Over the budget, an empty block adds nothing, and the text after it until the next block stays out.
func TestCardEmptyBlockKeepsTheProseOut(t *testing.T) {
	src := rulesOpen + rulesClose + "\nintro prose\n" + rulesOpen + "\nreal rule\n" + rulesClose
	got, _ := card([]source{{"A.md", src}}, "", 60)
	if !strings.Contains(got, "real rule") || strings.Contains(got, "intro prose") || strings.Contains(got, rulesClose) {
		t.Fatalf("card %q", got)
	}
}

// Over the budget with every file marked, the note names no file as unmarked.
func TestCardAllFilesMarkedNoteNamesNone(t *testing.T) {
	a := "prose\n" + rulesOpen + "\nrule a\n" + rulesClose
	b := rulesOpen + "\nrule b\n" + rulesClose + "\nprose"
	got, note := card([]source{{"A.md", a}, {"B.md", b}}, "", 60)
	if !strings.Contains(got, "rule a") || !strings.Contains(got, "rule b") || note != riOverNote(len(a)+len(b), 60) {
		t.Fatalf("card %q, note %q", got, note)
	}
}

// A card body exactly at the budget is not cut; one over it is cut and the note says so, after the files' note.
func TestCardCutAtBudget(t *testing.T) {
	rules := strings.Repeat("r", 100)
	if got, note := card(nil, rules, 100); !strings.Contains(got, "\n\n"+rules+"\nEverything said before") || note != "" {
		t.Fatalf("at the budget: card %q, note %q", got, note)
	}
	got, note := card(nil, rules+"tail", 100)
	if !strings.Contains(got, "\n\n"+rules+"…\nEverything said before") || note != "the card was cut at the budget" {
		t.Fatalf("over the budget: card %q, note %q", got, note)
	}
	src := rulesOpen + "\n" + strings.Repeat("k", 80) + "\n" + rulesClose + "\nprose"
	_, note = card([]source{{"A.md", src}}, "", 50)
	if want := riOverNote(len(src), 50) + "; the card was cut at the budget"; note != want {
		t.Fatalf("files and cut: note %q, want %q", note, want)
	}
}

// A call with no card, no context size or no session decides nothing and records nothing.
func TestReinjectionNeedsCardContextAndSession(t *testing.T) {
	for _, c := range []struct {
		name, sid, text string
		ctx             int
	}{
		{"no card", "s", "", 1000},
		{"no context", "s", "rule", 0},
		{"no session", "", "rule", 1000},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := riDB(t, t.TempDir())
			out, reason, err := reinjection(db, c.sid, c.ctx, c.text, 7, time.Now())
			if err != nil || out != "" || reason != "" {
				t.Fatalf("got %q %q %v, want nothing", out, reason, err)
			}
			if n := count(t, db, "select count(*) from injection"); n != 0 {
				t.Fatalf("%d records, want none", n)
			}
		})
	}
}

// The same context size again is no compaction: nothing for the same card, its new lines for a changed one.
func TestReinjectionSameContextIsNoReset(t *testing.T) {
	db := riDB(t, t.TempDir())
	now := time.Now()
	if _, reason, err := reinjection(db, "s", 1000, "rule one", 7, now); err != nil || reason != "start" {
		t.Fatalf("first call: %q %v", reason, err)
	}
	if out, reason, err := reinjection(db, "s", 1000, "rule one", 7, now); err != nil || out != "" || reason != "" {
		t.Fatalf("same size, same card: %q %q %v", out, reason, err)
	}
	out, reason, err := reinjection(db, "s", 1000, "rule one\nrule two", 7, now)
	if err != nil || reason != "change" || !strings.HasSuffix(out, "\nrule two") {
		t.Fatalf("same size, changed card: %q %q %v", out, reason, err)
	}
	if n := count(t, db, "select count(*) from injection where reason = 'reset'"); n != 0 {
		t.Fatalf("%d reset records, want none", n)
	}
}
