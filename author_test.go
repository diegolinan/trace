package main

import (
	"os"
	"path/filepath"
	"strings"
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

// SPEC I11: a Codex thread started by `codex exec` or through MCP holds a program's prompts.
func TestCodexProgramThreadsAreNotTheUsers(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-a-t-cli.jsonl", cmMeta("t-cli", s.cwd(), map[string]any{"source": "cli"})+cmUser(1, "lo tipeé yo"))
	s.write("rollout-b-t-vs.jsonl", cmMeta("t-vs", s.cwd(), map[string]any{"source": "vscode"})+cmUser(1, "desde el editor"))
	s.write("rollout-c-t-exec.jsonl", cmMeta("t-exec", s.cwd(), map[string]any{"source": "exec"})+cmUser(1, "escrito por un script"))
	s.write("rollout-d-t-mcp.jsonl", cmMeta("t-mcp", s.cwd(), map[string]any{"source": "mcp"})+cmUser(1, "escrito por otro agente"))
	s.imp("")
	for id, want := range map[string]string{"t-cli": "user", "t-vs": "user", "t-exec": "program", "t-mcp": "program"} {
		if n := s.n("select count(*) from event where session_id = '" + id + "' and kind = 'prompt' and speaker = '" + want + "'"); n != 1 {
			t.Errorf("%s: want its prompt stored with speaker %s", id, want)
		}
	}
}

// SPEC I12: an accepted suggestion is sent by the user but worded by the model.
func TestAcceptedSuggestionIsNotTheUsersWords(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "s1.jsonl", mutLines(
		mutUser("u1", 0, "no, eso no lo hagas", "promptSource", "suggestion_accepted"),
		mutUser("u2", 1, "no, nunca toques main", "promptSource", "typed")))
	f.importAll(t)
	for uuid, want := range map[string]string{"u1": "suggestion", "u2": "prompt"} {
		if got := mutStr(t, f.db, "select speaker || '/' || kind from event where uuid = ?", uuid+":0"); got != "user/"+want {
			t.Errorf("%s = %s, want user/%s", uuid, got, want)
		}
	}
	var lasting []string
	rows, err := typedLately(f.db, "claude-code", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sid, ts, text, cwd string
		var seq int
		rows.Scan(&sid, &seq, &ts, &text, &cwd)
		lasting = append(lasting, text)
	}
	rows.Close()
	if len(lasting) != 1 || lasting[0] != "no, nunca toques main" {
		t.Errorf("words read for refusals and rules = %q, want only the typed one", lasting)
	}
	// The start block still shows it, labelled as a suggestion.
	rzNoRoles(t)
	db := rzDB(t)
	cwd := t.TempDir()
	rzSession(t, db, "s", projectRole(cwd), cwd, "2026-11-10T10:00:00Z", "2026-11-10T10:05:00Z", 60)
	rzEvent(t, db, "s", 1, "2026-11-10T10:00:00Z", "user", "suggestion", "dale, publicá la versión")
	if b := rzBlock(t, db, cwd); !strings.Contains(b, "suggestion: dale, publicá la versión") {
		t.Errorf("the start block does not show the accepted suggestion as one:\n%s", b)
	}
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
