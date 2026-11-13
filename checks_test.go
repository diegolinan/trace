package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A check runs only after a matching tool call, logs passes and failures, and only a failure reaches the agent.
func TestChecks(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cs := []check{
		{Name: "links", On: "Bash", Match: `git\b.*\bcommit`, Run: "lint"},
		{Name: "bad", On: "(", Run: "x"},
	}
	var ran []string
	shell := func(dir, cmd string) (string, error) {
		ran = append(ran, dir+" "+cmd)
		if strings.Contains(dir, "dirty") {
			return "new.md has no inbound link\n", errors.New("exit status 1")
		}
		return "ok\n", nil
	}
	call := func(tool, input, cwd string) string {
		return runChecks(db, hookInput{SessionID: "s", ToolName: tool, ToolInput: []byte(input), Cwd: cwd}, cs, shell)
	}
	if got := call("Bash", `{"command":"git status"}`, "clean"); len(ran) != 0 || !strings.Contains(got, `"bad" is misconfigured`) {
		t.Fatalf("no match should run nothing, and a bad pattern is reported: ran %v, got %q", ran, got)
	}
	cs = cs[:1]
	if got := call("Write", `{"file_path":"git commit"}`, "clean"); len(ran) != 0 || got != "" {
		t.Fatalf("other tool: ran %v, got %q", ran, got)
	}
	if got := call("Bash", `{"command":"git add . && git commit -m x"}`, "clean"); len(ran) != 1 || got != "" {
		t.Fatalf("pass: ran %v, got %q", ran, got)
	}
	got := call("Bash", `{"command":"git commit -m y"}`, "dirty")
	if !strings.Contains(got, `"links" failed after Bash`) || !strings.Contains(got, "new.md has no inbound link") {
		t.Fatalf("failure text: %q", got)
	}
	if n := count(t, db, "select count(*) from check_run"); n != 2 {
		t.Fatalf("runs logged %d, want 2", n)
	}
	if n := count(t, db, "select count(*) from check_run where ok = 0 and target = 'git commit -m y' and output like '%inbound%'"); n != 1 {
		t.Fatal("the failure was not logged with its target and output")
	}
	if m := checkMatcher([]check{{On: "Bash"}, {On: "Write|Edit"}}); m != "Bash|Write|Edit" {
		t.Fatalf("matcher %q", m)
	}
	if checkMatcher(nil) != "" || checkMatcher([]check{{On: "Bash"}, {}}) != "*" {
		t.Fatal("matcher: none without checks, every tool when one has no tool")
	}
	// The real shell: output and a non-zero exit come back on every platform.
	if out, err := runShell(t.TempDir(), "echo bad && exit 3"); err == nil || !strings.Contains(out, "bad") {
		t.Fatalf("runShell: %q %v", out, err)
	}
}
