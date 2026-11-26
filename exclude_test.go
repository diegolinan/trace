package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// exRoots makes roots the system temp roots for one test (SPEC I4).
func exRoots(t *testing.T, roots ...string) {
	t.Helper()
	old := tempRoots
	tempRoots = roots
	t.Cleanup(func() { tempRoots = old })
}

// exAbs is an absolute folder on this system, outside every real temp root (t.TempDir() is under /tmp on Linux and
// /var/folders on macOS). It need not exist: excludedFor reads the name alone.
func exAbs(parts ...string) string {
	return filepath.Join(append([]string{filepath.VolumeName(os.Getenv("SystemDrive")) + string(filepath.Separator) + "work"}, parts...)...)
}

// SPEC I4: a folder is excluded when it lies inside a temp root, by its normalized name, the same for a Claude Code
// transcript folder name and a Codex cwd on every platform; an absolute scope or keep entry exempts it.
func TestExcludedTempFolders(t *testing.T) {
	smKeepCfg(t)
	cfg.Scope, cfg.Keep = "", nil
	base := exAbs()
	root := filepath.Join(base, "Temp")
	exRoots(t, root, `C:\Users\a\AppData\Local\Temp`, "/tmp", "/private/tmp", "/var/folders", "/private/var/folders")
	for in, want := range map[string]bool{
		strings.ToUpper(folderName(filepath.Join(root, "TestHook123"))): true, // a Claude Code transcript folder name
		filepath.Join(root, "x"):            true, // a Codex cwd
		root:                                true, // the root itself
		`C:\Users\a\AppData\Local\Temp\x`:   true, // Windows
		"/tmp/go-build1/x":                  true, // Linux
		"/private/tmp/x":                    true,
		"/var/folders/ab/T/TestX/001":       true, // macOS
		"/private/var/folders/ab/T/x":       true, // macOS, resolved
		filepath.Join(base, "Temp2", "x"):   false,
		filepath.Join(base, "my-temp-tool"): false, // the old rule's words are gone
		"C--work-scratch-pad":               false,
		"/home/u/tmp/x":                     false,
		`D:\Projects\Contemporary`:          false,
		"":                                  false,
	} {
		why := excludedFor(in)
		if got := why != ""; got != want {
			t.Errorf("excludedFor(%q) = %q, want excluded %v", in, why, want)
		}
	}
	if why := excludedFor(filepath.Join(root, "x")); !strings.Contains(why, root) {
		t.Errorf("the reason %q does not name the temp root %s", why, root)
	}

	// Explicit beats the default: an absolute scope or keep entry, cleaned; a fragment or a volume root exempts nothing.
	proj := filepath.Join(root, "proj")
	inProj := filepath.Join(proj, "sub")
	for _, c := range []struct {
		scope string
		keep  []string
		want  bool
	}{
		{scope: " " + proj + " , acme", want: false},
		{scope: proj + string(filepath.Separator), want: false},
		{scope: "proj", want: true},
		{scope: "acme," + filepath.Join(root, "pro"), want: true}, // a whole folder, not a prefix of its name
		{keep: []string{proj}, want: false},
		{keep: []string{"proj"}, want: true},
		{keep: []string{filepath.VolumeName(base) + string(filepath.Separator)}, want: true},
	} {
		cfg.Scope, cfg.Keep = scopeList(c.scope), c.keep
		if got := excludedFor(inProj) != ""; got != c.want {
			t.Errorf("scope %q, keep %q: excluded %v, want %v", c.scope, c.keep, got, c.want)
		}
	}
	cfg.Scope, cfg.Keep = "", []string{proj}
	if excludedFor(filepath.Join(root, "other")) == "" {
		t.Error("a keep entry exempted a folder outside it")
	}
}

// SPEC I4: the real roots are the system's temp folder as given and resolved, and the Unix temp folders.
func TestTempRootsIncludeResolvedTempDir(t *testing.T) {
	resolved, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roots := systemTempRoots()
	for _, want := range []string{os.TempDir(), resolved, "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
		if !slices.Contains(roots, want) {
			t.Errorf("temp roots %q lack %q", roots, want)
		}
	}
}

