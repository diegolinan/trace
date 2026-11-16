package main

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Behaviour tests for the import (ingest.go): what the store and the raw folder hold after an import.

type mutObj = map[string]any

// mutFix is one Claude Code projects folder (one project, in scope "acme"), its prompt log, its raw folder and a store.
type mutFix struct {
	dir string
	db  *sql.DB
}

func newMutFix(t *testing.T) *mutFix {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "projects", proj), 0o755)
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &mutFix{dir: dir, db: db}
}

// path is rel (slash-separated) inside the project's transcript folder.
func (f *mutFix) path(rel string) string {
	return filepath.Join(f.dir, "projects", proj, filepath.FromSlash(rel))
}

func (f *mutFix) raw(rel string) string {
	return filepath.Join(f.dir, "raw", proj, filepath.FromSlash(rel)+".gz")
}

func (f *mutFix) history() string { return filepath.Join(f.dir, "history.jsonl") }

func (f *mutFix) write(t *testing.T, rel, content string) {
	t.Helper()
	p := f.path(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutAppend(t *testing.T, p, content string) {
	t.Helper()
	w, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(content)
	w.Close()
}

func (f *mutFix) importTo(t *testing.T, archive string) {
	t.Helper()
	if err := runImport(f.db, filepath.Join(f.dir, "projects"), f.history(), "", archive, "acme", "test"); err != nil {
		t.Fatal(err)
	}
}

func (f *mutFix) importAll(t *testing.T) { t.Helper(); f.importTo(t, filepath.Join(f.dir, "raw")) }

func (f *mutFix) replaced(rel string) []string {
	olds, _ := filepath.Glob(filepath.Join(f.dir, "raw", proj, filepath.FromSlash(rel)+".replaced-*.gz"))
	return olds
}

// mutStr reads one text value; a null reads as "<null>".
func mutStr(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if !s.Valid {
		return "<null>"
	}
	return s.String
}

func mutGunzip(t *testing.T, p string) string {
	t.Helper()
	g, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	zr, err := gzip.NewReader(g)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mutSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func mutAt(min int) string { return fmt.Sprintf("2026-11-10T10:%02d:00.000Z", min) }

func mutLine(o mutObj) string {
	b, err := json.Marshal(o)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

func mutLines(rows ...mutObj) string {
	var b strings.Builder
	for _, o := range rows {
		b.WriteString(mutLine(o))
	}
	return b.String()
}

// mutUser is a typed prompt row; kv adds or replaces fields.
func mutUser(uuid string, min int, text string, kv ...any) mutObj {
	o := mutObj{"type": "user", "uuid": uuid, "timestamp": mutAt(min), "message": mutObj{"role": "user", "content": text}}
	for i := 0; i+1 < len(kv); i += 2 {
		o[kv[i].(string)] = kv[i+1]
	}
	return o
}

func mutAssistant(uuid string, min int, model string, blocks ...mutObj) mutObj {
	return mutObj{"type": "assistant", "uuid": uuid, "timestamp": mutAt(min), "message": mutObj{"model": model, "content": blocks}}
}

func mutToolResult(uuid string, min int, toolUse, content string, isError bool) mutObj {
	return mutObj{"type": "user", "uuid": uuid, "timestamp": mutAt(min), "message": mutObj{"role": "user", "content": []mutObj{
		{"type": "tool_result", "tool_use_id": toolUse, "content": content, "is_error": isError}}}}
}

func mutBoundary(uuid string, min int) mutObj {
	return mutObj{"type": "system", "subtype": "compact_boundary", "uuid": uuid, "timestamp": mutAt(min)}
}

// transcriptFiles lists main and subagent transcripts of in-scope projects, and the files under tool-results.
func TestTranscriptFilesListsOnlyTranscriptsInScope(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		proj + "/s1.jsonl",
		proj + "/s1/subagents/agent-a.jsonl",
		proj + "/s1/other/z.jsonl",          // not a subagents folder
		proj + "/s1/tool-results/sub/r.txt", // a file below a folder under tool-results
		"D--Work-other/o.jsonl",             // another project
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("{}\n"), 0o644)
	}
	out, results, err := transcriptFiles(root, "acme")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	want := []string{filepath.Join(root, proj, "s1", "subagents", "agent-a.jsonl"), filepath.Join(root, proj, "s1.jsonl")}
	sort.Strings(want)
	if fmt.Sprint(out) != fmt.Sprint(want) {
		t.Errorf("transcripts = %v, want %v", out, want)
	}
	if wantR := []string{filepath.Join(proj, "s1", "tool-results", "sub", "r.txt")}; fmt.Sprint(results) != fmt.Sprint(wantR) {
		t.Errorf("tool results = %v, want %v (files only, no folders)", results, wantR)
	}
}

// A tool result whose kept copy differs in size is copied again, and every listed result is kept.
func TestKeepResultsCopiesEveryResultAndReplacesAStaleCopy(t *testing.T) {
	projects, archive := t.TempDir(), t.TempDir()
	a, b := filepath.Join(proj, "s1", "tool-results", "a.txt"), filepath.Join(proj, "s1", "tool-results", "b.txt")
	for rel, text := range map[string]string{a: "full output of a", b: "output of b"} {
		os.MkdirAll(filepath.Dir(filepath.Join(projects, rel)), 0o755)
		os.WriteFile(filepath.Join(projects, rel), []byte(text), 0o644)
	}
	os.MkdirAll(filepath.Dir(filepath.Join(archive, a)), 0o755)
	os.WriteFile(filepath.Join(archive, a), []byte("short"), 0o644)
	if err := keepResults(projects, archive, []string{a, b}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{a: "full output of a", b: "output of b"} {
		if got, _ := os.ReadFile(filepath.Join(archive, rel)); string(got) != want {
			t.Errorf("kept %s = %q, want %q", rel, got, want)
		}
	}
}

// Reading on from the offset: an unchanged or grown transcript, even in a store from before tail_sha, is not set
// aside as rewritten, and its raw copy stays the whole file.
func TestGrownTranscriptIsNotSetAsideAsRewritten(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "g1.jsonl", mutLines(mutUser("g1", 0, "uno"), mutUser("g2", 1, "dos")))
	f.importAll(t)
	f.importAll(t)
	if olds := f.replaced("g1.jsonl"); len(olds) != 0 {
		t.Fatalf("unchanged transcript set aside: %v", olds)
	}
	mutAppend(t, f.path("g1.jsonl"), mutLine(mutUser("g3", 2, "tres")))
	f.importAll(t)
	if olds := f.replaced("g1.jsonl"); len(olds) != 0 {
		t.Fatalf("grown transcript set aside: %v", olds)
	}
	// A store from before tail_sha existed has no hash for the bytes read.
	if _, err := f.db.Exec("update source_file set tail_sha = null"); err != nil {
		t.Fatal(err)
	}
	mutAppend(t, f.path("g1.jsonl"), mutLine(mutUser("g4", 3, "cuatro")))
	f.importAll(t)
	if olds := f.replaced("g1.jsonl"); len(olds) != 0 {
		t.Fatalf("grown transcript without a stored hash set aside: %v", olds)
	}
	if got := count(t, f.db, "select count(*) from event where session_id = 'g1'"); got != 4 {
		t.Errorf("events = %d, want 4", got)
	}
	if got, want := mutGunzip(t, f.raw("g1.jsonl")), mutLines(mutUser("g1", 0, "uno"), mutUser("g2", 1, "dos"), mutUser("g3", 2, "tres"), mutUser("g4", 3, "cuatro")); got != want {
		t.Errorf("raw copy differs from the transcript:\n%s\nwant\n%s", got, want)
	}
}

// A transcript read up to its first byte and then rewritten is read again from its start.
func TestRewriteAfterOneByteIsReadAgain(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "o1.jsonl", "\n")
	f.importAll(t)
	f.write(t, "o1.jsonl", mutLine(mutUser("o1", 0, "hola")))
	f.importAll(t)
	if got := count(t, f.db, "select count(*) from event where uuid = 'o1:0' and text = 'hola'"); got != 1 {
		t.Errorf("rewritten prompt stored %d times, want 1", got)
	}
}

// The raw copy keeps a blank first line when the transcript grows after it.
func TestRawCopyKeepsBlankFirstLine(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "o2.jsonl", "\n")
	f.importAll(t)
	mutAppend(t, f.path("o2.jsonl"), mutLine(mutUser("o2", 0, "hola")))
	f.importAll(t)
	want, _ := os.ReadFile(f.path("o2.jsonl"))
	if got := mutGunzip(t, f.raw("o2.jsonl")); got != string(want) {
		t.Errorf("raw copy = %q, want %q", got, want)
	}
}

