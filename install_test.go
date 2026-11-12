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

	if err := install(path, "/opt/a/trace", io.Discard); err != nil {
		t.Fatal(err)
	}
	m := read()
	if countTrace(m) != len(hookEvents) || m["model"] != "opus" || !strings.Contains(mustJSON(m), "notify.sh") {
		t.Fatalf("after install: %s", mustJSON(m))
	}
	if _, err := os.Stat(path + ".trace-backup"); err != nil {
		t.Fatal("no backup of the previous file")
	}
	before, _ := os.ReadFile(path)
	install(path, "/opt/a/trace", io.Discard) // again: nothing to change
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("second install changed the file")
	}
	install(path, `C:\b\trace.exe`, io.Discard) // moved binary: replaced, not added
	if m = read(); countTrace(m) != len(hookEvents) || !strings.Contains(mustJSON(m), `C:\\b\\trace.exe`) {
		t.Fatalf("after move: %s", mustJSON(m))
	}

	if err := install(path, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	m = read()
	if _, ok := m["hooks"].(map[string]any)["SessionStart"]; ok || !strings.Contains(mustJSON(m), "notify.sh") || m["model"] != "opus" {
		t.Fatalf("after uninstall: %s", mustJSON(m))
	}

	os.WriteFile(path, []byte(`{not json`), 0o644)
	if err := install(path, "/opt/a/trace", io.Discard); err == nil {
		t.Fatal("a broken settings file must be left alone")
	}
	if b, _ := os.ReadFile(path); string(b) != `{not json` {
		t.Fatal("broken settings file was overwritten")
	}
	fresh := filepath.Join(dir, "new", "settings.json")
	if err := install(fresh, "/opt/a/trace", io.Discard); err != nil {
		t.Fatalf("missing settings file: %v", err)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
