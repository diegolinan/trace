package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// smOn turns the scrum master on in cfg for one test.
func smOn(t *testing.T) {
	t.Helper()
	old := cfg.ScrumMaster
	cfg.ScrumMaster = &scrumCfg{Model: "test-model"}
	t.Cleanup(func() { cfg.ScrumMaster = old })
}

// smEvent stores one event of a session on host, said by speaker, and returns its id.
func smEvent(t *testing.T, db *sql.DB, host, session, speaker, kind, text, ts string) int64 {
	t.Helper()
	rzExec(t, db, `insert or ignore into source_file(host, path) values (?, ?)`, host, session+".jsonl")
	rzExec(t, db, `insert or ignore into session(id, host, role) values (?, ?, 'acme')`, session, host)
	res, err := db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text, file_id)
		values (?, (select count(*) from event where session_id = ?), ?, ?, ?, ?, (select id from source_file where path = ?))`,
		session, session, ts, speaker, kind, text, session+".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// smRunner is a fake host that answers reply, or fails with err, and keeps the prompt it got.
type smRunner struct {
	reply  string
	err    error
	prompt string
	calls  int
}

func (r *smRunner) run(host, model, prompt string) (string, error) {
	r.prompt, r.calls = prompt, r.calls+1
	return r.reply, r.err
}

// SPEC T19, T21: off unless config.json has scrum_master; when on, the working agent's start block leaves the board to
// it and keeps adding, starting, stopping and closing.
func TestScrumOffByDefault(t *testing.T) {
	if scrumOn() {
		t.Fatal("the scrum master is on with no config")
	}
	var off strings.Builder
	taskSection(rzDB(t), &off, "Ana")
	smOn(t)
	var on strings.Builder
	taskSection(rzDB(t), &on, "Ana")
	for _, want := range []string{"-level", "task points", "task order", "task sprint -at"} {
		if !strings.Contains(off.String(), want) || strings.Contains(on.String(), want) {
			t.Errorf("%q: off has it %v, on has it %v", want, strings.Contains(off.String(), want), strings.Contains(on.String(), want))
		}
	}
	for _, want := range []string{"task add", "-self", "at once", "task stop", "task done", "task sprint close", "scrum master agent runs the board", "Most conversations put nothing on the board"} {
		if !strings.Contains(on.String(), want) {
			t.Errorf("on, the start block lacks %q:\n%s", want, on.String())
		}
	}
}

// SPEC T19: a run is due after a hand-off: the working agent changed the board since the last good run ended, in a
// turn of that host since its cursor. No hand-off, no run.
func TestScrumDue(t *testing.T) {
	db := rzDB(t)
	if scrumDue(db, claudeName) {
		t.Error("due with nothing said")
	}
	smEvent(t, db, claudeName, "s1", "user", "prompt", "acordate de llamar a Ana", "2026-11-18T10:00:00Z")
	if scrumDue(db, claudeName) {
		t.Error("due with words but no hand-off")
	}
	rzExec(t, db, `insert into task(text, created_at, level, rank) values ('x', '2026-11-18T10:01:00Z', 'task', 1)`)
	if !scrumDue(db, claudeName) || scrumDue(db, codexName) {
		t.Error("a hand-off: due on the host that was talked to only")
	}
	rzExec(t, db, `insert into scrum_run(trigger, host, started_at, ended_at, ok, events_to) values ('stop', ?, '2026-11-18T10:02:00Z', '2026-11-18T10:03:00Z', 1, (select max(id) from event))`, claudeName)
	if scrumDue(db, claudeName) {
		t.Error("due again with nothing new since a good run")
	}
	smEvent(t, db, claudeName, "s1", "user", "prompt", "y lo del CV", "2026-11-18T10:04:00Z")
	if scrumDue(db, claudeName) {
		t.Error("something new said and no hand-off: due")
	}
	rzExec(t, db, `update task set closed_at = '2026-11-18T10:05:00Z', closed_how = 'done'`)
	if !scrumDue(db, claudeName) {
		t.Error("not due after a close")
	}
	rzExec(t, db, `insert into scrum_run(trigger, host, started_at, ended_at, ok, events_to) values ('stop', ?, '2026-11-18T10:06:00Z', '2026-11-18T10:07:00Z', 0, (select max(id) from event))`, claudeName)
	if !scrumDue(db, claudeName) {
		t.Error("a failed run moved the cursor")
	}
}

// SPEC T20: the prompt holds the board and the new words of the host's own sessions only, verbatim.
func TestScrumPromptHostOnly(t *testing.T) {
	db := rzDB(t)
	rzExec(t, db, `insert into task(text, created_at, level, rank) values ('el CV', '2026-11-18T09:00:00Z', 'story', 1)`)
	smEvent(t, db, claudeName, "s1", "user", "prompt", "old words", "2026-11-18T09:00:00Z")
	rzExec(t, db, `insert into scrum_run(trigger, host, started_at, ended_at, ok, events_to) values ('stop', ?, '2026-11-18T09:01:00Z', '2026-11-18T09:02:00Z', 1, (select max(id) from event))`, claudeName)
	smEvent(t, db, claudeName, "s1", "user", "prompt", "después vemos lo de LinkedIn", "2026-11-18T10:00:00Z")
	last := smEvent(t, db, claudeName, "s1", "assistant", "reply", "dale, lo dejamos para después", "2026-11-18T10:00:05Z")
	smEvent(t, db, codexName, "c1", "user", "prompt", "CODEX-ONLY words", "2026-11-18T10:01:00Z")
	smEvent(t, db, claudeName, "s1", "assistant", "tool_call", "TOOL-CALL noise", "2026-11-18T10:02:00Z")
	prompt, to := scrumPrompt(db, claudeName)
	for _, want := range []string{"task 1 (story)", "después vemos lo de LinkedIn", "dale, lo dejamos para después", `"commands"`, `"note"`, "only a story carries a kind and points", "A sprint holds stories only"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, not := range []string{"CODEX-ONLY", "old words", "TOOL-CALL"} {
		if strings.Contains(prompt, not) {
			t.Errorf("prompt has %q", not)
		}
	}
	if to < last {
		t.Errorf("cursor %d, want at least %d", to, last)
	}
}

// SPEC T20: the reply is JSON: commands as lists of arguments and at most one note, capped; anything else is refused.
func TestScrumReplyParse(t *testing.T) {
	cmds, note, err := parseScrumReply("here it is:\n```json\n{\"commands\": [[\"task\", \"level\", \"1\", \"story\"]], \"note\": \"quedó pendiente LinkedIn\"}\n```")
	if err != nil || len(cmds) != 1 || strings.Join(cmds[0], " ") != "task level 1 story" || note != "quedó pendiente LinkedIn" {
		t.Errorf("parsed %q, %q, %v", cmds, note, err)
	}
	if _, note, _ := parseScrumReply(`{"commands": [], "note": "` + strings.Repeat("a", 600) + `"}`); len(note) > 500 {
		t.Errorf("note of %d bytes", len(note))
	}
	if cmds, note, err := parseScrumReply("{}"); err != nil || cmds != nil || note != "" {
		t.Errorf("an empty answer: %q, %q, %v", cmds, note, err)
	}
	for _, bad := range []string{"", "no json here", "} {", "x}", `{"commands": "task level 1 story"}`} {
		if _, _, err := parseScrumReply(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// SPEC T20: the fixed list: board commands only; sprint, but never sprint close.
func TestScrumAllows(t *testing.T) {
	for argv, want := range map[string]bool{
		"task level 1 story": true, "task sprint -at x -text y 1": true, "task sprint add 1": true, "task sprint out 1": true,
		"task sprint close": false, "task sprint": true, "task done 1": false, "task": false, "": false, "import": false, "level 1 story": false,
	} {
		if got := scrumAllows(strings.Fields(argv)); got != want {
			t.Errorf("%q: %v, want %v", argv, got, want)
		}
	}
}

// SPEC T20: only board commands from the fixed list run, in process; adding, closing, dropping, meeting, sprint close
// and anything else are refused.
func TestScrumExecAllowlist(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "anotá")))
	tkTrace(t, root, "task", "add", "-text", "the CV")
	db := tkStore(t, root)
	ran, refused := scrumExec(db, [][]string{
		{"task", "level", "1", "story"},
		{"task", "add", "-text", "LinkedIn", "-level", "story", "-points", "3"},
		{"task", "points", "1", "5"},
		{"task", "sprint", "-at", "2026-12-04T18:00:00Z", "-text", "para el viernes", "1"},
		{"task", "done", "1"},
		{"task", "drop", "1"},
		{"task", "met", "1", "1"},
		{"task", "sprint", "close"},
		{"task", "told", "3"},
		{"import"},
		{"task"},
		{"task", "level", "9", "story"}, // allowed, refused by the command itself
	})
	if len(ran) != 3 || len(refused) != 9 {
		t.Errorf("ran %q\nrefused %q", ran, refused)
	}
	if got := mutStr(t, db, `select group_concat(id || ':' || level || ':' || self || ':' || coalesce(points, '-') || ':' || (closed_at is null), ';') from (select * from task order by id)`); got != "1:story:0:5:1;2:task:1:-:1" {
		t.Errorf("tasks %q", got)
	}
}