// A line still being written adds nothing to the raw copy.
func TestPartialLineLeavesRawCopyAsItIs(t *testing.T) {
	f := newMutFix(t)
	whole := mutLine(mutUser("p1", 0, "uno"))
	f.write(t, "p1.jsonl", whole)
	f.importAll(t)
	before := mutSize(t, f.raw("p1.jsonl"))
	half := mutLine(mutUser("p2", 1, "dos"))
	mutAppend(t, f.path("p1.jsonl"), half[:20])
	f.importAll(t)
	if after := mutSize(t, f.raw("p1.jsonl")); after != before {
		t.Errorf("raw copy grew from %d to %d bytes on a partial line", before, after)
	}
	if got := count(t, f.db, "select archive_bytes from source_file where path = ?", f.path("p1.jsonl")); int64(got) != before {
		t.Errorf("archive_bytes = %d, want %d", got, before)
	}
}

// With no raw folder configured, no raw copy is made anywhere.
func TestNoRawFolderMakesNoRawCopy(t *testing.T) {
	f := newMutFix(t)
	t.Chdir(t.TempDir())
	f.write(t, "n1.jsonl", mutLine(mutUser("n1", 0, "hola")))
	f.importTo(t, "")
	if got := count(t, f.db, "select count(*) from source_file where archived_as is not null or archive_bytes <> 0"); got != 0 {
		t.Errorf("%d files have a raw copy recorded, want 0", got)
	}
	if left, _ := os.ReadDir("."); len(left) != 0 {
		t.Errorf("files written in the working folder: %v", left)
	}
	if got := count(t, f.db, "select count(*) from event where session_id = 'n1'"); got != 1 {
		t.Errorf("events = %d, want 1", got)
	}
}

