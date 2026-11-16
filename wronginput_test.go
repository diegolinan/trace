package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC E1: a -projects that is a file, or a -history that is a folder, fails and names the path; a missing
// -projects is still no Claude Code.
func TestWrongSourcePathsFail(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	folder := filepath.Join(root, "afolder")
	os.MkdirAll(folder, 0o755)
	for bad, args := range map[string][]string{
		file:   {"import", "-projects", file, "-history=", "-codex="},
		folder: {"import", "-projects=", "-history", folder, "-codex="},
	} {
		_, errOut, code := runTrace(t, root, "", nil, args...)
		if code != 1 || !strings.Contains(errOut, bad) {
			t.Errorf("%v: exit %d, want 1 naming %s\n%s", args, code, bad, errOut)
		}
	}
	if _, errOut, code := runTrace(t, root, "", nil, "import", "-projects", filepath.Join(root, "none"), "-history=", "-codex="); code != 0 {
		t.Errorf("a missing projects folder must be no Claude Code: exit %d\n%s", code, errOut)
	}
}

// SPEC E2: punctuation alone is no words.
func TestSearchNeedsRealWords(t *testing.T) {
	root := t.TempDir()
	_, errOut, code := runTrace(t, root, "", nil, "search", "-projects=", "-history=", "-codex=", "??", "!")
	if code != 1 || !strings.Contains(errOut, "search needs words") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
}
