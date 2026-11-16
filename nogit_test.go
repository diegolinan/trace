package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// SPEC G1 and G9: no source file outside tests starts a git process or looks for a .git.
func TestNoGitProcess(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`exec\.Command(Context)?\([^)]*"git"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if re.Match(b) {
			t.Errorf("%s starts a git process", f)
		}
		if bytes.Contains(b, []byte(`".git"`)) {
			t.Errorf("%s looks for a .git", f)
		}
	}
}

// SPEC G2: with no roles, the role is the folder the session ran in, with a .git above it or not.
func TestRoleIsTheFolder(t *testing.T) {
	old := cfg
	defer func() { cfg = old }()
	cfg.Roles = nil
	for _, withGit := range []bool{false, true} {
		dir := t.TempDir()
		sub := filepath.Join(dir, "site", "inner")
		os.MkdirAll(sub, 0o755)
		if withGit {
			os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
		}
		if got := roleFor(sub, "fallback"); got != projectRole(sub) {
			t.Errorf("git=%v: role %q, want the folder's own %q", withGit, got, projectRole(sub))
		}
	}
}

// SPEC G3: a configured role matches by its marker, nearest folder first; no .git is consulted and a "git" key is
// ignored.
func TestRoleByMarker(t *testing.T) {
	old := cfg
	defer func() { cfg = old }()
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"roles":[{"name":"x","label":"X","git":"file","marker":"ROLE.md","contains":"role: x"}]}`), 0o644)
	c, err := loadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Roles = c.Roles
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ROLE.md"), []byte("role: x\n"), 0o644)
	sub := filepath.Join(dir, "a", "b")
	os.MkdirAll(sub, 0o755)
	if got := roleFor(sub, "fallback"); got != "x" {
		t.Errorf("no .git anywhere: role %q, want x", got)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755) // a .git directory, though the role said "file": ignored
	if got := roleFor(sub, "fallback"); got != "x" {
		t.Errorf("with a .git directory: role %q, want x", got)
	}
	if got := roleFor(t.TempDir(), "fallback"); got != "fallback" {
		t.Errorf("a folder with no marker: role %q, want the fallback", got)
	}
}

// SPEC G4: the start block in folder F shows the newest other session from F or inside it, never a sibling's or a
// parent's.
func TestStartBlockFolderOrInside(t *testing.T) {
	old := cfg
	defer func() { cfg = old }()
	cfg.Roles = nil
	base := t.TempDir()
	f := filepath.Join(base, "site")
	inner := filepath.Join(f, "nested")
	sibling := filepath.Join(base, "other")
	samePrefix := filepath.Join(base, "site-old")
	db, err := openDB(filepath.Join(base, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range []struct{ id, cwd, end, text string }{
		{"in", inner, "2026-11-10T12:00:00Z", "NONCE_INNER"},
		{"sib", sibling, "2026-11-10T13:00:00Z", "NONCE_SIBLING"},
		{"par", base, "2026-11-10T14:00:00Z", "NONCE_PARENT"},
		{"pre", samePrefix, "2026-11-10T15:00:00Z", "NONCE_PREFIX"},
	} {
		if _, err := db.Exec(`insert into session(id, host, role, cwd, started_at, ended_at) values (?, 'claude-code', ?, ?, ?, ?)`,
			s.id, projectRole(s.cwd), s.cwd, s.end, s.end); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, 1, ?, 'assistant', 'reply', ?)`, s.id, s.end, s.text); err != nil {
			t.Fatal(err)
		}
		// each session's user refuses something, naming its own nonce
		if _, err := db.Exec(`insert into event(session_id, seq, ts, speaker, kind, text) values (?, 2, ?, 'user', 'prompt', ?)`, s.id, s.end, "no, eso no "+s.text+"_RULE"); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := resumeBlock(db, "new", "startup", projectRole(f), "claude-code", f, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "NONCE_INNER_RULE") {
		t.Errorf("the refusal from inside %s is missing:\n%s", f, got)
	}
	for _, not := range []string{"NONCE_SIBLING", "NONCE_PARENT", "NONCE_PREFIX"} {
		if strings.Contains(got, not) {
			t.Errorf("%s reached the start block for %s:\n%s", not, f, got)
		}
	}
}

// SPEC G5: the -repo flag and the repo key are accepted and ignored; a path anchor is the path as written.
func TestRepoIgnored(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"repo":"C:/x","user":"U"}`), 0o644)
	if c, err := loadConfig(home); err != nil || c.User != "U" {
		t.Fatalf("a config with a repo key: %+v %v", c, err)
	}
	if _, stderr, code := runTrace(t, t.TempDir(), "", nil, "search", "-repo", "C:/x", "nothing"); code != 0 || strings.Contains(stderr, "not defined") {
		t.Fatalf("-repo was refused (exit %d):\n%s", code, stderr)
	}
	a := anchors(`see D:\Work\acme\src\main.go`, "reply", "")
	if len(a) != 1 || a[0].value != "D:/Work/acme/src/main.go" {
		t.Fatalf("path anchor: %+v", a)
	}
}

