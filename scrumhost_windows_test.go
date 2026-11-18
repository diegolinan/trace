package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shFake writes a batch file standing in for a host's command line and returns its path.
func shFake(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// SPEC T19: Claude Code runs in print mode with no tools and no session kept, the prompt on stdin, marked as the
// child, with no session id of the user's; its printed answer is the answer.
func TestRunScrumHostClaude(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "user-session")
	exe := shFake(t, "claude", "echo child=%TRACE_SCRUM_CHILD% session=[%CLAUDE_CODE_SESSION_ID%] advisor=%CLAUDE_CODE_DISABLE_ADVISOR_TOOL% args=%*\r\nmore\r\n")
	out, err := runScrumHost(exe, claudeName, "m1", "THE PROMPT")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"child=1", "session=[]", "advisor=1", "-p --no-session-persistence", "--model m1", "THE PROMPT"} {
		if !strings.Contains(out, want) {
			t.Errorf("answer lacks %q:\n%s", want, out)
		}
	}
}

// SPEC T19: Codex runs exec read-only and ephemeral, the prompt on stdin; its last message, written where -o says,
// is the answer.
func TestRunScrumHostCodex(t *testing.T) {
	exe := shFake(t, "codex", "echo printed, not the answer\r\nmore > %7\r\n")
	out, err := runScrumHost(exe, codexName, "", "THE PROMPT")
	if err != nil || strings.TrimSpace(out) != "THE PROMPT" {
		t.Errorf("answer %q, %v", out, err)
	}
}

// SPEC T19: a host that fails fails the run, naming the host; and the default runner refuses from a test binary
// even when the host would answer.
func TestRunScrumHostFails(t *testing.T) {
	if _, err := runScrumHost(shFake(t, "claude", "exit /b 3\r\n"), claudeName, "", "p"); err == nil || !strings.Contains(err.Error(), claudeName) {
		t.Errorf("a failing host: %v", err)
	}
	old := scrumExe
	scrumExe = map[string]string{claudeName: shFake(t, "claude", "echo {}\r\n")}
	t.Cleanup(func() { scrumExe = old })
	if _, err := hostScrum(claudeName, "", "p"); err == nil {
		t.Error("the default runner ran from a test binary")
	}
}
