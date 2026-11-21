package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install adds one hook per event, keeps everything else, is idempotent, follows a moved binary, and uninstall
// removes only its own hooks.
func TestInstallUninstall(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"notify.sh"}]}]}}`), 0o644)
	read := func() map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(path)
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	countTrace := func(m map[string]any) (n int) {
		for _, groups := range m["hooks"].(map[string]any) {
			for _, g := range groups.([]any) {
				for _, h := range g.(map[string]any)["hooks"].([]any) {
					if isTraceCmd(h) {
						n++
					}
				}
			}
		}
		return n
	}

	checksOnly := map[string]string{"PostToolUse": "Bash"}
	if err := install(path, "/opt/a/trace", home, checksOnly, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	m := read()
	if countTrace(m) != len(hookEvents)-2 || m["model"] != "opus" || !strings.Contains(mustJSON(m), "notify.sh") {
		t.Fatalf("after install: %s", mustJSON(m))
	}
	if _, err := os.Stat(path + ".trace-backup"); err != nil {
		t.Fatal("no backup of the previous file")
	}
	before, _ := os.ReadFile(path)
	install(path, "/opt/a/trace", home, checksOnly, false, io.Discard) // again: nothing to change
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("second install changed the file")
	}
	install(path, `C:\b\trace.exe`, home, checksOnly, false, io.Discard) // moved binary: replaced, not added
	if m = read(); countTrace(m) != len(hookEvents)-2 || !strings.Contains(mustJSON(m), `C:\\b\\trace.exe`) || !strings.Contains(mustJSON(m), `"matcher":"Bash"`) {
		t.Fatalf("after move: %s", mustJSON(m))
	}
	install(path, `C:\b\trace.exe`, home, nil, false, io.Discard) // no checks configured: no PostToolUse hook at all
	if m = read(); countTrace(m) != len(hookEvents)-3 || m["hooks"].(map[string]any)["PostToolUse"] != nil {
		t.Fatalf("without checks: %s", mustJSON(m))
	}
	// The rule card: every tool call and every prompt.
	install(path, `C:\b\trace.exe`, home, optionalHooks(config{Reinject: &reinjectCfg{}, Checks: []check{{On: "Bash"}}}), false, io.Discard)
	if m = read(); countTrace(m) != len(hookEvents) || !strings.Contains(mustJSON(m), `"matcher":"*"`) || m["hooks"].(map[string]any)["UserPromptSubmit"] == nil {
		t.Fatalf("with reinject: %s", mustJSON(m))
	}

	if err := install(path, "", home, checksOnly, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	m = read()
	if _, ok := m["hooks"].(map[string]any)["SessionStart"]; ok || !strings.Contains(mustJSON(m), "notify.sh") || m["model"] != "opus" {
		t.Fatalf("after uninstall: %s", mustJSON(m))
	}

	os.WriteFile(path, []byte(`{not json`), 0o644)
	if err := install(path, "/opt/a/trace", home, checksOnly, false, io.Discard); err == nil {
		t.Fatal("a broken settings file must be left alone")
	}
	if b, _ := os.ReadFile(path); string(b) != `{not json` {
		t.Fatal("broken settings file was overwritten")
	}
	fresh := filepath.Join(dir, "new", "settings.json")
	if err := install(fresh, "/opt/a/trace", home, nil, false, io.Discard); err != nil {
		t.Fatalf("missing settings file: %v", err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// One TRACE home per settings file: another home's hooks stop install and survive uninstall byte for byte; hooks
// that name no home need -migrate, which binds them to this home.
func TestInstallOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	other := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/trace","args":["hook","-home","/other"]}]}]}}`
	os.WriteFile(path, []byte(other), 0o644)
	if err := install(path, "/x/trace", "/mine", nil, false, io.Discard); err == nil || !strings.Contains(err.Error(), "/other") {
		t.Fatalf("install over another home: %v", err)
	}
	if err := install(path, "", "/mine", nil, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != other {
		t.Fatalf("another home's hooks changed: %s", b)
	}

	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/trace","args":["hook"]}]}]}}`
	os.WriteFile(path, []byte(legacy), 0o644)
	for _, exe := range []string{"/x/trace", ""} {
		if err := install(path, exe, "/mine", nil, false, io.Discard); err == nil || !strings.Contains(err.Error(), "-migrate") {
			t.Fatalf("legacy hooks without -migrate (exe %q): %v", exe, err)
		}
		if b, _ := os.ReadFile(path); string(b) != legacy {
			t.Fatal("legacy hooks changed without -migrate")
		}
	}
	if err := install(path, "/x/trace", "/mine", nil, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &m)
	own, old, foreign := traceHooks(m, "/mine")
	if own != len(hookEvents)-3 || old != 0 || len(foreign) != 0 {
		t.Fatalf("after -migrate: own %d, legacy %d, foreign %v: %s", own, old, foreign, b)
	}
}

// Ownership reads -home the way the runtime does: the last one wins, -home=x counts, and an entry trace cannot
// read is another memory's, never taken over.
func TestHookHomeParsing(t *testing.T) {
	entry := func(args ...any) any {
		return map[string]any{"type": "command", "command": "/x/trace", "args": args}
	}
	for _, c := range []struct {
		name      string
		h         any
		home      string
		named, ok bool
	}{
		{"legacy", entry("hook"), "", false, true},
		{"one home", entry("hook", "-home", "/m"), "/m", true, true},
		{"last wins", entry("hook", "-home", "/m", "-home", "/o"), "/o", true, true},
		{"equals form", entry("hook", "-home=/o"), "/o", true, true},
		{"double dash flag", entry("hook", "--home", "/o"), "/o", true, true},
		{"after --", entry("hook", "--", "-home", "/o"), "", false, false},
		{"not a hook call", entry("import", "-home", "/m"), "", false, false},
		{"unknown flag", entry("hook", "-bogus", "-home", "/m"), "", false, false},
		{"empty home", entry("hook", "-home="), "", true, false},
		{"no args", map[string]any{"command": "/x/trace"}, "", false, false},
		{"non-string arg", entry("hook", 5.0), "", false, false},
	} {
		home, named, ok := hookHome(c.h)
		if home != c.home || named != c.named || ok != c.ok {
			t.Errorf("%s: home %q named %v ok %v", c.name, home, named, ok)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	for _, s := range []string{
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/trace","args":["hook","-home","/mine","-home","/other"]}]}]}}`,
		`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/trace","args":["hook","-home=/other"]}]}]}}`,
	} {
		os.WriteFile(path, []byte(s), 0o644)
		for _, migrate := range []bool{false, true} {
			if err := install(path, "/x/trace", "/mine", nil, migrate, io.Discard); err == nil {
				t.Errorf("another home's hook taken over (migrate %v): %s", migrate, s)
			}
			if b, _ := os.ReadFile(path); string(b) != s {
				t.Errorf("file changed: %s", b)
			}
		}
	}
}
