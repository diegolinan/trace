package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// icRoot is a throwaway profile: the user's home folder, Claude Code and Codex under root, a TRACE home not created
// yet, and a project folder.
func icRoot(t *testing.T) (root, home, proj string) {
	t.Helper()
	root = t.TempDir()
	for k, v := range map[string]string{"USERPROFILE": "user", "HOME": "user", "CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex"} {
		t.Setenv(k, filepath.Join(root, v))
	}
	home, proj = filepath.Join(root, "trace"), filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(root, "user"), 0o755)
	os.MkdirAll(proj, 0o755)
	return root, home, proj
}

// icRun runs a trace command in this process as a person does: at a console or not, typing typed. It returns what
// the command printed and its error.
func icRun(t *testing.T, console bool, typed string, args ...string) (string, error) {
	t.Helper()
	smKeepCfg(t)
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	oldHome, oldConsole, oldLines, oldOut := activeHome, stdinConsole, stdinLines, os.Stdout
	stdinConsole = func() bool { return console }
	stdinLines = bufio.NewReader(strings.NewReader(typed))
	os.Stdout = f
	func() {
		defer func() { activeHome, stdinConsole, stdinLines, os.Stdout = oldHome, oldConsole, oldLines, oldOut }()
		var o opts
		if o, err = parseArgs(args[0], args[1:]); err == nil {
			err = commands[args[0]](o)
		}
	}()
	f.Close()
	b, _ := os.ReadFile(f.Name())
	return string(b), err
}

// icConfig is home's config.json as a map; nil when there is none.
func icConfig(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("config.json: %v\n%s", err, b)
	}
	return m
}

// icTranscript writes a one-prompt Claude Code transcript of a session that ran in cwd.
func icTranscript(t *testing.T, root, cwd, id string) {
	t.Helper()
	line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u-" + id, "timestamp": "2026-11-20T10:00:00.000Z",
		"sessionId": id, "cwd": cwd, "message": map[string]any{"role": "user", "content": "words of " + id}})
	mmWrite(t, filepath.Join(root, "claude", "projects", notAlnum.ReplaceAllString(cwd, "-"), id+".jsonl"), string(line)+"\n")
}

// icNothingWritten fails when a refused install left the home, the user settings or the project's settings behind.
func icNothingWritten(t *testing.T, root, home, proj, what string) {
	t.Helper()
	for _, p := range []string{home, filepath.Join(root, "claude", "settings.json"), filepath.Join(proj, ".claude")} {
		if exists(p) {
			t.Errorf("%s: %s was written", what, p)
		}
	}
}

const icAgentAsk = "ask the person the name to call them; do not use the OS account, git name or another memory; pass their exact answer with -user"

// SPEC U1: with no name in config.json, install asks it at the terminal and saves the answer as typed; an empty answer
// or the end of input refuses with nothing written.
func TestInstallAsksTheName(t *testing.T) {
	root, home, proj := icRoot(t)
	out, err := icRun(t, true, "  José Ñandú  \r\n", "install", "-host", "claude-code", "-home", home, "-folder", proj)
	if err != nil {
		t.Fatalf("install at a terminal: %v\n%s", err, out)
	}
	if got := icConfig(t, home)["user"]; got != "José Ñandú" {
		t.Fatalf("user saved %q, want the answer trimmed", got)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "config.json")); !strings.Contains(string(b), "José Ñandú") {
		t.Fatalf("config.json does not hold the name as typed:\n%s", b)
	}
	for _, typed := range []string{"", "\n", "   \r\n"} {
		root, home, proj := icRoot(t)
		if out, err := icRun(t, true, typed, "install", "-host", "claude-code", "-home", home, "-folder", proj); err == nil {
			t.Errorf("answer %q: installed\n%s", typed, out)
		}
		icNothingWritten(t, root, home, proj, "answer "+typed)
	}
	_ = root
}

