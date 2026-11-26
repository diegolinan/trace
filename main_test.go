package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runTrace runs trace's main in a child process with a throwaway profile: every home it could touch (user, TRACE,
// Claude Code, Codex) points under root, so no test reaches the real ones.
func runTrace(t *testing.T, root, stdin string, env map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	exe := os.Args[0]
	if e, ok := env["_EXE"]; ok {
		exe = e
	}
	cmd := exec.Command(exe, "-test.run=^$")
	base := map[string]string{
		"USERPROFILE": filepath.Join(root, "user"), "HOME": filepath.Join(root, "user"),
		"TRACE_HOME": filepath.Join(root, "trace"), "CLAUDE_CONFIG_DIR": filepath.Join(root, "claude"),
		"CODEX_HOME": filepath.Join(root, "codex"),
	}
	for k, v := range env {
		base[k] = v
	}
	cmd.Env = append(os.Environ(), "TRACE_TEST_MAIN="+strings.Join(args, "\x1f"))
	for k, v := range base {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Dir = root
	if d, ok := env["_DIR"]; ok {
		cmd.Dir = d
	}
	// A shell sets PWD to the folder it starts in, and os.Getwd keeps that unresolved path. Without it, macOS
	// resolves the temp folder's /var link to /private/var and the folder no longer matches the stored cwd.
	cmd.Env = append(cmd.Env, "PWD="+cmd.Dir)
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// install and uninstall write nothing until -host names a known host, and uninstall -host codex never reaches
// Claude Code's settings.
func TestInstallNeedsHost(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "claude", "settings.json")
	for _, args := range [][]string{{"install"}, {"install", "-host", "coedx"}, {"uninstall"}, {"install", "-host="}} {
		if _, errOut, code := runTrace(t, root, "", nil, args...); code == 0 {
			t.Fatalf("%v: exit 0, want an error (%s)", args, errOut)
		}
		if exists(settings) || exists(filepath.Join(root, "trace")) {
			t.Fatalf("%v wrote something", args)
		}
	}
	os.MkdirAll(filepath.Dir(settings), 0o755)
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"C:/x/trace.exe","args":["hook"]}]}]}}`
	os.WriteFile(settings, []byte(legacy), 0o644)
	if _, errOut, code := runTrace(t, root, "", nil, "uninstall", "-host", "codex", "-migrate"); code != 0 {
		t.Fatalf("uninstall -host codex: exit %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(settings); string(b) != legacy {
		t.Fatalf("uninstall -host codex changed Claude Code's settings: %s", b)
	}
}

// A relative TRACE_HOME is stored absolute, so a hook started from another folder opens the same memory.
func TestRelativeHomeStoredAbsolute(t *testing.T) {
	// The child resolves its working folder (on macOS the temp dir /var is a link to /private/var), so compare there.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bin", "trace")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.MkdirAll(filepath.Dir(exe), 0o755)
	if b, err := os.ReadFile(os.Args[0]); err != nil || os.WriteFile(exe, b, 0o755) != nil {
		t.Fatal("cannot copy the test binary")
	}
	env := map[string]string{"_EXE": exe, "TRACE_HOME": "relative memory"}
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-all-projects"); code != 0 {
		t.Fatalf("install: exit %d: %s", code, errOut)
	}
	var m map[string]any
	b, _ := os.ReadFile(filepath.Join(root, "claude", "settings.json"))
	json.Unmarshal(b, &m)
	own, _, _ := traceHooks(m, filepath.Join(root, "relative memory"))
	if own == 0 {
		t.Fatalf("hooks do not name the absolute home: %s", b)
	}
}

// -home wins over TRACE_HOME, is read before any config.json (so a broken default config does not matter), and a
// "-home" after the search words is refused before any home opens (SPEC E6).
func TestHomeFlag(t *testing.T) {
	root := t.TempDir()
	bad, good := filepath.Join(root, "trace"), filepath.Join(root, "good dir")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "config.json"), []byte(`{broken`), 0o644)
	if _, errOut, code := runTrace(t, root, "", nil, "import", "-home", good, "-projects=", "-history=", "-codex="); code != 0 {
		t.Fatalf("import -home: exit %d: %s", code, errOut)
	}
	if !exists(filepath.Join(good, "trace.db")) || exists(filepath.Join(bad, "trace.db")) {
		t.Fatal("import -home did not use the given home only")
	}
	other, fresh := filepath.Join(root, "other"), filepath.Join(root, "fresh")
	if _, errOut, code := runTrace(t, root, "", map[string]string{"TRACE_HOME": fresh}, "search", "hello", "-home", other); code != 2 {
		t.Fatalf("search with -home after the words: exit %d, want 2 (SPEC E6): %s", code, errOut)
	}
	if exists(other) || exists(fresh) {
		t.Fatal(`"-home" after the search words opened a home`)
	}
	if _, _, code := runTrace(t, root, "", nil, "stats", "-home", good); code != 0 {
		t.Fatal("stats -home: broken default config still read")
	}
}

// A hook never fails the host's turn on a bad flag or a bad home: exit 0, nothing on stdout.
func TestHookFailsOpen(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"hook", "-bogus"}, {"hook", "-home="}, {"hook", "-host", "coedx"}} {
		out, _, code := runTrace(t, root, `{"hook_event_name":"Stop"}`, nil, args...)
		if code != 0 || out != "" {
			t.Fatalf("%v: exit %d, stdout %q", args, code, out)
		}
	}
}

// install -host claude-code binds the hooks to the chosen home, and uninstall removes them.
func TestInstallClaudeHome(t *testing.T) {
	root := t.TempDir()
	// Installed hooks are recognised by the binary's name, so this test runs a copy named trace.
	exe := filepath.Join(root, "bin", "trace")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.MkdirAll(filepath.Dir(exe), 0o755)
	if b, err := os.ReadFile(os.Args[0]); err != nil || os.WriteFile(exe, b, 0o755) != nil {
		t.Fatal("cannot copy the test binary")
	}
	env := map[string]string{"_EXE": exe}
	home := filepath.Join(root, "my memory")
	settings := filepath.Join(root, "claude", "settings.json")
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-home", home, "-codex=", "-user", "Ana", "-all-projects"); code != 0 {
		t.Fatalf("install: exit %d: %s", code, errOut)
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), `"-home"`) || !strings.Contains(string(b), filepath.Base(home)) {
		t.Fatalf("hooks do not name the home: %s", b)
	}
	if _, errOut, code := runTrace(t, root, "", env, "uninstall", "-host", "claude-code", "-home", home); code != 0 {
		t.Fatalf("uninstall: exit %d: %s", code, errOut)
	}
	if b, _ := os.ReadFile(settings); strings.Contains(string(b), "-home") {
		t.Fatalf("uninstall left hooks: %s", b)
	}
}
