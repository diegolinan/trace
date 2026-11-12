package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startDetached starts cmd with no console and outside the hook's job, so the host closing or killing the hook
// does not take the import with it. A job that forbids breakaway refuses the flag; then it starts without it.
func startDetached(cmd *exec.Cmd) error {
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	if err := cmd.Start(); err == nil {
		return nil
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
	if err := retry.Start(); err != nil {
		return err
	}
	*cmd = *retry
	return nil
}
