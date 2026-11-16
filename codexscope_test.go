package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// rolloutText is a minimal Codex rollout: its session_meta names the folder, then the user says text.
func rolloutText(id, cwd, text string, ordinal int) string {
	c, _ := json.Marshal(cwd)
	t, _ := json.Marshal(text)
	meta := `{"timestamp":"2026-11-10T12:00:00Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `","timestamp":"2026-11-10T12:00:00Z","cwd":` + string(c) + `}}` + "\n"
	return meta + userLine(text, ordinal, string(t))
}

func userLine(_ string, ordinal int, quoted string) string {
	return `{"timestamp":"2026-11-10T12:00:01Z","ordinal":` + itoa(ordinal) + `,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":` + quoted + `}]}}}` + "\n"
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func appendUser(t *testing.T, path, text string, ordinal int) {
	t.Helper()
	q, _ := json.Marshal(text)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(userLine(text, ordinal, string(q)))
	f.Close()
}

// SPEC I1: the scope mark holds only for the scope that set it.
func TestCodexScopeMarkFollowsScope(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	os.WriteFile(filepath.Join(day, "rollout-x-t-b.jsonl"), []byte(rolloutText("t-b", filepath.Join(root, "beta"), "NONCE_I1", 1)), 0o644)
	db, err := openDB(filepath.Join(root, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	imp := func(scope string) {
		if err := runImport(db, "", "", codex, "", scope, "test"); err != nil {
			t.Fatal(err)
		}
	}
	imp(filepath.Join(root, "alpha"))
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I1'"); n != 0 {
		t.Fatalf("out of scope, yet imported (%d)", n)
	}
	imp(filepath.Join(root, "alpha") + "," + filepath.Join(root, "beta"))
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I1'"); n != 1 {
		t.Fatalf("after the scope widened, the rollout was not imported (%d)", n)
	}
	// A mark from before the scope was recorded in it is read again.
	db.Exec("update source_file set skipped = 'scope', read_to_byte = 0 where path like '%t-b.jsonl'")
	db.Exec("delete from event where session_id = 't-b'")
	imp("")
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I1'"); n != 1 {
		t.Fatalf("an old mark kept the rollout out (%d)", n)
	}
	// A compressed rollout, marked under a narrower scope: unchanged size and mtime must not keep the old mark.
	zrollout := filepath.Join(day, "rollout-y-t-z.jsonl.zst")
	zf, _ := os.Create(zrollout)
	zw, _ := zstd.NewWriter(zf)
	zw.Write([]byte(rolloutText("t-z", filepath.Join(root, "beta"), "NONCE_I1_ZST", 1)))
	zw.Close()
	zf.Close()
	imp(filepath.Join(root, "alpha"))
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I1_ZST'"); n != 0 {
		t.Fatalf("compressed, out of scope, yet imported (%d)", n)
	}
	imp(filepath.Join(root, "alpha") + "," + filepath.Join(root, "beta"))
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I1_ZST'"); n != 1 {
		t.Fatalf("after the scope widened, the compressed rollout was not imported (%d)", n)
	}
}

// SPEC I2: re-running install -host codex from one folder of a wider scope keeps the other folders imported.
func TestCodexInstallKeepsConfigScope(t *testing.T) {
	root := t.TempDir()
	alpha, beta := filepath.Join(root, "alpha"), filepath.Join(root, "beta")
	os.MkdirAll(alpha, 0o755)
	os.MkdirAll(beta, 0o755)
	home := filepath.Join(root, "trace")
	os.MkdirAll(home, 0o755)
	cfgJSON, _ := json.Marshal(map[string]any{"claude": false, "scope": []string{alpha, beta}})
	os.WriteFile(filepath.Join(home, "config.json"), cfgJSON, 0o644)
	day := filepath.Join(root, "codex", "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	rollout := filepath.Join(day, "rollout-x-t-b.jsonl")
	os.WriteFile(rollout, []byte(rolloutText("t-b", beta, "NONCE_I2_FIRST", 1)), 0o644)
	if _, e, code := runTrace(t, root, "", nil, "import"); code != 0 {
		t.Fatalf("import: %d %s", code, e)
	}
	appendUser(t, rollout, "NONCE_I2_DURING", 2) // the beta session keeps writing while install runs in alpha
	if _, e, code := runTrace(t, root, "", map[string]string{"_DIR": alpha}, "install", "-host", "codex", "-mode", "agents"); code != 0 {
		t.Fatalf("install from alpha: %d %s", code, e)
	}
	appendUser(t, rollout, "NONCE_I2_LATER", 3)
	if _, e, code := runTrace(t, root, "", nil, "import"); code != 0 {
		t.Fatalf("import: %d %s", code, e)
	}
	out, _, _ := runTrace(t, root, "", nil, "search", "-all", "NONCE_I2_LATER")
	if !strings.Contains(out, "NONCE_I2_LATER") {
		t.Fatalf("after install ran from alpha, beta's new turn was not stored:\n%s", out)
	}
}

// SPEC S1: trace resume leaves out the session asking, not merely the newest one.
func TestResumeLeavesOutTheAsker(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := func(ago time.Duration) string { return time.Now().UTC().Add(-ago).Format(time.RFC3339) }
	add := func(id, ended, kind, text string) {
		db.Exec(`insert or ignore into session(id, host, role, started_at, ended_at) values (?, 'codex', 'r', ?, ?)`, id, ended, ended)
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, (select count(*) from event where session_id = ?), ?, 'assistant', ?, ?)`, id, id, ended, kind, text)
	}
	// A asks (its last event is the resume call); B, in the same folder, wrote after it.
	add("A", at(30*time.Second), "tool_call", `exec {"cmd":"\"C:\\x\\trace.exe\" resume -home \"C:\\h\""}`)
	add("B", at(10*time.Second), "reply", "editing system/tools/trace/resume.go")
	if got := liveSession(db, "codex", "r"); got != "A" {
		t.Errorf("two live sessions: left out %q, want the asker A", got)
	}
	// B's last call edits resume.go in the trace folder: that is not a resume call.
	add("B", at(5*time.Second), "tool_call", `apply_patch {"path":"system/tools/trace/resume.go"}`)
	if got := liveSession(db, "codex", "r"); got != "A" {
		t.Errorf("an edit of resume.go was taken for the call: left out %q", got)
	}
	// No resume call recorded: the newest recent session, as before.
	db.Exec("delete from event where session_id = 'A'")
	if got := liveSession(db, "codex", "r"); got != "B" {
		t.Errorf("no call recorded: left out %q, want the newest B", got)
	}
}

// SPEC I3: compressing a rollout whose rows have no ordinal stores nothing twice.
func TestCodexCompressedOldRows(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	plain := filepath.Join(day, "rollout-x-t-old2.jsonl")
	text := `{"timestamp":"2026-11-10T12:00:00Z","type":"session_meta","payload":{"id":"t-old2","timestamp":"2026-11-10T12:00:00Z","cwd":"C:\\w"}}` + "\n" +
		`{"timestamp":"2026-11-10T12:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"NONCE_I3"}]}}}` + "\n"
	os.WriteFile(plain, []byte(text), 0o644)
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
	imp()
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I3'"); n != 1 {
		t.Fatalf("plain rollout: %d", n)
	}
	zf, _ := os.Create(plain + ".zst")
	zw, _ := zstd.NewWriter(zf)
	zw.Write([]byte(text))
	zw.Close()
	zf.Close()
	os.Remove(plain)
	imp()
	if n := count(t, db, "select count(*) from event where text = 'NONCE_I3'"); n != 1 {
		t.Errorf("after compression the row is stored %d times", n)
	}
}
