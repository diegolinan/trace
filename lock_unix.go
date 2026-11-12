//go:build !windows

package main

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive OS lock on path without waiting; the kernel frees it when the process dies.
// Returns the file to keep open, or nil when another process holds it.
func tryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
