package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hidden is how trace starts its own detached copies. CREATE_NO_WINDOW, not DETACHED_PROCESS: a detached process has
// no console, so the git it starts (commit checks) got a new console window of its own, which flashed on the screen
// at every Stop; a hidden console is inherited instead.
var hidden = syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}

// startDetached starts cmd outside the hook's job, so the host closing or killing the hook does not take the import
// with it. A job that forbids breakaway refuses the flag; then it starts without it.
func startDetached(cmd *exec.Cmd) error {
	if err := startAlone(cmd); err == nil {
		return nil
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	attr := hidden
	retry.SysProcAttr = &attr
	if err := retry.Start(); err != nil {
		return err
	}
	*cmd = *retry
	return nil
}

// startAlone starts cmd outside the caller's job, or not at all: a job that forbids breakaway refuses it. The alarm
// starts this way, since inside the caller's job it would die with the caller (SPEC A1).
func startAlone(cmd *exec.Cmd) error {
	attr := hidden
	attr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	cmd.SysProcAttr = &attr
	return cmd.Start()
}