// SPEC T21: a run is recorded with what it did; a good one moves the cursor and its note reaches the next typed prompt
// once, labelled as the scrum master's; a failed one keeps the cursor.
func TestScrumRunRecordsAndHandsNote(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "después vemos lo de LinkedIn")))
	tkTrace(t, root, "task", "add", "-text", "the CV")
	db := tkStore(t, root)
	fail := &smRunner{err: errors.New("no credit")}
	if err := scrumRun(db, "stop", claudeName, fail.run); err == nil {
		t.Error("a failed host run is no error")
	}
	if !scrumDue(db, claudeName) {
		t.Error("a failed run moved the cursor")
	}
	good := &smRunner{reply: `{"commands": [["task", "level", "1", "story"], ["task", "done", "1"]], "note": "quedó pendiente LinkedIn"}`}
	if err := scrumRun(db, "stop", claudeName, good.run); err != nil {
		t.Fatal(err)
	}
	if good.calls != 1 || !strings.Contains(good.prompt, "después vemos lo de LinkedIn") {
		t.Errorf("host got %d calls, prompt %q", good.calls, good.prompt)
	}
	if got := mutStr(t, db, `select group_concat(ok || '|' || coalesce(commands, '') || '|' || coalesce(refused, '') || '|' || coalesce(note, '') || '|' || coalesce(error, ''), ';') from (select * from scrum_run order by id)`); !strings.Contains(got, "0||||no credit") || !strings.Contains(got, `1|["task","level","1","story"]|["task","done","1"]|quedó pendiente LinkedIn|`) {
		t.Errorf("runs %q", got)
	}
	if scrumDue(db, claudeName) {
		t.Error("a good run left the cursor")
	}
	if other := scrumNotes(db, codexName); len(other) != 0 {
		t.Errorf("a Claude Code note handed to Codex: %q", other)
	}
	lines := scrumNotes(db, claudeName)
	if len(lines) != 1 || !strings.Contains(lines[0], "quedó pendiente LinkedIn") || !strings.Contains(lines[0], "scrum master") {
		t.Errorf("notes %q", lines)
	}
	if again := scrumNotes(db, claudeName); len(again) != 0 {
		t.Errorf("note handed twice: %q", again)
	}
}

