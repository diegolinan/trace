package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC I13: with CLAUDE_CONFIG_DIR set, the import reads that profile's transcripts and prompt log, and nothing from
// ~/.claude.
func TestClaudeConfigDirIsReadToo(t *testing.T) {
	root := t.TempDir() // runTrace sets HOME to root/user and CLAUDE_CONFIG_DIR to root/claude
	write := func(base, word string) {
		dir := filepath.Join(root, base, "projects", "C--work-acme")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "s-"+word+".jsonl"), []byte(mutLines(mutUser("u-"+word, 0, word))), 0o644)
		os.WriteFile(filepath.Join(root, base, "history.jsonl"), []byte(`{"display":"`+word+`-log","timestamp":1763000000000,"project":"C:\\work\\acme","sessionId":"gone-`+word+`"}`+"\n"), 0o644)
	}
	write("claude", "profileword")
	write(filepath.Join("user", ".claude"), "defaultword")
	if _, errOut, code := runTrace(t, root, "", nil, "import", "-codex="); code != 0 {
		t.Fatalf("import: exit %d\n%s", code, errOut)
	}
	for word, want := range map[string]bool{"profileword": true, "profileword-log": true, "defaultword": false, "defaultword-log": false} {
		out, _, _ := runTrace(t, root, "", nil, "search", "-all", "-codex=", word)
		if got := strings.Contains(out, word); got != want {
			t.Errorf("search %s: found %v, want %v\n%s", word, got, want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "trace", "raw", ".claude", "history.jsonl.gz")); err != nil {
		t.Errorf("the profile's prompt log was not copied: %v", err)
	}
}