// A long transcript rewritten in its last 4 KB before the offset, same size, is read again.
func TestLongTranscriptRewrittenInPlaceIsReadAgain(t *testing.T) {
	f := newMutFix(t)
	var rows []mutObj
	for i := 0; i < 35; i++ {
		rows = append(rows, mutUser(fmt.Sprintf("big%02d", i), i, strings.Repeat("x", 150)))
	}
	text := mutLines(rows...)
	if len(text) < 6000 {
		t.Fatalf("test setup: transcript only %d bytes", len(text))
	}
	f.write(t, "l1.jsonl", text)
	f.importAll(t)
	f.write(t, "l1.jsonl", strings.Replace(text, `"uuid":"big34"`, `"uuid":"new34"`, 1))
	f.importAll(t)
	if got := count(t, f.db, "select count(*) from event where uuid = 'new34:0'"); got != 1 {
		t.Errorf("rewritten row stored %d times, want 1", got)
	}
}

// A rewritten transcript keeps its old raw copy beside a new one that holds the new file.
func TestRewriteKeepsOldRawCopyAndWritesTheNewOne(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "r1.jsonl", mutLines(mutUser("r1", 0, "uno"), mutUser("r2", 1, "dos")))
	f.importAll(t)
	shorter := mutLine(mutUser("r3", 2, "otro"))
	f.write(t, "r1.jsonl", shorter)
	f.importAll(t)
	if olds := f.replaced("r1.jsonl"); len(olds) != 1 {
		t.Fatalf("old raw copies = %v, want one", olds)
	}
	if got := mutGunzip(t, f.raw("r1.jsonl")); got != shorter {
		t.Errorf("new raw copy = %q, want %q", got, shorter)
	}
	if got := mutStr(t, f.db, "select archived_as from source_file where path = ?", f.path("r1.jsonl")); got != f.raw("r1.jsonl") {
		t.Errorf("archived_as = %s, want %s", got, f.raw("r1.jsonl"))
	}
}

