package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// SPEC C1: a check's command reaches the shell as written; a quoted path with spaces works.
func TestCheckCommandAsWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space")
	os.MkdirAll(dir, 0o755)
	f := filepath.Join(dir, "note.txt")
	os.WriteFile(f, []byte("NONCE_C1"), 0o644)
	run := `cat "` + f + `"`
	if runtime.GOOS == "windows" {
		run = `type "` + f + `"`
	}
	out, err := runShell(context.Background(), t.TempDir(), run)
	if err != nil || !strings.Contains(out, "NONCE_C1") {
		t.Fatalf("%s: %v\n%s", run, err, out)
	}
}

// SPEC C2: the checks of one call share one limit; a check past it is stopped with its children, the call returns
// soon after, and a check still waiting is reported as not run.
func TestChecksShareOneLimit(t *testing.T) {
	old := checkLimit
	checkLimit = time.Second
	defer func() { checkLimit = old }()
	slow := "sleep 20; echo done" // sh waits on sleep, a child that holds the output pipe
	if runtime.GOOS == "windows" {
		slow = "ping -n 20 127.0.0.1"
	}
	cs := []check{{Name: "first", On: "Bash", Run: slow}, {Name: "second", On: "Bash", Run: slow}}
	start := time.Now()
	got := runChecks(nil, hookInput{SessionID: "s", ToolName: "Bash", ToolInput: []byte(`{"command":"x"}`), Cwd: t.TempDir()}, cs, runShell)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("two slow checks with a 1 s limit took %v", d)
	}
	if !strings.Contains(got, `"first" failed`) || !strings.Contains(got, `"second" was not run`) {
		t.Errorf("report:\n%s", got)
	}
}
