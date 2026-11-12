package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// A main thread, a subagent that copies the parent's first rows, a compressed old thread, a thread outside
// scope, and a session index.
const codexMain = `{"timestamp":"2026-11-10T10:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"t-main","timestamp":"2026-11-10T10:00:00Z","cwd":"D:\\Work\\acme","cli_version":"0.170.0","source":"cli","git":{"branch":"main"}}}
{"timestamp":"2026-11-10T10:00:01Z","ordinal":1,"type":"turn_context","payload":{"cwd":"D:\\Work\\acme","model":"gpt-6.1-sol"}}
{"timestamp":"2026-11-10T10:00:02Z","ordinal":2,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions"}]}}
{"timestamp":"2026-11-10T10:00:03Z","ordinal":3,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"arreglá el login"}]}}}
{"timestamp":"2026-11-10T10:00:04Z","ordinal":4,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning","summary_text":["mirar auth.go"]}}}
{"timestamp":"2026-11-10T10:00:05Z","ordinal":5,"type":"response_item","payload":{"type":"custom_tool_call","call_id":"c1","name":"exec","input":"go test ./..."}}
{"timestamp":"2026-11-10T10:00:06Z","ordinal":6,"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":[{"type":"input_text","text":"ok auth 0.2s"}]}}
{"timestamp":"2026-11-10T10:00:07Z","ordinal":7,"type":"response_item","payload":{"type":"function_call","call_id":"c2","name":"read","namespace":"fs","arguments":"{\"path\":\"auth.go\"}"}}
{"timestamp":"2026-11-10T10:00:08Z","ordinal":8,"type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":"package auth"}}
{"timestamp":"2026-11-10T10:00:09Z","ordinal":9,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","data":"AAAA"}}}
{"timestamp":"2026-11-10T10:00:10Z","ordinal":10,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"listo, arreglado"}]}}
{"timestamp":"2026-11-10T10:00:10Z","ordinal":11,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"text","text":"listo, arreglado"}],"phase":"final_answer"}}}
{"timestamp":"2026-11-10T10:00:11Z","ordinal":12,"type":"response_item","payload":{"type":"agent_message","author":"/root","recipient":"/root/helper","content":[{"type":"input_text","text":"revisá los tests"}]}}
{"timestamp":"2026-11-10T10:00:12Z","ordinal":13,"type":"compacted","payload":{"message":"resumen: login arreglado"}}
{"timestamp":"2026-11-10T10:00:13Z","ordinal":14,"type":"event_msg","payload":{"type":"turn_aborted","reason":"interrupted"}}
{"timestamp":"2026-11-10T10:00:14Z","ordinal":15,"type":"event_msg","payload":{"type":"brand_new_kind"}}
not json at all
{"timestamp":"2026-11-10T10:00:15Z","ordinal":16,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"half written`

const codexSub = `{"timestamp":"2026-11-10T11:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"t-sub","parent_thread_id":"t-main","timestamp":"2026-11-10T11:00:00Z","cwd":"D:\\Work\\acme","source":{"subagent":{"other":"x"}},"subagent_history_start_ordinal":2}}
{"timestamp":"2026-11-10T11:00:01Z","ordinal":1,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"copia del padre"}]}}}
{"timestamp":"2026-11-10T11:00:02Z","ordinal":2,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"text","text":"tests revisados"}]}}}
`

const codexOld = `{"timestamp":"2026-10-01T09:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"t-old","timestamp":"2026-10-01T09:00:00Z","cwd":"D:\\Work\\acme"}}
{"timestamp":"2026-10-01T09:00:01Z","ordinal":1,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"sesión vieja comprimida"}]}}}
`

const codexOther = `{"timestamp":"2026-11-10T12:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"t-other","timestamp":"2026-11-10T12:00:00Z","cwd":"D:\\Clients\\secret"}}
{"timestamp":"2026-11-10T12:00:01Z","ordinal":1,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"texto de otro cliente"}]}}}
`

func codexFixture(t *testing.T) (home string) {
	home = t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	os.WriteFile(filepath.Join(day, "rollout-a-t-main.jsonl"), []byte(codexMain), 0o644)
	os.WriteFile(filepath.Join(day, "rollout-b-t-sub.jsonl"), []byte(codexSub), 0o644)
	os.WriteFile(filepath.Join(day, "rollout-c-t-other.jsonl"), []byte(codexOther), 0o644)
	old := filepath.Join(home, "sessions", "2026", "10", "01")
	os.MkdirAll(old, 0o755)
	f, _ := os.Create(filepath.Join(old, "rollout-d-t-old.jsonl.zst"))
	zw, _ := zstd.NewWriter(f)
	zw.Write([]byte(codexOld))
	zw.Close()
	f.Close()
	os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(
		`{"id":"t-main","thread_name":"login fix","updated_at":"2026-11-10T10:00:14Z"}`+"\n"+
			`{"id":"t-other","thread_name":"secret client","updated_at":"2026-11-10T12:00:01Z"}`+"\n"), 0o644)
	return home
}

