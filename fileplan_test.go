package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An uninstall whose AGENTS.md write fails (the file changed after it was planned) puts the hooks it removed back.
func TestWriteBothUndoesHooks(t *testing.T) {
	dir := t.TempDir()
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	hooksPath, agentsPath := filepath.Join(dir, "hooks.json"), filepath.Join(dir, "AGENTS.md")
	os.WriteFile(hooksPath, []byte("with ours"), 0o644)
	os.WriteFile(agentsPath, []byte("with our block"), 0o644)
	hooks := filePlan{path: hooksPath, old: []byte("with ours"), next: []byte("without"), existed: true}
	ag := filePlan{path: agentsPath, old: []byte("with our block"), next: []byte("no block"), existed: true}
	os.WriteFile(agentsPath, []byte("edited meanwhile"), 0o644)
	var w strings.Builder
	if err := writeBoth(hooks, ag, "H", &w); err == nil {
		t.Fatal("the AGENTS.md write over a changed file must fail")
	}
	if read(hooksPath) != "with ours" || read(agentsPath) != "edited meanwhile" || !strings.Contains(w.String(), "put TRACE's hooks back") {
		t.Fatalf("hooks %q, AGENTS.md %q, said %q", read(hooksPath), read(agentsPath), w.String())
	}
	w.Reset()
	os.WriteFile(agentsPath, []byte("with our block"), 0o644)
	os.WriteFile(hooksPath, []byte("with ours"), 0o644)
	if err := writeBoth(hooks, ag, "H", &w); err != nil || read(hooksPath) != "without" || read(agentsPath) != "no block" {
		t.Fatalf("both written: %v %q %q", err, read(hooksPath), read(agentsPath))
	}
}

// An install whose second write fails removes the config it wrote, only while that file is still its own.
func TestUndoCodexConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	var w strings.Builder
	os.WriteFile(cfgPath, []byte("mine"), 0o644)
	undoConfig(filePlan{path: cfgPath, next: []byte("mine")}, &w)
	if exists(cfgPath) || !strings.Contains(w.String(), "removed") {
		t.Fatalf("its own config was not removed: %v %q", exists(cfgPath), w.String())
	}
	w.Reset()
	os.WriteFile(cfgPath, []byte("edited by someone"), 0o644)
	undoConfig(filePlan{path: cfgPath, next: []byte("mine")}, &w)
	if b, _ := os.ReadFile(cfgPath); string(b) != "edited by someone" || !strings.Contains(w.String(), "left") {
		t.Fatalf("a changed config was touched: %q %q", b, w.String())
	}
}

// A failed install undoes its writes: the old content comes back, a file it created goes, and a file someone changed
// since is left alone. writeChecked never overwrites a file that changed under it, and keeps the backup it is given.
func TestFilePlanUndo(t *testing.T) {
	dir := t.TempDir()
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	edited := filepath.Join(dir, "hooks.json")
	os.WriteFile(edited, []byte("old"), 0o644)
	p := filePlan{path: edited, old: []byte("old"), next: []byte("new"), existed: true}
	if err := p.write(); err != nil || read(edited) != "new" {
		t.Fatalf("write: %v %q", err, read(edited))
	}
	if err := p.undo(); err != nil || read(edited) != "old" {
		t.Fatalf("undo of an edit: %v %q", err, read(edited))
	}

	created := filepath.Join(dir, "sub", "new.json")
	c := filePlan{path: created, next: []byte("made")}
	if err := c.write(); err != nil || read(created) != "made" {
		t.Fatalf("write of a new file: %v", err)
	}
	if err := c.undo(); err != nil || exists(created) {
		t.Fatalf("undo of a created file: %v, still there: %v", err, exists(created))
	}

	c.write()
	os.WriteFile(created, []byte("someone else"), 0o644)
	if err := c.undo(); err == nil || read(created) != "someone else" {
		t.Fatalf("undo over another writer's change: %v %q", err, read(created))
	}

	// writeChecked: the file changed since it was read, so nothing is written; with a backup, the old text is kept.
	if err := writeChecked(edited, "stale", true, "x", ""); err == nil || read(edited) != "old" {
		t.Fatalf("a changed file was overwritten: %v %q", err, read(edited))
	}
	if err := writeChecked(created, "", false, "x", ""); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a file that appeared was overwritten: %v", err)
	}
	backup := filepath.Join(dir, "hooks.json.bak")
	if err := writeChecked(edited, "old", true, "next", backup); err != nil || read(edited) != "next" || read(backup) != "old" {
		t.Fatalf("backup: %v %q %q", err, read(edited), read(backup))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}