// SPEC T19: the scrum master's own session never runs TRACE's hooks, and the default host runner refuses to run
// from a test binary.
func TestScrumChildIsInert(t *testing.T) {
	_, dbPath := shStore(t)
	t.Setenv(scrumChildVar, "1")
	var out strings.Builder
	err := hook(hookInput{Event: "SessionStart", Source: "startup", SessionID: "x", Cwd: filepath.Dir(dbPath)}, nil, dbPath, claudeName, "", func(*sql.DB, string) error {
		t.Error("the child imported")
		return nil
	}, &out)
	if err != nil || out.Len() != 0 {
		t.Errorf("hook in the child: %v, %q", err, out.String())
	}
	if _, err := hostScrum(claudeName, "m", "p"); err == nil {
		t.Error("the default runner ran from a test binary")
	}
}

// SPEC T19: when on, a Stop starts a detached scrum run for its host and returns; a session start starts none; off, or
// in the scrum master's own session, none starts.
func TestScrumHooksSpawn(t *testing.T) {
	_, dbPath := shStore(t)
	var spawned []string
	old := spawnScrum
	spawnScrum = func(_ []string, trigger, host string) error { spawned = append(spawned, trigger+":"+host); return nil }
	t.Cleanup(func() { spawnScrum = old })
	start := func(host string) {
		var w strings.Builder
		in := hookInput{Event: "SessionStart", Source: "startup", SessionID: "s9", Cwd: filepath.Dir(dbPath)}
		if err := hook(in, nil, dbPath, host, "", func(*sql.DB, string) error { return nil }, &w); err != nil {
			t.Fatal(err)
		}
	}
	shStop(t, dbPath, claudeName, hookInput{SessionID: "s9"})
	start(claudeName)
	if len(spawned) != 0 {
		t.Fatalf("off, spawned %q", spawned)
	}
	smOn(t)
	shStop(t, dbPath, claudeName, hookInput{SessionID: "s9"})
	shStop(t, dbPath, codexName, hookInput{SessionID: "s9"})
	start(claudeName)
	if got := strings.Join(spawned, " "); got != "stop:claude-code stop:codex" {
		t.Errorf("spawned %q", got)
	}
	if said := smStderr(t, func() { shStop(t, dbPath, claudeName, hookInput{SessionID: "s9"}) }); said != "" {
		t.Errorf("a start that worked said %q", said)
	}
	spawnScrum = func([]string, string, string) error { return errors.New("no exe") }
	if said := smStderr(t, func() { shStop(t, dbPath, claudeName, hookInput{SessionID: "s9"}) }); !strings.Contains(said, "the scrum master did not start: no exe") {
		t.Errorf("a start that failed said %q", said)
	}
	spawnScrum = func(_ []string, trigger, host string) error { spawned = append(spawned, trigger+":"+host); return nil }
	t.Setenv(scrumChildVar, "1")
	shStop(t, dbPath, claudeName, hookInput{SessionID: "s9"})
	if len(spawned) != 3 {
		t.Errorf("the child spawned: %q", spawned)
	}
}