func TestCodexImport(t *testing.T) {
	codex := codexFixture(t)
	db, err := openDB(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	empty := t.TempDir()
	run := func() {
		if err := runImport(db, filepath.Join(empty, "projects"), filepath.Join(empty, "h.jsonl"), codex, "", "", "acme", "test"); err != nil {
			t.Fatal(err)
		}
	}
	run()
	for _, c := range []struct {
		q    string
		want int
	}{
		{"select count(*) from session where host = 'codex'", 3},
		{"select count(*) from session where id = 't-main' and role like 'project:%' and model = 'gpt-6.1-sol' and branch_first = 'main' and compactions = 1", 1},
		{"select count(*) from session where id = 't-sub' and role = 'subagent' and parent_id = 't-main'", 1},
		{"select count(*) from event where session_id = 't-main' and speaker = 'user' and kind = 'prompt' and text = 'arreglá el login'", 1},
		{"select count(*) from event where session_id = 't-main' and kind = 'prompt'", 1}, // the half-written line waits
		{"select count(*) from event where session_id = 't-main' and kind = 'reply' and text = 'listo, arreglado'", 1},
		{"select count(*) from event where session_id = 't-main' and kind = 'thinking'", 1},
		{"select count(*) from event where kind = 'tool_call' and tool_name = 'exec' and tool_use_id = 'c1'", 1},
		{"select count(*) from event where kind = 'tool_call' and tool_name = 'fs.read'", 1},
		{"select count(*) from event where kind = 'tool_result' and text = 'ok auth 0.2s' and tool_name = 'exec'", 1},
		{"select count(*) from event where kind = 'tool_result' and text = 'package auth'", 1},
		{"select count(*) from event where kind = 'relay' and text like '/root -> /root/helper:%'", 1},
		{"select count(*) from event where kind = 'summary' and text = 'resumen: login arreglado'", 1},
		{"select count(*) from event where kind = 'boundary' and text like 'turn aborted%'", 1},
		{"select count(*) from event where kind = 'injected' and text like '# AGENTS.md%'", 1},
		{"select count(*) from event where text = 'AAAA'", 0},
		{"select count(*) from event where session_id = 't-sub'", 1}, // the parent's copied prefix is skipped
		{"select count(*) from event where session_id = 't-old' and text = 'sesión vieja comprimida'", 1},
		{"select count(*) from event where text like '%otro cliente%'", 0},
		{"select count(*) from session where id = 't-other'", 0},
		{"select count(*) from source_file where skipped = 'scope'", 1},
		{"select count(*) from session_name where session_id = 't-main' and name = 'login fix'", 1},
		{"select count(*) from session_name where name = 'secret client'", 0},
		{"select count(*) from unmapped where signature = 'codex event_msg/brand_new_kind'", 1},
		{"select bad_lines from source_file where path like '%t-main.jsonl'", 1},
	} {
		if got := count(t, db, c.q); got != c.want {
			t.Errorf("%s = %d, want %d", c.q, got, c.want)
		}
	}
	before := count(t, db, "select count(*) from event")
	main := filepath.Join(codex, "sessions", "2026", "11", "10", "rollout-a-t-main.jsonl")
	f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`"}]}}}` + "\n")
	f.Close()
	run()
	if n := count(t, db, "select count(*) from event") - before; n != 1 {
		t.Errorf("the finished line added %d events, want 1", n)
	}
	if n := count(t, db, "select count(*) from event where text = 'half written'"); n != 1 {
		t.Errorf("finished line not stored once: %d", n)
	}
	// A week later Codex compresses the file and deletes the plain one: the rows are keyed by thread and ordinal,
	// so nothing is stored twice.
	plain, _ := os.ReadFile(main)
	zf, _ := os.Create(main + ".zst")
	zw, _ := zstd.NewWriter(zf)
	zw.Write(plain)
	zw.Close()
	zf.Close()
	os.Remove(main)
	run()
	if n := count(t, db, "select count(*) from event"); n != before+1 {
		t.Errorf("the compressed copy changed the events: %d, want %d", n, before+1)
	}
	if n := count(t, db, "select count(*) from source_file where path like '%t-main.jsonl.zst' and size > 0"); n != 1 {
		t.Error("compressed copy not recorded")
	}
}

// No Codex on the machine is no Codex, not an error (CI's fresh HOME has none).
func TestCodexMissing(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	if err := runImport(db, filepath.Join(dir, "p"), filepath.Join(dir, "h"), filepath.Join(dir, "no-codex"), "", "", "", "test"); err != nil {
		t.Fatalf("missing Codex home: %v", err)
	}
}

// trace resume shows a Codex session as Codex, never under a Claude role label.
func TestCodexResume(t *testing.T) {
	codex := codexFixture(t)
	db, _ := openDB(filepath.Join(t.TempDir(), "c.db"))
	defer db.Close()
	empty := t.TempDir()
	runImport(db, filepath.Join(empty, "p"), filepath.Join(empty, "h"), codex, "", "", "acme", "test")
	var role string
	db.QueryRow("select role from session where id = 't-main'").Scan(&role)
	var out strings.Builder
	if err := resumeBlock(db, "", "startup", role, "codex", &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Where the last Codex session in this checkout stopped", "arreglá el login", "listo, arreglado", "login fix"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	var claude strings.Builder
	resumeBlock(db, "", "startup", role, "claude-code", &claude)
	if claude.Len() != 0 {
		t.Errorf("a Codex session showed as Claude Code:\n%s", claude.String())
	}
}

var _ = sql.ErrNoRows