// SPEC U2: no console, or Claude Code's own shell, is no person: install refuses, says who must answer, writes nothing.
func TestInstallNameWithoutTerminal(t *testing.T) {
	for _, c := range []struct {
		name       string
		console    bool
		claudecode string
	}{{"no console", false, ""}, {"Claude Code's shell", true, "1"}} {
		root, home, proj := icRoot(t)
		t.Setenv("CLAUDECODE", c.claudecode)
		out, err := icRun(t, c.console, "Ana\n", "install", "-host", "claude-code", "-home", home, "-folder", proj)
		if err == nil || !strings.Contains(err.Error(), icAgentAsk) || !strings.Contains(err.Error(), `you are the person, run again with -user "Your name"`) {
			t.Errorf("%s: %v\n%s", c.name, err, out)
		}
		icNothingWritten(t, root, home, proj, c.name)
	}
}

// SPEC U2: NUL or /dev/null, a file and a pipe are not a console, though Go calls NUL a character device.
func TestConsoleCheck(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	file, _ := os.CreateTemp(t.TempDir(), "f")
	defer file.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	for name, f := range map[string]*os.File{os.DevNull: null, "a file": file, "a pipe": r} {
		if consoleIn(f) {
			t.Errorf("%s taken for a console", name)
		}
	}
}

// SPEC U3: a name is one line of at most 60 characters; ñ, accents, quotes and < & > are saved as typed.
func TestInstallNameValidated(t *testing.T) {
	for _, bad := range []string{"   ", "Ana\nBob", "Ana\tB", "Ana\x00", "Ana\xff", strings.Repeat("ñ", 61)} {
		root, home, proj := icRoot(t)
		if out, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-folder", proj, "-user", bad); err == nil {
			t.Errorf("-user %q: installed\n%s", bad, out)
		}
		icNothingWritten(t, root, home, proj, "-user "+bad)
	}
	for _, good := range []string{strings.Repeat("ñ", 60), `Ana "la" Ñ & <Bo>`} {
		_, home, proj := icRoot(t)
		if out, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-folder", proj, "-user", " "+good+" "); err != nil {
			t.Fatalf("-user %q: %v\n%s", good, err, out)
		}
		b, _ := os.ReadFile(filepath.Join(home, "config.json"))
		if icConfig(t, home)["user"] != good || strings.Contains(string(b), `\u00`) {
			t.Errorf("-user %q saved as:\n%s", good, b)
		}
	}
}

// SPEC U4: -user is taken with no question; a stored name is kept, and a new -user replaces it and says so.
func TestInstallKeepsOrChangesTheName(t *testing.T) {
	_, home, proj := icRoot(t)
	if out, err := icRun(t, true, "", "install", "-host", "claude-code", "-home", home, "-folder", proj, "-user", "Ana"); err != nil {
		t.Fatalf("-user at a terminal with nothing typed: %v\n%s", err, out)
	}
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	home, proj = filepath.Join(root, "trace"), filepath.Join(root, "proj")
	os.MkdirAll(proj, 0o755)
	mmWrite(t, filepath.Join(home, "config.json"), `{"scope": "proj", "user": "Ana"}`)
	install := func(extra ...string) string {
		t.Helper()
		out, errOut, code := runTrace(t, root, "", env, append([]string{"install", "-host", "claude-code", "-codex=", "-folder", proj}, extra...)...)
		if code != 0 {
			t.Fatalf("install %v: exit %d\n%s\n%s", extra, code, out, errOut)
		}
		return out
	}
	if out := install(); !strings.Contains(out, "Ana") || strings.Contains(out, "name changed") || icConfig(t, home)["user"] != "Ana" {
		t.Fatalf("reinstall without -user:\n%s", out)
	}
	if out := install("-user", "Beto"); !strings.Contains(out, "name changed from Ana to Beto") || icConfig(t, home)["user"] != "Beto" {
		t.Fatalf("reinstall with -user Beto:\n%s", out)
	}
	if out := install(); !strings.Contains(out, "Beto") || strings.Contains(out, "name changed") || icConfig(t, home)["user"] != "Beto" {
		t.Fatalf("reinstall after the change:\n%s", out)
	}
}

