package main

import "testing"

// SPEC I4: temp and scratch folders are left out the same way for both hosts and every platform.
func TestExcludedTempFolders(t *testing.T) {
	for in, want := range map[string]bool{
		"D--Temp-TestHook123":                         true, // a Claude Code transcript folder name
		folderName(`C:\Users\a\AppData\Local\Temp\x`): true, // a Codex cwd, as importCodex passes it
		folderName("/tmp/go-build1/x"):                true, // Linux
		folderName("/var/folders/ab/T/TestX/001"):     true, // macOS
		folderName("/private/var/folders/ab/T/x"):     true, // macOS, resolved
		"C--work-scratch-pad":                         true,
		"C--work-acme":                                false,
		folderName("/home/u/attempt"):                 false,
		folderName(`D:\Projects\Contemporary`):        false,
	} {
		if got := excludedFor(in) != ""; got != want {
			t.Errorf("excludedFor(%q) = %v, want %v", in, got, want)
		}
	}
}
