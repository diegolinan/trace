package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// stdinConsole says whether stdin is a console a person types in; tests replace it.
var stdinConsole = func() bool { return consoleIn(os.Stdin) }

// stdinLines is what the person types at that console.
var stdinLines = bufio.NewReader(os.Stdin)

// person says whether a person can answer at stdin: a console, and not Claude Code's own shell, where an agent runs
// the commands (SPEC U2).
func person() bool { return stdinConsole() && os.Getenv("CLAUDECODE") != "1" }

// ask prints a question and returns the line typed, "" at the end of input.
func ask(question string) string {
	fmt.Print(question)
	line, _ := stdinLines.ReadString('\n')
	return line
}

// name is the name the agents call the person: -user, else the one config.json holds, else asked at the terminal.
// It is never guessed (SPEC U1, U2, U4).
func (o opts) name() (string, error) {
	switch {
	case o.given["user"]:
		return validName(o.user)
	case cfg.User != "":
		return cfg.User, nil
	case !person():
		return "", errors.New("install needs the name the agents should call the person, and there is no terminal here to ask it; nothing done.\n" +
			"If you are an agent: ask the person the name to call them; do not use the OS account, git name or another memory; pass their exact answer with -user.\n" +
			`If you are the person, run again with -user "Your name".`)
	}
	return validName(ask("The name the agents should call you: "))
}

// validName is one line of text, trimmed, of 1 to 60 characters (SPEC U3).
func validName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) || utf8.RuneCountInString(s) > 60 {
		return "", errors.New("the name must be one line of 1 to 60 characters, with no control characters; nothing done")
	}
	return s, nil
}

// agreeEveryProject says what a memory with no scope stores, and goes on only with -all-projects or the person's yes
// (SPEC U5).
func (o opts) agreeEveryProject() error {
	n, size := onDisk(o.projects, o.codexDir)
	fmt.Printf("this memory has no scope: it will store every conversation on this machine, %d transcripts and rollouts now (%.1f MB), and every new one; to keep one project only, install with -folder <project folder>\n", n, float64(size)/1e6)
	switch {
	case o.allProjects:
		return nil
	case !person():
		return errors.New("there is no terminal here to ask whether this memory should keep every conversation on this machine; nothing done.\n" +
			"If you are an agent: ask the person whether it should keep every project or one; pass -all-projects only if they say every project, else -folder <their project folder>.\n" +
			"If you are the person, run again with -all-projects, or with -folder <project folder>.")
	}
	if a := strings.ToLower(strings.TrimSpace(ask("Store every conversation on this machine? (yes/no) "))); a == "y" || a == "yes" {
		return nil
	}
	return errors.New("not installed: to keep one project only, install with -folder <project folder>; nothing done")
}

// onDisk counts the transcripts and rollouts an import with no scope reads, and their size with the tool results.
func onDisk(projects, codexDir string) (n int, size int64) {
	files, results, _ := transcriptFiles(projects, "")
	n = len(files)
	for _, r := range results {
		files = append(files, filepath.Join(projects, r))
	}
	if codexDir != "" {
		rollouts := codexFiles(codexDir)
		n, files = n+len(rollouts), append(files, rollouts...)
	}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			size += fi.Size()
		}
	}
	return n, size
}

// claudeInstall is one Claude Code install, worked out and checked before anything is written (SPEC U8).
type claudeInstall struct {
	o          opts
	exe, path  string   // the binary the hooks run, the settings file they go in
	scope      string   // what the memory keeps after the install
	everywhere bool     // the user settings already run this home's hooks
	plan       filePlan // config.json with the name, and the scope when it had none
}

// checkClaude runs every check of a Claude Code install, asks what only the person can say, and plans config.json.
func (o opts) checkClaude(exe string) (*claudeInstall, error) {
	in := &claudeInstall{o: o, exe: exe, path: settingsFor(o.folder), everywhere: runsHome(claudeSettings(), o.home)}
	if err := folderFits(o.folder, o.home); err != nil {
		return nil, err
	}
	if err := settingsCheck(in.path, exe, o.home, o.migrate); err != nil {
		return nil, err
	}
	saved, err := loadConfig(o.home) // cfg.Scope holds -scope too
	if err != nil {
		return nil, err
	}
	if in.scope, err = in.keptScope(saved.Scope); err != nil {
		return nil, err
	}
	if err := in.askAndPlan(saved.Scope); err != nil {
		return nil, err
	}
	return in, nil
}

// askAndPlan gets what only the person can say, the name and a yes for a memory of every project, then plans
// config.json: the name, and the scope when it had none.
func (in *claudeInstall) askAndPlan(saved scopeList) (err error) {
	if in.o.user, err = in.o.name(); err != nil {
		return err
	}
	if in.scope == "" && !in.everywhere {
		if err := in.o.agreeEveryProject(); err != nil {
			return err
		}
	}
	set := map[string]any{"user": in.o.user}
	if saved == "" && in.scope != "" {
		set["scope"] = in.scope
	}
	in.plan, err = planConfig(in.o.home, set)
	return err
}