// SPEC U5: a memory left with no scope stores every conversation on the machine only with the person's yes, or
// -all-projects; a reinstall of a memory the user settings already run asks nothing.
func TestInstallEveryProjectAsks(t *testing.T) {
	setup := func() (root, home, proj string) {
		root, home, proj = icRoot(t)
		icTranscript(t, root, proj, "s1")
		icTranscript(t, root, filepath.Join(root, "elsewhere"), "s2")
		mmWrite(t, filepath.Join(root, "codex", "sessions", "rollout-x.jsonl"), "{}\n")
		mmWrite(t, filepath.Join(root, "claude", "projects", notAlnum.ReplaceAllString(proj, "-"), "s1", "tool-results", "r.txt"), strings.Repeat("x", 1_500_000))
		return root, home, proj
	}
	root, home, proj := setup()
	_, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-user", "Ana")
	if err == nil || !strings.Contains(err.Error(), "ask the person") || !strings.Contains(err.Error(), "-all-projects") {
		t.Errorf("no terminal: %v", err)
	}
	icNothingWritten(t, root, home, proj, "no terminal")
	for _, no := range []string{"no\n", "", "n\n"} {
		root, home, proj := setup()
		out, err := icRun(t, true, no, "install", "-host", "claude-code", "-home", home, "-user", "Ana")
		if err == nil || !strings.Contains(out, "every conversation on this machine, 3 transcripts and rollouts now (1.5 MB)") || !strings.Contains(out, "-folder") {
			t.Errorf("answer %q: %v\n%s", no, err, out)
		}
		icNothingWritten(t, root, home, proj, "answer "+no)
	}
	for _, c := range []struct {
		console bool
		typed   string
		flags   []string
	}{{true, "yes\n", nil}, {true, "Y\n", nil}, {false, "", []string{"-all-projects"}}} {
		root, home, _ := setup()
		args := append([]string{"install", "-host", "claude-code", "-home", home, "-user", "Ana"}, c.flags...)
		if out, err := icRun(t, c.console, c.typed, args...); err != nil || !exists(filepath.Join(root, "claude", "settings.json")) {
			t.Errorf("%q %v: %v\n%s", c.typed, c.flags, err, out)
		}
		if _, has := icConfig(t, home)["scope"]; has {
			t.Errorf("%q %v: a scope was saved", c.typed, c.flags)
		}
	}

	root = t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-all-projects"); code != 0 {
		t.Fatalf("install -all-projects: exit %d\n%s", code, errOut)
	}
	if out, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex="); code != 0 || strings.Contains(out, "every conversation") {
		t.Fatalf("reinstall asked again: exit %d\n%s\n%s", code, out, errOut)
	}
}

// SPEC U6: with no scope in config.json, -folder (cleaned) or -scope is saved, the first import keeps only that, and a
// home install creates gets a .gitignore.
func TestInstallFolderSavesScope(t *testing.T) {
	for _, c := range []struct {
		name, config string
		flags        []string
		scope        string
	}{
		{"new home, trailing separator", "", nil, ""},
		{"config with no scope", `{"user": "Ana", "pin_phrases": ["keep"]}`, nil, ""},
		{"-scope", "", []string{"-scope", "proj"}, "proj"},
	} {
		root, home, proj := icRoot(t)
		if c.config != "" {
			mmWrite(t, filepath.Join(home, "config.json"), c.config)
		}
		icTranscript(t, root, proj, "mine")
		icTranscript(t, root, filepath.Join(root, "elsewhere"), "other")
		args := append([]string{"install", "-host", "claude-code", "-home", home, "-codex=", "-user", "Ana", "-folder", proj + string(filepath.Separator)}, c.flags...)
		if out, err := icRun(t, false, "", args...); err != nil {
			t.Fatalf("%s: %v\n%s", c.name, err, out)
		}
		want := c.scope
		if want == "" {
			want = proj
		}
		if got := icConfig(t, home)["scope"]; got != want {
			t.Errorf("%s: scope saved %v, want %s", c.name, got, want)
		}
		db, err := openDB(filepath.Join(home, "trace.db"))
		if err != nil {
			t.Fatal(err)
		}
		if mine, other := count(t, db, "select count(*) from session where id = 'mine'"), count(t, db, "select count(*) from session where id = 'other'"); mine != 1 || other != 0 {
			t.Errorf("%s: first import stored mine %d, other %d; want 1 and 0", c.name, mine, other)
		}
		db.Close()
		b, err := os.ReadFile(filepath.Join(home, ".gitignore"))
		if fresh := c.config == ""; fresh != (err == nil) || fresh && string(b) != "*\n" {
			t.Errorf("%s: .gitignore %q (%v)", c.name, b, err)
		}
	}
}

