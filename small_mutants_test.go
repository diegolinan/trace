package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// smKeepCfg restores cfg and the patterns compiled from it when the test ends.
func smKeepCfg(t *testing.T) {
	t.Helper()
	old := cfg
	t.Cleanup(func() { cfg = old; compileConfig() })
}

// smFailing is a check command that prints marker and fails, in the platform's shell.
func smFailing(marker string) string {
	if runtime.GOOS == "windows" {
		return "echo " + marker + " & exit /b 1"
	}
	return "echo " + marker + "; exit 1"
}

// smHook runs ruleHook as the hook does and returns what the agent would read ("" when it writes nothing).
func smHook(t *testing.T, in hookInput, dbPath string, checks bool) string {
	t.Helper()
	var w bytes.Buffer
	if err := ruleHook(in, "r", "claude-code", dbPath, checks, &w); err != nil {
		t.Fatal(err)
	}
	if w.Len() == 0 {
		return ""
	}
	var out struct {
		Hook struct {
			Text string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(w.Bytes(), &out); err != nil || out.Hook.Text == "" {
		t.Fatalf("hook output %q: %v", w.String(), err)
	}
	return out.Hook.Text
}

func smExists(p string) bool { _, err := os.Stat(p); return err == nil }

// A check whose Match pattern does not compile is reported to the agent, never skipped in silence.
func TestCheckBadMatchPatternIsReported(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = []check{{Name: "bad-match", On: "Bash", Match: "(", Run: "exit 0"}}, nil
	in := hookInput{Event: "PostToolUse", SessionID: "s", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`), Cwd: t.TempDir()}
	if got := smHook(t, in, filepath.Join(t.TempDir(), "m.db"), true); !strings.Contains(got, `"bad-match" is misconfigured`) {
		t.Fatalf("a Match that does not compile was not reported: %q", got)
	}
}

// An empty field of the tool input adds nothing to the target, so a Match anchored at the start still finds the
// command, and the run is logged with the command alone.
func TestCheckTargetSkipsEmptyFields(t *testing.T) {
	db := riDB(t, t.TempDir())
	cs := []check{{Name: "commit", On: "Bash", Match: `^git commit`, Run: "lint"}}
	fail := func(context.Context, string, string) (string, error) { return "broken\n", errors.New("exit status 1") }
	in := hookInput{SessionID: "s", ToolName: "Bash", ToolInput: json.RawMessage(`{"file_path":"","command":"git commit -m x"}`), Cwd: "w"}
	if got := runChecks(db, in, cs, fail); !strings.Contains(got, `"commit" failed`) {
		t.Fatalf("the check did not apply to the command: %q", got)
	}
	if n := count(t, db, "select count(*) from check_run where target = 'git commit -m x'"); n != 1 {
		t.Fatalf("runs logged with the bare command as target: %d, want 1", n)
	}
}

// A check command that leaves a background process holding its output still returns a few seconds after the
// command itself ends, not when that process does.
func TestCheckDoesNotWaitForAStrayChild(t *testing.T) {
	stray := "sleep 20 & echo started"
	if runtime.GOOS == "windows" {
		stray = "start /b ping -n 20 127.0.0.1"
	}
	start := time.Now()
	runShell(context.Background(), os.TempDir(), stray) // not t.TempDir: the stray process keeps its folder busy
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the check waited %v for a process it left running", d)
	}
}

// A tool call no check applies to opens no store: the database file is not even created.
func TestHookOpensNoStoreWhenNoCheckApplies(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = []check{{Name: "writes", On: "Write", Run: smFailing("SM-NOT-HERE")}}, nil
	dbPath := filepath.Join(t.TempDir(), "m.db")
	in := hookInput{Event: "PostToolUse", SessionID: "s", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`), Cwd: t.TempDir()}
	if got := smHook(t, in, dbPath, true); got != "" {
		t.Fatalf("output for a call no check applies to: %q", got)
	}
	if smExists(dbPath) {
		t.Fatal("the store was opened for a call no check applies to")
	}
}

// Checks run only when the hook was installed for them and only after a tool call, never on a typed prompt.
func TestChecksRunOnlyWhenOnAndAfterAToolCall(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = []check{{Name: "any", Run: smFailing("SM-CHECK-RAN")}}, nil
	tool := hookInput{Event: "PostToolUse", SessionID: "s", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`), Cwd: t.TempDir()}
	if got := smHook(t, tool, filepath.Join(t.TempDir(), "m.db"), false); got != "" {
		t.Errorf("checks ran with checks off: %q", got)
	}
	prompt := hookInput{Event: "UserPromptSubmit", SessionID: "s", Cwd: t.TempDir()}
	if got := smHook(t, prompt, filepath.Join(t.TempDir(), "m.db"), true); got != "" {
		t.Errorf("checks ran on a typed prompt: %q", got)
	}
	if got := smHook(t, tool, filepath.Join(t.TempDir(), "m.db"), true); !strings.Contains(got, "SM-CHECK-RAN") {
		t.Errorf("the check did not run after a tool call with checks on: %q", got)
	}
}

// smUsage is a Claude Code assistant line reporting tokens of context.
func smUsage(t *testing.T, tokens int) string {
	return riLine(t, map[string]any{"type": "assistant", "message": map[string]any{"usage": map[string]any{"input_tokens": tokens}}})
}

// With the rule card on, a call whose transcript reports no context opens no store.
func TestCardWithoutContextOpensNoStore(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, &reinjectCfg{}
	dir := t.TempDir()
	tr := riWrite(t, dir, "s.jsonl", riClaudeRecord(t, "instructions", [2]string{filepath.Join(dir, "gone.md"), "SM-RULE keep it"}))
	dbPath := filepath.Join(dir, "m.db")
	in := hookInput{Event: "UserPromptSubmit", SessionID: "s1", Transcript: tr, Cwd: dir}
	if got := smHook(t, in, dbPath, false); got != "" {
		t.Fatalf("output without context: %q", got)
	}
	if smExists(dbPath) {
		t.Fatal("the store was opened for a call with no context")
	}
}

// smCardRun makes a session whose first reading is one token of context, then one with a million: the first is the
// baseline, so the second brings the card. It returns the second output and what the hook wrote to stderr.
func smCardRun(t *testing.T) (string, string) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, &reinjectCfg{}
	dir := t.TempDir()
	rec := riClaudeRecord(t, "instructions", [2]string{filepath.Join(dir, "gone.md"), "SM-RULE keep it"})
	tr := riWrite(t, dir, "s.jsonl", rec, smUsage(t, 1))
	dbPath := filepath.Join(dir, "m.db")
	in := hookInput{Event: "UserPromptSubmit", SessionID: "s1", Transcript: tr, Cwd: dir}
	var second string
	stderr := smStderr(t, func() {
		if got := smHook(t, in, dbPath, false); got != "" {
			t.Errorf("the baseline call injected: %q", got)
		}
		riWrite(t, dir, "s.jsonl", rec, smUsage(t, 1), smUsage(t, 1000000))
		second = smHook(t, in, dbPath, false)
	})
	return second, stderr
}

// One token of context is a reading: it sets the baseline, and the card comes once enough context follows.
func TestOneTokenOfContextIsTheBaseline(t *testing.T) {
	if got, _ := smCardRun(t); !strings.Contains(got, "SM-RULE keep it") {
		t.Fatalf("no card after a million tokens over a one-token baseline: %q", got)
	}
}

// A card call that goes well writes nothing to stderr.
func TestCardRunWritesNoError(t *testing.T) {
	if _, stderr := smCardRun(t); stderr != "" {
		t.Fatalf("stderr: %q", stderr)
	}
}

// smStderr runs f and returns what it wrote to os.Stderr.
func smStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() { os.Stderr = old }()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}

// A path or url said twice in one event is one tag.
func TestAnchorsOncePerValue(t *testing.T) {
	got := anchors("edit docs/a.md, then docs/a.md again; see https://example.com/x and https://example.com/x", "reply", "Bash")
	n := map[string]int{}
	for _, a := range got {
		n[a.typ+" "+a.value]++
	}
	for k, c := range n {
		if c != 1 {
			t.Errorf("%s tagged %d times", k, c)
		}
	}
	if n["path docs/a.md"] != 1 || n["url https://example.com/x"] != 1 || n["tool Bash"] != 1 {
		t.Errorf("tags: %v", got)
	}
}

// A pin phrase pins only what the user typed (a prompt or a steering message), never the agent's reply.
func TestPinOnlyFromTypedMessages(t *testing.T) {
	smKeepCfg(t)
	cfg.PinPhrases = nil
	compileConfig()
	pinned := func(kind string) bool {
		return slices.Contains(anchors("remember this: tabs, not spaces", kind, ""), anchorHit{"pin", "user"})
	}
	if !pinned("prompt") || !pinned("steering") {
		t.Errorf("a typed message with the phrase is not pinned: prompt %v, steering %v", pinned("prompt"), pinned("steering"))
	}
	if pinned("reply") {
		t.Error("the agent's reply was pinned")
	}
}

// smHookEntries is hooks.json's TRACE entry for each event, by event name.
func smHookEntries(t *testing.T, b []byte) map[string]map[string]any {
	t.Helper()
	var f struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for ev, gs := range f.Hooks {
		for _, g := range gs {
			for _, h := range g.Hooks {
				if codexHookOwner(h) != "" {
					out[ev] = h
				}
			}
		}
	}
	return out
}

// The events whose output reaches the model carry Codex's context limit; the others (Stop, Interrupt, SessionEnd)
// do not.
func TestCodexContextLimitOnlyWhereOutputReachesTheModel(t *testing.T) {
	smKeepCfg(t)
	cfg = config{Reinject: &reinjectCfg{}, Checks: []check{{On: "Bash"}}}
	b, err := codexHooksPlan(nil, `C:\t\trace.exe`, `D:\mem`)
	if err != nil {
		t.Fatal(err)
	}
	entries := smHookEntries(t, b)
	want := map[string]bool{"SessionStart": true, "Stop": false, "Interrupt": false, "SessionEnd": false,
		"SubagentStart": true, "PostToolUse": true, "UserPromptSubmit": true}
	for ev, ctx := range want {
		e, ok := entries[ev]
		if !ok {
			t.Errorf("%s: no TRACE entry", ev)
			continue
		}
		if limit, has := e["additionalContextLimit"]; has != ctx || (ctx && limit != float64(codexContextLimit)) {
			t.Errorf("%s: additionalContextLimit %v (present %v), want present %v", ev, limit, has, ctx)
		}
	}
}

// codexHookHomes names each TRACE home once and nothing for another tool's hook.
func TestCodexHookHomesEachOnce(t *testing.T) {
	smKeepCfg(t)
	cfg = config{}
	old := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-tool"}]}]}}`)
	b, err := codexHooksPlan(old, `C:\t\trace.exe`, `D:\memA`)
	if err == nil {
		b, err = codexHooksPlan(b, `C:\t\trace.exe`, `D:\memB`)
	}
	if err != nil {
		t.Fatal(err)
	}
	got := codexHookHomes(b)
	slices.Sort(got)
	if strings.Join(got, "|") != `D:\memA|D:\memB` {
		t.Fatalf("homes %q, want each TRACE home once", got)
	}
}

// Installing says what Codex will run and reports a change; removing reports a change and prints nothing.
func TestSetCodexHooksSaysWhatItDid(t *testing.T) {
	smKeepCfg(t)
	cfg = config{}
	dir := t.TempDir()
	exe, home := `C:\t\trace.exe`, `D:\mem`
	var w bytes.Buffer
	changed, err := setCodexHooks(dir, exe, home, &w)
	if err != nil || !changed {
		t.Fatalf("install: changed %v, err %v", changed, err)
	}
	if !strings.Contains(w.String(), "Codex will run") || !strings.Contains(w.String(), psScript(exe, home)) {
		t.Errorf("install output: %q", w.String())
	}
	w.Reset()
	changed, err = setCodexHooks(dir, "", home, &w)
	if err != nil || !changed {
		t.Fatalf("remove: changed %v, err %v", changed, err)
	}
	if w.Len() != 0 {
		t.Errorf("remove printed: %q", w.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "hooks.json")); bytes.Contains(b, []byte(codexHookMark)) {
		t.Errorf("hooks left after removal: %s", b)
	}
}

// The user's name is config's user, else "the user".
func TestUserNameFromConfig(t *testing.T) {
	smKeepCfg(t)
	cfg.User = "Ana"
	if got := userName(); got != "Ana" {
		t.Errorf("with user Ana: %q", got)
	}
	cfg.User = ""
	if got := userName(); got != "the user" {
		t.Errorf("with no user: %q", got)
	}
}

// With roles configured, a folder none of them names is "other"; with none, it is its own project.
func TestFolderRoleWithAndWithoutRoles(t *testing.T) {
	smKeepCfg(t)
	cfg.Roles = []roleRule{{Name: "shop", Folder: "shop"}}
	if got := folderRole("D--work-blog"); got != "other" {
		t.Errorf("one role, another folder: %q, want other", got)
	}
	if got := folderRole("D--work-shop"); got != "shop" {
		t.Errorf("one role, its folder: %q", got)
	}
	cfg.Roles = nil
	if got := folderRole("D--Work-Blog"); got != "project:d--work-blog" {
		t.Errorf("no roles: %q", got)
	}
}

// A role's label comes from that role alone; a role without one, or an unknown role, gets the fallback.
func TestRoleLabelFromItsOwnRole(t *testing.T) {
	smKeepCfg(t)
	cfg.Roles = []roleRule{{Name: "plain"}, {Name: "shop", Label: "The shop"}}
	for role, want := range map[string]string{"plain": "fallback", "unknown": "fallback", "shop": "The shop"} {
		if got := roleLabel(role, "fallback"); got != want {
			t.Errorf("roleLabel(%q) = %q, want %q", role, got, want)
		}
	}
}

// A lone "no" refuses; "no" followed by more corrects; an approval counts only as the answer's first clause, so it
// does not count after "no" or after a request to explain.
func TestMarksNoAndFirstClause(t *testing.T) {
	cases := map[string]marks{
		"no":                {rejection: true},
		"no, dale":          {correction: true},
		"no entiendo, dale": {},
		"dale":              {approval: true},
		"mira el log, dale": {},
	}
	for text, want := range cases {
		if got := markText(text); got != want {
			t.Errorf("markText(%q) = %+v, want %+v", text, got, want)
		}
	}
}

// Reopening a store keeps each raw copy's committed length: only a store from before the column fills it in.
func TestReopenKeepsArchiveBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	copyFile := filepath.Join(dir, "s1.jsonl.gz")
	os.WriteFile(copyFile, make([]byte, 1234), 0o644)
	db.Exec(`insert into source_file(host, path, archived_as, archive_bytes) values ('claude-code', 't1', ?, 10)`, copyFile)
	db.Close()
	db = riDB(t, dir)
	if n := count(t, db, "select archive_bytes from source_file where path = 't1'"); n != 10 {
		t.Fatalf("committed length %d after reopening, want 10", n)
	}
}

