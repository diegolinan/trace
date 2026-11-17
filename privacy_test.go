package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC I3: a store made before the plain-name keys is mended once when opened.
func TestZstKeysMended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`insert into session(id, host, role) values ('s', 'codex', 'r')`)
	for _, e := range []struct{ uuid, text string }{
		{"codex:t1:@rollout-a.jsonl:10", "twin plain"},
		{"codex:t1:@rollout-a.jsonl.zst:10", "twin compressed"},
		{"codex:t2:@rollout-b.jsonl.zst:20", "compressed only"},
		{"codex:t3:7", "with an ordinal"},
	} {
		res, err := db.Exec(`insert into event(uuid, session_id, seq, ts, speaker, kind, text) values (?, 's', 1, '2026-11-10T10:00:00Z', 'user', 'prompt', ?)`, e.uuid, e.text)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		db.Exec(`insert into anchor(event_id, type, value) values (?, 'tool', 'x')`, id)
	}
	db.Exec("pragma user_version = 0")
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]int{
		"select count(*) from event":                                                 3,
		"select count(*) from event where uuid like '%.zst:%'":                       0,
		"select count(*) from event where uuid = 'codex:t2:@rollout-b.jsonl:20'":     1,
		"select count(*) from event where text = 'twin plain'":                       1,
		"select count(*) from anchor where event_id not in (select id from event)":   0,
		"select count(*) from event_fts where event_fts match '\"twin compressed\"'": 0,
	} {
		if got := count(t, db, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

// SPEC P1: after every path that writes text (an import with an instructions record, a check, the Claude and Codex
// instruction copies, a rule card), no text column of any table holds the secret.
func TestNoSecretInTheStore(t *testing.T) {
	tok := "gh" + "p_" + strings.Repeat("Q7", 18) // a GitHub-token shape redact knows; built so no literal token sits here
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rules := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(rules, []byte("deploy with "+tok), 0o644)
	files, _ := json.Marshal([]map[string]string{{"path": rules, "type": "Project", "content": "deploy with " + tok}})

	// An import: a Claude transcript whose instructions record carries the secret (loaded_text).
	proj := filepath.Join(dir, "projects", "C--work-acme")
	os.MkdirAll(proj, 0o755)
	transcript := filepath.Join(proj, "s9.jsonl")
	os.WriteFile(transcript, []byte(
		`{"type":"user","sessionId":"s9","uuid":"u1","timestamp":"2026-11-10T10:00:00Z","cwd":"C:\\work\\acme","message":{"role":"user","content":"hola"}}`+"\n"+
			`{"type":"attachment","sessionId":"s9","uuid":"u2","timestamp":"2026-11-10T10:00:01Z","cwd":"C:\\work\\acme","attachment":{"type":"instructions","files":`+string(files)+`}}`+"\n"), 0o644)
	if err := runImport(db, filepath.Join(dir, "projects"), "", "", "", "", "test"); err != nil {
		t.Fatal(err)
	}
	// A check: the command and its output name the secret (check_run).
	runChecks(db, hookInput{SessionID: "s9", ToolName: "Bash", ToolInput: []byte(`{"command":"curl -H 'Authorization: token ` + tok + `'"}`), Cwd: dir},
		[]check{{Name: "leak", On: "Bash", Run: "echo " + tok + " && exit 1"}}, runShell)
	// The Claude and Codex instruction copies (claude_instructions, codex_instructions).
	srcs := claudeSources(db, transcript)
	msg, _ := json.Marshal("# AGENTS.md instructions for C:\\work\\acme\n\n<INSTRUCTIONS>\nkey " + tok + "\n</INSTRUCTIONS>")
	rollout := filepath.Join(dir, "rollout.jsonl")
	os.WriteFile(rollout, []byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":`+string(msg)+`}]}}`+"\n"), 0o644)
	srcs = append(srcs, codexSources(db, rollout)...)
	// A rule card from those files (card).
	text, _ := card(srcs, "", 8000)
	now := time.Now()
	reinjection(db, "s9", 1000, text, 7, now)
	reinjection(db, "s9", 1000000, text, 7, now)
	// A task the agent writes down (task).
	if err := taskAdd(db, newTask{text: "rotate " + tok}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if secretScan(t, db, tok) == 0 {
		t.Error("no redacted text was stored at all: the paths above did not write")
	}
}

// SPEC P1: rows stored before redaction reached these tables are redacted once when the store is opened.
func TestOldRowsRedacted(t *testing.T) {
	tok := "gh" + "p_" + strings.Repeat("Z3", 18)
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into check_run(ts, session_id, name, tool, target, ok, output) values ('2026-11-10', 's', 'c', 'Bash', 'curl ` + tok + `', 0, 'said ` + tok + `')`,
		`insert into loaded_text(sha, first_seen, text) values ('a', '2026-11-10', 'key ` + tok + `')`,
		`insert into claude_instructions(transcript, offset, files) values ('t', 1, '[{"content":"key ` + tok + `"}]')`,
		`insert into codex_instructions(rollout, offset, text) values ('r', 1, 'key ` + tok + `')`,
		`insert into card(sha, first_seen, text) values ('b', '2026-11-10', 'key ` + tok + `')`,
		`pragma user_version = 1`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := secretScan(t, db, tok); n < 6 {
		t.Errorf("%d redacted texts, want the 6 old ones", n)
	}
}

// secretScan fails t for every text column of every table that holds tok, and returns how many texts are redacted.
func secretScan(t *testing.T, db *sql.DB, tok string) (stored int) {
	t.Helper()
	tables, _ := db.Query("select name from sqlite_master where type = 'table' and name not like 'sqlite_%' and name not like '%fts%'")
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	for _, tb := range names {
		cols, _ := db.Query("select name, type from pragma_table_info(?)", tb)
		var text []string
		for cols.Next() {
			var c, ty string
			cols.Scan(&c, &ty)
			if ty == "" || strings.Contains(strings.ToLower(ty), "text") {
				text = append(text, c)
			}
		}
		cols.Close()
		for _, c := range text {
			var n int
			db.QueryRow(`select count(*) from "`+tb+`" where "`+c+`" like ?`, "%"+tok+"%").Scan(&n)
			if n > 0 {
				t.Errorf("%s.%s holds the secret in %d row(s)", tb, c, n)
			}
			db.QueryRow(`select count(*) from "` + tb + `" where "` + c + `" like '%REDACTED%'`).Scan(&n)
			stored += n
		}
	}
	return stored
}
