package main

import (
	"math"
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
	// The whole range, as Go's own cmd/go lockedfile does: no length word a mutation could change unseen.
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, math.MaxUint32, math.MaxUint32, ol); err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}
