package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// imSettings writes a Claude Code settings file whose hooks are the given event -> hook entries, one group each.
func imSettings(t *testing.T, path string, hooks map[string][]any) {
	t.Helper()
	m := map[string]any{}
	for ev, entries := range hooks {
		m[ev] = []any{map[string]any{"hooks": entries}}
	}
	b, err := json.Marshal(map[string]any{"hooks": m})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func imHook(args ...any) map[string]any {
	return map[string]any{"type": "command", "command": "/x/trace", "args": args}
}

func imRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// install and uninstall say what they did: hooks added or removed, and the backup only when there was a file to keep.
func TestInstallReportsWhatItDid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	var out strings.Builder
	if err := install(path, "/x/trace", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hooks added in "+path+": SessionStart") || strings.Contains(out.String(), "previous file kept") {
		t.Fatalf("first install: %q", out.String())
	}
	out.Reset()
	if err := install(path, "/y/trace", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hooks added in "+path) || !strings.Contains(out.String(), "previous file kept as "+path+".trace-backup") {
		t.Fatalf("install over a file: %q", out.String())
	}
	out.Reset()
	if err := install(path, "", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hooks removed in "+path) || strings.Contains(out.String(), "hooks added") {
		t.Fatalf("uninstall: %q", out.String())
	}
}

// Uninstall of a home that has no hooks in a file holding only another home's says so and stops there.
func TestUninstallOnlyAnotherHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{"Stop": {imHook("hook", "-home", "/other")}})
	before := imRead(t, path)
	var out strings.Builder
	if err := install(path, "", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no TRACE hooks for /mine in "+path+"; those of /other are left as they are") ||
		strings.Contains(out.String(), "already up to date") {
		t.Fatalf("uninstall output: %q", out.String())
	}
	if imRead(t, path) != before {
		t.Fatal("another home's hooks changed")
	}
}

// Uninstall of a file with no TRACE hooks at all finds nothing to change.
func TestUninstallWithoutTraceHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{"Stop": {map[string]any{"type": "command", "command": "notify.sh"}}})
	var out strings.Builder
	if err := install(path, "", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hooks already up to date in "+path) {
		t.Fatalf("uninstall output: %q", out.String())
	}
}

// Uninstall removes this home's hooks (and, with -migrate, those naming no home) and keeps another home's hooks and
// trace entries it cannot read, even when they sit next to its own.
func TestUninstallKeepsOthersBesideOwn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{
		"Stop":       {imHook("hook", "-home", "/mine"), imHook("hook", "-home", "/other")},
		"SessionEnd": {imHook("hook", "-bogus")},
		"PreCompact": {imHook("hook")},
	})
	var out strings.Builder
	if err := install(path, "", "/mine", nil, true, &out); err != nil {
		t.Fatal(err)
	}
	got := imRead(t, path)
	if strings.Contains(got, `"/mine"`) || strings.Contains(got, "PreCompact") || !strings.Contains(got, `"/other"`) || !strings.Contains(got, "-bogus") {
		t.Fatalf("after uninstall -migrate: %s\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "hooks removed in "+path) {
		t.Fatalf("uninstall -migrate output: %q", out.String())
	}

	// Without -migrate and without hooks naming no home: own goes, the other stays.
	imSettings(t, path, map[string][]any{"Stop": {imHook("hook", "-home", "/mine"), imHook("hook", "-home", "/other")}})
	out.Reset()
	if err := install(path, "", "/mine", nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if got := imRead(t, path); strings.Contains(got, `"/mine"`) || !strings.Contains(got, `"/other"`) {
		t.Fatalf("after uninstall: %s\n%s", got, out.String())
	}
}

// A hook list entry that is not an object is someone else's: install keeps it and adds its own beside it.
func TestInstallKeepsNonObjectHookEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{"Stop": {"echo hi"}})
	if err := install(path, "/x/trace", "/mine", nil, false, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	got := imRead(t, path)
	if !strings.Contains(got, `"echo hi"`) || !strings.Contains(got, `"/mine"`) {
		t.Fatalf("after install: %s", got)
	}
}

