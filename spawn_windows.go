package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startDetached starts cmd outside the hook's job, so the host closing or killing the hook does not take the import
// with it. A job that forbids breakaway refuses the flag; then it starts without it. CREATE_NO_WINDOW, not
// DETACHED_PROCESS: a detached process has no console, so the git it starts (commit checks) got a new console
// window of its own, which flashed on the screen at every Stop; a hidden console is inherited instead.
func startDetached(cmd *exec.Cmd) error {
	attr := syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	breakaway := attr
	breakaway.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	cmd.SysProcAttr = &breakaway
	if err := cmd.Start(); err == nil {
		return nil
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.SysProcAttr = &attr
	if err := retry.Start(); err != nil {
		return err
	}
	*cmd = *retry
	return nil
}