// A store from before archive_bytes gets every raw copy's size, not only the first one's.
func TestArchiveBytesBackfillEveryCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(dir, "a.gz"), filepath.Join(dir, "b.gz")
	os.WriteFile(a, make([]byte, 100), 0o644)
	os.WriteFile(b, make([]byte, 200), 0o644)
	db.Exec(`insert into source_file(host, path, archived_as) values ('claude-code', 'ta', ?), ('claude-code', 'tb', ?)`, a, b)
	if _, err := db.Exec("alter table source_file drop column archive_bytes"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = riDB(t, dir)
	if n := count(t, db, "select archive_bytes from source_file where path = 'ta'"); n != 100 {
		t.Errorf("first copy %d bytes, want 100", n)
	}
	if n := count(t, db, "select archive_bytes from source_file where path = 'tb'"); n != 200 {
		t.Errorf("second copy %d bytes, want 200", n)
	}
}

// The redaction mend runs once per store (SPEC P1): a store already at version 2 is not rewritten when opened.
func TestRedactionMendRunsOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	const raw = "key sk-abcdefghijklmnopqrstuvwxyz0123"
	db.Exec(`insert into card(sha, first_seen, text) values ('c1', 'now', ?)`, raw)
	db.Close()
	db = riDB(t, dir)
	var got string
	db.QueryRow("select text from card where sha = 'c1'").Scan(&got)
	if got != raw {
		t.Fatalf("a mended store was mended again: %q", got)
	}
}