// The session record: started at its first timed row (an untimed title row before it does not start it), ended at
// its last, the last branch, and the last real model (a <synthetic> row is not a model).
func TestSessionRecordFollowsItsRows(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "m1.jsonl", mutLines(
		mutObj{"type": "custom-title", "customTitle": "nombre"},
		mutUser("m1", 0, "hola", "cwd", "proj-a", "gitBranch", "main", "version", "2.1.0"),
		mutWith(mutAssistant("m2", 1, "claude-opus-5-5", mutObj{"type": "text", "text": "listo"}), "gitBranch", "feat"),
		mutAssistant("m3", 2, "<synthetic>", mutObj{"type": "text", "text": "aviso"}),
	))
	f.importAll(t)
	got := mutStr(t, f.db, "select started_at || '|' || ended_at || '|' || cwd || '|' || branch_first || '|' || branch_last || '|' || coalesce(model, '<null>') from session where id = 'm1'")
	want := strings.Join([]string{mutAt(0), mutAt(2), "proj-a", "main", "feat", "claude-opus-5-5"}, "|")
	if got != want {
		t.Errorf("session = %s, want %s", got, want)
	}
}

func mutWith(o mutObj, kv ...any) mutObj {
	for i := 0; i+1 < len(kv); i += 2 {
		o[kv[i].(string)] = kv[i+1]
	}
	return o
}

// A transcript found after its prompt-log session (trace restore) moves the start back and fills the version.
func TestTranscriptAfterPromptLogFillsTheSession(t *testing.T) {
	f := newMutFix(t)
	noon := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).UnixMilli()
	os.WriteFile(f.history(), []byte(mutLine(mutObj{"display": "hola", "timestamp": noon, "project": `D:\Work\acme`, "sessionId": "s9"})), 0o644)
	f.importAll(t)
	f.write(t, "s9.jsonl", mutLines(mutUser("s9a", 0, "antes", "version", "2.1.0"), mutUser("s9b", 5, "luego")))
	f.importAll(t)
	if got := mutStr(t, f.db, "select started_at || '|' || coalesce(version_first, '<null>') from session where id = 's9'"); got != mutAt(0)+"|2.1.0" {
		t.Errorf("session = %s, want %s|2.1.0", got, mutAt(0))
	}
}

// A rename appended alone to a known session's transcript renames the session.
func TestRenameAppendedAloneRenamesTheSession(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "k1.jsonl", mutLine(mutUser("k1", 0, "hola")))
	f.importAll(t)
	mutAppend(t, f.path("k1.jsonl"), mutLine(mutObj{"type": "custom-title", "customTitle": "segundo-nombre"}))
	f.importAll(t)
	if got := mutStr(t, f.db, "select title from session where id = 'k1'"); got != "segundo-nombre" {
		t.Errorf("title = %s, want segundo-nombre", got)
	}
}

// A subagent takes its agent type from the .meta.json beside its transcript; a main session does not.
func TestSubagentTakesAgentTypeFromItsMetaFile(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "s3.jsonl", mutLine(mutUser("s3", 0, "hola")))
	f.write(t, "s3.meta.json", `{"agentType":"main-type"}`)
	f.write(t, "s3/subagents/agent-q.jsonl", mutLine(mutUser("q1", 1, "buscá")))
	f.write(t, "s3/subagents/agent-q.meta.json", `{"agentType":"Explore"}`)
	f.write(t, "s3/subagents/agent-n.jsonl", mutLine(mutUser("n1", 2, "sin meta")))
	f.importAll(t)
	for id, want := range map[string]string{"agent-q": "Explore", "s3": "<null>", "agent-n": "<null>"} {
		if got := mutStr(t, f.db, "select agent_type from session where id = ?", id); got != want {
			t.Errorf("%s agent_type = %s, want %s", id, got, want)
		}
	}
}

// Each compaction boundary counts once: a row repeated in the same read once, a new one appended later once more.
func TestCompactionBoundariesCountOnce(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "c1.jsonl", mutLines(mutUser("c1", 0, "uno"), mutBoundary("b1", 1), mutBoundary("b1", 1), mutUser("c2", 2, "dos")))
	f.importAll(t)
	if got := count(t, f.db, "select compactions from session where id = 'c1'"); got != 1 {
		t.Errorf("compactions = %d after a repeated boundary, want 1", got)
	}
	mutAppend(t, f.path("c1.jsonl"), mutLines(mutBoundary("b2", 3), mutUser("c3", 4, "tres")))
	f.importAll(t)
	if got := count(t, f.db, "select compactions from session where id = 'c1'"); got != 2 {
		t.Errorf("compactions = %d after an appended boundary, want 2", got)
	}
	if got := count(t, f.db, "select compaction_no from event where uuid = 'c3:0'"); got != 2 {
		t.Errorf("compaction_no after the second boundary = %d, want 2", got)
	}
}