// A trace hook whose arguments trace cannot read (a number, a stray word) is another memory's: install refuses it
// as unreadable rather than taking it over or asking for -migrate.
func TestInstallRefusesUnreadableHookArgs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, h := range []map[string]any{imHook("hook", 5), imHook("hook", "-home", "/mine", "stray")} {
		imSettings(t, path, map[string][]any{"Stop": {h}})
		before := imRead(t, path)
		err := install(path, "/x/trace", "/mine", nil, false, &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "an unreadable trace entry") {
			t.Fatalf("install over %v: %v", h["args"], err)
		}
		if imRead(t, path) != before {
			t.Fatalf("file changed over %v", h["args"])
		}
	}
}

// Homes compare as paths: on Windows a hook naming this home in another case is this home's, elsewhere it is not.
func TestInstallHomeCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	imSettings(t, path, map[string][]any{"Stop": {imHook("hook", "-home", "C:/Mine/Home")}})
	err := install(path, "/x/trace", "c:/mine/home", nil, false, &strings.Builder{})
	if runtime.GOOS == "windows" {
		if got := imRead(t, path); err != nil || strings.Contains(got, "C:/Mine/Home") || !strings.Contains(got, "c:/mine/home") {
			t.Fatalf("same home in another case (%v): %s", err, got)
		}
	} else if err == nil || !strings.Contains(err.Error(), "C:/Mine/Home") {
		t.Fatalf("another home by case: %v", err)
	}
}

