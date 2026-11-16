package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SPEC I9: a prompt a program wrote (a session started with `claude -p` or through MCP) is not the user's.
func TestProgramPromptsAreNotTheUsers(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "h1.jsonl", mutLine(mutUser("h1", 0, "lo tipeé yo", "entrypoint", "cli")))
	f.write(t, "w1.jsonl", mutLine(mutUser("w1", 1, "escrito por un script", "entrypoint", "sdk-cli")))
	f.write(t, "m1.jsonl", mutLine(mutUser("m1", 2, "escrito por otro agente", "entrypoint", "mcp")))
	f.write(t, "v1.jsonl", mutLine(mutUser("v1", 3, "lo tipeé en el editor", "entrypoint", "claude-vscode")))
	f.importAll(t)
	for id, want := range map[string]string{"h1": "user", "w1": "program", "m1": "program", "v1": "user"} {
		if got := mutStr(t, f.db, "select speaker from event where session_id = ? and kind = 'prompt'", id); got != want {
			t.Errorf("%s prompt speaker = %s, want %s", id, got, want)
		}
	}
	if got := mutStr(t, f.db, "select role from session where id = 'm1'"); got != "worker" {
		t.Errorf("an MCP-started session's role = %s, want worker", got)
	}
}

// SPEC I9: a store made before I9 keeps worker prompts as the user's; opening it corrects them once.
func TestOldWorkerPromptsBecomeProgram(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("insert into session(id, host, role) values ('w', 'claude-code', 'worker'), ('p', 'claude-code', 'main')")
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values
		('w', 1, '2026-11-10T10:00:00Z', 'user', 'prompt', 'escrito por un script'),
		('p', 1, '2026-11-10T10:00:00Z', 'user', 'prompt', 'lo tipeé yo')`)
	db.Exec("pragma user_version = 2")
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, want := range map[string]string{"w": "program", "p": "user"} {
		if got := mutStr(t, db, "select speaker from event where session_id = ?", id); got != want {
			t.Errorf("%s speaker = %s, want %s", id, got, want)
		}
	}
	// Once: a store already at the current version is not scanned again on every open.
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values ('w', 2, '2026-11-10T10:01:00Z', 'user', 'prompt', 'later')`)
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	if got := mutStr(t, db, "select speaker from event where session_id = 'w' and seq = 2"); got != "user" {
		t.Errorf("the one-time correction ran again on a store at the current version: speaker %s", got)
	}
	db.Close()
}

// SPEC I10: in a configured period, prompts typed in the user's terminal were someone else's.
func TestNotTypedByUserPeriod(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	cfg.NotTypedByUser = []period{{From: "2026-11-01", To: "2026-11-02", Note: "an earlier test run"},
		{From: "2026-11-10", To: "2026-11-10", Note: "another agent drove the console"}}
	f := newMutFix(t)
	at := func(d int) int64 { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC).UnixMilli() }
	os.WriteFile(f.history(), []byte(mutLines(
		mutObj{"display": "antes", "timestamp": at(9), "project": `D:\Work\acme`, "sessionId": "h9"},
		mutObj{"display": "dentro", "timestamp": at(10), "project": `D:\Work\acme`, "sessionId": "h10"},
		mutObj{"display": "después", "timestamp": at(11), "project": `D:\Work\acme`, "sessionId": "h11"})), 0o644)
	f.write(t, "t1.jsonl", mutLine(mutUser("t1", 0, "en una terminal ese día", "entrypoint", "cli")))
	f.importAll(t)
	for id, want := range map[string]string{"h9": "user", "h10": "program", "h11": "user", "t1": "program"} {
		if got := mutStr(t, f.db, "select speaker from event where session_id = ? and kind = 'prompt'", id); got != want {
			t.Errorf("%s prompt speaker = %s, want %s", id, got, want)
		}
	}
}
