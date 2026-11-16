package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A copy of this test binary started as `startupinfo` writes how it was asked to show its window to the file
// GROUPC_STARTUP names, and exits.
func init() {
	if out := os.Getenv("GROUPC_STARTUP"); out != "" && len(os.Args) > 1 && os.Args[1] == "startupinfo" {
		var si windows.StartupInfo
		windows.GetStartupInfo(&si)
		os.WriteFile(out, []byte(fmt.Sprintf("%v %d", si.Flags&windows.STARTF_USESHOWWINDOW != 0, si.ShowWindow)), 0o644)
		os.Exit(0)
	}
}

// startupOf runs cmd and returns what its startupinfo child wrote.
func startupOf(t *testing.T, cmd *exec.Cmd, out string) string {
	t.Helper()
	if err := cmd.Wait(); err != nil {
		b, _ := os.ReadFile(out)
		t.Fatalf("%v %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A detached import starts with its window hidden.
func TestStartDetachedHidesWindow(t *testing.T) {
	out := filepath.Join(t.TempDir(), "startup.txt")
	t.Setenv("GROUPC_STARTUP", out)
	cmd := exec.Command(os.Args[0], "startupinfo")
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	if got := startupOf(t, cmd, out); got != fmt.Sprintf("true %d", windows.SW_HIDE) {
		t.Fatalf("started with show-window %q, want hidden", got)
	}
}

// setJobLimits sets a job's limit flags.
func setJobLimits(job windows.Handle, flags uint32) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = flags
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// Inside a job that forbids breakaway, the detached import still starts, without breaking away, and still hidden.
// The test process itself goes into the job, so the coverage of the retry counts; a process cannot leave a job, so
// at the end the job lets every child break away, silently, as if it were in none.
func TestStartDetachedInsideJob(t *testing.T) {
	out := filepath.Join(t.TempDir(), "startup.txt")
	t.Setenv("GROUPC_STARTUP", out)
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := setJobLimits(job, 0); err != nil {
		t.Fatal(err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := setJobLimits(job, windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK|windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK); err != nil {
			t.Errorf("the test process stays in a job that forbids breakaway: %v", err)
		}
		windows.CloseHandle(job)
	}()
	cmd := exec.Command(os.Args[0], "startupinfo")
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	if got := startupOf(t, cmd, out); got != fmt.Sprintf("true %d", windows.SW_HIDE) {
		t.Fatalf("inside a job: %q", got)
	}
}

// SPEC S2: on a volume with short names, a binary path with a space becomes its 8.3 form. This laptop's volumes have
// none, so the system call is stood in for.
func TestShellPathShortForm(t *testing.T) {
	real := getShortPathName
	defer func() { getShortPathName = real }()
	short := `C:\Users\JANEDO~1\trace.exe`
	getShortPathName = func(_ *uint16, buf *uint16, n uint32) (uint32, error) {
		if buf == nil && n != 0 {
			t.Errorf("GetShortPathName got no buffer with a size of %d: the size is the buffer's, so 0", n)
		}
		s, _ := windows.UTF16FromString(short)
		if n < uint32(len(s)) {
			return uint32(len(s)), nil // the size it needs, with the closing zero
		}
		copy(unsafe.Slice(buf, n), s)
		return uint32(len(s) - 1), nil
	}
	long := `C:\Users\Jane Doe\trace.exe`
	if got := shellPath(long); got != short {
		t.Fatalf("shellPath(%q) = %q, want the short form %q", long, got, short)
	}
	getShortPathName = func(*uint16, *uint16, uint32) (uint32, error) { return 0, windows.ERROR_FILE_NOT_FOUND }
	if got := shellPath(long); got != `"`+long+`"` {
		t.Fatalf("with no short form: %q, want the quoted path", got)
	}
}

// A check that runs past its limit is stopped even where taskkill cannot run: then cmd itself is killed.
func TestCheckStoppedWithoutTaskkill(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := shellCommand(ctx, "set /p x=")
	cmd.Stdin = r // waits for a line that never comes, with no child process of its own
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // taskkill is not found
	time.Sleep(300 * time.Millisecond)
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "exit status 0") {
			t.Fatalf("a stopped check ended as if it passed: %v", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("the check still ran 10 s after its limit")
	}
}
