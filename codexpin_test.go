package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pins readCodex: every event, session and unmapped signature the Codex fixtures import to, as the code made them
// before readCodex was split into a pure record mapping and its storage.
func TestReadCodexPinned(t *testing.T) {
	codex := codexFixture(t)
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	// More record kinds than the main fixture holds: the old user_message, a namespaced call with arguments,
	// an agent message, a developer message and two compactions.
	os.WriteFile(filepath.Join(day, "rollout-e-t-more.jsonl"), []byte(
		`{"timestamp":"2026-11-10T13:00:00Z","type":"session_meta","payload":{"id":"t-more","timestamp":"2026-11-10T13:00:00Z","cwd":"D:\\Work\\acme"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:01Z","type":"turn_context","payload":{"model":"gpt-x"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"viejo formato"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:03Z","type":"response_item","payload":{"type":"function_call","namespace":"mcp","name":"look","arguments":"{\"q\":1}","call_id":"c9"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c9","output":"seen"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:05Z","type":"response_item","payload":{"type":"agent_message","author":"/root","recipient":"/root/w","content":[{"type":"output_text","text":"go"}]}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:06Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev note"}]}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:07Z","type":"compacted","payload":{"message":"first summary"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:08Z","type":"compacted","payload":{"message":"second summary"}}`+"\n"+
			`{"timestamp":"2026-11-10T13:00:09Z","type":"response_item","payload":{"type":"brand_new"}}`+"\n"+
			`not json`+"\n"), 0o644)
	db, err := openDB(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	empty := t.TempDir()
	if err := runImport(db, filepath.Join(empty, "projects"), filepath.Join(empty, "h.jsonl"), codex, "", "acme", "test"); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, q := range []string{
		`select uuid, session_id, seq, ts, speaker, kind, text, coalesce(tool_name, ''), coalesce(tool_input, ''), coalesce(tool_use_id, ''), compaction_no from event order by uuid`,
		`select id, role, coalesce(parent_id, ''), coalesce(model, ''), compactions, coalesce(ended_at, ''), coalesce(excluded, '') from session order by id`,
		`select signature, count from unmapped order by signature`,
		`select path, bad_lines from source_file order by path`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			s := fmt.Sprint(vals...)
			s = strings.ReplaceAll(s, filepath.ToSlash(codex), "<codex>")
			s = strings.ReplaceAll(s, codex, "<codex>")
			if strings.HasPrefix(s, "<codex>") { // a file path: the same golden on every platform
				s = strings.ReplaceAll(s, `\`, "/")
			}
			lines = append(lines, s)
		}
		rows.Close()
	}
	got := strings.Join(lines, "\n") + "\n"
	const golden = "testdata/readcodex.golden.txt"
	if os.Getenv("TRACE_WRITE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(golden, []byte(got), 0o644)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("readCodex changed what it stores:\n got:\n%s\nwant:\n%s", got, want)
	}
}
