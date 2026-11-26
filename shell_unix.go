//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// shellCommand runs command through sh, which receives it as one argument, as written (SPEC C1). It runs in its own
// process group, so when ctx ends the group is killed with everything sh started (SPEC C2).
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}

// shellPath is p as an agent's shell runs it as written (SPEC S2): quoted when it holds a space.
func shellPath(p string) string {
	if strings.Contains(p, " ") {
		return `"` + p + `"`
	}
	return p
}

// consoleIn says whether f is a terminal a person types in (SPEC U2): /dev/null is not one, though Go calls it a
// character device, and neither is a pipe. A terminal has a window size; nothing else does.
func consoleIn(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}
