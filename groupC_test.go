package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A copy of this test binary that startImport or startAlarm starts (os.Executable) writes the arguments it got to the
// file GROUPC_SPAWNED names and exits, so a test sees what the detached import or alarm would run without running one.
func init() {
	if out := os.Getenv("GROUPC_SPAWNED"); out != "" && len(os.Args) > 1 && (os.Args[1] == "import" || os.Args[1] == "alarm") {
		os.WriteFile(out, []byte(strings.Join(os.Args[1:], "\n")), 0o644)
		os.Exit(0)
	}
}

// SPEC G5: a hook entry with -repo, which main accepts and ignores, is read by its -home like any other: install
// replaces this home's entry instead of refusing it as unreadable.
func TestHookHomeAcceptsRepo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{"Stop": {imHook("hook", "-repo", "/src", "-home", "/mine")}})
	var out strings.Builder
	if err := install(path, "/x/trace", "/mine", nil, false, &out); err != nil {
		t.Fatalf("install over its own hook with -repo: %v", err)
	}
	if got := imRead(t, path); strings.Contains(got, "-repo") || !strings.Contains(got, `"/mine"`) {
		t.Fatalf("the -repo entry was not replaced: %s", got)
	}
}

// install run through a link to the binary writes hooks that run the binary itself, with forward slashes as Claude
// Code's settings use them: the link may be moved or removed later.
func TestInstallWritesTheResolvedBinary(t *testing.T) {
	root := t.TempDir()
	real := mmTraceExe(t, root)
	link := filepath.Join(root, "link", filepath.Base(real))
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(real, link); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		// Without the privilege for links, the binary named in another case stands in: the resolved path is the one
		// with the case on disk.
		link = filepath.Join(root, "BIN", strings.ToUpper(filepath.Base(real)))
	}
	if _, errOut, code := runTrace(t, root, "", map[string]string{"_EXE": link}, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-all-projects"); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, errOut)
	}
	want, _ := filepath.EvalSymlinks(real)
	want = filepath.ToSlash(want)
	got := imRead(t, filepath.Join(root, "claude", "settings.json"))
	if !strings.Contains(got, `"command": "`+want+`"`) {
		t.Fatalf("hooks do not run %s:\n%s", want, got)
	}
}

// The detached import is this binary run as `import`, with the hook's own flags and the trigger after them.
func TestStartImportRunsImport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("GROUPC_SPAWNED", out)
	if err := startImport([]string{"-home", "/h"}, "hook:Stop s1"); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for i := 0; i < 100 && len(b) == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		b, _ = os.ReadFile(out)
	}
	if want := "import\n-home\n/h\n-trigger\nhook:Stop s1"; string(b) != want {
		t.Fatalf("the detached import got %q, want %q", b, want)
	}
}

// An import that cannot start says so instead of reporting success.
func TestStartImportReportsFailure(t *testing.T) {
	if err := startImport([]string{"a\x00b"}, "t"); err == nil {
		t.Fatal("an import that could not start returned no error")
	}
}

// A waiting import retries twice a second, not in a busy loop: with the lock held, a 700 ms limit ends at the
// third try, about a second in.
func TestImportOnceRetriesEveryHalfSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	held, err := tryLock(path + ".lock")
	if err != nil || held == nil {
		t.Fatalf("lock: %v", err)
	}
	defer held.Close()
	start := time.Now()
	if err := importOnce(path, 700*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("gave up after %v: the retries do not wait half a second", d)
	}
}

// A hook that panics fails open: the panic becomes a line on stderr and the hook returns, so it exits 0.
func TestHookPanicFailsOpen(t *testing.T) {
	returned := false
	errOut := smStderr(t, func() {
		failOpen(func() { panic("boom") })
		returned = true
	})
	if !returned || !strings.Contains(errOut, "trace hook: internal error: boom") {
		t.Fatalf("returned %v, stderr %q", returned, errOut)
	}
}

// A search on a store trace cannot read reports the error instead of "not found".
func TestSearchReportsUnreadableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	os.WriteFile(path, nil, 0o644)
	db, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := search(db, "hello", 10, "", "", "", false); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("search on a store without tables: %v", err)
	}
}
