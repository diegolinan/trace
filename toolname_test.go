package main

import (
	"os"
	"path/filepath"
	"testing"
)

// SPEC I5: a result read in a later import is named after its own session's call, though another session used the
// same call id with another tool, stored after it.
func TestLaterResultNamedFromItsOwnSession(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	call := func(id, tool string) string {
		return `{"timestamp":"2026-11-10T12:00:02Z","ordinal":1,"type":"response_item","payload":{"type":"function_call","name":"` + tool + `","arguments":"{}","call_id":"same"}}` + "\n"
	}
	result := `{"timestamp":"2026-11-10T12:00:03Z","ordinal":2,"type":"response_item","payload":{"type":"function_call_output","call_id":"same","output":"done"}}` + "\n"
	meta := func(id string) string {
		return `{"timestamp":"2026-11-10T12:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-10T12:00:00Z","cwd":"/w"}}` + "\n"
	}
	db, err := openDB(filepath.Join(root, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	imp := func() {
		if err := runImport(db, "", "", codex, "", "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	// Another thread, stored first, uses the same call id for a relay tool.
	os.WriteFile(filepath.Join(day, "rollout-b-t-b.jsonl"), []byte(meta("t-b")+call("t-b", "mcp__relay_send")), 0o644)
	imp()
	a := filepath.Join(day, "rollout-a-t-a.jsonl")
	os.WriteFile(a, []byte(meta("t-a")+call("t-a", "exec")), 0o644)
	imp()
	f, _ := os.OpenFile(a, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(result)
	f.Close()
	imp()
	var name, kind string
	db.QueryRow("select coalesce(tool_name, ''), kind from event where session_id = 't-a' and text = 'done'").Scan(&name, &kind)
	if name != "exec" || kind != "tool_result" {
		t.Errorf("t-a's result named %q as %s, want exec as tool_result", name, kind)
	}
}
