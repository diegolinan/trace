package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// An empty marked block adds nothing to the card, not even blank lines.
func TestEmptyRuleBlockAddsNoBlankLines(t *testing.T) {
	got := ruleBlocks(rulesOpen + rulesClose + "\nprose\n" + rulesOpen + "\nreal rule\n" + rulesClose)
	if got != "real rule" {
		t.Errorf("ruleBlocks: %q, want %q", got, "real rule")
	}
}

// SPEC P2: the redacted field names the kinds removed from the text and from the tool input, each once.
func TestRedactedNamesEveryKind(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gh := "gh" + "p_" + strings.Repeat("Q7", 18) // github-token
	aws := "AKIA" + strings.Repeat("Z", 16)      // aws-key
	db.Exec(`insert into session(id, host, role) values ('s', 'claude-code', 'r')`)
	db.Exec(`insert into source_file(id, host, path) values (1, 'claude-code', 't.jsonl')`)
	im := &importer{db: db, touched: map[string]bool{}}
	tx, _ := db.Begin()
	for i, e := range []ev{
		{speaker: "assistant", kind: "tool_call", text: "Bash " + gh, toolInput: `{"command":"` + gh + ` ` + aws + `"}`},
		{speaker: "assistant", kind: "tool_call", text: "Bash ls", toolInput: `{"command":"` + aws + `"}`},
		{speaker: "assistant", kind: "reply", text: "token " + gh},
	} {
		if err := im.insertEvent(tx, "u"+itoa(i), "s", i+1, "2026-11-10T10:00:00Z", "", 0, 1, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	for uuid, want := range map[string]string{"u0": "github-token,aws-key", "u1": "aws-key", "u2": "github-token"} {
		var got string
		db.QueryRow("select coalesce(redacted, '') from event where uuid = ?", uuid).Scan(&got)
		if got != want {
			t.Errorf("%s: redacted %q, want %q", uuid, got, want)
		}
	}
}
