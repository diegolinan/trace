package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Behaviour tests for codex.go: each pins what a user sees in the store after importing Codex rollouts.

// cmLine is one rollout line; ordinal < 0 leaves the ordinal out.
func cmLine(ordinal int, typ string, payload any) string {
	l := map[string]any{"timestamp": "2026-11-10T12:00:00Z", "type": typ, "payload": payload}
	if ordinal >= 0 {
		l["ordinal"] = ordinal
	}
	b, _ := json.Marshal(l)
	return string(b) + "\n"
}

func cmMeta(id, cwd string, extra map[string]any) string {
	p := map[string]any{"id": id, "timestamp": "2026-11-10T12:00:00Z", "cwd": cwd}
	for k, v := range extra {
		p[k] = v
	}
	return cmLine(0, "session_meta", p)
}

func cmItem(ordinal int, item map[string]any) string {
	return cmLine(ordinal, "event_msg", map[string]any{"type": "item_completed", "item": item})
}

func cmUser(ordinal int, text string) string {
	return cmItem(ordinal, map[string]any{"type": "UserMessage", "content": []any{map[string]any{"type": "text", "text": text}}})
}

func cmCall(ordinal int, callID, namespace, name, args string) string {
	return cmLine(ordinal, "response_item", map[string]any{"type": "function_call", "call_id": callID, "namespace": namespace, "name": name, "arguments": args})
}

func cmOutput(ordinal int, callID, output string) string {
	return cmLine(ordinal, "response_item", map[string]any{"type": "function_call_output", "call_id": callID, "output": output})
}

// cmStore is a Codex home with one day folder and a fresh store.
type cmStore struct {
	t                *testing.T
	root, codex, day string
	db               *sql.DB
}

