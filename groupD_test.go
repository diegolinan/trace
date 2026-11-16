package main

import (
	"runtime"
	"strings"
	"testing"
)

// A quoted binary path gets the PowerShell form beside it on Windows only; elsewhere the quoted path runs as written
// (SPEC S2).
func TestGDSearchLineQuotedPathFormPerShell(t *testing.T) {
	exe := `"C:\Jane Doe\trace.exe"`
	want := "`" + exe + " search <words>`"
	if runtime.GOOS == "windows" {
		want += " (in PowerShell: `& " + exe + " search <words>`)"
	}
	if got := cmdLine(exe, "", "search", " <words>"); got != want {
		t.Errorf("on %s cmdLine = %s, want %s", runtime.GOOS, got, want)
	}
}

// Output cut at a limit is cut at a character start; a run of bytes with none (binary output) keeps nothing of it
// rather than half a character.
func TestGDClipWithNoCharacterStart(t *testing.T) {
	if got := clip(strings.Repeat("\x80", 10), 5); got != "…" {
		t.Errorf("clip = %q, want only the mark", got)
	}
}

// A tool result with no words leaves no event.
func TestGDToolResultWithoutWords(t *testing.T) {
	for _, content := range []string{`""`, `"  \n "`, `[]`} {
		if p := rzParse(t, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":`+content+`}]}}`); len(p.events) != 0 {
			t.Errorf("content %s yielded %s", content, describe(p))
		}
	}
}

// The start block labels a message that sets a standing rule "rule" and one that refuses something "refused".
func TestGDStartBlockLabelsRules(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	rule, refusal := "nunca uses archivos temporales", "no hace falta usar archivos R"
	if !markText(rule).rule || markText(refusal).rule {
		t.Fatal("fixture: the marks do not read the rule as a rule and the refusal as none")
	}
	rzSession(t, db, "s", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	rzRefusal(t, db, "s", 1, "2026-11-10T08:10:00Z", rule)
	rzRefusal(t, db, "s", 2, "2026-11-10T08:20:00Z", refusal)
	got := rzBlock(t, db, f)
	lines := rzRefusalLines(got)
	if len(lines) != 2 || !strings.Contains(lines[0], "rule: "+rule) || !strings.Contains(lines[1], "refused: "+refusal) {
		t.Errorf("want the rule then the refusal, each labelled, got %q in:\n%s", lines, got)
	}
}

// A store that cannot be read makes the start block and its list of refusals fail with the error, instead of showing
// nothing as if there were nothing to show.
func TestGDStartBlockUnreadableStoreFails(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	db.Close()
	f := t.TempDir()
	var out strings.Builder
	if err := resumeBlock(db, "new", "startup", projectRole(f), "claude-code", f, &out); err == nil {
		t.Error("the start block of an unreadable store gave no error")
	}
	if err := refusals(db, &out, "Ana", "claude-code", projectRole(f), f); err == nil {
		t.Error("the refusals of an unreadable store gave no error")
	}
	if out.Len() != 0 {
		t.Errorf("an unreadable store wrote %q", out.String())
	}
}
