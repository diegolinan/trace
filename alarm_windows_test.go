package main

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// SPEC A1: inside a job that forbids breakaway the alarm is not started at all, where an import still starts inside
// the job: an alarm there would die with its caller. The test process goes into such a job and, as a process cannot
// leave a job, lets every child break away at the end.
func TestStartAloneRefusedInsideJob(t *testing.T) {
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
	if err := startAlone(cmd); err == nil {
		cmd.Wait()
		t.Fatal("started inside a job that forbids breakaway")
	}
	if cmd.Process != nil {
		t.Fatal("a process was left from the refused start")
	}
}

// pidAlive: a process that has exited is not alive even while a handle keeps its process object, and a pid no process
// has is not alive either.
func TestPidAliveExitedButHeld(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	cmd.Wait()
	if pidAlive(cmd.Process.Pid) {
		t.Error("an exited process whose object a handle keeps is alive")
	}
	if pidAlive(1 << 30) {
		t.Error("a pid no process has is alive")
	}
}