func newCMStore(t *testing.T) *cmStore {
	t.Helper()
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(filepath.Join(root, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &cmStore{t: t, root: root, codex: codex, day: day, db: db}
}

func (s *cmStore) cwd() string { return filepath.Join(s.root, "w") }

func (s *cmStore) write(name, text string) string {
	s.t.Helper()
	p := filepath.Join(s.day, name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (s *cmStore) appendText(path, text string) {
	s.t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		s.t.Fatal(err)
	}
	f.WriteString(text)
	f.Close()
}

func (s *cmStore) writeZst(name, text string) string {
	s.t.Helper()
	p := filepath.Join(s.day, name)
	f, err := os.Create(p)
	if err != nil {
		s.t.Fatal(err)
	}
	zw, _ := zstd.NewWriter(f)
	zw.Write([]byte(text))
	zw.Close()
	f.Close()
	return p
}

func (s *cmStore) imp(scope string) {
	s.t.Helper()
	if err := runImport(s.db, "", "", s.codex, "", scope, "test"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *cmStore) n(q string, args ...any) int { s.t.Helper(); return count(s.t, s.db, q, args...) }

// Only files named rollout-*.jsonl or rollout-*.jsonl.zst are rollouts, in sessions/ and archived_sessions/; a
// folder with a rollout's name and any other .jsonl are not.
func TestCodexFilesListsOnlyRollouts(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "11", "10")
	arch := filepath.Join(home, "archived_sessions")
	os.MkdirAll(filepath.Join(day, "rollout-folder.jsonl"), 0o755)
	os.MkdirAll(arch, 0o755)
	for _, p := range []string{
		filepath.Join(day, "rollout-a.jsonl"), filepath.Join(day, "rollout-b.jsonl.zst"), filepath.Join(day, "notes.jsonl"),
		filepath.Join(day, "rollout-c.txt"), filepath.Join(arch, "rollout-z.jsonl"),
	} {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	got := codexFiles(home)
	slices.Sort(got)
	want := []string{filepath.Join(arch, "rollout-z.jsonl"), filepath.Join(day, "rollout-a.jsonl"), filepath.Join(day, "rollout-b.jsonl.zst")}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("codexFiles = %q, want %q", got, want)
	}
}

// A rollout whose session_meta is its only line, not yet ended by a newline, already names its thread.
func TestCodexMetaWithoutNewlineStoresSession(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-nl.jsonl", strings.TrimSuffix(cmMeta("t-nl", s.cwd(), nil), "\n"))
	s.imp("")
	if n := s.n("select count(*) from session where id = 't-nl' and host = 'codex'"); n != 1 {
		t.Errorf("session from a meta line with no newline: %d, want 1", n)
	}
}

// A file that does not start with a session_meta naming a thread stores nothing: no session, no event.
func TestCodexNotARolloutStoresNothing(t *testing.T) {
	for _, c := range []struct {
		name, text string
		zst        bool
	}{
		{"empty", "", false},
		{"empty compressed", "", true}, // a plain empty file is never opened (read to its end); a compressed one is
		{"first line not JSON", "not json\n" + cmUser(1, "NONCE_NOT_A_ROLLOUT"), false},
		{"first line not session_meta", cmLine(0, "turn_context", map[string]any{"id": "t-fake", "model": "m"}) + cmUser(1, "NONCE_NOT_A_ROLLOUT"), false},
		{"session_meta without id", cmMeta("", `C:\w`, nil) + cmUser(1, "NONCE_NOT_A_ROLLOUT"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCMStore(t)
			if c.zst {
				s.writeZst("rollout-x-t-none.jsonl.zst", c.text)
			} else {
				s.write("rollout-x-t-none.jsonl", c.text)
			}
			s.imp("")
			if n := s.n("select count(*) from session"); n != 0 {
				t.Errorf("%d sessions stored, want 0", n)
			}
			if n := s.n("select count(*) from event"); n != 0 {
				t.Errorf("%d events stored, want 0", n)
			}
		})
	}
}

// A plain rollout rewritten shorter than what was read is read again from its start.
func TestCodexShrunkRolloutReadFromStart(t *testing.T) {
	s := newCMStore(t)
	p := s.write("rollout-x-t-sh.jsonl", cmMeta("t-sh", s.cwd(), nil)+cmUser(1, strings.Repeat("long ", 200)))
	s.imp("")
	s.write("rollout-x-t-sh.jsonl", cmMeta("t-sh", s.cwd(), nil)+cmUser(2, "NONCE_SHRUNK"))
	s.imp("")
	if n := s.n("select count(*) from event where text = 'NONCE_SHRUNK'"); n != 1 {
		t.Errorf("the shrunk rollout's new turn stored %d times, want 1 (%s)", n, p)
	}
}

// A compressed rollout with the size and time it had when read is not read again: its broken line counts once.
func TestCodexUnchangedZstNotReadAgain(t *testing.T) {
	s := newCMStore(t)
	s.writeZst("rollout-x-t-z.jsonl.zst", cmMeta("t-z", s.cwd(), nil)+"not json\n"+cmUser(1, "NONCE_ZST"))
	s.imp("")
	s.imp("")
	if n := s.n("select bad_lines from source_file where path like '%t-z.jsonl.zst'"); n != 1 {
		t.Errorf("bad_lines after two runs = %d, want 1", n)
	}
	if n := s.n("select count(*) from event where text = 'NONCE_ZST'"); n != 1 {
		t.Errorf("event stored %d times, want 1", n)
	}
}

// A plain rollout whose scope mark is cleared is read from its first byte: its session_meta is not a broken line.
func TestCodexClearedMarkReadsFromFirstByte(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-m.jsonl", cmMeta("t-m", filepath.Join(s.root, "beta"), nil)+cmUser(1, "NONCE_MARK"))
	s.imp(filepath.Join(s.root, "alpha"))
	if n := s.n("select count(*) from source_file where skipped like 'scope:%'"); n != 1 {
		t.Fatalf("out-of-scope rollout not marked: %d", n)
	}
	s.imp("")
	if n := s.n("select count(*) from event where text = 'NONCE_MARK'"); n != 1 {
		t.Errorf("event after the mark cleared: %d, want 1", n)
	}
	if n := s.n("select bad_lines from source_file where path like '%t-m.jsonl'"); n != 0 {
		t.Errorf("bad_lines = %d, want 0", n)
	}
}

// A thread is a subagent when it names a parent thread, or when its source says subagent: either alone suffices.
func TestCodexSubagentByParentOrSource(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-a-t-p.jsonl", cmMeta("t-p", s.cwd(), nil)+cmUser(1, "padre"))
	s.write("rollout-b-t-c1.jsonl", cmMeta("t-c1", s.cwd(), map[string]any{"parent_thread_id": "t-p", "source": "cli"})+cmUser(1, "hijo uno"))
	s.write("rollout-c-t-c2.jsonl", cmMeta("t-c2", s.cwd(), map[string]any{"source": map[string]any{"subagent": map[string]any{"other": "x"}}})+cmUser(1, "hijo dos"))
	s.imp("")
	if n := s.n("select count(*) from session where id = 't-c1' and role = 'subagent' and parent_id = 't-p'"); n != 1 {
		t.Errorf("thread with a parent but a cli source is not a subagent of t-p")
	}
	if n := s.n("select count(*) from session where id = 't-c2' and role = 'subagent' and parent_id is null"); n != 1 {
		t.Errorf("thread with a subagent source but no parent is not a subagent")
	}
	if n := s.n("select count(*) from session where id = 't-p' and role != 'subagent'"); n != 1 {
		t.Errorf("the main thread was taken for a subagent")
	}
}

// Reading a rollout again from its start (here its compressed copy) counts its compactions once.
func TestCodexRereadCountsCompactionsOnce(t *testing.T) {
	s := newCMStore(t)
	text := cmMeta("t-c", s.cwd(), nil) + cmUser(1, "antes") + cmLine(2, "compacted", map[string]any{"message": "resumen"}) + cmUser(3, "después")
	plain := s.write("rollout-x-t-c.jsonl", text)
	s.imp("")
	if n := s.n("select compactions from session where id = 't-c'"); n != 1 {
		t.Fatalf("compactions after the plain read = %d, want 1", n)
	}
	s.writeZst("rollout-x-t-c.jsonl.zst", text)
	os.Remove(plain)
	s.imp("")
	if n := s.n("select compactions from session where id = 't-c'"); n != 1 {
		t.Errorf("compactions after the compressed copy = %d, want 1", n)
	}
}

// A turn_context without a model does not erase the model an earlier turn named.
func TestCodexModelKeptWhenLaterTurnNamesNone(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-mo.jsonl", cmMeta("t-mo", s.cwd(), nil)+
		cmLine(1, "turn_context", map[string]any{"model": "gpt-m1"})+
		cmLine(2, "turn_context", map[string]any{"cwd": "x"})+cmUser(3, "hola"))
	s.imp("")
	if n := s.n("select count(*) from session where id = 't-mo' and model = 'gpt-m1'"); n != 1 {
		t.Errorf("the model gpt-m1 was lost")
	}
}

// An image generation record is a blob: skipped unread, it never shows up as an unmapped kind.
func TestCodexImageGenerationSkippedUnread(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-img.jsonl", cmMeta("t-img", s.cwd(), nil)+
		cmLine(1, "response_item", map[string]any{"type": "image_generation_call", "result": "AAAA"})+cmUser(2, "hola"))
	s.imp("")
	if n := s.n("select count(*) from unmapped where signature like '%image_generation%'"); n != 0 {
		t.Errorf("an image blob was parsed and reported unmapped (%d)", n)
	}
	if n := s.n("select count(*) from event where session_id = 't-img'"); n != 1 {
		t.Errorf("events = %d, want only the prompt", n)
	}
}

// A record that says nothing (an empty reasoning summary, an empty reply) stores no event.
func TestCodexEmptyRecordsStoreNothing(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-e.jsonl", cmMeta("t-e", s.cwd(), nil)+
		cmItem(1, map[string]any{"type": "Reasoning", "summary_text": []string{}})+
		cmItem(2, map[string]any{"type": "AgentMessage", "content": []any{}})+cmUser(3, "hola"))
	s.imp("")
	if n := s.n("select count(*) from event where session_id = 't-e'"); n != 1 {
		t.Errorf("events = %d, want only the prompt", n)
	}
}

