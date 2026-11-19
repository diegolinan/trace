package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trace resume says when there is nothing, and shows the last session of this folder when there is.
func TestResumeCommand(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	env := map[string]string{"_DIR": work}
	out, errOut, code := runTrace(t, root, "", env, "resume", "-host", "codex") // the sessions below are Codex's; the default follows the home (SPEC S3)
	if code != 0 || !strings.Contains(out, "No earlier Codex session in this folder.") {
		t.Fatalf("empty store: exit %d\n%s\n%s", code, out, errOut)
	}
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at) values ('c1', 'codex', ?, ?, '2026-11-10T10:00:00Z', '2026-11-10T11:00:00Z')`, projectRole(work), work)
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values ('c1', 1, '2026-11-10T10:30:00Z', 'assistant', 'reply', 'NONCE_RESUME_CMD')`)
	db.Close()
	out, errOut, code = runTrace(t, root, "", env, "resume", "-host", "codex", "-not", "x")
	if code != 0 || !strings.Contains(out, "NONCE_RESUME_CMD") {
		t.Fatalf("one session: exit %d\n%s\n%s", code, out, errOut)
	}
}

// trace rules says why the card has no files, and that re-injection is off.
func TestRulesCommand(t *testing.T) {
	root := t.TempDir()
	_, errOut, code := runTrace(t, root, "", nil, "rules")
	if code != 0 || !strings.Contains(errOut, "no Claude Code session in this folder is in the store yet") || !strings.Contains(errOut, "re-injection is off") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
}

// trace restore refuses without a raw copies folder, and rebuilds the store from one.
func TestRestoreCommand(t *testing.T) {
	root := t.TempDir()
	if _, errOut, code := runTrace(t, root, "", nil, "restore", "-archive", filepath.Join(root, "none")); code != 1 || !strings.Contains(errOut, "restore needs the raw copies folder") {
		t.Fatalf("no archive: exit %d\n%s", code, errOut)
	}
	raw := filepath.Join(root, "raw")
	os.MkdirAll(filepath.Join(raw, "C--work-acme"), 0o755)
	f, _ := os.Create(filepath.Join(raw, "C--work-acme", "s7.jsonl.gz"))
	zw := gzip.NewWriter(f)
	zw.Write([]byte(`{"type":"user","sessionId":"s7","uuid":"u1","timestamp":"2026-11-10T10:00:00Z","cwd":"C:\\work\\acme","message":{"role":"user","content":"NONCE_RESTORE_CMD"}}` + "\n"))
	zw.Close()
	f.Close()
	if _, errOut, code := runTrace(t, root, "", nil, "restore", "-archive", raw); code != 0 {
		t.Fatalf("restore: exit %d\n%s", code, errOut)
	}
	if out, _, _ := runTrace(t, root, "", nil, "search", "-all", "NONCE_RESTORE_CMD"); !strings.Contains(out, "NONCE_RESTORE_CMD") {
		t.Fatalf("restored text not found:\n%s", out)
	}
}

// trace hook always exits 0 with nothing on stdout for an event it has nothing to say on, even with bad input.
func TestHookCommandFailsOpen(t *testing.T) {
	root := t.TempDir()
	for _, stdin := range []string{`{"hook_event_name":"Stop","session_id":"s","cwd":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`, `not json`, ``} {
		if out, errOut, code := runTrace(t, root, stdin, nil, "hook", "-db", filepath.Join(root, "m.db")); code != 0 || out != "" {
			t.Fatalf("stdin %q: exit %d, stdout %q\n%s", stdin, code, out, errOut)
		}
	}
	if _, _, code := runTrace(t, root, "", nil, "hook", "-bogus"); code != 0 {
		t.Fatalf("a bad flag: exit %d", code)
	}
}

// folderTranscript finds the newest main session's transcript for exactly this folder, not a folder inside it.
func TestFolderTranscript(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	work, inner := filepath.Join(dir, "work"), filepath.Join(dir, "work", "inner")
	for i, s := range []struct{ id, cwd, end, path string }{
		{"old", work, "2026-11-10T10:00:00Z", "old.jsonl"},
		{"new", work, "2026-11-10T12:00:00Z", "new.jsonl"},
		{"deeper", inner, "2026-11-10T13:00:00Z", "deeper.jsonl"},
	} {
		db.Exec(`insert into session(id, host, role, cwd, ended_at) values (?, 'claude-code', 'r', ?, ?)`, s.id, s.cwd, s.end)
		res, _ := db.Exec(`insert into source_file(host, path) values ('claude-code', ?)`, s.path)
		fid, _ := res.LastInsertId()
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text, file_id) values (?, ?, ?, 'user', 'prompt', 'x', ?)`, s.id, i, s.end, fid)
	}
	if got := folderTranscript(db, work); got != "new.jsonl" {
		t.Errorf("work: %q, want new.jsonl", got)
	}
	if got := folderTranscript(db, filepath.Join(dir, "elsewhere")); got != "" {
		t.Errorf("a folder with no session: %q", got)
	}
	if got := folderTranscript(nil, work); got != "" {
		t.Errorf("no store: %q", got)
	}
}
