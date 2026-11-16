package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// rzDB is an empty store in a temp folder.
func rzDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func rzExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// rzSession stores an ended Claude Code session that ran in cwd.
func rzSession(t *testing.T, db *sql.DB, id, role, cwd, start, end string, active int) {
	t.Helper()
	rzExec(t, db, `insert into session(id, host, role, cwd, started_at, ended_at, active_secs) values (?, 'claude-code', ?, ?, ?, ?, ?)`,
		id, role, cwd, start, end, active)
}

func rzEvent(t *testing.T, db *sql.DB, sid string, seq int, ts, speaker, kind, text string) {
	t.Helper()
	rzExec(t, db, `insert into event(session_id, seq, ts, speaker, kind, text) values (?, ?, ?, ?, ?, ?)`, sid, seq, ts, speaker, kind, text)
}

// rzRefusal stores one typed message that refuses something; the fixture fails if the marks would not read it so.
func rzRefusal(t *testing.T, db *sql.DB, sid string, seq int, ts, text string) {
	t.Helper()
	if !lasting(text) {
		t.Fatalf("fixture: %q is not a lasting refusal", text)
	}
	rzEvent(t, db, sid, seq, ts, "user", "prompt", text)
}

// rzNoRoles runs the test with no roles configured: every folder is its own project.
func rzNoRoles(t *testing.T) {
	old := cfg
	cfg.Roles = nil
	t.Cleanup(func() { cfg = old })
}