// A row with no uuid or no time is not stored as an event.
func TestRowsWithoutUuidOrTimeAreNotStored(t *testing.T) {
	f := newMutFix(t)
	noUUID := mutUser("", 1, "sin uuid")
	delete(noUUID, "uuid")
	noTime := mutUser("h1", 2, "sin hora")
	delete(noTime, "timestamp")
	f.write(t, "h1.jsonl", mutLines(mutUser("h0", 0, "con todo"), noUUID, noTime))
	f.importAll(t)
	if got := count(t, f.db, "select count(*) from event where session_id = 'h1'"); got != 1 {
		t.Errorf("events = %d, want 1 (only the row with uuid and time)", got)
	}
}

// The role comes from the cwd's marker for a main session and stays "subagent" for a subagent; an SDK entrypoint
// makes a main session a worker, never a subagent.
func TestRolesFromCwdAndEntrypoint(t *testing.T) {
	f := newMutFix(t)
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "ROLE.md"), []byte("role: main\n"), 0o644)
	f.write(t, "r1.jsonl", mutLine(mutUser("r1", 0, "hola", "cwd", cwd)))
	f.write(t, "r1/subagents/agent-r.jsonl", mutLine(mutUser("r2", 1, "buscá", "cwd", cwd)))
	f.write(t, "w1.jsonl", mutLine(mutUser("w1", 2, "trabajo", "cwd", "x", "entrypoint", "sdk-cli")))
	f.write(t, "w1/subagents/agent-w.jsonl", mutLine(mutUser("w2", 3, "ayuda", "cwd", "x", "entrypoint", "sdk-ts")))
	f.importAll(t)
	for id, want := range map[string]string{"r1": "main", "agent-r": "subagent", "w1": "worker", "agent-w": "subagent"} {
		if got := mutStr(t, f.db, "select role from session where id = ?", id); got != want {
			t.Errorf("%s role = %s, want %s", id, got, want)
		}
	}
}

// The harness's title names a session until a /rename, which it never overrides; only renames are names given.
func TestTitlesAndNames(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "t1.jsonl", mutLines(mutObj{"type": "ai-title", "aiTitle": "titulo automatico"}, mutUser("t1", 0, "hola")))
	f.write(t, "t2.jsonl", mutLines(mutUser("t2", 0, "hola"),
		mutObj{"type": "custom-title", "customTitle": "mio"}, mutObj{"type": "ai-title", "aiTitle": "auto"}))
	f.importAll(t)
	for id, want := range map[string]string{"t1": "titulo automatico", "t2": "mio"} {
		if got := mutStr(t, f.db, "select title from session where id = ?", id); got != want {
			t.Errorf("%s title = %s, want %s", id, got, want)
		}
	}
	if got := mutStr(t, f.db, "select group_concat(session_id || ':' || name) from session_name where session_id in ('t1', 't2')"); got != "t2:mio" {
		t.Errorf("names = %s, want t2:mio", got)
	}
}

// resolveTool names a result after the call seen earlier in the same read, even when the store does not have it.
func TestResolveToolNamesResultFromThisRead(t *testing.T) {
	f := newMutFix(t)
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	tr := &transcriptRead{tx: tx, toolNames: map[string]string{}}
	call := ev{speaker: "assistant", kind: "tool_call", toolName: "Bash", toolUse: "t1"}
	tr.resolveTool(&call)
	res := ev{speaker: "tool", kind: "tool_result", toolUse: "t1", isError: boolp(false)}
	tr.resolveTool(&res)
	if res.toolName != "Bash" || res.exitCode == nil || *res.exitCode != 0 {
		t.Errorf("result = tool %q exit %v, want Bash exit 0", res.toolName, res.exitCode)
	}
}