// The compressed-key mend runs once per store (SPEC I3): a store at version 1 keeps its rows as stored and moves on
// to the current version, 3.
func TestZstMendRunsOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "codex:s:@r.jsonl.zst:1"
	db.Exec(`insert into session(id, host, role) values ('s', 'codex', 'r')`)
	if _, err := db.Exec(`insert into event(uuid, session_id, seq, ts, speaker, kind, text) values (?, 's', 1, 't', 'user', 'prompt', 'hi')`, key); err != nil {
		t.Fatal(err)
	}
	db.Exec("pragma user_version = 1")
	db.Close()
	db = riDB(t, dir)
	if n := count(t, db, "select count(*) from event where uuid = ?", key); n != 1 {
		t.Errorf("a mended store's row was mended again: %d rows keep the key", n)
	}
	if n := count(t, db, "pragma user_version"); n != 3 {
		t.Errorf("user_version %d, want 3", n)
	}
}

// A detached command runs once.
func TestStartDetachedRunsOnce(t *testing.T) {
	out := filepath.Join(t.TempDir(), "runs.txt")
	cmd := exec.Command("sh", "-c", "echo x >> '"+out+"'")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "echo x>>"+out)
	}
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // time for a second copy, if one was started, to write too
	b, _ := os.ReadFile(out)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("the command ran %d times, want 1", n)
	}
}