// imRollouts writes one Codex rollout per cwd ("" for a session_meta without a cwd) and returns the Codex home.
func imRollouts(t *testing.T, cwds ...string) string {
	t.Helper()
	codex := t.TempDir()
	day := filepath.Join(codex, "sessions", "2026", "11", "10")
	os.MkdirAll(day, 0o755)
	for i, c := range cwds {
		payload := map[string]any{"id": "t" + itoa(i)}
		if c != "" {
			payload["cwd"] = c
		}
		b, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
		if err := os.WriteFile(filepath.Join(day, "rollout-"+itoa(i)+".jsonl"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return codex
}

// imFolderLines is the folder list a Codex install prints when no rollout ran in its folder.
func imFolderLines(out string) []string {
	_, list, found := strings.Cut(out, "add the right ones to \"scope\" in")
	if !found {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(list, "\n")[1:] {
		if !strings.HasPrefix(l, "  ") {
			break
		}
		lines = append(lines, strings.TrimSpace(l))
	}
	return lines
}

// The rollout count leaves out sessions without a folder, and the folder list appears only when none ran here.
func TestCodexInstallRolloutCount(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if err := installCodex(t.TempDir(), dir, imRollouts(t, dir, ""), "agents", "", "", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 of 1 Codex rollouts ran in "+dir) || strings.Contains(out.String(), "folders the rollouts ran in") {
		t.Fatalf("one rollout here, one without a folder: %q", out.String())
	}
	out.Reset()
	if err := installCodex(t.TempDir(), t.TempDir(), imRollouts(t), "agents", "", "", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 of 0 Codex rollouts") || strings.Contains(out.String(), "folders the rollouts ran in") {
		t.Fatalf("no rollouts: %q", out.String())
	}
	out.Reset()
	if err := installCodex(t.TempDir(), t.TempDir(), imRollouts(t, "/elsewhere/one"), "agents", "", "", false, &out); err != nil {
		t.Fatal(err)
	}
	if got := imFolderLines(out.String()); len(got) != 1 || got[0] != "1  /elsewhere/one" {
		t.Fatalf("one rollout elsewhere: %q from %q", got, out.String())
	}
}

// The folder list runs from the most rollouts to the fewest, ties by name. Several installs, since the order the
// folders are counted in changes from run to run.
func TestCodexInstallFolderOrder(t *testing.T) {
	cwds := []string{"/w/b", "/w/b", "/w/b", "/w/a", "/w/a"}
	ties := []string{"/w/t3", "/w/t6", "/w/t1", "/w/t5", "/w/t2", "/w/t4", "/w/t7"}
	codex := imRollouts(t, append(cwds, ties...)...)
	want := []string{"3  /w/b", "2  /w/a", "1  /w/t1", "1  /w/t2", "1  /w/t3", "1  /w/t4", "1  /w/t5", "1  /w/t6", "1  /w/t7"}
	for i := 0; i < 5; i++ {
		var out strings.Builder
		if err := installCodex(t.TempDir(), t.TempDir(), codex, "agents", "", "", false, &out); err != nil {
			t.Fatal(err)
		}
		if got := imFolderLines(out.String()); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("folder list:\n%q\nwant\n%q", got, want)
		}
	}
}

// An existing config.json that reads Claude Code is not a Codex memory, even with this folder in scope.
func TestCodexInstallRefusesClaudeConfig(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	b, _ := json.Marshal(map[string]any{"claude": true, "scope": []string{dir}})
	os.WriteFile(filepath.Join(home, "config.json"), b, 0o644)
	err := installCodex(home, dir, t.TempDir(), "agents", "", "", false, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "is not a Codex memory for this folder") || exists(filepath.Join(dir, "AGENTS.md")) {
		t.Fatalf("a Claude Code config was taken: %v", err)
	}
}

// Hooks mode without this binary's path refuses before writing anything.
func TestCodexHooksModeNeedsBinary(t *testing.T) {
	home, codex := t.TempDir(), t.TempDir()
	err := installCodex(home, t.TempDir(), codex, "hooks", "", "", false, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "hooks mode needs") {
		t.Fatalf("hooks mode without a binary: %v", err)
	}
	if exists(filepath.Join(home, "config.json")) || exists(filepath.Join(codex, "hooks.json")) {
		t.Fatal("a refused install wrote something")
	}
}

// Hooks mode says what it wrote the first time, and that nothing was needed the second.
func TestCodexHooksInstallReports(t *testing.T) {
	home, dir, codex := t.TempDir(), t.TempDir(), t.TempDir()
	cfgPath, hooksPath := filepath.Join(home, "config.json"), filepath.Join(codex, "hooks.json")
	var out strings.Builder
	if err := installCodex(home, dir, codex, "hooks", "/bin/trace", "", false, &out); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, "wrote "+cfgPath+"\n") || !strings.Contains(o, "wrote TRACE's hooks (") || !strings.Contains(o, ") in "+hooksPath) ||
		strings.Contains(o, "already runs TRACE's hooks") || !strings.Contains(o, "registered; Codex runs them only after you review") {
		t.Fatalf("first install: %q", o)
	}
	out.Reset()
	if err := installCodex(home, dir, codex, "hooks", "/bin/trace", "", false, &out); err != nil {
		t.Fatal(err)
	}
	o = out.String()
	if !strings.Contains(o, "kept "+cfgPath) || !strings.Contains(o, hooksPath+" already runs TRACE's hooks for this home") ||
		strings.Contains(o, "wrote TRACE's hooks") || !strings.Contains(o, "registered; Codex runs them only after you review") {
		t.Fatalf("second install: %q", o)
	}
}

// Uninstall takes out every block of this home and says the memory itself is kept.
func TestCodexUninstallEveryBlock(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	agents := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(agents, []byte("# Rules\n"+agentsBlock(home)+"middle\n"+agentsBlock(home)), 0o644)
	var out strings.Builder
	if err := uninstallCodex(home, dir, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if got := imRead(t, agents); got != "# Rules\nmiddle\n" {
		t.Fatalf("AGENTS.md after uninstall: %q", got)
	}
	if !strings.Contains(out.String(), "removed the trace resume block from "+agents) || !strings.Contains(out.String(), "the memory in "+home+" is kept") {
		t.Fatalf("uninstall output: %q", out.String())
	}
}

// Install over another home's block names that home.
func TestCodexInstallNamesOtherHome(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agentsBlock("/other")), 0o644)
	err := installCodex(t.TempDir(), dir, t.TempDir(), "agents", "", "", false, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "another TRACE home (/other);") {
		t.Fatalf("another home's block: %v", err)
	}
}

// When AGENTS.md cannot be written (its folder is gone), the config this run wrote is removed again, and it says so.
func TestCodexInstallUndoesConfig(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "config.json")
	gone := filepath.Join(t.TempDir(), "gone")
	var out strings.Builder
	if err := installCodex(home, gone, t.TempDir(), "agents", "", "", false, &out); err == nil {
		t.Fatal("AGENTS.md in a missing folder was written")
	}
	if exists(cfgPath) || !strings.Contains(out.String(), "removed "+cfgPath+" again") {
		t.Fatalf("config not undone: %v %q", exists(cfgPath), out.String())
	}
}
