//go:build !windows

package main

import (
	"io"
	"net"
	"syscall"
)

// dialInbox connects to a Claude Code inbox, a Unix socket.
func dialInbox(path string) (io.ReadWriteCloser, error) { return net.Dial("unix", path) }

// pidAlive says whether a process runs; one owned by another user runs.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
