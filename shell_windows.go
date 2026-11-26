package main

import (
	"context"
	"os"
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
	if s := shortPath(p); s != "" && !strings.Contains(s, " ") {
		return s
	}
	return `"` + p + `"`
}

// getShortPathName is the system call, a variable so a test can stand in for a volume with short names.
var getShortPathName = windows.GetShortPathName

// shortPath is p's 8.3 form, or "" when there is none. The first call asks for the size the form needs, with its
// closing zero, so any length fits.
func shortPath(p string) string {
	long, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return ""
	}
	n, err := getShortPathName(long, nil, 0) // no buffer, so a size of 0
	if err != nil {
		return ""
	}
	buf := make([]uint16, n)
	if _, err := getShortPathName(long, &buf[0], n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// consoleIn says whether f is a console a person types in (SPEC U2). NUL is not one, though Go calls it a character
// device, and neither is a pipe.
func consoleIn(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
