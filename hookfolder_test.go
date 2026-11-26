package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hfSetup is a throwaway profile whose TRACE home is in scope "proj" only.
func hfSetup(t *testing.T) (root, home string) {
	t.Helper()
	root = t.TempDir()
	home = filepath.Join(root, "trace")
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"scope":"proj"}`), 0o644)
	return root, home
}

// hfOwn is how many hooks of home the settings file at path runs.
func hfOwn(t *testing.T, path, home string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	own, _, _ := traceHooks(m, home)
	return own
}

// SPEC H1: -folder installs into that folder's settings.local.json, never the user settings, and only for an existing
// absolute folder in scope; uninstall -folder removes them there.
func TestInstallFolder(t *testing.T) {
	root, home := hfSetup(t)
	env := map[string]string{"_EXE": mmTraceExe(t, root)} // hooks of a binary not named trace are not TRACE's
	dir := filepath.Join(root, "proj")
	os.MkdirAll(dir, 0o755)
	user := filepath.Join(root, "claude", "settings.json")
	local := filepath.Join(dir, ".claude", "settings.local.json")

	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-folder", dir); code != 0 {
		t.Fatalf("install -folder: exit %d\n%s", code, errOut)
	}
	if hfOwn(t, local, home) == 0 {
		t.Fatalf("no hooks of %s in %s", home, local)
	}
	if exists(user) {
		t.Fatalf("install -folder wrote the user settings")
	}

	other := filepath.Join(root, "elsewhere")
	os.MkdirAll(other, 0o755)
	file := filepath.Join(root, "proj-file")
	os.WriteFile(file, nil, 0o644)
	for bad, why := range map[string]string{"proj": "existing absolute folder", filepath.Join(root, "proj-missing"): "existing absolute folder",
		file: "existing absolute folder", other: "outside this home's scope"} {
		if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-folder", bad); code == 0 || !strings.Contains(errOut, why) {
			t.Errorf("-folder %s: exit %d, want a refusal that says %q: %s", bad, code, why, errOut)
		}
	}
	if exists(filepath.Join(other, ".claude")) || exists(filepath.Join(root, "proj-missing")) || exists(user) {
		t.Fatalf("a refused -folder wrote something")
	}

	if _, errOut, code := runTrace(t, root, "", env, "uninstall", "-host", "claude-code", "-folder", dir); code != 0 {
		t.Fatalf("uninstall -folder: exit %d\n%s", code, errOut)
	}
	if n := hfOwn(t, local, home); n != 0 {
		t.Fatalf("uninstall -folder left %d hooks", n)
	}
}

// SPEC H2: stats lists the folders in scope where main sessions started lately with no hooks of this home there or in
// the user settings.
func TestStatsFoldersWithoutHooks(t *testing.T) {
	root, home := hfSetup(t)
	env := map[string]string{"_EXE": mmTraceExe(t, root)} // hooks of a binary not named trace are not TRACE's
	f := func(name string) string { d := filepath.Join(root, name); os.MkdirAll(d, 0o755); return d }
	hooked, bare, outside, old, sub, scratch := f("proj-hooked"), f("proj-bare"), f("elsewhere"), f("proj-old"), f("proj-sub"), f("proj-scratch")
	// A settings file there that runs no hooks of this home, and one that runs a single one.
	unrelated, one := f("proj-unrelated"), f("proj-one")
	os.MkdirAll(filepath.Join(unrelated, ".claude"), 0o755)
	os.WriteFile(filepath.Join(unrelated, ".claude", "settings.json"), []byte(`{"theme":"dark"}`), 0o644)
	os.MkdirAll(filepath.Join(one, ".claude"), 0o755)
	single, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": env["_EXE"], "args": []any{"hook", "-home", home}}}}}}})
	os.WriteFile(filepath.Join(one, ".claude", "settings.json"), single, 0o644)
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-folder", hooked); code != 0 {
		t.Fatalf("install -folder: exit %d\n%s", code, errOut)
	}
	db, err := openDB(filepath.Join(home, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, s := range []struct{ id, role, parent, cwd, started, excluded string }{
		{"s1", "main", "", hooked, at(time.Hour), ""},
		{"s2", "main", "", bare, at(2 * time.Hour), ""},
		{"s3", "main", "", bare, at(time.Hour), ""},
		{"s4", "main", "", outside, at(time.Hour), ""},
		{"s5", "main", "", old, at(40 * 24 * time.Hour), ""},
		{"s6", "subagent", "s1", sub, at(time.Hour), ""},
		{"s7", "main", "", scratch, at(time.Hour), "scratch"},
		{"s8", "main", "", unrelated, at(time.Hour), ""},
		{"s9", "main", "", one, at(time.Hour), ""},
	} {
		if _, err := db.Exec(`insert into session(id, host, role, parent_id, cwd, started_at, excluded) values (?, 'claude-code', ?, nullif(?, ''), ?, ?, nullif(?, ''))`,
			s.id, s.role, s.parent, s.cwd, s.started, s.excluded); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	out, errOut, code := runTrace(t, root, "", nil, "stats")
	if code != 0 {
		t.Fatalf("stats: exit %d\n%s", code, errOut)
	}
	_, section, ok := strings.Cut(out, "-- folders where Claude Code sessions started in the last 30 days without this home's hooks")
	if !ok {
		t.Fatalf("no such section:\n%s", out)
	}
	section, _, _ = strings.Cut(section, "\n-- ")
	if !strings.Contains(section, bare+"  2  ") || !strings.Contains(section, unrelated+"  1  ") {
		t.Errorf("want %s with 2 sessions and %s with 1:\n%s", bare, unrelated, section)
	}
	for _, d := range []string{hooked, outside, old, sub, scratch, one} {
		if strings.Contains(section, d+"  ") {
			t.Errorf("%s listed:\n%s", d, section)
		}
	}

	// Hooks of this home in the user settings cover every folder.
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana"); code != 0 {
		t.Fatalf("install: exit %d\n%s", code, errOut)
	}
	out, _, _ = runTrace(t, root, "", nil, "stats")
	_, section, _ = strings.Cut(out, "without this home's hooks")
	section, _, _ = strings.Cut(section, "\n-- ")
	if strings.Contains(section, bare) {
		t.Errorf("user hooks installed, still listed:\n%s", section)
	}
}