// SPEC I17: in an excluded folder the hooks still speak: the helper line reaches a subagent there. A role this
// memory does not speak to stays silent.
func TestHooksRunInExcludedFolder(t *testing.T) {
	smKeepCfg(t)
	cfg.Scope, cfg.Roles = "", nil
	root := t.TempDir()
	exRoots(t, root)
	cwd := filepath.Join(root, "probe")
	projects := filepath.Join(t.TempDir(), "projects")
	in := hookInput{Event: "SubagentStart", SessionID: "s1", Cwd: cwd, Transcript: filepath.Join(projects, folderName(cwd), "s1.jsonl")}
	if excludedFor(cwd) == "" {
		t.Fatal("the fixture's folder is not excluded")
	}
	var out strings.Builder
	if err := hook(in, nil, filepath.Join(root, "t.db"), "claude-code", projects, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "You are a helper agent") {
		t.Errorf("SubagentStart in an excluded folder wrote %q, want the helper line", out.String())
	}
	if _, mine := hookRole(in, "codex"); !mine {
		t.Error("a Codex session in an excluded folder is not spoken to")
	}
	start := in
	start.Event, start.Source = "SessionStart", "startup"
	out.Reset()
	noImport := func(*sql.DB, string) error { return nil }
	if err := hook(start, nil, filepath.Join(root, "t.db"), "claude-code", projects, noImport, &out); err != nil || !strings.Contains(out.String(), "rule add") {
		t.Errorf("SessionStart in an excluded folder wrote %q (%v), want its start block", out.String(), err)
	}
	cfg.Roles = []roleRule{{Name: "main", Marker: "ROLE.md", Contains: "role: main"}} // no folder holds the marker
	out.Reset()
	if err := hook(in, nil, filepath.Join(root, "t.db"), "claude-code", projects, nil, &out); err != nil || out.Len() != 0 {
		t.Errorf("a role this memory does not speak to got %q (%v)", out.String(), err)
	}
}

// SPEC I18: every import decides I4 again from the stored cwd: a keep entry reaches rows stored before it, rows
// excluded by the old name rules are mended, and a session with no folder keeps what it has.
func TestExcludedRecomputedOnImport(t *testing.T) {
	smKeepCfg(t)
	cfg.Scope, cfg.Keep = "", nil
	base := t.TempDir()
	root := filepath.Join(base, "Temp")
	exRoots(t, root)
	db := rzDB(t)
	probe, kept, tool := filepath.Join(root, "probe"), filepath.Join(root, "kept"), filepath.Join(base, "my-temp-tool")
	rzExec(t, db, `insert into session(id, host, role, cwd, excluded) values ('p', 'codex', 'main', ?, null), ('k', 'claude-code', 'main', ?, null),
		('w', 'claude-code', 'main', ?, 'probe or test folder: x'), ('n', 'prompt-log', 'main', null, 'old'),
		('p2', 'claude-code', 'main', ?, null), ('w2', 'claude-code', 'main', ?, 'old')`, probe, kept, tool, probe, tool) // a folder decided once, for two sessions
	imp := func() {
		t.Helper()
		if err := runImport(db, "", "", "", "", "", "test"); err != nil {
			t.Fatal(err)
		}
	}
	imp()
	ex := func(id string) string { return mutStr(t, db, "select excluded from session where id = ?", id) }
	if !strings.Contains(ex("p"), root) || !strings.Contains(ex("k"), root) || ex("w") != "<null>" || ex("n") != "old" || ex("p2") != ex("p") || ex("w2") != "<null>" {
		t.Errorf("after an import: p %q, k %q, w %q, n %q, p2 %q, w2 %q", ex("p"), ex("k"), ex("w"), ex("n"), ex("p2"), ex("w2"))
	}
	cfg.Keep = []string{kept}
	imp()
	if ex("k") != "<null>" || !strings.Contains(ex("p"), root) {
		t.Errorf("after keep: k %q, p %q", ex("k"), ex("p"))
	}
}