// SPEC U7: a folder that is no project, a folder outside -scope, narrowing a memory of every project and a scope that
// would leave stored sessions outside are refused, with nothing written.
func TestInstallFolderRefusals(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	home, proj, user := filepath.Join(root, "trace"), filepath.Join(root, "proj"), filepath.Join(root, "user")
	os.MkdirAll(proj, 0o755)
	os.MkdirAll(user, 0o755)
	mmWrite(t, filepath.Join(home, "config.json"), `{"user": "Ana"}`)
	refused := func(why string, args ...string) {
		t.Helper()
		out, errOut, code := runTrace(t, root, "", env, append([]string{"install", "-host", "claude-code", "-codex="}, args...)...)
		if code == 0 || !strings.Contains(errOut, why) {
			t.Errorf("%v: exit %d, want a refusal that says %q\n%s\n%s", args, code, why, out, errOut)
		}
	}
	// The volume root is checked without running install: a broken check (a mutant) would write the real root's .claude.
	if vol := filepath.VolumeName(root) + string(filepath.Separator); folderFits(vol, home) == nil || !strings.Contains(folderFits(vol, home).Error(), "not a project folder") {
		t.Errorf("-folder %s: %v", vol, folderFits(vol, home))
	}
	for _, f := range []string{home, user} {
		refused("not a project folder", "-folder", f)
		if exists(filepath.Join(f, ".claude")) {
			t.Errorf("-folder %s: hooks written", f)
		}
	}
	refused("outside this home's scope", "-folder", proj, "-scope", "elsewhere")

	// Sessions stored outside the folder.
	db, err := openDB(filepath.Join(home, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`insert into session(id, host, role, cwd) values ('a', 'claude-code', 'main', ?), ('a2', 'claude-code', 'main', ?), ('b', 'claude-code', 'main', ?), ('c', 'claude-code', 'main', null)`,
		filepath.Join(root, "elsewhere"), filepath.Join(root, "other"), proj)
	db.Close()
	refused("2 sessions", "-folder", proj)
	refused("delete "+home+" and install again", "-folder", proj)
	os.Remove(filepath.Join(home, "trace.db"))

	// A memory of every project, run by the user settings.
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-all-projects"); code != 0 {
		t.Fatalf("install -all-projects: exit %d\n%s", code, errOut)
	}
	refused("every project", "-folder", proj)
	if exists(filepath.Join(proj, ".claude")) {
		t.Fatal("a refused -folder wrote its settings")
	}
	if _, has := icConfig(t, home)["scope"]; has {
		t.Fatal("a refused install saved a scope")
	}
}