// keptScope is the scope the memory keeps: config.json's, else -scope, else the folder, else every project. A new one
// is refused when it would narrow a memory of every project in silence, or leave stored sessions outside (SPEC U6, U7).
func (in *claudeInstall) keptScope(saved scopeList) (string, error) {
	o := in.o
	if saved != "" {
		return o.scope, nil
	}
	scope := cmp.Or(o.scope, o.folder)
	switch {
	case scope == "":
		return "", nil
	case o.folder != "" && in.everywhere:
		return "", errors.New("the user settings already run this memory in every folder, and its config.json has no scope, so it keeps every project; -folder would narrow it in silence. For one project, install with a new -home; nothing done")
	}
	if n := storedOutside(o.dbPath, scope); n > 0 {
		return "", fmt.Errorf("this memory already holds %d sessions that ran outside %s, and search would keep showing them; to keep only %s, delete %s and install again; nothing done", n, scope, scope, o.home)
	}
	return scope, nil
}

// storedOutside counts the sessions a store holds that ran outside scope; a session with no folder recorded cannot be
// placed and is not counted.
func storedOutside(dbPath, scope string) (n int) {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return 0
	}
	defer db.Close()
	rows, err := db.Query("select cwd from session where cwd is not null")
	for err == nil && rows.Next() { // Next closes the rows when it returns false
		var cwd string
		if rows.Scan(&cwd) == nil && !inScope(cwd, scope) {
			n++
		}
	}
	return n
}

// settingsCheck runs install's checks on the settings file, before anything is written.
func settingsCheck(path, exe, home string, migrate bool) error {
	s, _, err := readSettings(path)
	if err == nil {
		_, legacy, foreign := traceHooks(s, home)
		err = refusal(path, exe, home, legacy, foreign, migrate)
	}
	return err
}

// write writes config.json, then the hooks, putting config.json back when the hooks cannot be written; then it says
// where to start and imports with the scope it saved.
func (in *claudeInstall) write() error {
	o := in.o
	if err := writeConfig(in.plan, os.Stdout); err != nil {
		return err
	}
	cfg.Scope = scopeList(in.scope) // the first import keeps what config.json now says
	if err := install(in.path, in.exe, o.home, optionalHooks(cfg), o.migrate, os.Stdout); err != nil {
		undoConfig(in.plan, os.Stdout)
		return err
	}
	where := "Claude Code runs this memory in every folder, and it keeps every session"
	switch {
	case o.folder != "":
		where = fmt.Sprintf("open your Claude Code sessions in %s: sessions started anywhere else do not use this memory (`trace stats` lists the folders where sessions started without its hooks)", o.folder)
	case in.scope != "":
		where = fmt.Sprintf("Claude Code runs this memory in every folder, and it keeps the sessions that run in %s", in.scope)
	}
	startHint(os.Stdout, cfg.User, o.user, where)
	return o.firstImport(o.projects, o.history, in.scope)
}

// startHint says, before the first import (which a person may stop), the name the agents use and where a session must
// start to use this memory (SPEC U4, U9).
func startHint(w io.Writer, old, name, where string) {
	if old != "" && old != name {
		fmt.Fprintf(w, "name changed from %s to %s\n", old, name)
	}
	fmt.Fprintf(w, "the agents call you %s (config.json \"user\"; change it with install -user)\n", name)
	fmt.Fprintln(w, where)
	fmt.Fprintln(w, "a new session there starts with a TRACE block; if you see none, you are in another folder or did not restart the agent")
}

// planConfig is home's config.json with the keys in set changed and every other key kept; no change when each key
// already holds its value, so a person's file is not rewritten (SPEC U8, U11).
func planConfig(home string, set map[string]any) (filePlan, error) {
	p := filePlan{path: filepath.Join(home, "config.json")}
	old, existed, err := readIfThere(p.path)
	m := map[string]any{}
	if err == nil && existed {
		err = json.Unmarshal(bytes.TrimPrefix(old, []byte("\xef\xbb\xbf")), &m)
	}
	if err != nil {
		return p, err
	}
	p.old, p.existed = old, existed
	before := normalize(m)
	maps.Copy(m, set)
	if reflect.DeepEqual(before, normalize(m)) {
		return p, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // the name is kept as typed: < > & too
	enc.SetIndent("", "  ")
	err = enc.Encode(m)
	p.next = b.Bytes()
	return p, err
}

// writeConfig writes config.json when the plan changes it. A home it creates gets a .gitignore holding *, so a home
// inside a repository is not committed (SPEC U6).
func writeConfig(p filePlan, w io.Writer) error {
	if !p.changed() {
		fmt.Fprintln(w, "kept", p.path)
		return nil
	}
	home := filepath.Dir(p.path)
	if _, err := os.Stat(home); os.IsNotExist(err) {
		if err := os.MkdirAll(home, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(home, ".gitignore"), []byte("*\n"), 0o644); err != nil {
			return err
		}
	}
	if err := p.write(); err != nil {
		return err
	}
	fmt.Fprintln(w, "wrote", p.path)
	return nil
}

// undoConfig puts config.json back as it was before this run, while it still holds what this run wrote.
func undoConfig(p filePlan, w io.Writer) {
	if !p.changed() {
		return
	}
	if err := p.undo(); err != nil {
		fmt.Fprintln(w, "left", p.path, "in place:", err)
		return
	}
	fmt.Fprintln(w, map[bool]string{false: "removed " + p.path + " again", true: "put " + p.path + " back as it was"}[p.existed])
}
