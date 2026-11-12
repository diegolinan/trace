package main

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const proj = "D--Work-acme--claude-worktrees-aux"

var transcript = []string{
	`{"type":"user","uuid":"u1","timestamp":"2026-11-10T10:00:00.000Z","sessionId":"s1","cwd":"D:\\x","gitBranch":"main","version":"2.1.321","entrypoint":"cli","turnOrigin":"human","message":{"role":"user","content":"Revisá la acción en system/queue.md y Q47, mi clave es sk-or-v1-abcdefghijklmnopqrstuvwxyz123456"}}`,
	`{"type":"assistant","uuid":"a1","timestamp":"2026-11-10T10:00:05.000Z","sessionId":"s1","message":{"model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"pienso"},{"type":"text","text":"Lo miro."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`,
	`{"type":"user","uuid":"u2","timestamp":"2026-11-10T10:00:06.000Z","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.txt","is_error":false}]},"toolUseResult":{"stdout":"a.txt","stderr":""}}`,
	`{"type":"agent-name","agentName":"first-try","sessionId":"s1"}`,
	`{"type":"attachment","uuid":"q1","timestamp":"2026-11-10T10:00:07.000Z","sessionId":"s1","attachment":{"type":"queued_command","prompt":"no, eso no","origin":{"kind":"human"}}}`,
	`{"type":"user","uuid":"u3","timestamp":"2026-11-10T10:01:00.000Z","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"Your questions have been answered","is_error":false}]},"toolUseResult":{"answers":{"¿Borro?":"No"},"annotations":{}}}`,
	`{"type":"user","uuid":"u4","timestamp":"2026-11-10T10:02:00.000Z","sessionId":"s1","toolDenialKind":"user-rejected","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","content":"The user doesn't want to proceed with this tool use.","is_error":true}]}}`,
	`{"type":"system","subtype":"compact_boundary","uuid":"b1","timestamp":"2026-11-10T10:30:00.000Z","sessionId":"s1","compactMetadata":{"trigger":"auto","preTokens":900000}}`,
	`{"type":"user","uuid":"u5","timestamp":"2026-11-10T10:30:01.000Z","sessionId":"s1","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued"}}`,
	`{"type":"custom-title","customTitle":"launch-day","sessionId":"s1"}`,
	`{"type":"assistant","uuid":"a2","timestamp":"2026-11-10T10:31:00.000Z","sessionId":"s1","message":{"content":[{"type":"tool_use","id":"t4","name":"mcp__relay__ask","input":{"message":"hola"}}]}}`,
	`{"type":"user","uuid":"u6","timestamp":"2026-11-10T10:31:30.000Z","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t4","content":"respuesta del otro agente","is_error":false}]}}`,
}

