package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC I14: "remember this" pins a typed prompt, never a program's.
func TestProgramPromptIsNotPinned(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "typed.jsonl", mutLine(mutUser("typed", 0, "remember this: tabs, not spaces", "entrypoint", "cli")))
	f.write(t, "script.jsonl", mutLine(mutUser("script", 1, "Remember this: the codeword is X", "entrypoint", "sdk-cli")))
	f.importAll(t)
	for id, want := range map[string]int{"typed": 1, "script": 0} {
		if got := count(t, f.db, "select count(*) from anchor a join event e on e.id = a.event_id where a.type = 'pin' and e.session_id = ?", id); got != want {
			t.Errorf("%s: %d pins, want %d", id, got, want)
		}
	}
}

// SPEC E4: a hook's import (-trigger) is quiet too; stats tables start with their column names.
func TestHookImportQuietAndStatsHeaders(t *testing.T) {
	root := t.TempDir()
	if _, errOut, _ := runTrace(t, root, "", nil, "import", "-codex=", "-trigger", "startup"); strings.Contains(errOut, "events added") {
		t.Errorf("a hook's import printed the line:\n%s", errOut)
	}
	out, _, _ := runTrace(t, root, "", nil, "stats")
	if !strings.Contains(out, "-- last runs\nstarted  ended  trigger  files  events  error\n") {
		t.Errorf("last runs has no column names:\n%s", out)
	}
}

// SPEC E5: the flag help names CLAUDE_CONFIG_DIR, not only ~/.claude.
func TestFlagHelpNamesClaudeConfigDir(t *testing.T) {
	_, errOut, _ := runTrace(t, t.TempDir(), "", nil, "import", "-h")
	for _, flag := range []string{"-projects", "-history"} {
		i := strings.Index(errOut, "  "+flag)
		if i < 0 || !strings.Contains(errOut[i:i+300], "CLAUDE_CONFIG_DIR") {
			t.Errorf("%s help does not name CLAUDE_CONFIG_DIR:\n%s", flag, errOut)
		}
	}
}

// SPEC S6: program-only sessions are named as such, with search -all, and no -not x hint that would show nothing.
func TestResumeExplainsProgramSessions(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range []string{"p-one", "p-two"} { // sessions I4 keeps out of recall, as a newcomer's in D:\Temp were
		db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at, excluded) values (?, 'claude-code', ?, ?, ?, ?, 'temp')`, id, projectRole(work), work, now, now)
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, 1, ?, 'user', 'prompt', 'Remember this')`, id, now)
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, 2, ?, 'assistant', 'reply', 'OK')`, id, now)
	}
	// A row from before I18 with no folder keeps the old reason, which must not leak into the sentence.
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0o755)
	for _, r := range [][2]string{{"p-old", work}, {"o-old", other}} {
		db.Exec(`insert into session(id, host, role, started_at, ended_at, excluded) values (?, 'claude-code', ?, ?, ?, 'probe or test folder: x')`, r[0], projectRole(r[1]), now, now)
	}
	db.Close()
	keptLine := func(out, n string) string {
		_, line, _ := strings.Cut(out, n+" sessions here are stored but kept out of this block")
		line, _, _ = strings.Cut(line, "\n")
		return line
	}
	out, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work, "TRACE_TEST_TEMP_ROOT": root}, "resume", "-codex=")
	line := keptLine(out, "3")
	if code != 0 || !strings.Contains(line, "inside the system temp folder "+root) || !strings.Contains(line, `keep "`+work+`"`) || !strings.Contains(line, "trace search -all") || strings.Contains(out, "-not x") || strings.Contains(out, "probe or test") {
		t.Errorf("exit %d\n%s%s", code, out, errOut)
	}
	// A folder outside every temp root whose only kept-out session has no folder gets no keep to run.
	out, errOut, code = runTrace(t, root, "", map[string]string{"_DIR": other}, "resume", "-codex=")
	if line = keptLine(out, "1"); code != 0 || !strings.Contains(line, "inside the system temp folder, which") || strings.Contains(line, "keep") || strings.Contains(out, "probe or test") {
		t.Errorf("outside the temp roots: exit %d\n%s%s", code, out, errOut)
	}
}

// SPEC I15: a transcript that starts with time-only rows (claude -p's queue-operation) still gets its session's folder
// and, for a program, the worker role, from its first event row.
func TestSessionFromFirstEventRow(t *testing.T) {
	f := newMutFix(t)
	queue := mutObj{"type": "queue-operation", "operation": "enqueue", "timestamp": mutAt(0), "sessionId": "q1", "content": "The codeword is X"}
	f.write(t, "q1.jsonl", mutLines(queue, mutUser("u1", 1, "The codeword is X", "cwd", `D:\work\proj`, "entrypoint", "sdk-cli")))
	f.importAll(t)
	if got := mutStr(t, f.db, "select coalesce(cwd, '') || '|' || role from session where id = 'q1'"); got != `D:\work\proj|worker` {
		t.Errorf("session = %q, want the event row's folder and the worker role", got)
	}
}

// SPEC I16: a tool denial in a program's session is the program's; in a typed session it stays the user's.
func TestProgramDenialIsNotTheUsers(t *testing.T) {
	f := newMutFix(t)
	deny := func(uuid, entry string) mutObj {
		return mutObj{"type": "user", "uuid": uuid, "timestamp": mutAt(2), "entrypoint": entry, "toolDenialKind": "user-rejected",
			"message": mutObj{"role": "user", "content": []mutObj{{"type": "tool_result", "tool_use_id": "t-" + uuid, "content": "Execute skill: schedule.", "is_error": true}}}}
	}
	f.write(t, "p1.jsonl", mutLines(mutUser("p1u", 0, "go", "entrypoint", "sdk-cli"), deny("p1d", "sdk-cli")))
	f.write(t, "h1.jsonl", mutLines(mutUser("h1u", 0, "go", "entrypoint", "cli"), deny("h1d", "cli")))
	f.importAll(t)
	for id, want := range map[string]string{"p1": "program", "h1": "user"} {
		if got := mutStr(t, f.db, "select speaker from event where session_id = ? and kind = 'refusal'", id); got != want {
			t.Errorf("%s denial speaker = %q, want %q", id, got, want)
		}
	}
}
