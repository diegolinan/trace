package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC E4: the side import before a command is silent; an asked-for import reports; stats never prints <nil>.
func TestQuietSideImportAndStats(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"resume", "-codex="}, {"task", "list", "-codex="}, {"search", "-codex=", "anything"}} {
		_, errOut, _ := runTrace(t, root, "", nil, args...)
		if strings.Contains(errOut, "events added") {
			t.Errorf("%v printed the import line:\n%s", args, errOut)
		}
	}
	if _, errOut, _ := runTrace(t, root, "", nil, "import", "-codex="); !strings.Contains(errOut, "events added") {
		t.Errorf("trace import did not report:\n%s", errOut)
	}
	out, errOut, code := runTrace(t, root, "", nil, "stats")
	if code != 0 || strings.Contains(out, "<nil>") || !strings.Contains(out, "hook output that never reached the session\ncount  last\n0  -") {
		t.Errorf("stats: exit %d\n%s%s", code, out, errOut)
	}
}

// SPEC S4: when S1 leaves out the only session, resume says which one and how to see it.
func TestResumeSaysWhoWasLeftOut(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at) values ('just-now', 'claude-code', ?, ?, ?, ?)`, projectRole(work), work, now, now)
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values ('just-now', 0, ?, 'user', 'prompt', 'fix the login test')`, now)
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values ('just-now', 1, ?, 'assistant', 'reply', 'NONCE_JUST_NOW')`, now)
	db.Close()
	out, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work}, "resume", "-codex=")
	if code != 0 || !strings.Contains(out, "No earlier Claude Code session in this folder.") || !strings.Contains(out, "just-now") || !strings.Contains(out, "-not x") {
		t.Errorf("exit %d\n%s%s", code, out, errOut)
	}
	// SPEC S5: with -not x the session shows, and with no branch recorded the line names none
	out, _, _ = runTrace(t, root, "", map[string]string{"_DIR": work}, "resume", "-codex=", "-not", "x")
	if !strings.Contains(out, "NONCE_JUST_NOW") || strings.Contains(out, "branch") {
		t.Errorf("-not x: %s", out)
	}
	// and a recorded branch is named
	db, _ = openDB(filepath.Join(root, "trace", "trace.db"))
	db.Exec(`update session set branch_first = 'feature-x', branch_last = 'feature-x' where id = 'just-now'`)
	db.Close()
	out, _, _ = runTrace(t, root, "", map[string]string{"_DIR": work}, "resume", "-codex=", "-not", "x")
	if !strings.Contains(out, ", branch feature-x, ") {
		t.Errorf("a recorded branch is not named: %s", out)
	}
}
