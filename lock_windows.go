package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive OS lock on path without waiting; the kernel frees it when the process dies,
// unlike a PID file (memsearch #788). Returns the file to keep open, or nil when another process holds it.
func tryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol); err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