// SPEC G8: the card's files are the ones the transcript says Claude Code loaded, read again from disk; with no
// record, none. A .git anywhere changes nothing.
func TestClaudeSourcesFromTranscript(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	rules, gone := filepath.Join(dir, "proj", "CLAUDE.md"), filepath.Join(dir, "mem", "MEMORY.md")
	write(rules, "rule now")
	write(filepath.Join(dir, "CLAUDE.md"), "a parent's file the host did not load")
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	tr := filepath.Join(dir, "t.jsonl")
	rec := func(files ...[3]string) string {
		var fs []map[string]string
		for _, f := range files {
			fs = append(fs, map[string]string{"path": f[0], "type": f[1], "content": f[2]})
		}
		b, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "instructions", "files": fs}})
		return string(b) + "\n"
	}
	write(tr, `{"type":"user","message":{"role":"user","content":"hi"}}`+"\n")
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if s := claudeSources(db, tr); len(s) != 0 {
		t.Fatalf("no record yet must give no files: %v", s)
	}
	f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(rec([3]string{rules, "Project", "rule then"}, [3]string{gone, "AutoMem", "memory as loaded"}))
	f.Close()
	got := claudeSources(db, tr)
	if len(got) != 2 || got[0].text != "rule now" || got[1].text != "memory as loaded" {
		t.Fatalf("sources: %+v", got)
	}
	for _, s := range got {
		if strings.Contains(s.text, "a parent's file") {
			t.Fatalf("a file the host did not load: %+v", got)
		}
	}
	// read again from where it stopped: the cached record still holds
	if again := claudeSources(db, tr); len(again) != 2 {
		t.Fatalf("second read: %+v", again)
	}
}

// SPEC G5: no commit detection; a hexadecimal string is text.
func TestNoCommitAnchors(t *testing.T) {
	for _, a := range anchors("fixed in 0f3acf8c5 and 4096f1349ae3e8f91601845b377b002d376a2310", "reply", "") {
		if a.typ == "sha?" || a.typ == "commit" {
			t.Errorf("commit anchor %+v", a)
		}
	}
}

// SPEC G6: a check runs in the session's folder, not in a repository root above it.
func TestChecksRunInSessionFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git to build a repository around the session folder")
	}
	old := cfg
	defer func() { cfg = old }()
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skip(string(out))
	}
	sub := filepath.Join(repo, "session-folder-g6")
	os.MkdirAll(sub, 0o755)
	run := "pwd; exit 1"
	if runtime.GOOS == "windows" {
		run = "cd & exit /b 1"
	}
	cfg.Checks = []check{{Name: "where", On: "Bash", Match: ".", Run: run}}
	cfg.Reinject = nil
	in := hookInput{Event: "PostToolUse", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`), Cwd: sub, SessionID: "s"}
	var w bytes.Buffer
	if err := ruleHook(in, "r", "claude-code", filepath.Join(t.TempDir(), "m.db"), true, &w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "session-folder-g6") {
		t.Errorf("the check did not run in the session's folder:\n%s", w.String())
	}
}