// rzBlock is the start block a new session in cwd gets, with no roles.
func rzBlock(t *testing.T, db *sql.DB, cwd string) string {
	t.Helper()
	var out strings.Builder
	if err := resumeBlock(db, "new", "startup", projectRole(cwd), "claude-code", cwd, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// rzRefusalLines are the lines of the block's list of refusals and rules.
func rzRefusalLines(block string) []string {
	_, rest, ok := strings.Cut(block, "refused or set as a rule, last 14 days")
	if !ok {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(rest, "\n")[1:] {
		if !strings.HasPrefix(l, "- ") {
			break
		}
		lines = append(lines, l)
	}
	return lines
}

func rzLocal(t *testing.T, ts string) string {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	return tm.Local().Format("2006-01-02 15:04")
}

// The header names when the session ran in local time, how long it was active and how long ago it ended.
func TestStartBlockHeaderTimes(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	start, end := "2026-11-10T10:00:00Z", "2026-11-10T11:02:05Z"
	rzSession(t, db, "hdr", projectRole(f), f, start, end, 3725)
	rzEvent(t, db, "hdr", 1, end, "assistant", "reply", "listo")
	got := rzBlock(t, db, f)
	for _, want := range []string{rzLocal(t, start) + " to " + rzLocal(t, end) + " local", "1h2m5s active", "- " + rzLocal(t, end) + " reply: listo"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, start) || strings.Contains(got, end) {
		t.Errorf("a raw timestamp reached the block:\n%s", got)
	}
	if !regexp.MustCompile(`; ended -?[0-9hms]+ ago\.`).MatchString(got) {
		t.Errorf("the header does not say how long ago the session ended:\n%s", got)
	}
}

// A section with nothing in it is left out: no pins, no refusals, no words of the user give no heading.
func TestStartBlockLeavesOutEmptySections(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	rzSession(t, db, "only", projectRole(f), f, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "only", 1, "2026-11-10T10:30:00Z", "assistant", "reply", "hecho")
	got := rzBlock(t, db, f)
	if n := strings.Count(got, "### "); n != 1 || !strings.Contains(got, "### The last replies") {
		t.Errorf("want the replies section alone, got %d sections:\n%s", n, got)
	}
}

// On Windows a folder is the same folder whatever its case (SPEC G4); elsewhere case makes another folder.
func TestStartBlockFolderCase(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	base := t.TempDir()
	stored, asked := filepath.Join(base, "SITE"), filepath.Join(base, "site")
	rzSession(t, db, "cased", projectRole(stored), stored, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "cased", 1, "2026-11-10T10:30:00Z", "assistant", "reply", "NONCE_CASED")
	got := rzBlock(t, db, asked)
	if shown := strings.Contains(got, "NONCE_CASED"); shown != (runtime.GOOS == "windows") {
		t.Errorf("on %s the session from %s shown %v for %s:\n%s", runtime.GOOS, stored, shown, asked, got)
	}
}

// The search line names -home only when this memory is not the default one, however the path is written.
func TestStartBlockSearchNamesOnlyAnotherHome(t *testing.T) {
	rzNoRoles(t)
	def, other := t.TempDir(), t.TempDir()
	t.Setenv("TRACE_HOME", def)
	old := activeHome
	t.Cleanup(func() { activeHome = old })
	db := rzDB(t)
	f := t.TempDir()
	rzSession(t, db, "s", projectRole(f), f, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "s", 1, "2026-11-10T10:30:00Z", "assistant", "reply", "hecho")
	searchOf := func(home string) string {
		activeHome = home
		_, line, _ := strings.Cut(rzBlock(t, db, f), "Everything else said or done: ")
		return line
	}
	for _, home := range []string{"", def, def + string(filepath.Separator)} {
		if line := searchOf(home); strings.Contains(line, "-home") {
			t.Errorf("home %q is the default one, yet the search line names it: %s", home, line)
		}
	}
	if line := searchOf(other); !strings.Contains(line, ` search -home "`+other+`" <words>`+"`") {
		t.Errorf("another home is missing from the search line: %s", line)
	}
}

// A path that needs no quotes is a command as written in every shell: no PowerShell form beside it (SPEC S2).
func TestSearchLineUnquotedPathAlone(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "trace.exe")
	if got, want := searchLine(exe, ""), "`"+exe+" search <words>`"; got != want {
		t.Errorf("searchLine = %s, want %s", got, want)
	}
}

// With no roles, the refusals listed are the newest eight lasting ones typed in this folder or inside it, from any
// session, never a sibling's or a same-prefix folder's, and never a plain request (SPEC G4).
func TestStartBlockRefusalsNewestEightFromFolder(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	base := t.TempDir()
	f := filepath.Join(base, "site")
	inner, sibling, prefix := filepath.Join(f, "nested"), filepath.Join(base, "other"), filepath.Join(base, "site-old")
	rzSession(t, db, "old", projectRole(inner), inner, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	for i := 1; i <= 9; i++ {
		rzRefusal(t, db, "old", i, "2026-11-10T08:0"+string(rune('0'+i))+":00Z", "no hace falta usar archivos R"+string(rune('0'+i)))
	}
	rzEvent(t, db, "old", 10, "2026-11-10T08:30:00Z", "user", "prompt", "dale, Saca del codigo todo lo mio")
	rzSession(t, db, "sib", projectRole(sibling), sibling, "2026-11-10T09:00:00Z", "2026-11-10T10:00:00Z", 60)
	rzRefusal(t, db, "sib", 1, "2026-11-10T09:30:00Z", "no hace falta usar archivos SIB")
	rzSession(t, db, "pre", projectRole(prefix), prefix, "2026-11-10T09:00:00Z", "2026-11-10T10:00:00Z", 60)
	rzRefusal(t, db, "pre", 1, "2026-11-10T09:40:00Z", "no hace falta usar archivos PRE")
	rzSession(t, db, "shown", projectRole(f), f, "2026-11-10T11:00:00Z", "2026-11-10T12:00:00Z", 60)
	rzEvent(t, db, "shown", 1, "2026-11-10T11:30:00Z", "assistant", "reply", "hecho")
	got := rzBlock(t, db, f)
	lines := rzRefusalLines(got)
	if len(lines) != 8 {
		t.Fatalf("%d refusals listed, want 8:\n%s", len(lines), got)
	}
	for i, l := range lines { // newest last: R2 .. R9
		if want := "refused: no hace falta usar archivos R" + string(rune('2'+i)); !strings.Contains(l, want) {
			t.Errorf("line %d is %q, want %q", i, l, want)
		}
	}
	for _, not := range []string{"SIB", "PRE", "Saca del codigo"} {
		if strings.Contains(got, not) {
			t.Errorf("%s reached the start block:\n%s", not, got)
		}
	}
}

// With roles configured, the refusals listed are the role's own, wherever its sessions ran.
func TestStartBlockRefusalsOfTheRoleOnly(t *testing.T) {
	db := rzDB(t)
	elsewhere, f := t.TempDir(), t.TempDir()
	rzSession(t, db, "a1", "aux", elsewhere, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	rzRefusal(t, db, "a1", 1, "2026-11-10T08:30:00Z", "no hace falta usar archivos AUX")
	rzSession(t, db, "m1", "main", f, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	rzRefusal(t, db, "m1", 1, "2026-11-10T08:40:00Z", "no hace falta usar archivos MAIN")
	rzSession(t, db, "a2", "aux", elsewhere, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "a2", 1, "2026-11-10T10:30:00Z", "assistant", "reply", "hecho")
	var out strings.Builder
	if err := resumeBlock(db, "new", "startup", "aux", "claude-code", f, &out); err != nil {
		t.Fatal(err)
	}
	lines := rzRefusalLines(out.String())
	if len(lines) != 1 || !strings.Contains(lines[0], "AUX") {
		t.Errorf("want the aux refusal alone, got %q in:\n%s", lines, out.String())
	}
}

// Each refusal shows the end of the reply it answered: the whole reply up to 150 bytes, else "…" and its last 150;
// a refusal with no reply before it shows none.
func TestStartBlockRefusalAnswering(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	exact, long := strings.Repeat("y", 150), strings.Repeat("h", 50)+strings.Repeat("z", 150)
	rzSession(t, db, "t0", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T08:10:00Z", 60)
	rzRefusal(t, db, "t0", 1, "2026-11-10T08:05:00Z", "no hace falta usar archivos T0")
	rzSession(t, db, "t150", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T08:20:00Z", 60)
	rzEvent(t, db, "t150", 1, "2026-11-10T08:14:00Z", "assistant", "reply", exact)
	rzRefusal(t, db, "t150", 2, "2026-11-10T08:15:00Z", "no hace falta usar archivos T150")
	rzSession(t, db, "t200", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T08:30:00Z", 60)
	rzEvent(t, db, "t200", 1, "2026-11-10T08:24:00Z", "assistant", "reply", long)
	rzRefusal(t, db, "t200", 2, "2026-11-10T08:25:00Z", "no hace falta usar archivos T200")
	got := rzBlock(t, db, f)
	want := map[string]string{
		"T0":   "no hace falta usar archivos T0",
		"T150": "no hace falta usar archivos T150 (answering: " + exact + ")",
		"T200": "no hace falta usar archivos T200 (answering: …" + strings.Repeat("z", 150) + ")",
	}
	lines := rzRefusalLines(got)
	if len(lines) != 3 {
		t.Fatalf("%d refusals listed, want 3:\n%s", len(lines), got)
	}
	for _, l := range lines {
		key := strings.Fields(strings.SplitN(l, "archivos ", 2)[1])[0]
		if !strings.HasSuffix(l, want[key]) {
			t.Errorf("line %q\n does not end with %q", l, want[key])
		}
	}
}

// rzParse is what parseRow yields for one transcript line of the session itself.
func rzParse(t *testing.T, line string) parsed {
	t.Helper()
	var r row
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	return parseRow(&r, false)
}

func rzOne(t *testing.T, p parsed) ev {
	t.Helper()
	if len(p.events) != 1 {
		t.Fatalf("%d events, want 1: %s", len(p.events), describe(p))
	}
	return p.events[0]
}

// A tool result keeps only the words of its text blocks: an image or an empty text block adds nothing.
func TestParseToolResultTextBlocksOnly(t *testing.T) {
	e := rzOne(t, rzParse(t, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a"},{"type":"image","source":{"type":"base64"}},{"type":"text","text":""},{"type":"text","text":"b"}]}]}}`))
	if e.kind != "tool_result" || e.text != "a\nb" {
		t.Errorf("got %s/%s %q, want tool_result \"a\\nb\"", e.speaker, e.kind, e.text)
	}
	if p := rzParse(t, `{"type":"user","message":{"role":"user"}}`); len(p.events) != 0 {
		t.Errorf("a user row with no content yielded %s", describe(p))
	}
}

// A tool call the user rejected is the user's refusal, an error whatever the result block says.
func TestParseRejectedToolIsRefusal(t *testing.T) {
	e := rzOne(t, rzParse(t, `{"type":"user","toolDenialKind":"user-rejected","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","content":"The user doesn't want to proceed with this tool use.","is_error":false}]}}`))
	if e.speaker != "user" || e.kind != "refusal" || e.isError == nil || !*e.isError || e.toolUse != "t3" {
		t.Errorf("got %s/%s isError %v use %q, want a user refusal that is an error", e.speaker, e.kind, e.isError, e.toolUse)
	}
}

// The user's answers are each question and answer in question order, with a note only where the user wrote one.
func TestParseAnswersExact(t *testing.T) {
	e := rzOne(t, rzParse(t, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"answered"}]},"toolUseResult":{"answers":{"¿Sigo?":"Sí","¿Borro?":"No"},"annotations":{"¿Sigo?":{"notes":"con cuidado"},"¿Borro?":{"notes":""}}}}`))
	if want := "Q: ¿Borro?\nA: No\nQ: ¿Sigo?\nA: Sí\nNote: con cuidado\n"; e.speaker != "user" || e.kind != "answer" || e.text != want {
		t.Errorf("got %s/%s %q, want user/answer %q", e.speaker, e.kind, e.text, want)
	}
}

// A tool call's text keeps its input whole up to 4,000 bytes; past that it is cut before the character that
// straddles the limit and marked with "…".
func TestParseToolCallInputClip(t *testing.T) {
	call := func(input string) string {
		b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": json.RawMessage(input)}}}})
		return rzOne(t, rzParse(t, string(b))).text
	}
	exact := `{"c":"` + strings.Repeat("a", 3992) + `"}`
	if len(exact) != 4000 {
		t.Fatalf("fixture is %d bytes", len(exact))
	}
	if got := call(exact); got != "Bash "+exact {
		t.Errorf("an input of exactly 4000 bytes was changed: ends %q", got[len(got)-10:])
	}
	over := `{"c":"` + strings.Repeat("a", 3993) + `éz"}` // é is bytes 3999 and 4000
	got := call(over)
	if want := "Bash " + over[:3999] + "…"; got != want || !utf8.ValidString(got) {
		t.Errorf("clipped input ends %q, want %q", got[len(got)-8:], want[len(want)-8:])
	}
}

// rzStubImport replaces the detached import with a counter for the test.
func rzStubImport(t *testing.T) *int {
	calls := new(int)
	old := spawnImport
	spawnImport = func([]string, string) error { *calls++; return nil }
	t.Cleanup(func() { spawnImport = old })
	return calls
}

// With no transcripts folder, a transcript path admits nothing: only the cwd can, so a session outside scope is not
// imported and gets nothing.
func TestHookNoTranscriptsFolderAdmitsByCwdOnly(t *testing.T) {
	old := cfg.Scope
	cfg.Scope = "acme"
	defer func() { cfg.Scope = old }()
	base := t.TempDir()
	dbPath := filepath.Join(base, "m.db")
	in := hookInput{Event: "SessionStart", Source: "startup", Cwd: filepath.Join(base, "other"), SessionID: "s1", Transcript: filepath.Join("D--Work-acme", "s1.jsonl")}
	ran := false
	run := func(*sql.DB, string) error { ran = true; return nil }
	var out strings.Builder
	if err := hook(in, nil, dbPath, "claude-code", "", run, &out); err != nil || ran || out.Len() != 0 {
		t.Errorf("outside scope with no transcripts folder: err %v, imported %v, output %q", err, ran, out.String())
	}
	in.Cwd = filepath.Join(base, "acme")
	if err := hook(in, nil, dbPath, "claude-code", "", run, &out); err != nil || !ran {
		t.Errorf("a cwd in scope was not admitted: err %v, imported %v", err, ran)
	}
}

// After a compaction the rule card is printed only when reinject is on and the session is this memory's; the import
// starts either way.
func TestCompactCardNeedsReinjectAndMine(t *testing.T) {
	calls := rzStubImport(t)
	oldRe := cfg.Reinject
	t.Cleanup(func() { cfg.Reinject = oldRe })
	dir := t.TempDir()
	record, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "instructions",
		"files": []map[string]string{{"path": filepath.Join(dir, "gone", "CLAUDE.md"), "type": "Project", "content": "NONCE_CARD_RULE"}}}})
	transcriptIn := func(folder string) string {
		p := filepath.Join(dir, "projects", folder, "s1.jsonl")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, append(record, '\n'), 0o644)
		return p
	}
	mine, other := transcriptIn(proj), transcriptIn("C--other-project")
	for _, c := range []struct {
		name       string
		transcript string
		reinject   bool
		card       bool
	}{
		{"mine, reinject on", mine, true, true},
		{"mine, reinject off", mine, false, false},
		{"not mine, reinject on", other, true, false},
	} {
		cfg.Reinject = nil
		if c.reinject {
			cfg.Reinject = &reinjectCfg{}
		}
		before := *calls
		var out strings.Builder
		in := hookInput{Event: "SessionStart", Source: "compact", SessionID: "s1", Cwd: dir, Transcript: c.transcript}
		if err := hook(in, nil, filepath.Join(dir, "none.db"), "claude-code", filepath.Join(dir, "projects"), nil, &out); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := strings.Contains(out.String(), "NONCE_CARD_RULE"); got != c.card || (!c.card && out.Len() != 0) {
			t.Errorf("%s: card printed %v, want %v; output %q", c.name, got, c.card, out.String())
		}
		if *calls != before+1 {
			t.Errorf("%s: the import was not started", c.name)
		}
	}
}

// SessionStart waits for an import that is running, up to a few seconds, then imports itself.
func TestSessionStartWaitsForRunningImport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.db")
	held, err := tryLock(dbPath + ".lock")
	if err != nil || held == nil {
		t.Fatalf("lock: %v", err)
	}
	time.AfterFunc(700*time.Millisecond, func() { held.Close() })
	ran := false
	run := func(*sql.DB, string) error { ran = true; return nil }
	in := hookInput{Event: "SessionStart", Source: "startup", SessionID: "new", Cwd: dir, Transcript: filepath.Join(dir, "projects", proj, "new.jsonl")}
	var out strings.Builder
	if err := hook(in, nil, dbPath, "claude-code", filepath.Join(dir, "projects"), run, &out); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("SessionStart gave up on an import that freed the lock after 700 ms")
	}
}

// A SessionStart import that fails says so on stderr and the hook still exits 0; one that works says nothing.
func TestHookImportFailureSaysSo(t *testing.T) {
	root := t.TempDir()
	notAFolder := filepath.Join(root, "projects.txt")
	os.WriteFile(notAFolder, []byte("x"), 0o644)
	cwd, _ := json.Marshal(root)
	in := `{"hook_event_name":"SessionStart","source":"startup","session_id":"s9","cwd":` + string(cwd) + `}`
	for _, c := range []struct {
		projects string
		fails    bool
	}{{"", false}, {notAFolder, true}} {
		_, errOut, code := runTrace(t, root, in, nil, "hook", "-db", filepath.Join(root, "m.db"), "-projects="+c.projects, "-history=", "-codex=")
		if said := strings.Contains(errOut, "trace: import failed"); code != 0 || said != c.fails {
			t.Errorf("-projects %q: exit %d, failure reported %v, want %v; stderr %q", c.projects, code, said, c.fails, errOut)
		}
	}
}

// The role comes from the session's folder when the hook names one: a helper in a checkout of a configured role is
// told whose helper it is, though its transcript folder belongs to no role.
func TestSubagentRoleFromCwd(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "ROLE.md"), []byte("role: aux\n"), 0o644)
	var out strings.Builder
	in := hookInput{Event: "SubagentStart", Cwd: cwd, Transcript: filepath.Join("x", "C--other-project", "s1.jsonl")}
	if err := hook(in, nil, "", "claude-code", "x", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "helper agent started by Agent A, not Agent A") {
		t.Errorf("SubagentStart output %q", out.String())
	}
}
