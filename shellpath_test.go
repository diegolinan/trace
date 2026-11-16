package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// SPEC S2: a binary path with a space becomes one a shell runs as written; one without is unchanged.
func TestShellPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Jane Doe")
	os.MkdirAll(dir, 0o755)
	exe := filepath.Join(dir, "trace.exe")
	os.WriteFile(exe, []byte("x"), 0o644)
	got := shellPath(exe)
	switch {
	case !strings.Contains(got, " "):
		if _, err := os.Stat(got); err != nil {
			t.Errorf("short form %q does not name the file: %v", got, err)
		}
	case got != `"`+exe+`"`:
		t.Errorf("shellPath(%q) = %q: neither a path without a space nor the quoted path", exe, got)
	}
	// The line an agent reads: one form per shell when the path stays quoted on Windows.
	line := searchLine(got, "")
	if strings.HasPrefix(got, `"`) && runtime.GOOS == "windows" && !strings.Contains(line, "`& "+got+" search <words>`") {
		t.Errorf("no PowerShell form beside the quoted path: %s", line)
	}
	if !strings.Contains(line, "`"+got+" search <words>`") {
		t.Errorf("search line: %s", line)
	}
	plain := filepath.Join(t.TempDir(), "trace.exe")
	if !strings.Contains(plain, " ") && shellPath(plain) != plain {
		t.Errorf("a path without a space changed: %q", shellPath(plain))
	}
}
