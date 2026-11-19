package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC S3: resume with no -host answers for the home's agent: Claude Code unless the memory is Codex-only.
func TestResumeDefaultsToTheHomesAgent(t *testing.T) {
	for config, want := range map[string]string{"": "Claude Code", `{"claude": false}`: "Codex"} {
		root := t.TempDir()
		if config != "" {
			os.MkdirAll(filepath.Join(root, "trace"), 0o755)
			os.WriteFile(filepath.Join(root, "trace", "config.json"), []byte(config), 0o644)
		}
		out, errOut, code := runTrace(t, root, "", nil, "resume", "-codex=")
		if code != 0 || !strings.Contains(out, "No earlier "+want+" session") {
			t.Errorf("config %q: exit %d, want %q\n%s%s", config, code, want, out, errOut)
		}
	}
}
