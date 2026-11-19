package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mmTraceExe copies the test binary to root/bin/trace(.exe): installs recognise TRACE's hooks by the binary's name.
func mmTraceExe(t *testing.T, root string) string {
	t.Helper()
	exe := filepath.Join(root, "bin", "trace")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.MkdirAll(filepath.Dir(exe), 0o755)
	if b, err := os.ReadFile(os.Args[0]); err != nil || os.WriteFile(exe, b, 0o755) != nil {
		t.Fatal("cannot copy the test binary")
	}
	return exe
}

// mmStore opens the default store of a runTrace root (TRACE_HOME is root/trace).
func mmStore(t *testing.T, root string) string {
	t.Helper()
	p := filepath.Join(root, "trace", "trace.db")
	db, err := openDB(p)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return p
}

func mmWrite(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A broken config.json stops any command but a hook with exit 1 and says so; nothing is done.
func TestBrokenConfigExitsOne(t *testing.T) {
	root := t.TempDir()
	mmWrite(t, filepath.Join(root, "trace", "config.json"), `{broken`)
	_, errOut, code := runTrace(t, root, "", nil, "import", "-projects=", "-history=", "-codex=")
	if code != 1 || !strings.Contains(errOut, "config.json is not valid JSON, nothing done") {
		t.Fatalf("exit %d, want 1\n%s", code, errOut)
	}
	if exists(filepath.Join(root, "trace", "trace.db")) {
		t.Fatal("a broken config still imported")
	}
}

// An empty -home is a usage error (exit 2), not the working folder.
func TestEmptyHomeIsUsageError(t *testing.T) {
	root := t.TempDir()
	_, errOut, code := runTrace(t, root, "", nil, "import", "-home=", "-projects=", "-history=", "-codex=")
	if code != 2 || !strings.Contains(errOut, "-home needs a folder") {
		t.Fatalf("exit %d, want 2\n%s", code, errOut)
	}
	if exists(filepath.Join(root, "trace.db")) {
		t.Fatal("an empty -home imported into the working folder")
	}
}

// A hook that works answers on stdout and writes no error on stderr: no internal error, no error line.
func TestHookQuietWhenFine(t *testing.T) {
	root := t.TempDir()
	in := `{"hook_event_name":"SubagentStart","session_id":"s","cwd":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`
	out, errOut, code := runTrace(t, root, in, nil, "hook", "-db", filepath.Join(root, "m.db"), "-projects=", "-history=", "-codex=")
	if code != 0 || !strings.Contains(out, "You are a helper agent") || strings.Contains(errOut, "trace hook:") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// install -host claude-code refuses hooks that name no home unless -migrate is given, and changes nothing.
func TestInstallLegacyNeedsMigrate(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	settings := filepath.Join(root, "claude", "settings.json")
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"C:/x/trace.exe","args":["hook"]}]}]}}`
	mmWrite(t, settings, legacy)
	_, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=")
	if code != 1 || !strings.Contains(errOut, "run again with -migrate") {
		t.Fatalf("exit %d, want 1\n%s", code, errOut)
	}
	if b, _ := os.ReadFile(settings); string(b) != legacy {
		t.Fatalf("settings changed without -migrate: %s", b)
	}
}

// install -host claude-code notes when config says claude: false, then ends with the first import; uninstall does
// neither, and claude: true gives no note.
func TestInstallClaudeNoteAndFirstImport(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	const note = "config.json says claude: false"
	mmWrite(t, filepath.Join(root, "trace", "config.json"), `{"claude": false}`)
	out, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=")
	if code != 0 || !strings.Contains(out, note) || !strings.Contains(out, "first import...") {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	if !exists(filepath.Join(root, "trace", "trace.db")) {
		t.Fatal("install did not run the first import")
	}
	out, errOut, code = runTrace(t, root, "", env, "uninstall", "-host", "claude-code")
	if code != 0 || strings.Contains(out, note) || strings.Contains(out, "first import") {
		t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
	}

	root2 := t.TempDir()
	env2 := map[string]string{"_EXE": mmTraceExe(t, root2)}
	mmWrite(t, filepath.Join(root2, "trace", "config.json"), `{"claude": true}`)
	out, errOut, code = runTrace(t, root2, "", env2, "install", "-host", "claude-code", "-codex=")
	if code != 0 || strings.Contains(out, note) {
		t.Fatalf("install with claude: true: exit %d\n%s\n%s", code, out, errOut)
	}
}

// install -host codex -mode hooks writes hooks that run this binary, then ends with the first import.
func TestInstallCodexHooksFirstImport(t *testing.T) {
	root := t.TempDir()
	exe := mmTraceExe(t, root)
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	codexDir := filepath.Join(root, "codex")
	os.MkdirAll(codexDir, 0o755)
	env := map[string]string{"_EXE": exe, "_DIR": work}
	out, errOut, code := runTrace(t, root, "", env, "install", "-host", "codex", "-mode", "hooks", "-codex", codexDir)
	if code != 0 || !strings.Contains(out, "first import...") {
		t.Fatalf("install: exit %d\n%s\n%s", code, out, errOut)
	}
	b, err := os.ReadFile(filepath.Join(codexDir, "hooks.json"))
	want := exe // install writes the path resolved, which on Windows also expands 8.3 short names (C:\Users\RUNNER~1)
	if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		want = real
	}
	if err != nil || !strings.Contains(string(b), filepath.ToSlash(want)) {
		t.Fatalf("hooks.json does not run this binary (%v): %s", err, b)
	}
	if !exists(filepath.Join(root, "trace", "trace.db")) {
		t.Fatal("install did not run the first import")
	}
}

// resume leaves out the session written to in the last two minutes (the one asking), and says nothing of a failed
// import when the import worked.
func TestResumeSkipsLiveSession(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, s := range []struct{ id, end, text string }{
		{"old", "2026-11-10T11:00:00Z", "NONCE_OLD_SESSION"},
		{"live", now.Format(time.RFC3339), "NONCE_LIVE_SESSION"},
	} {
		db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at) values (?, 'codex', ?, ?, '2026-11-10T10:00:00Z', ?)`, s.id, projectRole(work), work, s.end)
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, ?, ?, 'assistant', 'reply', ?)`, s.id, i+1, s.end, s.text)
	}
	db.Close()
	out, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work}, "resume", "-host", "codex") // Codex sessions (SPEC S3)
	if code != 0 || !strings.Contains(out, "NONCE_OLD_SESSION") || strings.Contains(out, "NONCE_LIVE_SESSION") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(errOut, "import failed") {
		t.Fatalf("a working import reported as failed: %s", errOut)
	}
}

// resume waits for an import another process is running, instead of skipping its own at once.
func TestResumeWaitsForRunningImport(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	dbPath := mmStore(t, root)
	lock, err := tryLock(dbPath + ".lock")
	if err != nil || lock == nil {
		t.Fatalf("cannot take the lock: %v", err)
	}
	release := time.AfterFunc(2*time.Second, func() { lock.Close() })
	defer func() { release.Stop(); lock.Close() }()
	_, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work}, "resume")
	if code != 0 || strings.Contains(errOut, "another import is running; skipped") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	// resume's import is quiet (SPEC E4): its run in the store shows it waited and ran
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := count(t, db, "select count(*) from ingest_run where trigger = 'resume' and ended_at is not null"); n != 1 {
		t.Fatalf("resume's import runs: %d, want 1\n%s", n, errOut)
	}
}

// import reports its counts on stderr; an import whose prompt log cannot be read (a broken .gz) fails with exit 1.
func TestImportReportsAndFails(t *testing.T) {
	root := t.TempDir()
	_, errOut, code := runTrace(t, root, "", nil, "import", "-projects=", "-history=", "-codex=")
	if code != 0 || !strings.Contains(errOut, "files 0, events added 0, sessions touched 0") {
		t.Fatalf("import: exit %d\n%s", code, errOut)
	}
	broken := filepath.Join(root, "history.jsonl.gz")
	mmWrite(t, broken, "not gzip data")
	_, errOut, code = runTrace(t, root, "", nil, "import", "-projects=", "-history", broken, "-codex=")
	if code != 1 || !strings.Contains(errOut, "file(s) failed") {
		t.Fatalf("unreadable prompt log: exit %d, want 1\n%s", code, errOut)
	}
}

// stats prints every section, with no error line, from a store.
func TestStatsPrintsSections(t *testing.T) {
	root := t.TempDir()
	mmStore(t, root)
	out, errOut, code := runTrace(t, root, "", nil, "stats")
	if code != 0 || !strings.Contains(out, "-- sessions by host and role") || !strings.Contains(out, "-- files with errors") || strings.Contains(out, "error:") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
}

// rules prints the card of this folder's newest Claude Code session, with no empty note, and how often it comes
// back: factor times its tokens.
func TestRulesCardFigures(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	tr := filepath.Join(root, "t.jsonl")
	rec, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "instructions",
		"files": []map[string]string{{"path": filepath.Join(root, "gone", "CLAUDE.md"), "type": "Project", "content": "NONCE_RULE keep answers short"}}}})
	mmWrite(t, tr, string(rec)+"\n")
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`insert into session(id, host, role, cwd, ended_at) values ('r1', 'claude-code', 'r', ?, '2026-11-10T10:00:00Z')`, work)
	res, _ := db.Exec(`insert into source_file(host, path) values ('claude-code', ?)`, tr)
	fid, _ := res.LastInsertId()
	db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text, file_id) values ('r1', 1, '2026-11-10T10:00:00Z', 'user', 'prompt', 'x', ?)`, fid)
	db.Close()
	out, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work}, "rules")
	if code != 0 || !strings.Contains(out, "NONCE_RULE") || strings.Contains(errOut, "note:") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	m := regexp.MustCompile(`card: (\d+) characters, about (\d+) tokens; it comes back every (\S+) tokens`).FindStringSubmatch(errOut)
	if m == nil {
		t.Fatalf("no card figures: %s", errOut)
	}
	chars, _ := strconv.Atoi(m[1])
	factor, _ := (*reinjectCfg)(nil).limits()
	if want := fmt.Sprintf("%.0f", factor*float64(chars)/4); chars != len(out) || m[3] != want {
		t.Fatalf("card %s chars (stdout %d), comes back every %s tokens, want %s", m[1], len(out), m[3], want)
	}
}

