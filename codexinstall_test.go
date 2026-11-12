package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInScope(t *testing.T) {
	for _, c := range []struct {
		folder, scope string
		want          bool
	}{
		{`D:\Work\acme`, "", true},
		{`D:\Work\acme`, `D:\Work\acme`, true},                           // a full path
		{"D--Projects-BUSQUEDAS-LABORALES", "busquedas-laborales", true}, // Claude folder name, either case
		{`D:\Projects\diegolinan.me`, "diegolinan.me", true},             // normalized like the folder
		{`D:\Work\other`, `D:\Work\acme, D:\Work\other`, true},           // a list
		{`D:\Work\third`, `D:\Work\acme,D:\Work\other`, false},
		{`D:\Work\third`, " , ", false}, // only separators: nothing, never everything
	} {
		if got := inScope(c.folder, c.scope); got != c.want {
			t.Errorf("inScope(%q, %q) = %v", c.folder, c.scope, got)
		}
	}
	var cfgs [2]config
	json.Unmarshal([]byte(`{"scope":"a"}`), &cfgs[0])
	json.Unmarshal([]byte(`{"scope":["a","b"]}`), &cfgs[1])
	if cfgs[0].Scope != "a" || cfgs[1].Scope != "a,b" {
		t.Fatalf("scope string and list: %q %q", cfgs[0].Scope, cfgs[1].Scope)
	}
	if json.Unmarshal([]byte(`{"scope":3}`), &cfgs[0]) == nil {
		t.Fatal("a number scope should be an error")
	}
}

func TestInstallCodex(t *testing.T) {
	home, dir, codex := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TRACE_HOME", "")
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	meta := func(cwd string) string {
		b, _ := json.Marshal(cwd)
		return `{"type":"session_meta","payload":{"id":"t","cwd":` + string(b) + `}}` + "\n"
	}
	os.WriteFile(filepath.Join(day, "rollout-a.jsonl"), []byte(meta(dir)), 0o644)
	os.WriteFile(filepath.Join(day, "rollout-b.jsonl"), []byte(meta(`D:\elsewhere`)), 0o644)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# Rules"), 0o644)

	var out strings.Builder
	if err := installCodex(home, dir, codex, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 of 2 Codex rollouts") {
		t.Fatalf("count: %s", out.String())
	}
	c, err := loadConfig(home)
	if err != nil || c.Claude == nil || *c.Claude || string(c.Scope) != dir {
		t.Fatalf("config: %+v %v", c, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(string(b), "# Rules\nAt the start of a session, run `trace resume`") {
		t.Fatalf("AGENTS.md: %q", b)
	}
	// Never overwrites a config, so a Claude Code memory cannot be turned into a Codex one by accident.
	if installCodex(home, dir, codex, io.Discard) == nil {
		t.Fatal("a second install should refuse the existing config")
	}
	// A run elsewhere lists the folders the rollouts ran in, and the line is not added twice.
	out.Reset()
	if err := installCodex(t.TempDir(), t.TempDir(), codex, &out); err != nil || !strings.Contains(out.String(), `D:\elsewhere`) {
		t.Fatalf("folder list: %v %s", err, out.String())
	}
	installCodex(t.TempDir(), dir, codex, io.Discard)
	if b2, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(b2) != string(b) {
		t.Fatalf("line added twice: %q", b2)
	}
}