// SPEC U8: the settings file is checked before config.json is written; when the settings write fails, config.json is
// put back as it was; install writes config.json only when a key changes.
func TestInstallUndoesConfig(t *testing.T) {
	root, home, proj := icRoot(t)
	local := filepath.Join(proj, ".claude", "settings.local.json")
	mmWrite(t, local, `{not json`)
	if _, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-folder", proj, "-user", "Ana"); err == nil || exists(home) {
		t.Fatalf("settings that do not parse: %v; home written: %v", err, exists(home))
	}

	const old = "{\"scope\":   \"proj\",\n \"anchors\": {\"t\": \"T-[0-9]+\"}}"
	for _, c := range []struct{ name, config string }{{"config created", ""}, {"config changed", old}} {
		_, home, proj := icRoot(t)
		local := filepath.Join(proj, ".claude", "settings.local.json")
		mmWrite(t, local, `{}`)
		os.MkdirAll(local+".trace-backup", 0o755) // the settings write fails: its backup cannot be written
		if c.config != "" {
			mmWrite(t, filepath.Join(home, "config.json"), c.config)
		}
		if _, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-folder", proj, "-user", "Ana"); err == nil {
			t.Fatalf("%s: the settings write did not fail", c.name)
		}
		b, err := os.ReadFile(filepath.Join(home, "config.json"))
		if c.config == "" && err == nil || c.config != "" && string(b) != c.config {
			t.Errorf("%s: config.json after the failed install: %q (%v)", c.name, b, err)
		}
	}

	_, home, proj = icRoot(t)
	const same = "{\"scope\": \"proj\",   \"user\": \"Ana\", \"x\": [1]}"
	mmWrite(t, filepath.Join(home, "config.json"), same)
	if _, err := icRun(t, false, "", "install", "-host", "claude-code", "-home", home, "-folder", proj); err != nil {
		t.Fatal(err)
	}
	if got := imRead(t, filepath.Join(home, "config.json")); got != same {
		t.Fatalf("a reinstall that changes no key rewrote config.json:\n%s", got)
	}
	_ = root
}

// SPEC U9: install prints the name and where to start sessions before the first import, per host and mode.
func TestInstallSaysWhereToStart(t *testing.T) {
	before := func(what, out string, wants ...string) {
		t.Helper()
		imp := strings.Index(out, "first import...")
		for _, w := range wants {
			if i := strings.Index(out, w); i < 0 || imp < 0 || i > imp {
				t.Errorf("%s: %q not before the first import:\n%s", what, w, out)
			}
		}
	}
	const block = "a new session there starts with a TRACE block; if you see none, you are in another folder or did not restart the agent"
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	proj := filepath.Join(root, "proj")
	os.MkdirAll(proj, 0o755)
	out, errOut, code := runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=", "-user", "Ana", "-folder", proj)
	if code != 0 {
		t.Fatalf("install -folder: exit %d\n%s", code, errOut)
	}
	before("-folder", out, "Ana", "open your Claude Code sessions in "+proj+": sessions started anywhere else do not use this memory", "trace stats", block)

	root = t.TempDir()
	env = map[string]string{"_EXE": mmTraceExe(t, root)}
	mmWrite(t, filepath.Join(root, "trace", "config.json"), `{"scope": "proj", "user": "Ana"}`)
	out, errOut, code = runTrace(t, root, "", env, "install", "-host", "claude-code", "-codex=")
	if code != 0 {
		t.Fatalf("install: exit %d\n%s", code, errOut)
	}
	before("user settings", out, "Ana", "Claude Code runs this memory in every folder", "proj", block)

	for mode, want := range map[string]string{"agents": "open your Codex sessions in ", "hooks": "the hooks run in every Codex session"} {
		root := t.TempDir()
		work, codexDir := filepath.Join(root, "work"), filepath.Join(root, "codex")
		os.MkdirAll(work, 0o755)
		os.MkdirAll(codexDir, 0o755)
		env := map[string]string{"_EXE": mmTraceExe(t, root), "_DIR": work}
		out, errOut, code := runTrace(t, root, "", env, "install", "-host", "codex", "-mode", mode, "-codex", codexDir, "-user", "Ana")
		if code != 0 {
			t.Fatalf("codex %s: exit %d\n%s", mode, code, errOut)
		}
		if mode == "agents" {
			want += work
		}
		before("codex "+mode, out, "Ana", want, block)
	}
}