func setup(t *testing.T) (dir string, db *sql.DB, run func() int) {
	dir = t.TempDir()
	p := filepath.Join(dir, "projects", proj)
	os.MkdirAll(filepath.Join(p, "s1", "subagents"), 0o755)
	os.WriteFile(filepath.Join(p, "s1.jsonl"), []byte(strings.Join(transcript, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(p, "s1", "subagents", "agent-x1.jsonl"), []byte(
		`{"type":"user","uuid":"v1","timestamp":"2026-11-10T10:05:00.000Z","sessionId":"s1","message":{"role":"user","content":"Buscá repos"}}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(
		`{"display":"prompt de una sesión perdida [Pasted text #1 +2 lines]","pastedContents":{"1":{"content":"texto pegado"}},"timestamp":1760000000000,"project":"D:\\Work\\acme","sessionId":"lost"}`+"\n"+
			`{"display":"Revisá la acción","timestamp":1762768800000,"project":"D:\\Work\\acme","sessionId":"s1"}`+"\n"+
			`{"display":"otro proyecto","timestamp":1762768800000,"project":"D:\\Work\\other","sessionId":"other"}`+"\n"), 0o644)
	var err error
	db, err = openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run = func() int {
		before := count(t, db, "select count(*) from event")
		if err := runImport(db, filepath.Join(dir, "projects"), filepath.Join(dir, "history.jsonl"), filepath.Join(dir, "raw"), "", "acme", "test"); err != nil {
			t.Fatal(err)
		}
		return count(t, db, "select count(*) from event") - before
	}
	return
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestMain(m *testing.M) {
	cfg = config{User: "Ana", RelayTools: []string{"mcp__relay"}, Roles: []roleRule{
		{Name: "aux", Label: "Agent A", Git: "file", Marker: "ROLE.md", Contains: "role: aux", Folder: "claude-worktrees-aux"},
		{Name: "main", Label: "the main agent", Git: "dir", Marker: "ROLE.md", Contains: "role: main", Folder: "work-acme"},
	}, Repo: "D:/Work/acme", Anchors: map[string]string{"queue_id": `\bQ\d{1,3}\b`, "card": `\b(?:E\d{3}|[Cc]ard \d{1,3})\b`}}
	compileConfig()
	os.Exit(m.Run())
}

func TestImport(t *testing.T) {
	dir, db, run := setup(t)
	if n := run(); n != 14 {
		t.Fatalf("first import added %d events, want 14", n)
	}
	checks := []struct {
		q    string
		want int
	}{
		{"select count(*) from event where speaker='user' and kind='prompt' and session_id='s1'", 1},
		{"select count(*) from event where speaker='user' and kind='steering' and text='no, eso no'", 1},
		{"select count(*) from event where speaker='user' and kind='answer' and text like '%A: No%'", 1},
		{"select count(*) from event where speaker='user' and kind='refusal'", 1},
		{"select count(*) from event where kind='tool_result' and tool_name='Bash' and exit_code=0", 1},
		{"select count(*) from event where kind='summary' and speaker='model-summary'", 1},
		{"select count(*) from event where kind='boundary' and compaction_no=1", 1},
		{"select count(*) from event where kind='summary' and compaction_no=1", 1},
		{"select count(*) from event where redacted='openrouter-key' and text not like '%abcdefghij%'", 1},
		{"select count(*) from anchor where type='path' and value='system/queue.md'", 1},
		{"select count(*) from anchor where type='queue_id' and value='Q47'", 1},
		{"select count(*) from session where id='agent-x1' and role='subagent' and parent_id='s1'", 1},
		{"select count(*) from event where session_id='agent-x1' and speaker='assistant' and kind='prompt'", 1},
		{"select count(*) from session where id='s1' and role='aux' and title='launch-day' and compactions=1", 1},
		{"select count(*) from event where session_id='lost' and text like '%texto pegado%'", 1},
		{"select count(*) from event where session_id='other'", 0},
		{"select count(*) from event where speaker='relay' and kind='relay' and text='respuesta del otro agente'", 1},
		{"select count(*) from event where uuid like 'hist:s1:%'", 0},        // s1 survives: its prompt is already there
		{"select count(*) from event_fts where event_fts match 'accion'", 1}, // "acción" found without the accent
	}
	for _, c := range checks {
		if got := count(t, db, c.q); got != c.want {
			t.Errorf("%s = %d, want %d", c.q, got, c.want)
		}
	}
	if n := run(); n != 0 {
		t.Fatalf("second import added %d events, want 0", n)
	}
	// A line still being written is left for the next run; once complete it is read.
	f, _ := os.OpenFile(filepath.Join(dir, "projects", proj, "s1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	half := `{"type":"assistant","uuid":"a9","timestamp":"2026-11-10T11:00:00.000Z","sessionId":"s1","message":{"content":[{"type":"text","text":"listo"}]}}`
	f.WriteString(half[:40])
	f.Sync()
	if n := run(); n != 0 {
		t.Fatalf("partial line added %d events", n)
	}
	f.WriteString(half[40:] + "\n")
	f.Close()
	if n := run(); n != 1 {
		t.Fatalf("completed line added %d events, want 1", n)
	}
	// The raw copy reads back as the whole file across appended gzip members.
	gz, err := os.Open(filepath.Join(dir, "raw", proj, "s1.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := gzip.NewReader(gz)
	got, _ := io.ReadAll(zr)
	gz.Close()
	want, _ := os.ReadFile(filepath.Join(dir, "projects", proj, "s1.jsonl"))
	if string(got) != string(want) {
		t.Fatalf("raw copy differs: %d vs %d bytes", len(got), len(want))
	}
}

// Claude Code rewrites transcripts in place, splices records and replays rows after compaction.
func TestRewriteSpliceReplay(t *testing.T) {
	dir, db, run := setup(t)
	run()
	p := filepath.Join(dir, "projects", proj, "s1.jsonl")
	orig, _ := os.ReadFile(p)
	// Same-size rewrite: one prompt's words change, length kept.
	changed := strings.Replace(string(orig), "Revisá la acción", "Mirá esa función", 1)
	if len(changed) != len(orig) {
		t.Fatalf("test setup: lengths differ %d %d", len(changed), len(orig))
	}
	changed = strings.Replace(changed, `"uuid":"u1"`, `"uuid":"w1"`, 1)
	os.WriteFile(p, []byte(changed), 0o644)
	if n := run(); n != 1 {
		t.Fatalf("same-size rewrite added %d events, want 1 (the changed prompt)", n)
	}
	if olds, _ := filepath.Glob(filepath.Join(dir, "raw", proj, "s1.jsonl.replaced-*.gz")); len(olds) != 1 {
		t.Fatalf("old raw copy not kept: %v", olds)
	}
	// Spliced line: a cut record followed by a whole one, and two whole records on one line; plus a replayed boundary.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","uuid":"c1","timest{"type":"assistant","uuid":"c2","timestamp":"2026-11-10T12:00:00.000Z","sessionId":"s1","message":{"content":[{"type":"text","text":"dos"}]}}` + "\n")
	f.WriteString(`{"type":"assistant","uuid":"c3","timestamp":"2026-11-10T12:00:01.000Z","sessionId":"s1","message":{"content":[{"type":"text","text":"tres"}]}}{"type":"assistant","uuid":"c4","timestamp":"2026-11-10T12:00:02.000Z","sessionId":"s1","message":{"content":[{"type":"text","text":"cuatro"}]}}` + "\n")
	f.WriteString(transcript[6] + "\n")
	f.Close()
	if n := run(); n != 3 {
		t.Fatalf("spliced lines added %d events, want 3", n)
	}
	if got := count(t, db, "select bad_lines from source_file where path = ?", p); got != 1 {
		t.Errorf("bad_lines = %d, want 1", got)
	}
	if got := count(t, db, "select compactions from session where id = 's1'"); got != 1 {
		t.Errorf("compactions = %d after a replayed boundary, want 1", got)
	}
}

// A run killed mid-archive leaves a half gzip member; the next run cuts it off. A second importer is turned away.
func TestKilledArchiveAndLock(t *testing.T) {
	dir, _, run := setup(t)
	run()
	gzPath := filepath.Join(dir, "raw", proj, "s1.jsonl.gz")
	g, _ := os.OpenFile(gzPath, os.O_APPEND|os.O_WRONLY, 0)
	g.Write([]byte{0x1f, 0x8b, 8, 0, 0, 0}) // a member's header, then the process "died"
	g.Close()
	p := filepath.Join(dir, "projects", proj, "s1.jsonl")
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","uuid":"k1","timestamp":"2026-11-10T13:00:00.000Z","sessionId":"s1","message":{"content":[{"type":"text","text":"otra"}]}}` + "\n")
	f.Close()
	run()
	gz, _ := os.Open(gzPath)
	zr, err := gzip.NewReader(gz)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	gz.Close()
	want, _ := os.ReadFile(p)
	if err != nil || string(got) != string(want) {
		t.Fatalf("raw copy after a killed run: err %v, %d vs %d bytes", err, len(got), len(want))
	}

	l1, err := tryLock(filepath.Join(dir, "x.lock"))
	if err != nil || l1 == nil {
		t.Fatalf("first lock: %v %v", l1, err)
	}
	if l2, err := tryLock(filepath.Join(dir, "x.lock")); err != nil || l2 != nil {
		t.Fatalf("second lock should be refused while the first is held: %v %v", l2, err)
	}
	l1.Close()
	if l3, err := tryLock(filepath.Join(dir, "x.lock")); err != nil || l3 == nil {
		t.Fatalf("lock not free after release: %v %v", l3, err)
	} else {
		l3.Close()
	}
}

func TestRedactAndAnchors(t *testing.T) {
	s, r := redact("export TYPESAFE_API_KEY=abcd1234abcd1234abcd and ghp_" + strings.Repeat("a", 36))
	if strings.Contains(s, "abcd1234") || !strings.Contains(r, "assigned-secret") || !strings.Contains(r, "github-token") {
		t.Fatalf("redact: %q %q", s, r)
	}
	a := anchors(`D:\Work\acme\.claude\worktrees\aux\system\tools\trace\main.go, card 41 y E035`, "reply", "")
	want := map[string]bool{"path system/tools/trace/main.go": false, "card card 41": false, "card E035": false}
	for _, h := range a {
		if _, ok := want[h.typ+" "+h.value]; ok {
			want[h.typ+" "+h.value] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing anchor %s in %v", k, a)
		}
	}
}

// The hook returns before the import finishes, and the detached import still lands with the hook's event name.
func TestHookDetaches(t *testing.T) {
	dir, db, _ := setup(t)
	exe := filepath.Join(dir, "trace.exe")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	hook := exec.Command(exe, "hook", "-db", filepath.Join(dir, "m.db"), "-projects", filepath.Join(dir, "projects"),
		"-history", filepath.Join(dir, "history.jsonl"), "-archive", filepath.Join(dir, "raw"), "-repo=", "-scope", "acme")
	hook.Env = append(os.Environ(), "TRACE_HOME="+dir) // never the machine's own config.json
	hook.Stdin = strings.NewReader(`{"hook_event_name":"SessionEnd","session_id":"s1"}`)
	start := time.Now()
	if out, err := hook.CombinedOutput(); err != nil || len(out) > 0 {
		t.Fatalf("hook: %v %s", err, out)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("hook took %v; it must return before the import", d)
	}
	for i := 0; ; i++ {
		if count(t, db, "select count(*) from ingest_run where trigger like 'hook:SessionEnd%' and ended_at is not null") == 1 {
			break
		}
		if i == 60 {
			t.Fatal("detached import never finished")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if n := count(t, db, "select count(*) from event"); n != 14 {
		t.Errorf("events %d, want 14", n)
	}
}

// SessionStart on a new session imports first, then shows where the newest other session of the same role stopped.
func TestSessionStartResume(t *testing.T) {
	dir, db, _ := setup(t)
	dbPath := filepath.Join(dir, "m.db")
	run := func(db *sql.DB, trigger string) error {
		return runImport(db, filepath.Join(dir, "projects"), filepath.Join(dir, "history.jsonl"), filepath.Join(dir, "raw"), "", "acme", trigger)
	}
	var out strings.Builder
	// A later Codex session in the same checkout shares the role (role = which checkout) but is not the same agent (host = which agent).
	if _, err := db.Exec(`insert into session(id, host, role, started_at, ended_at) values ('cx', 'codex', 'aux', '2026-11-10T11:00:00Z', '2026-11-10T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	in := hookInput{Event: "SessionStart", Source: "clear", SessionID: "new", Transcript: filepath.Join(dir, "projects", proj, "new.jsonl")}
	if err := hook(in, nil, dbPath, run, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Where Agent A stopped", "Session s1 (named first-try, then launch-day),", "steering: no, eso no", "Bash", "search <words>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-or-v1") {
		t.Error("a secret reached the injected block")
	}
	if count(t, db, "select count(*) from ingest_run where trigger like 'hook:SessionStart:clear new'") != 1 {
		t.Error("SessionStart did not import first")
	}
	out.Reset()
	in.Transcript = filepath.Join(dir, "projects", "C--other-project", "x.jsonl")
	if hook(in, nil, dbPath, run, &out); out.Len() != 0 {
		t.Errorf("a session outside scope got a block:\n%s", out.String())
	}
}

// One git process answers every commit candidate of a run, known or not.
func TestCommitBatch(t *testing.T) {
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skip("no git")
	}
	full := strings.TrimSpace(string(head))
	im := &importer{repo: "../../..", shaCache: map[string]string{}}
	defer im.closeGit()
	for i := 0; i < 3; i++ {
		if got := im.commit(full[:9]); got != full {
			t.Fatalf("commit(%s) = %q, want %s", full[:9], got, full)
		}
		if got := im.commit(fmt.Sprintf("0000000%d", i)); got != "" {
			t.Fatalf("unknown sha resolved to %q", got)
		}
		delete(im.shaCache, full[:9])
	}
}

// The role comes from the checkout's own binding files, so a moved folder keeps it; the folder name is the fallback.
func TestRoleFor(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	mk("moved-b/.git", "gitdir: x")
	mk("moved-b/ROLE.md", "role: aux\n")
	mk("moved-b/system/tools/x", "")
	mk("main/.git/HEAD", "ref")
	mk("main/ROLE.md", "role: main\n")
	mk("other-wt/.git", "gitdir: y")
	for cwd, want := range map[string]string{
		"moved-b": "aux", "moved-b/system/tools": "aux", "main": "main", "other-wt": "other", "gone": "fallback",
	} {
		if got := roleFor(filepath.Join(dir, cwd), "fallback"); got != want {
			t.Errorf("roleFor(%s) = %s, want %s", cwd, got, want)
		}
	}
	if _, role, _, _ := sessionFor(`C:\p\d--work-acme\s.jsonl`); role != "main" {
		t.Errorf("lower-case main folder got role %s", role)
	}
	// With no roles configured, every checkout is its own project, and its sessions get the hooks.
	saved := cfg
	cfg = config{}
	defer func() { cfg = saved }()
	if got := roleFor(filepath.Join(dir, "main"), "fallback"); got != projectRole(filepath.Join(dir, "main")) || !ours(got) {
		t.Errorf("no roles: roleFor(main) = %s", got)
	}
}

// Machine text in a user row is never filed as the user's words.
func TestTyped(t *testing.T) {
	for in, want := range map[string]string{
		"[Request interrupted by user for tool use]": "harness/boundary",
		"[[bridge-round:round-abc]] hacé esto":       "relay/relay",
		"<command-name>/compact</command-name>":      "user/command",
		"<bash-input> rm x</bash-input>":             "user/command",
		"[Image #15] <- busca este paper":            "user/prompt",
		"no, eso no":                                 "user/prompt",
	} {
		if e := typed("user", in); e.speaker+"/"+e.kind != want {
			t.Errorf("typed(%q) = %s/%s, want %s", in, e.speaker, e.kind, want)
		}
	}
}

// Tool-results are copied to raw, workflow subagents are read, and a changed or deleted text leaves the indexes.
func TestCaptureAndIndex(t *testing.T) {
	dir, db, run := setup(t)
	p := filepath.Join(dir, "projects", proj)
	os.MkdirAll(filepath.Join(p, "s1", "tool-results"), 0o755)
	os.WriteFile(filepath.Join(p, "s1", "tool-results", "big.txt"), []byte("salida completa"), 0o644)
	wf := filepath.Join(p, "s1", "subagents", "workflows", "wf1")
	os.MkdirAll(wf, 0o755)
	os.WriteFile(filepath.Join(wf, "agent-w.jsonl"), []byte(
		`{"type":"user","uuid":"w1","timestamp":"2026-11-10T10:06:00.000Z","sessionId":"s1","message":{"role":"user","content":"Revisá el flujo"}}`+"\n"), 0o644)
	run()
	if b, err := os.ReadFile(filepath.Join(dir, "raw", proj, "s1", "tool-results", "big.txt")); string(b) != "salida completa" {
		t.Errorf("tool-results not copied: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw", proj, "s1", "subagents", "workflows", "wf1", "agent-w.jsonl.gz")); err != nil {
		t.Error("workflow subagent raw copy missing:", err)
	}
	if count(t, db, "select count(*) from session where id = 'agent-w' and role = 'subagent' and parent_id = 's1'") != 1 {
		t.Error("workflow subagent not read as a subagent of s1")
	}
	hits := func(w string) int {
		return count(t, db, "select count(*) from event_fts where event_fts match ?", w) + count(t, db, "select count(*) from event_tri where event_tri match ?", w)
	}
	db.Exec("update event set text = 'palabranueva' where text = 'Revisá el flujo'")
	if hits("flujo") != 0 || hits("palabranueva") != 2 {
		t.Errorf("update left the indexes stale: flujo %d, palabranueva %d", hits("flujo"), hits("palabranueva"))
	}
	db.Exec("delete from event where text = 'palabranueva'")
	if hits("palabranueva") != 0 {
		t.Error("delete left the text searchable")
	}
}

// A subagent of a configured role is told it is a helper; other projects get nothing.
func TestSubagentStart(t *testing.T) {
	var out strings.Builder
	in := hookInput{Event: "SubagentStart", Transcript: filepath.Join("x", proj, "s1.jsonl")}
	if err := hook(in, nil, "", nil, &out); err != nil {
		t.Fatal(err)
	}
	var got struct{ HookSpecificOutput map[string]string }
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil || !strings.Contains(got.HookSpecificOutput["additionalContext"], "helper agent started by Agent A, not Agent A") {
		t.Errorf("bad SubagentStart output %q: %v", out.String(), err)
	}
	out.Reset()
	in.Transcript = filepath.Join("x", "C--other-project", "s1.jsonl")
	if hook(in, nil, "", nil, &out); out.Len() != 0 {
		t.Errorf("another project got %q", out.String())
	}
	if len(compactSteer) > 1500 {
		t.Errorf("compactSteer is %d bytes", len(compactSteer))
	}
}
