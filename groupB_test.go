package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Behaviour tests for codex.go, codexhooks.go and codexinstall.go: each fails with one of their surviving mutants.

// An unchanged plain rollout is not read again: its broken line counts once over two runs.
func TestCodexUnchangedPlainNotReadAgain(t *testing.T) {
	s := newCMStore(t)
	s.write("rollout-x-t-p.jsonl", cmMeta("t-p", s.cwd(), nil)+"not json\n"+cmUser(1, "NONCE_PLAIN"))
	s.imp("")
	s.imp("")
	if n := s.n("select bad_lines from source_file where path like '%t-p.jsonl'"); n != 1 {
		t.Errorf("bad_lines after two runs = %d, want 1", n)
	}
}

// A thread that reuses a call id names each result after the latest call with that id before it, whether the result
// is read in the same run as its call or in a later one (SPEC I6).
func TestCodexReusedCallIDNamedByLatestCall(t *testing.T) {
	s := newCMStore(t)
	p := s.write("rollout-x-t-re.jsonl", cmMeta("t-re", s.cwd(), nil)+
		cmCall(1, "x", "", "alpha", "{}")+cmOutput(2, "x", "uno")+cmCall(3, "x", "", "beta", "{}"))
	s.imp("")
	s.appendText(p, cmOutput(4, "x", "dos")+cmCall(5, "x", "", "gamma", "{}")+cmOutput(6, "x", "tres"))
	s.imp("")
	for text, want := range map[string]string{"uno": "alpha", "dos": "beta", "tres": "gamma"} {
		var got string
		s.db.QueryRow("select coalesce(tool_name, '') from event where session_id = 't-re' and kind = 'tool_result' and text = ?", text).Scan(&got)
		if got != want {
			t.Errorf("result %q is named %q, want %q", text, got, want)
		}
	}
}

// A compressed rollout is decoded in the goroutine that reads it, so one window is in memory at a time, not one per
// core.
func TestCodexZstDecodedInTheReader(t *testing.T) {
	s := newCMStore(t)
	var big strings.Builder // many blocks, so a concurrent decoder would still be running after the first read
	big.WriteString(cmMeta("t-g", s.cwd(), nil))
	for i := 1; i < 400; i++ {
		big.WriteString(cmUser(i, strings.Repeat(itoa(i*7919)+" ", 2000)))
	}
	p := s.writeZst("rollout-x-t-g.jsonl.zst", big.String())
	before := runtime.NumGoroutine()
	rc, err := openRollout(p)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	head := make([]byte, 100)
	if _, err := io.ReadFull(rc, head); err != nil || string(head) != big.String()[:len(head)] {
		t.Fatalf("read %q: %v", head, err)
	}
	if during := runtime.NumGoroutine(); during > before {
		t.Errorf("decoding runs in %d more goroutines", during-before)
	}
}

// writeChecked leaves no temporary file behind when it cannot write: the folder is missing, the backup cannot be
// written, or the path is a folder the file cannot replace. The target stays as it was.
func TestWriteCheckedFailsClean(t *testing.T) {
	dir := t.TempDir()
	tmps := func() []string { m, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); return m }

	if err := writeChecked(filepath.Join(dir, "missing", "a.json"), "", false, "x", ""); err == nil {
		t.Error("a write into a missing folder succeeded")
	}

	target := filepath.Join(dir, "b.json")
	os.WriteFile(target, []byte("old"), 0o644)
	if err := writeChecked(target, "old", true, "new", filepath.Join(dir, "missing", "b.bak")); err == nil {
		t.Error("a write whose backup cannot be written succeeded")
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Errorf("the target was written without its backup: %q", b)
	}

	folder := filepath.Join(dir, "c.json")
	os.Mkdir(folder, 0o755)
	if err := writeChecked(folder, "", false, "x", ""); err == nil {
		t.Error("a write over a folder succeeded")
	}
	if fi, err := os.Stat(folder); err != nil || !fi.IsDir() {
		t.Errorf("the folder was replaced: %v", err)
	}
	if left := tmps(); len(left) != 0 {
		t.Errorf("temporary files left: %q", left)
	}
}

// An install refuses an AGENTS.md that asks for another home in any block, not only the first: here this home's
// block comes first and another's second. Nothing is written.
func TestCodexInstallSeesEveryOtherBlock(t *testing.T) {
	home, other, dir, codex := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	text := "# Rules\n" + agentsBlock(home) + agentsBlock(other)
	os.WriteFile(agents, []byte(text), 0o644)
	var out strings.Builder
	err := installCodex(home, dir, codex, "agents", "", false, &out)
	if err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("install over another home's second block: %v", err)
	}
	if b, _ := os.ReadFile(agents); string(b) != text || exists(filepath.Join(home, "config.json")) {
		t.Errorf("something was written: AGENTS.md %q", b)
	}
}

// A rollout marked outside the scope is not opened again while that scope is in force: rewritten to name a folder
// inside the scope, it still stores nothing, and keeps its mark.
func TestCodexMarkHoldsUnderItsScope(t *testing.T) {
	s := newCMStore(t)
	alpha := filepath.Join(s.root, "alpha")
	s.write("rollout-x-t-k.jsonl", cmMeta("t-k", filepath.Join(s.root, "beta"), nil)+cmUser(1, "fuera"))
	s.imp(alpha)
	s.write("rollout-x-t-k.jsonl", cmMeta("t-k", alpha, nil)+cmUser(1, "NONCE_MARKED"))
	s.imp(alpha)
	if n := s.n("select count(*) from event where text = 'NONCE_MARKED'"); n != 0 {
		t.Errorf("a marked rollout was read under its own scope: %d events", n)
	}
	if n := s.n("select count(*) from source_file where skipped = ?", "scope:"+alpha); n != 1 {
		t.Errorf("the mark under its own scope is gone (%d marks)", n)
	}
}

// A compressed rollout whose mark is cleared on a run that cannot read its session_meta keeps its mark until a run
// reads it: put back as it was, size and time included, it is read under the new scope.
func TestCodexClearedMarkNotLostOnUnreadableRun(t *testing.T) {
	s := newCMStore(t)
	text := cmMeta("t-u", filepath.Join(s.root, "beta"), nil) + cmUser(1, "NONCE_BACK")
	p := s.writeZst("rollout-x-t-u.jsonl.zst", text)
	fi, _ := os.Stat(p)
	s.imp(filepath.Join(s.root, "alpha"))
	s.writeZst("rollout-x-t-u.jsonl.zst", cmUser(1, "no meta yet"))
	s.imp("")
	s.writeZst("rollout-x-t-u.jsonl.zst", text)
	os.Chtimes(p, fi.ModTime(), fi.ModTime())
	s.imp("")
	if n := s.n("select count(*) from event where text = 'NONCE_BACK'"); n != 1 {
		t.Errorf("the rollout put back was not read: %d events", n)
	}
}
