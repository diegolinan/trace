package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// shellCommand runs command through cmd. Go's argument escaping turns a quote into \", which cmd does not
// read, so the command line is given as written (SPEC C1); /S makes cmd strip only the outer pair of quotes.
// When ctx ends, the whole process tree is ended: killing cmd alone leaves what it started running (SPEC C2).
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /S /C "` + command + `"`}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW: no console flashes
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return cmd
}

// shellPath is p as an agent's shell runs it as written (SPEC S2). A path with a space would need quotes in cmd
// and bash but & "..." in PowerShell, so it becomes its 8.3 short form, which has none; a volume without short
// names gets the quoted path.
func shellPath(p string) string {
	if !strings.Contains(p, " ") {
		return p
	}
	long, err := windows.UTF16PtrFromString(p)
	if err == nil {
		buf := make([]uint16, windows.MAX_PATH)
		if n, err := windows.GetShortPathName(long, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
			if s := windows.UTF16ToString(buf[:n]); !strings.Contains(s, " ") {
				return s
			}
		}
	}
	return `"` + p + `"`
}