// ftsQuery drops a word that is only punctuation instead of searching for an empty phrase.
func TestFtsQueryDropsPunctuation(t *testing.T) {
	if got := ftsQuery(`hello ?? world`, "AND"); got != `"hello" AND "world"` {
		t.Fatalf("got %s", got)
	}
}

// search: every word first; else the best partial matches, said once; else the words as an exact substring of three
// characters or more; else not found, naming the default kinds unless -all. -since leaves earlier events out. The
// time shown is local, and a search reports no import on stderr.
func TestSearchTries(t *testing.T) {
	root := t.TempDir()
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	const ts = "2026-11-10T10:30:00Z"
	db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at) values ('sessionid-long', 'claude-code', 'r', 'C:\w', ?, ?)`, ts, ts)
	for i, e := range []struct{ kind, text string }{{"prompt", "alpha bravo"}, {"prompt", "xxqzwxx"}, {"thinking", "NONCE_THINK"}} {
		db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values ('sessionid-long', ?, ?, 'user', ?, ?)`, i+1, ts, e.kind, e.text)
	}
	db.Close()
	search := func(args ...string) string {
		t.Helper()
		out, errOut, code := runTrace(t, root, "", nil, append([]string{"search"}, args...)...)
		if code != 0 || strings.Contains(errOut, "import before search failed") || strings.Contains(errOut, "events added") {
			t.Fatalf("search %v: exit %d\n%s", args, code, errOut)
		}
		return out
	}
	const partial, substring, notFound = "(no hit with every word; showing the best partial matches)", "(no hit with every word; showing the exact substring matches)", "not found in"
	parsed, _ := time.Parse(time.RFC3339, ts)
	if out := search("alpha"); !strings.Contains(out, parsed.Local().Format("2006-01-02 15:04")+"  user/prompt  r  [sessioni #1]") ||
		strings.Contains(out, "no hit with every word") || strings.Contains(out, notFound) || strings.Count(out, "bravo") != 1 {
		t.Fatalf("every word:\n%s", out)
	}
	if out := search("alpha", "charlie"); strings.Count(out, partial) != 1 || !strings.Contains(out, "alpha") || strings.Contains(out, notFound) {
		t.Fatalf("partial:\n%s", out)
	}
	if out := search("qzw"); !strings.Contains(out, substring) || !strings.Contains(out, "qzw") {
		t.Fatalf("substring:\n%s", out)
	}
	if out := search("NONCE_THINK"); !strings.Contains(out, "not found in this memory (default kinds; -all widens it)") {
		t.Fatalf("default kinds:\n%s", out)
	}
	if out := search("-all", "NONCE_THINK"); !strings.Contains(out, "NONCE_THINK") || strings.Contains(out, "default kinds") {
		t.Fatalf("-all:\n%s", out)
	}
	if out := search("-since", "2026-12-01", "alpha"); !strings.Contains(out, notFound) {
		t.Fatalf("-since:\n%s", out)
	}
}