// A call to a relay tool stays a tool call; only its result is another agent's words.
func TestCodexRelayToolCallStaysCall(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-r.jsonl", cmMeta("t-r", s.cwd(), nil)+cmCall(1, "r1", "mcp__relay", "send", `{"to":"b"}`)+cmOutput(2, "r1", "hola desde b"))
	s.imp("")
	if n := s.n("select count(*) from event where session_id = 't-r' and kind = 'tool_call' and speaker = 'assistant' and tool_name = 'mcp__relay.send'"); n != 1 {
		t.Errorf("the relay tool call is not stored as a tool call")
	}
	if n := s.n("select count(*) from event where session_id = 't-r' and kind = 'relay' and speaker = 'relay' and text = 'hola desde b'"); n != 1 {
		t.Errorf("the relay tool's result is not stored as relay")
	}
}

// A result read in a later run than its call is still named after that call.
func TestCodexLaterResultNamedByEarlierCall(t *testing.T) {
	s := newCMStore(t)
	p := s.write("rollout-x-t-l.jsonl", cmMeta("t-l", s.cwd(), nil)+cmCall(1, "c7", "", "exec", `{"cmd":"ls"}`))
	s.imp("")
	s.appendText(p, cmOutput(2, "c7", "a.txt"))
	s.imp("")
	if n := s.n("select count(*) from event where kind = 'tool_result' and text = 'a.txt' and tool_name = 'exec'"); n != 1 {
		t.Errorf("the later result is not named exec")
	}
}