// A relay tool's call stays a call (its result is the relay); a non-shell result gets no exit code; a result read in
// a later import than its call is named from the store.
func TestToolResultsAcrossImports(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "x1.jsonl", mutLines(
		mutAssistant("x1", 0, "m", mutObj{"type": "tool_use", "id": "t-relay", "name": "mcp__relay__ask", "input": mutObj{"message": "hola"}}),
		mutToolResult("x2", 1, "t-relay", "respuesta", false),
		mutAssistant("x3", 2, "m", mutObj{"type": "tool_use", "id": "t-read", "name": "Read", "input": mutObj{"file_path": "a.txt"}}),
		mutToolResult("x4", 3, "t-read", "contenido", false),
		mutAssistant("x5", 4, "m", mutObj{"type": "tool_use", "id": "t-bash", "name": "Bash", "input": mutObj{"command": "ls"}}),
	))
	f.importAll(t)
	mutAppend(t, f.path("x1.jsonl"), mutLine(mutToolResult("x6", 5, "t-bash", "a.txt", false)))
	f.importAll(t)
	checks := map[string]string{
		"select speaker || '/' || kind from event where uuid = 'x1:0'":                            "assistant/tool_call",
		"select speaker || '/' || kind from event where uuid = 'x2:0'":                            "relay/relay",
		"select coalesce(exit_code, '<null>') from event where uuid = 'x4:0'":                     "<null>",
		"select tool_name || '/' || coalesce(exit_code, '<null>') from event where uuid = 'x6:0'": "Bash/0",
	}
	for q, want := range checks {
		if got := mutStr(t, f.db, q); got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
}

func TestShellSucceeded(t *testing.T) {
	cases := []struct {
		isError *bool
		tool    string
		want    bool
	}{
		{nil, "Bash", false},
		{boolp(false), "Bash", true},
		{boolp(false), "PowerShell", true},
		{boolp(true), "Bash", false},
		{boolp(false), "Read", false},
	}
	for _, c := range cases {
		if got := shellSucceeded(&ev{toolName: c.tool, isError: c.isError}); got != c.want {
			t.Errorf("shellSucceeded(%s, isError %v) = %v, want %v", c.tool, c.isError, got, c.want)
		}
	}
}

// A secret only in the part of a tool input that the event text clips off is still named in redacted.
func TestSecretOnlyInClippedToolInputIsNamed(t *testing.T) {
	f := newMutFix(t)
	token := "ghp_" + strings.Repeat("A1b2C3", 6)
	f.write(t, "y1.jsonl", mutLine(mutAssistant("y1", 0, "m",
		mutObj{"type": "tool_use", "id": "t1", "name": "Bash", "input": mutObj{"command": strings.Repeat("a", 4100) + " " + token}})))
	f.importAll(t)
	if got := mutStr(t, f.db, "select coalesce(redacted, '<null>') || '/' || instr(tool_input, 'ghp_') from event where uuid = 'y1:0'"); got != "github-token/0" {
		t.Errorf("redacted/position of the token = %s, want github-token/0", got)
	}
}

// The prompt log: a line with no session is skipped, every prompt of a lost session is kept, an empty paste keeps
// its placeholder, prompts sit before any compaction, and the log counts no bad lines.
func TestPromptLogPrompts(t *testing.T) {
	f := newMutFix(t)
	at := func(m int) int64 { return time.Date(2026, 11, 10, 9, m, 0, 0, time.UTC).UnixMilli() }
	os.WriteFile(f.history(), []byte(mutLines(
		mutObj{"display": "sin sesion", "timestamp": at(0), "project": `D:\Work\acme`},
		mutObj{"display": "primero", "timestamp": at(1), "project": `D:\Work\acme`, "sessionId": "lost2"},
		mutObj{"display": "segundo [Pasted text #1 +3 lines]", "timestamp": at(2), "project": `D:\Work\acme`, "sessionId": "lost2",
			"pastedContents": mutObj{"1": mutObj{"content": ""}}},
	)), 0o644)
	f.importAll(t)
	checks := map[string]int{
		"select count(*) from event where text = 'sin sesion'":                         0,
		"select count(*) from event where session_id = 'lost2'":                        2,
		"select count(*) from event where text = 'segundo [Pasted text #1 +3 lines]'":  1,
		"select count(*) from event where session_id = 'lost2' and compaction_no <> 0": 0,
		"select bad_lines from source_file where host = 'prompt-log'":                  0,
	}
	for q, want := range checks {
		if got := count(t, f.db, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

// Every session that got events has its active time computed.
func TestActiveTimeForEveryTouchedSession(t *testing.T) {
	_, db, run := setup(t)
	run()
	if got := count(t, db, "select count(*) from session"); got < 2 {
		t.Fatalf("test setup: %d sessions", got)
	}
	if got := count(t, db, "select count(*) from session where active_secs is null"); got != 0 {
		t.Errorf("%d sessions without active time, want 0", got)
	}
}
