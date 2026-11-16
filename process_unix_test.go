//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// SPEC C2: when the limit runs out, the check is stopped with every process it started, not only its shell.
func TestCheckKillsWhatItStarted(t *testing.T) {
	old := checkLimit
	checkLimit = time.Second
	defer func() { checkLimit = old }()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cs := []check{{Name: "slow", On: "Bash", Run: "sleep 30 & echo $! > '" + pidFile + "'; wait"}}
	runChecks(nil, hookInput{SessionID: "s", ToolName: "Bash", ToolInput: []byte(`{"command":"x"}`), Cwd: t.TempDir()}, cs, runShell)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the check did not start its child: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	defer syscall.Kill(pid, syscall.SIGKILL)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
	}
	t.Errorf("the check's child %d is still running after the limit", pid)
}

// A detached import runs in a session of its own, so the host's hangup or group kill does not reach it.
func TestStartDetachedOwnSession(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	sid, err := unix.Getsid(cmd.Process.Pid)
	if err != nil || sid != cmd.Process.Pid {
		t.Errorf("detached process %d is in session %d (%v), want its own", cmd.Process.Pid, sid, err)
	}
}