// SPEC U10: uninstall asks for no name and checks no folder.
func TestUninstallAsksNothing(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"_EXE": mmTraceExe(t, root)}
	home := filepath.Join(root, "trace")
	mmWrite(t, filepath.Join(home, "config.json"), `{"scope": "proj"}`)
	if _, errOut, code := runTrace(t, root, "", env, "uninstall", "-host", "claude-code", "-folder", filepath.Join(root, "gone")); code != 0 {
		t.Fatalf("uninstall -folder of a missing folder: exit %d\n%s", code, errOut)
	}
	outside := filepath.Join(root, "elsewhere")
	local := filepath.Join(outside, ".claude", "settings.local.json")
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": env["_EXE"], "args": []any{"hook", "-home", home}}}}}}})
	mmWrite(t, local, string(b))
	if _, errOut, code := runTrace(t, root, "", env, "uninstall", "-host", "claude-code", "-folder", outside); code != 0 || hfOwn(t, local, home) != 0 {
		t.Fatalf("uninstall -folder outside the scope: exit %d, hooks left %d\n%s", code, hfOwn(t, local, home), errOut)
	}
}

// SPEC U11: a Codex install asks the name first, keeps the config's other keys, puts the config back when AGENTS.md
// cannot be written, and refuses -folder.
func TestCodexInstallName(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	home := filepath.Join(root, "trace")
	env := map[string]string{"_DIR": work}
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "codex", "-mode", "agents"); code == 0 || !strings.Contains(errOut, icAgentAsk) {
		t.Fatalf("codex with no name and no terminal: exit %d\n%s", code, errOut)
	}
	if exists(home) || exists(filepath.Join(work, "AGENTS.md")) {
		t.Fatal("a refused codex install wrote something")
	}
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "codex", "-mode", "agents", "-user", "Ana", "-folder", work); code == 0 || !strings.Contains(errOut, "-folder is for -host claude-code") {
		t.Fatalf("codex -folder: exit %d\n%s", code, errOut)
	}
	if exists(home) || exists(filepath.Join(work, "AGENTS.md")) {
		t.Fatal("codex -folder wrote something")
	}

	cfgJSON, _ := json.Marshal(map[string]any{"claude": false, "scope": []string{work}, "x-keep": map[string]any{"a": 1}})
	mmWrite(t, filepath.Join(home, "config.json"), string(cfgJSON))
	if _, errOut, code := runTrace(t, root, "", env, "install", "-host", "codex", "-mode", "agents", "-user", "Ñu"); code != 0 {
		t.Fatalf("codex -user: exit %d\n%s", code, errOut)
	}
	if c := icConfig(t, home); c["user"] != "Ñu" || c["x-keep"] == nil || c["claude"] != false {
		t.Fatalf("codex config after -user: %v", c)
	}

	gone := filepath.Join(root, "gone")
	home2 := t.TempDir()
	cfgJSON, _ = json.Marshal(map[string]any{"claude": false, "scope": []string{gone}})
	mmWrite(t, filepath.Join(home2, "config.json"), string(cfgJSON))
	var out strings.Builder
	if err := installCodex(home2, gone, t.TempDir(), "agents", "", "Ñu", false, &out); err == nil {
		t.Fatal("AGENTS.md in a missing folder was written")
	}
	if got := imRead(t, filepath.Join(home2, "config.json")); got != string(cfgJSON) {
		t.Fatalf("config not put back after AGENTS.md failed: %s\n%s", got, out.String())
	}
}

// SPEC U12: the one-line installers pass TRACE_USER as -user and, without it, let trace ask at the terminal.
func TestInstallScriptsPassTheName(t *testing.T) {
	sh, ps := imRead(t, "install.sh"), imRead(t, "install.ps1")
	for _, want := range []string{`-user "$TRACE_USER"`, "/dev/tty"} {
		if !strings.Contains(sh, want) {
			t.Errorf("install.sh lacks %s", want)
		}
	}
	if !strings.Contains(ps, `'-user', $env:TRACE_USER`) {
		t.Error("install.ps1 does not pass TRACE_USER as -user")
	}
}