// SPEC T19: a store that cannot be opened fails the run.
func TestCmdScrumBadStore(t *testing.T) {
	smOn(t)
	dir := t.TempDir() // a folder where the store should be
	if err := cmdScrum(opts{dbPath: dir, trigger: "stop", host: claudeName}); err == nil {
		t.Error("a store that is a folder ran")
	}
}

// SPEC T19: trace scrum runs the scrum master only when on and due, one run at a time per store.
func TestCmdScrum(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "después vemos lo de LinkedIn")))
	tkTrace(t, root, "task", "add", "-text", "the CV")
	dbPath := filepath.Join(root, "trace", "trace.db")
	fake := &smRunner{reply: `{"commands": [], "note": ""}`}
	old := hostScrum
	hostScrum = fake.run
	t.Cleanup(func() { hostScrum = old })
	o := opts{dbPath: dbPath, trigger: "stop", host: claudeName}
	if err := cmdScrum(o); err != nil || fake.calls != 0 {
		t.Fatalf("off: %v, %d calls", err, fake.calls)
	}
	smOn(t)
	lock, err := tryLock(dbPath + ".scrum.lock")
	if err != nil || lock == nil {
		t.Fatal(err)
	}
	if err := cmdScrum(o); err != nil || fake.calls != 0 {
		t.Errorf("lock held: %v, %d calls", err, fake.calls)
	}
	lock.Close()
	if err := cmdScrum(o); err != nil || fake.calls != 1 {
		t.Errorf("due: %v, %d calls", err, fake.calls)
	}
	if err := cmdScrum(o); err != nil || fake.calls != 1 {
		t.Errorf("not due again: %v, %d calls", err, fake.calls)
	}
}