// SPEC S7: a session inside the system temp folder, in a memory of every project and not a program's, is told the
// way out in its start block, never cut, and by trace resume; nobody else is.
func TestStartBlockTellsTheWayOut(t *testing.T) {
	rzNoRoles(t)
	cfg.Scope = ""
	root := t.TempDir()
	cwd := filepath.Join(root, "proj")
	os.MkdirAll(cwd, 0o755)
	exRoots(t, root)
	want := cwd + " is inside the system temp folder " + root + ", so other sessions do not recall it; if Ana says it is a project, run "
	db := bgBigStore(t, cwd)
	b := bgHook(t, db, cwd)
	if !strings.Contains(b, want) || !strings.Contains(b, `keep "`+cwd+`"`) || len(b) > 9500 || !strings.Contains(b, "Shortened to fit") {
		t.Errorf("a shortened block (%d bytes) lacks the way out:\n%s", len(b), b[max(0, len(b)-600):])
	}
	var codex strings.Builder
	if err := hookBlock(rzDB(t), "new", "startup", projectRole(cwd), "codex", cwd, &codex); err != nil || !strings.Contains(codex.String(), want) {
		t.Errorf("Codex's block lacks the way out (%v):\n%s", err, codex.String())
	}
	if rz := rzBlock(t, db, cwd); strings.Contains(rz, want) {
		t.Error("resume's block holds the line; cmdResume prints it apart, so an empty block still reads as empty")
	}
	cfg.Scope = "acme"
	if strings.Contains(bgHook(t, db, cwd), want) {
		t.Error("a memory with a scope got the line")
	}
	cfg.Scope = ""
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "sdk-cli")
	if strings.Contains(bgHook(t, db, cwd), want) {
		t.Error("a program's session got the line")
	}
	os.Unsetenv("CLAUDE_CODE_ENTRYPOINT")
	if out := bgHook(t, db, t.TempDir()); strings.Contains(out, "system temp folder") {
		t.Error("a folder outside the temp root got the line")
	}

	root = t.TempDir()
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	out, errOut, code := runTrace(t, root, "", map[string]string{"_DIR": work, "TRACE_TEST_TEMP_ROOT": root}, "resume", "-codex=")
	if code != 0 || !strings.Contains(out, "No earlier Claude Code session in this folder.") || !strings.Contains(out, work+" is inside the system temp folder "+root) {
		t.Errorf("trace resume: exit %d\n%s%s", code, out, errOut)
	}
}

// SPEC I19: trace keep saves an absolute folder, cleaned, once; it refuses a relative path, a volume root and a temp
// root, with nothing written.
func TestKeepFolder(t *testing.T) {
	smKeepCfg(t)
	oldHome := activeHome
	t.Cleanup(func() { activeHome = oldHome })
	base := t.TempDir()
	root := filepath.Join(base, "Temp")
	exRoots(t, root)
	home := filepath.Join(base, "trace")
	mmWrite(t, filepath.Join(home, "config.json"), `{"user": "Ana", "anchors": {"x": "y"}}`)
	keep := func(folder string) error {
		t.Helper()
		o, err := parseArgs("keep", []string{"-home", home, folder})
		if err != nil {
			return err
		}
		return cmdKeep(o)
	}
	for _, bad := range []string{"proj", filepath.VolumeName(base) + string(filepath.Separator), root, root + string(filepath.Separator)} {
		if err := keep(bad); err == nil {
			t.Errorf("keep %q: no refusal", bad)
		}
		if _, has := icConfig(t, home)["keep"]; has {
			t.Fatalf("keep %q wrote config.json", bad)
		}
	}
	proj := filepath.Join(root, "proj")
	for range 2 {
		if err := keep(proj + string(filepath.Separator)); err != nil {
			t.Fatal(err)
		}
	}
	m := icConfig(t, home)
	if got, ok := m["keep"].([]any); !ok || len(got) != 1 || got[0] != proj || m["user"] != "Ana" || m["anchors"] == nil {
		t.Errorf("config.json = %v, want keep [%s] and the other keys kept", m, proj)
	}
	if cfg, _ = loadConfig(home); excludedFor(filepath.Join(proj, "a")) != "" {
		t.Errorf("the saved keep entry %q does not exempt its folder", cfg.Keep)
	}
}

// SPEC I20: trace check-ignore -v says whether a folder is excluded, and why.
func TestCheckIgnore(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Temp")
	kept := filepath.Join(root, "kept")
	mmWrite(t, filepath.Join(base, "trace", "config.json"), `{"user": "Ana", "keep": [`+jsonStr(kept)+`]}`)
	env := map[string]string{"TRACE_TEST_TEMP_ROOT": root}
	for folder, want := range map[string][]string{
		filepath.Join(root, "x"):    {"excluded", root, `keep "` + filepath.Join(root, "x") + `"`},
		filepath.Join(kept, "sub"):  {"recalled", root, kept},
		filepath.Join(base, "work"): {"recalled", "inside no system temp folder"},
	} {
		out, errOut, code := runTrace(t, base, "", env, "check-ignore", "-v", folder)
		for _, w := range want {
			if code != 0 || !strings.Contains(out, w) {
				t.Errorf("check-ignore %s: exit %d, want %q\n%s%s", folder, code, w, out, errOut)
			}
		}
	}
}

func jsonStr(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }
