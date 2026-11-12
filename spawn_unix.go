//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// startDetached starts cmd in its own session, so the host's hangup or process-group kill does not reach it.
func startDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