// SPEC T20: a run reads at most scrumWords of the new conversation, the newest; exactly that much is not cut.
func TestScrumPromptCut(t *testing.T) {
	db := rzDB(t)
	head := len("[2026-11-18T10:00:00Z user] ") + len("\n")
	smEvent(t, db, claudeName, "s1", "user", "prompt", strings.Repeat("~", scrumWords-head), "2026-11-18T10:00:00Z")
	if prompt, _ := scrumPrompt(db, claudeName); strings.Contains(prompt, "(earlier words cut)") {
		t.Error("cut at exactly the limit")
	}
	smEvent(t, db, claudeName, "s1", "user", "prompt", "NEWEST", "2026-11-18T10:01:00Z")
	prompt, _ := scrumPrompt(db, claudeName)
	if !strings.Contains(prompt, "(earlier words cut)\n") || !strings.Contains(prompt, "NEWEST") || strings.Count(prompt, "~") >= scrumWords-head {
		t.Errorf("over the limit: cut %v, newest kept %v", strings.Contains(prompt, "(earlier words cut)"), strings.Contains(prompt, "NEWEST"))
	}
}

// SPEC T19: only a Stop, the end of a turn that may hold a hand-off, starts a run.
func TestScrumTrigger(t *testing.T) {
	for _, c := range []struct {
		in   hookInput
		want string
	}{
		{hookInput{Event: "Stop"}, "stop"}, {hookInput{Event: "SessionStart", Source: "startup"}, ""}, {hookInput{Event: "SessionStart", Source: "resume"}, ""},
		{hookInput{Event: "SessionStart", Source: "compact"}, ""}, {hookInput{Event: "PreCompact"}, ""}, {hookInput{Event: "SessionEnd"}, ""}, {hookInput{Event: "UserPromptSubmit"}, ""},
	} {
		if got := scrumTrigger(c.in); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.in.Event, c.in.Source, got, c.want)
		}
	}
}

// SPEC T19: each host's headless flags, and the child's environment: marked, without the user's session ids.
func TestScrumArgsAndEnv(t *testing.T) {
	if got := strings.Join(scrumArgs(claudeName, "", "L"), " "); got != "-p --no-session-persistence --tools  --strict-mcp-config --setting-sources project --output-format text" {
		t.Errorf("claude %q", got)
	}
	if got := strings.Join(scrumArgs(codexName, "gpt", "L"), " "); got != "exec --ephemeral --skip-git-repo-check -s read-only -o L --model gpt -" {
		t.Errorf("codex %q", got)
	}
	env := strings.Join(scrumEnv([]string{"PATH=x", "CLAUDE_CODE_SESSION_ID=s1", "CODEX_THREAD_ID=c1", scrumChildVar + "=0"}), " ")
	if env != "PATH=x "+scrumChildVar+"=1 CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1" {
		t.Errorf("env %q", env)
	}
}

// SPEC T19: trace scrum waits for an import that is going, so it reads the turn that just ended, then runs; the
// default runner, refusing from a test binary, is recorded as a failed run.
func TestCmdScrumWaitsForImport(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "después vemos lo de LinkedIn")))
	if err := os.MkdirAll(filepath.Join(root, "trace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "trace", "config.json"), []byte(`{"scrum_master": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tkTrace(t, root, "task", "add", "-text", "the CV") // the hand-off
	dbPath := filepath.Join(root, "trace", "trace.db")
	lock, err := tryLock(dbPath + ".lock")
	if err != nil || lock == nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(1500 * time.Millisecond); lock.Close() }()
	if _, errOut, code := runTrace(t, root, "", nil, "scrum", "-trigger", "stop", "-host", claudeName); code != 1 || !strings.Contains(errOut, "test binary") {
		t.Errorf("scrum: exit %d, %q", code, errOut)
	}
	if got := mutStr(t, tkStore(t, root), `select coalesce(group_concat(ok || ':' || error, ';'), '') from scrum_run`); got != "0:no scrum master host runs from a test binary" {
		t.Errorf("runs %q", got)
	}
}
