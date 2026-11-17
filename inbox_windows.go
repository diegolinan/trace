package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// dialInbox opens a Claude Code inbox, a named pipe, as a file: Windows opens a pipe's client end by its name.
func dialInbox(path string) (io.ReadWriteCloser, error) { return os.OpenFile(path, os.O_RDWR, 0) }

// pidAlive says whether a process runs. One this user may not query (elevated) runs.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}