// A result is named after the call of its own thread, even when another thread used the same call id.
func TestCodexResultNamedByItsOwnThreadsCall(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-a-t-1.jsonl", cmMeta("t-1", s.cwd(), nil)+cmCall(1, "same", "", "alpha", "{}")+cmOutput(2, "same", "uno"))
	s.write("rollout-b-t-2.jsonl", cmMeta("t-2", s.cwd(), nil)+cmCall(1, "same", "", "beta", "{}")+cmOutput(2, "same", "dos"))
	s.imp("")
	if n := s.n("select count(*) from event where session_id = 't-2' and kind = 'tool_result' and tool_name = 'beta'"); n != 1 {
		var got string
		s.db.QueryRow("select coalesce(tool_name, '') from event where session_id = 't-2' and kind = 'tool_result'").Scan(&got)
		t.Errorf("t-2's result is named %q, want beta", got)
	}
}

// cmNamed stores the threads ids and imports with the session index lines given.
func cmNamed(t *testing.T, ids []string, index ...string) *cmStore {
	s := newCMStore(t)
	for i, id := range ids {
		s.write("rollout-"+string(rune('a'+i))+"-"+id+".jsonl", cmMeta(id, s.cwd(), nil)+cmUser(1, "hola "+id))
	}
	os.WriteFile(filepath.Join(s.codex, "session_index.jsonl"), []byte(strings.Join(index, "\n")+"\n"), 0o644)
	s.imp("")
	return s
}

// Every stored thread the session index names gets its name, not only the first.
func TestCodexNamesEveryStoredThread(t *testing.T) {
	s := cmNamed(t, []string{"t-n1", "t-n2"},
		`{"id":"t-n1","thread_name":"uno","updated_at":"2026-11-10T12:00:00Z"}`,
		`{"id":"t-n2","thread_name":"dos","updated_at":"2026-11-10T12:00:00Z"}`)
	if n := s.n("select count(*) from session_name where (session_id, name) in (values ('t-n1', 'uno'), ('t-n2', 'dos'))"); n != 2 {
		t.Errorf("named threads = %d, want 2", n)
	}
}

// An index line that does not parse whole, or has no name, gives no name.
func TestCodexNamesSkipMalformedIndexLines(t *testing.T) {
	s := cmNamed(t, []string{"t-n"},
		`{"id":"t-n","thread_name":"mal","updated_at":5}`,
		`{"id":"t-n","thread_name":""}`,
		`{"id":"t-n","thread_name":"bien"}`)
	if n := s.n("select count(*) from session_name where session_id = 't-n' and name != 'bien'"); n != 0 {
		t.Errorf("a malformed index line gave %d names", n)
	}
	if n := s.n("select count(*) from session_name where session_id = 't-n' and name = 'bien'"); n != 1 {
		t.Errorf("the good index line gave no name")
	}
}

// A record kind trace does not map shows up in unmapped under its own signature, whatever its payload type.
func TestCodexUnknownKindReportedUnderItsSignature(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-u.jsonl", cmMeta("t-u", s.cwd(), nil)+cmLine(1, "event_msg", map[string]any{"type": "additional_tools"})+cmUser(2, "hola"))
	s.imp("")
	if n := s.n("select count(*) from unmapped where signature = 'codex event_msg/additional_tools'"); n != 1 {
		t.Errorf("event_msg/additional_tools not reported as unmapped")
	}
}
