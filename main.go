// TRACE, Transcript Recall And Continuity Engine: a coding agent's own memory. A memory trace is neuroscience's
// name for the physical mark a memory leaves, the engram. Every Claude Code session in scope is copied verbatim
// into one SQLite file, by code, with no model in the loop; hooks show the next session where the last one stopped.
// Settings live in <home>/config.json (config.go).
//
// Usage:
//
//	trace install -host claude-code   add hooks that run this home to Claude Code's user settings, then import
//	                                  (uninstall removes them; -migrate takes over hooks that name no home;
//	                                  -folder <dir>: that folder's .claude/settings.local.json instead, and its scope;
//	                                  -user <name>: the name the agents call you, asked when none is saved)
//	trace install -host codex   a Codex-only memory for the current folder: config.json and an AGENTS.md line
//	-home <folder>          on any command: the TRACE home to use (default $TRACE_HOME, else ~/.trace)
//	trace import            read new bytes of every transcript in scope; safe to run any time
//	trace hook              for host hooks: starts a detached import and returns at once, exit 0 (a Stop that holds the
//	                        turn for a task not told exits 2)
//	trace resume            where the last session in this folder stopped, for agents without hooks (-host codex)
//	trace search [-n 10] [-who user|assistant|subagent] [-all] [-since 2026-11-01 [11:30] | 3h] [-until ...] [-zone UTC-3] <words>
//	                        (no words with -since or -until: what was said in that window, oldest first)
//	trace show [-n 3] [-all] <session>#<seq>   a search hit with what was said around it
//	                        flags always go before the words; a flag after them is refused (SPEC E6)
//	trace rules             the rule card a session in this folder gets (re-injection, opt-in)
//	trace stats
//	trace task add -text "<what>" [-at "YYYY-MM-DD HH:MM" [-zone UTC-3]] [-self] | done <id> | drop <id> | told <id> | list   the user's tasks
//	trace lesson add -text "<lesson>" -evidence "<words of a tool result>" [-machine] [-about <regexp>] | drop <id> | list [-all]   what agents learned by testing
//	trace alarm [-stop]     fires tasks at their time; started by the start hook and task add -at (-stop: stop it)
//	trace keep <folder>     recall the sessions of a project inside the system temp folder (config.json "keep")
//	trace check-ignore -v <folder>   whether sessions there are kept out of recall, and why
//	trace version           the installed version (also -version, --version)
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: trace install -host claude-code|codex | uninstall -host ... | import | restore | hook | resume | search <words> | show <session>#<seq> | rules | stats | task add|done|drop|told|list | rule add|drop|list | lesson add|drop|list | alarm [-stop] | keep <folder> | check-ignore -v <folder> | version")
		os.Exit(2)
	}
	cmd := os.Args[1]
	if isVersionArg(cmd) { // before the home and config.json, so it answers even when they are broken (SPEC E3)
		fmt.Println(versionString(version, buildInfo()))
		return
	}
	o, err := parseArgs(cmd, os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace:", err)
		if cmd == "hook" {
			return // fail open: a hook must never block the host's turn; Claude Code reads exit 2 as "block"
		}
		os.Exit(exitCode(err))
	}
	if cmd == "hook" {
		runHook(o)
		return
	}
	run, ok := commands[cmd]
	if !ok {
		fail(fmt.Errorf("unknown command %q", cmd))
	}
	if err := run(o); err != nil {
		fail(err)
	}
}

// opts are one command's settings: its flags, with what config.json and the defaults fill in for the flags not given.
type opts struct {
	cmd, home, dbPath, projects, history, codexDir, host, notID, archive, scope, trigger, who, since, until, mode string
	text, at, zone, level, under, kind, points, quote, replaces, about, evidence, folder, user                    string
	n                                                                                                             int
	all, migrate, stop, self, decision, machine, allProjects, verbose                                             bool
	after, needs, then                                                                                            listFlag
	given                                                                                                         map[string]bool
	args                                                                                                          []string
}

// usageError is a command line trace cannot run: it ends with exit 2, every other error with 1 (exitCode); a hook
// ends with 0 whatever it is.
type usageError struct{ error }

func exitCode(err error) int {
	if errors.As(err, new(usageError)) {
		return 2
	}
	return 1
}

// parseArgs reads the flags, then config.json from the home they name, then fills the defaults.
func parseArgs(cmd string, args []string) (opts, error) {
	o := opts{cmd: cmd}
	// A hook must never exit 2 on a bad flag: Claude Code reads exit 2 as "block".
	onErr := flag.ExitOnError
	if cmd == "hook" {
		onErr = flag.ContinueOnError
	}
	n := 0
	if cmd == "task" || cmd == "rule" || cmd == "lesson" {
		n = min(1, len(args)) // flag parsing stops at the first word, so the subcommand comes off first
	}
	sub := args[:n:n] // capped, so appending to it never writes over args
	args = args[n:]
	fs := o.flagSet(onErr)
	if err := fs.Parse(args); err != nil {
		return o, usageError{err} // only a hook gets here (ContinueOnError)
	}
	if f := flagAfterWords(fs); f != "" { // before any home is read (SPEC E6)
		return o, usageError{fmt.Errorf("-%s comes after the words; put flags before them (for task, rule and lesson, right after the subcommand); nothing done", f)}
	}
	o.args = append(sub, fs.Args()...)
	o.given = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { o.given[f.Name] = true })
	if err := o.resolveHome(); err != nil {
		return o, usageError{err}
	}
	var err error
	if cfg, err = loadConfig(o.home); err != nil {
		return o, fmt.Errorf("config.json is not valid JSON, nothing done: %w", err)
	}
	o.defaults()
	if err := o.checkHost(); err != nil {
		return o, err
	}
	cfg.Scope = scopeList(o.scope) // the hook's scope gate and the import read the same scope
	compileConfig()
	return o, nil
}

// flagSet is every flag trace takes, bound to o. Install reads a hook entry's -home with it too (hookHome), so the
// two never disagree on what a hook command means. Flags are parsed before config.json is read, because -home says
// which config to read; the defaults that come from the config are filled in afterwards for the flags not given.
func (o *opts) flagSet(onErr flag.ErrorHandling) *flag.FlagSet {
	fs := flag.NewFlagSet(o.cmd, onErr)
	fs.StringVar(&o.home, "home", "", "TRACE home: config.json and, unless config moves it, the data (default $TRACE_HOME, else ~/.trace)")
	fs.StringVar(&o.dbPath, "db", "", "database file (default <data>/trace.db)")
	fs.StringVar(&o.projects, "projects", "", "Claude Code transcripts folder (empty: none; default $CLAUDE_CONFIG_DIR/projects, else ~/.claude/projects, unless config says claude: false)")
	fs.StringVar(&o.history, "history", "", "Claude Code prompt log (empty: none; default $CLAUDE_CONFIG_DIR/history.jsonl, else ~/.claude/history.jsonl, unless config says claude: false)")
	fs.StringVar(&o.codexDir, "codex", codexHome(), "Codex home, for its session rollouts (empty: no Codex)")
	fs.StringVar(&o.host, "host", "", "install, uninstall (required), hook (default claude-code), resume (default codex): claude-code or codex")
	fs.StringVar(&o.folder, "folder", "", "install, uninstall -host claude-code: that folder's .claude/settings.local.json instead of the user settings (SPEC H1)")
	fs.StringVar(&o.notID, "not", "", "resume: a session id to leave out (default: the session written to in the last two minutes)")
	fs.StringVar(&o.archive, "archive", "", "raw copies folder (empty: no copy; default <data>/raw)")
	fs.StringVar(&o.scope, "scope", "", "folders in scope, comma-separated (empty: all; default from config)")
	fs.StringVar(&o.trigger, "trigger", "manual", "what started this import (hook name)")
	fs.IntVar(&o.n, "n", 10, "hits")
	fs.StringVar(&o.who, "who", "", "only this speaker")
	fs.BoolVar(&o.all, "all", false, "also search thinking, tool results, summaries, injected text and excluded sessions")
	fs.StringVar(&o.since, "since", "", "search: only what was said from this day (YYYY-MM-DD), minute (YYYY-MM-DD HH:MM, in -zone), RFC 3339 time or span back (90m, 3h, 2d)")
	fs.StringVar(&o.until, "until", "", "search: only what was said up to the end of this day, minute, time or span back, as -since")
	fs.StringVar(&o.mode, "mode", "", "install -host codex (required): agents (a block in AGENTS.md) or hooks (Codex hooks in CODEX_HOME/hooks.json)")
	fs.StringVar(&o.text, "text", "", "task add: what the task is; lesson add: what the agent learned by testing (SPEC L1)")
	fs.StringVar(&o.evidence, "evidence", "", "lesson add: at least 3 words of the tool result in this session that showed it (SPEC L1)")
	fs.BoolVar(&o.machine, "machine", false, "lesson add: a lesson for every session of this store, not only this folder (SPEC L1)")
	fs.StringVar(&o.at, "at", "", "task add: when, as YYYY-MM-DD HH:MM in -zone (default the computer's zone) or RFC 3339")
	fs.StringVar(&o.zone, "zone", "", "task add -at, search -since/-until: the zone they are in, as an offset (UTC-3) or a zone name (America/Los_Angeles); default the computer's")
	fs.BoolVar(&o.stop, "stop", false, "alarm: stop the running alarm and wait for it to exit")
	fs.Var(&o.after, "after", "task add: a task this one waits on (repeatable)")
	fs.Var(&o.needs, "needs", "task add: something else this one waits on, in words (repeatable)")
	fs.Var(&o.then, "then", "task add: a criterion of done, Given / When / Then in words (repeatable)")
	fs.StringVar(&o.level, "level", "", "task add: epic, story, task (default) or subtask (SPEC T10)")
	fs.StringVar(&o.under, "under", "", "task add: the open task one level up this one goes under (SPEC T11)")
	fs.StringVar(&o.kind, "kind", "", "task add: a story's kind: story (default), bug, chore, spike or impediment (SPEC T14)")
	fs.StringVar(&o.points, "points", "", "task add: a story's points: 1, 2, 3, 5, 8 or 13 (SPEC T15)")
	fs.BoolVar(&o.self, "self", false, "task add: the agent adds it on its own; no message of the user asked for it")
	fs.StringVar(&o.quote, "quote", "", "rule add: the user's words that set the rule, at least 3, as he typed them (SPEC D1)")
	fs.BoolVar(&o.decision, "decision", false, "rule add: a decision about the work, not a rule of how to work (SPEC D1)")
	fs.StringVar(&o.replaces, "replaces", "", "rule add, lesson add: the record this one replaces (SPEC D2, L2)")
	fs.StringVar(&o.about, "about", "", "rule add, lesson add: a regexp of the files or commands the record is about; it comes back before such a tool call (SPEC D11, L5)")
	fs.StringVar(&o.user, "user", "", "install: the name the agents call the person, saved as config.json's \"user\"; asked at the terminal when config.json has none (SPEC U1, U4)")
	fs.BoolVar(&o.allProjects, "all-projects", false, "install -host claude-code: with no -folder and no scope, keep every conversation on this machine without asking (SPEC U5)")
	fs.BoolVar(&o.migrate, "migrate", false, "install, uninstall: take over TRACE hooks that name no home (installed before -home existed)")
	fs.BoolVar(&o.verbose, "v", false, "check-ignore: say why (git's form; the answer always says why, SPEC I20)")
	fs.String("repo", "", "ignored: kept so hooks written for an older TRACE still run (SPEC G5)")
	return fs
}

// resolveHome is the -home flag, else the default home, always absolute: the home is written into hooks and
// AGENTS.md, which run from other folders.
func (o *opts) resolveHome() error {
	if !o.given["home"] {
		o.home = traceHome()
	}
	h, err := filepath.Abs(o.home)
	if o.home == "" || err != nil {
		return fmt.Errorf("-home needs a folder")
	}
	o.home, activeHome = h, h
	return nil
}

// defaults fills each flag not given from config.json and the home.
func (o *opts) defaults() {
	data := dataDir(o.home, cfg)
	def := func(name string, p *string, v string) {
		if !o.given[name] {
			*p = v
		}
	}
	def("db", &o.dbPath, filepath.Join(data, "trace.db"))
	if claudeOn(cfg) {
		def("projects", &o.projects, filepath.Join(claudeDir(), "projects"))
		def("history", &o.history, filepath.Join(claudeDir(), "history.jsonl"))
	}
	def("archive", &o.archive, filepath.Join(data, "raw"))
	def("scope", &o.scope, string(cfg.Scope))
	switch o.cmd {
	case "hook":
		def("host", &o.host, "claude-code") // the hooks installed before -host existed are Claude Code's
	case "resume":
		def("host", &o.host, map[bool]string{true: "claude-code", false: "codex"}[claudeOn(cfg)]) // SPEC S3
	}
}

// claudeOn says whether a config imports Claude Code's sessions: unless it says claude: false.
func claudeOn(c config) bool {
	return c.Claude == nil || *c.Claude
}

func (o *opts) checkHost() error {
	if o.given["host"] && o.host != "claude-code" && o.host != "codex" {
		return fmt.Errorf("unknown -host %q: claude-code or codex", o.host)
	}
	return nil
}

// importFn runs the import these settings name, with trigger as its reason.
func (o opts) importFn(trigger string) func(*sql.DB) error {
	return func(db *sql.DB) error {
		return runImport(db, o.projects, o.history, o.codexDir, o.archive, o.scope, trigger)
	}
}

// commands are every command but hook, which fails open and is run apart.
var commands = map[string]func(opts) error{
	"install":      cmdInstall,
	"uninstall":    cmdInstall,
	"resume":       cmdResume,
	"import":       cmdImport,
	"restore":      cmdRestore,
	"search":       cmdSearch,
	"show":         cmdShow,
	"stats":        cmdStats,
	"rules":        cmdRules,
	"task":         cmdTask,
	"rule":         cmdRecords,
	"lesson":       cmdRecords,
	"alarm":        cmdAlarm,
	"keep":         cmdKeep,
	"check-ignore": cmdCheckIgnore,
}

// runHook answers a host hook. Fail open: a hook error must never block or fail the host's turn.
func runHook(o opts) {
	failOpen(func() {
		run := func(db *sql.DB, trigger string) error { return o.importFn(trigger)(db) }
		in := readHookInput(os.Stdin)
		var out strings.Builder
		err := hook(in, os.Args[2:], o.dbPath, o.host, o.projects, run, &out)
		os.Stdout.WriteString(out.String())
		logHookOut(o.dbPath, in, out.String())
		if h := (holdTurn{}); errors.As(err, &h) {
			fmt.Fprintln(os.Stderr, h.reason)
			os.Exit(2) // the one exit 2 a hook gives: a Claude Code Stop that keeps the turn going (SPEC A8)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "trace hook:", err)
		}
	})
}

// logHookOut records how many characters a hook call wrote to the agent (SPEC K2); a call that wrote nothing logs
// nothing, and a store that cannot be opened loses the row, never the output.
func logHookOut(dbPath string, in hookInput, out string) {
	if out == "" || in.SessionID == "" {
		return
	}
	db, err := openDB(dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	event := in.Event
	if in.Source != "" {
		event += ":" + in.Source
	}
	db.Exec(`insert into hook_out(ts, session_id, event, chars) values (?, ?, ?, ?)`,
		time.Now().UTC().Format(storedTS), in.SessionID, event, len([]rune(out)))
}

// failOpen runs f and turns a panic into a line on stderr. A panic exits 2, which both hosts read as "block" on
// UserPromptSubmit, PostToolUse and Stop; recovered, the hook returns to main and exits 0.
func failOpen(f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "trace hook: internal error:", r)
		}
	}()
	f()
}

// cmdInstall installs or uninstalls the integration for -host, then imports once. Nothing is written until the host
// is known: without -host, install used to mean Claude Code.
func cmdInstall(o opts) error {
	if !o.given["host"] {
		return fmt.Errorf("%s needs -host claude-code or -host codex; nothing done", o.cmd)
	}
	if o.host == "codex" {
		if o.folder != "" {
			return fmt.Errorf("-folder is for -host claude-code; Codex installs for the folder it runs in, so run %s from the project folder; nothing done", o.cmd)
		}
		return o.installCodex()
	}
	return o.installClaude()
}

// exeFor is this binary's path when the integration runs it (need), else "".
func exeFor(need bool) (string, error) {
	if !need {
		return "", nil
	}
	return selfPath()
}

func (o opts) installClaude() error {
	exe, err := exeFor(o.cmd == "install")
	if err != nil {
		return err
	}
	if o.folder != "" {
		o.folder = filepath.Clean(o.folder) // with a trailing separator the project would be outside its own scope (SPEC U6)
	}
	if o.cmd != "install" {
		return install(settingsFor(o.folder), "", o.home, nil, o.migrate, os.Stdout) // no name, no folder check (SPEC U10)
	}
	if !claudeOn(cfg) {
		fmt.Println("note: this home's config.json says claude: false, so Claude Code will see this memory but its own sessions are not imported")
	}
	in, err := o.checkClaude(exe)
	if err != nil {
		return err
	}
	return in.write()
}

func (o opts) installCodex() error {
	cwd, _ := os.Getwd()
	if o.cmd == "uninstall" {
		return uninstallCodex(o.home, cwd, o.codexDir, o.migrate, os.Stdout)
	}
	exe, err := exeFor(o.mode == "hooks")
	if err != nil {
		return err
	}
	user, err := o.name() // before any check that writes (SPEC U11)
	if err != nil {
		return err
	}
	if err := installCodex(o.home, cwd, o.codexDir, o.mode, exe, user, o.migrate, os.Stdout); err != nil {
		return err
	}
	// The scope config.json holds now: the one installCodex just wrote, or the one already there (SPEC I2).
	c, err := loadConfig(o.home)
	if err != nil {
		return err
	}
	where := map[string]string{"agents": "open your Codex sessions in " + cwd + ": its AGENTS.md asks them to run trace resume",
		"hooks": "the hooks run in every Codex session once you trust them, and this memory keeps those that run in " + string(c.Scope)}[o.mode]
	startHint(os.Stdout, cfg.User, user, where)
	return o.firstImport("", "", string(c.Scope))
}

// firstImport is the import an install ends with; when it fails the integration stays installed.
func (o opts) firstImport(projects, history, scope string) error {
	fmt.Println("first import...")
	err := importOnce(o.dbPath, time.Minute, func(db *sql.DB) error {
		return runImport(db, projects, history, o.codexDir, o.archive, scope, "install")
	})
	if err != nil {
		return fmt.Errorf("the integration is installed, but the first import failed; run `trace import -home %s` again to retry:\n%w", o.home, err)
	}
	return nil
}

// cmdResume prints where the last session in this folder stopped. AGENTS.md mode: the agent runs this itself when a
// session starts, instead of a hook.
func cmdResume(o opts) error {
	if err := importOnce(o.dbPath, 5*time.Second, o.importFn("resume")); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import failed, showing what is stored:", err)
	}
	cwd, _ := os.Getwd()
	role := roleFor(cwd, projectRole(cwd))
	db, err := openReadOnly(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	skip := o.notID
	if skip == "" {
		skip = liveSession(db, o.host, role)
	}
	var buf strings.Builder
	if err := resumeBlock(db, skip, "startup", role, o.host, cwd, &buf); err != nil {
		return err
	}
	if buf.Len() == 0 {
		fmt.Printf("No earlier %s session in this folder.\n", hostName(o.host))
		explainEmptyResume(db, o, role, skip, cwd)
	}
	fmt.Print(buf.String() + keepHint(cwd)) // apart from the block, which reads as "no earlier session" when empty
	return nil
}

// explainEmptyResume says why resume showed nothing: S1 left out the one session that has something to show (SPEC
// S4), or the stored sessions are kept out of recall because they ran in a temporary folder (S6, I4).
func explainEmptyResume(db *sql.DB, o opts, role, skip, cwd string) {
	if skip != "" && o.notID == "" {
		var again strings.Builder
		if resumeBlock(db, "x", "startup", role, o.host, cwd, &again) == nil && again.Len() > 0 {
			fmt.Printf("The newest one, %s, wrote in the last two minutes, so it was taken as the session asking; run it again with `-not x` to see it.\n", skip)
			return
		}
	}
	var kept int
	db.QueryRow("select count(*) from session where host = ? and role = ? and excluded is not null", o.host, role).Scan(&kept)
	if kept > 0 {
		fmt.Printf("%d sessions here are stored but kept out of this block: %s; `trace search -all <words>` finds what is in these.\n", kept, keptOutWhy(cwd))
	}
}

// keptOutWhy is why S6's sessions are kept out: this folder's temp root and the way out when it is excluded itself;
// else only that they ran inside the system temp folder (other folders of the role, or rows with no folder).
func keptOutWhy(cwd string) string {
	if root := tempRootOf(cwd); root != "" {
		return fmt.Sprintf("they ran inside the system temp folder %s, which other sessions do not recall. If this folder is a project, run %s", root, keepCmd(cwd))
	}
	return "they ran inside the system temp folder, which other sessions do not recall"
}

// cmdImport waits for a running import: the hook that fired last must still read the newest bytes.
func cmdImport(o opts) error {
	return importOnce(o.dbPath, time.Minute, o.importFn(o.trigger))
}

// cmdRestore reads the raw copies back into the store: for a lost or broken trace.db, or a transcript the host
// deleted. The archive is the source here, so nothing is archived again; events already stored are skipped (uuid).
func cmdRestore(o opts) error {
	if st, err := os.Stat(o.archive); err != nil || !st.IsDir() {
		return fmt.Errorf("restore needs the raw copies folder; %s is not one (set -archive)", o.archive)
	}
	return importOnce(o.dbPath, time.Minute, func(db *sql.DB) error {
		// shortcut: the prompt log's copy is where the archive puts ~/.claude/history.jsonl; a -history elsewhere
		// is archived under another folder and not restored. Upgrade if anyone moves it.
		return runImport(db, o.archive, filepath.Join(o.archive, ".claude", "history.jsonl.gz"), "", "", o.scope, "restore")
	})
}

// cmdSearch reads what is new first, so the live session is searchable without a per-turn hook (cass).
func cmdSearch(o opts) error {
	if err := importNow(o.dbPath, o.importFn("search")); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import before search failed, searching what is stored:", err)
	}
	db, err := openReadOnly(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	since, until, err := searchWindow(o, time.Now())
	if err != nil {
		return err
	}
	return search(db, strings.Join(o.args, " "), o.n, o.who, since, until, o.all)
}

// cmdShow opens a search hit with what was said around it (SPEC Q3), after reading what is new.
func cmdShow(o opts) error {
	if err := importNow(o.dbPath, o.importFn("show")); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import before show failed, showing what is stored:", err)
	}
	db, err := openReadOnly(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	n := 3
	if o.given["n"] {
		n = o.n
	}
	return show(db, strings.Join(o.args, ""), n, o.all)
}

func cmdStats(o opts) error {
	db, err := openReadOnly(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return stats(db, o.home)
}

// cmdRules prints the rule card a Claude Code session in this folder gets, and why it holds what it holds. The files
// are the ones the newest Claude Code session in this folder recorded as loaded (SPEC G8).
func cmdRules(o opts) error {
	cwd, _ := os.Getwd()
	db, _ := openReadOnly(o.dbPath)
	transcript := folderTranscript(db, cwd)
	text, note := sessionCard(db, cwd, transcript, roleFor(cwd, projectRole(cwd)), "claude-code", reinjectBudget()) // a Codex card needs a session's rollout
	for _, s := range claudeSources(nil, transcript) {
		fmt.Fprintf(os.Stderr, "%7d  %s\n", len(s.text), s.label)
	}
	if db != nil {
		db.Close()
	}
	if transcript == "" {
		fmt.Fprintln(os.Stderr, "note: no Claude Code session in this folder is in the store yet, so the card has no files")
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, "note:", note)
	}
	if cfg.Reinject == nil {
		fmt.Fprintln(os.Stderr, `re-injection is off; "reinject": {} in config.json turns it on (then run trace install -host claude-code)`)
	}
	fmt.Fprintf(os.Stderr, "card: %d characters, about %d tokens; it comes back every %.0f tokens of new context\n\n",
		len(text), len(text)/4, reinjectFactor()*float64(len(text))/4)
	fmt.Print(text)
	return nil
}

// folderTranscript is the transcript of the newest Claude Code session that ran in exactly this folder ("" if none).
func folderTranscript(db *sql.DB, cwd string) string {
	if db == nil {
		return ""
	}
	rows, err := db.Query(`select coalesce(s.cwd, ''), (select f.path from event e join source_file f on f.id = e.file_id
		where e.session_id = s.id and f.path not like '%tool-results%' limit 1)
		from session s where s.host = 'claude-code' and s.parent_id is null order by s.ended_at desc`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var p sql.NullString
		rows.Scan(&c, &p)
		if p.Valid && inFolder(cwd, c) && inFolder(c, cwd) {
			return p.String
		}
	}
	return ""
}

// importOnce runs one import at a time per database (deja-vu and memsearch #788 for the lock kind). While
// another holds the lock it retries for up to wait, then exits quietly; the next import reads everything new.
func importOnce(dbPath string, wait time.Duration, run func(*sql.DB) error) error {
	return importLocked(dbPath, func(path string) (*os.File, error) { return waitLock(path, wait) }, run)
}

// importNow is importOnce for search, which does not wait: one try at the lock, and the stored rows when it is held.
func importNow(dbPath string, run func(*sql.DB) error) error {
	return importLocked(dbPath, tryLock, run)
}

// waitLock tries the lock every half second until wait has passed; nil when another still holds it.
func waitLock(path string, wait time.Duration) (*os.File, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := tryLock(path)
		if err != nil || lock != nil {
			return lock, err
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func importLocked(dbPath string, lockFn func(string) (*os.File, error), run func(*sql.DB) error) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	lock, err := lockFn(dbPath + ".lock")
	if err != nil {
		return err
	}
	if lock == nil {
		fmt.Fprintln(os.Stderr, "another import is running; skipped")
		return nil
	}
	defer lock.Close()
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := run(db); err != nil {
		return err
	}
	_, err = db.Exec("pragma wal_checkpoint(TRUNCATE)") // keep the WAL small between imports
	return err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "trace:", err)
	var own exitCodeError
	if errors.As(err, &own) {
		os.Exit(own.code)
	}
	os.Exit(1)
}

// reportedImport are the imports the user asked for, the only ones that print their counts (SPEC E4): a command's own
// first read stays quiet, and so does a hook's, whose stderr ends up in the host's transcript.
var reportedImport = map[string]bool{"manual": true, "restore": true, "install": true}

func runImport(db *sql.DB, projects, history, codex, archive, scope, trigger string) error {
	start := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec("insert into ingest_run(started_at, trigger) values (?, ?)", start, trigger)
	if err != nil {
		return err
	}
	runID, _ := res.LastInsertId()
	im := &importer{db: db, archiveDir: archive, scope: scope, touched: map[string]struct{}{}, unmapped: map[string]int{}}
	files, errs := im.importClaude(projects, history, archive, scope)
	if codex != "" {
		cf, cerrs := im.importAllCodex(codex)
		files, errs = append(files, cf...), append(errs, cerrs...)
	}
	for sig, n := range im.unmapped {
		db.Exec("insert into unmapped(signature, count, last_seen) values (?, ?, ?) on conflict(signature) do update set count = count + excluded.count, last_seen = excluded.last_seen", sig, n, start)
	}
	if err := activeTime(db, im.touched); err != nil {
		errs = append(errs, err.Error())
	}
	if err := errors.Join(markNotTypedByUser(db), markExcluded(db)); err != nil {
		errs = append(errs, err.Error())
	}
	msg := strings.Join(errs, "\n")
	db.Exec("update ingest_run set ended_at = ?, files_seen = ?, events_added = ?, error = nullif(?, '') where id = ?",
		time.Now().UTC().Format(time.RFC3339), len(files), im.added, msg, runID)
	if reportedImport[trigger] { // stderr: a SessionStart hook's stdout is the text injected into the session
		fmt.Fprintf(os.Stderr, "files %d, events added %d, sessions touched %d\n", len(files), im.added, len(im.touched))
	}
	if msg != "" {
		return fmt.Errorf("%d file(s) failed:\n%s", len(errs), msg)
	}
	return nil
}

// importClaude reads Claude Code's transcripts in scope, keeps their full tool results, then the prompt log: after
// the transcripts, so prompts whose session survives are not imported twice. It returns the files seen and the
// errors, one per failed file.
func (im *importer) importClaude(projects, history, archive, scope string) ([]string, []string) {
	files, results, err := transcriptFiles(projects, scope)
	if err != nil {
		return files, []string{err.Error()}
	}
	var errs []string
	if err := keepResults(projects, archive, results); err != nil {
		errs = append(errs, err.Error())
	}
	errs = append(errs, im.importTranscripts(files, history)...)
	return files, append(errs, im.importHistory(history)...)
}

// importTranscripts reads each Claude Code transcript but the prompt log, and returns one error per failed file.
func (im *importer) importTranscripts(files []string, history string) []string {
	var errs []string
	for _, f := range files {
		if filepath.Clean(f) == filepath.Clean(history) { // restore walks raw/, where the prompt log's copy sits (SPEC R1)
			continue
		}
		if err := im.importFile(f, "claude-code"); err != nil {
			errs = append(errs, err.Error())
			im.db.Exec("update source_file set last_error = ? where path = ?", err.Error(), f)
		}
	}
	return errs
}

// importHistory reads Claude Code's prompt log, if there is one, and returns its error.
func (im *importer) importHistory(history string) []string {
	st, err := os.Stat(history)
	if err != nil {
		return nil
	}
	if st.IsDir() { // a wrong -history says so (SPEC E1)
		return []string{history + " is a folder, not Claude Code's prompt log"}
	}
	if err := im.importFile(history, "prompt-log"); err != nil {
		return []string{err.Error()}
	}
	return nil
}

// importAllCodex reads every Codex rollout, then copies the thread names. It returns the rollouts seen and the errors.
func (im *importer) importAllCodex(codex string) ([]string, []string) {
	var errs []string
	files := codexFiles(codex)
	for _, f := range files {
		if err := im.importCodex(f); err != nil {
			errs = append(errs, err.Error())
			im.db.Exec("update source_file set last_error = ? where path = ?", err.Error(), f)
		}
	}
	if err := codexNames(im.db, codex); err != nil {
		errs = append(errs, err.Error())
	}
	return files, errs
}

var defaultKinds = "'prompt','steering','answer','refusal','reply','handback','relay','tool_call'"

// ftsQuery quotes each word so FTS5 syntax in typed words cannot break the query.
func ftsQuery(q, op string) string {
	var parts []string
	for _, w := range strings.Fields(q) {
		w = strings.Trim(w, `.,;:!?¿¡()[]{}"'`)
		if w != "" {
			parts = append(parts, `"`+strings.ReplaceAll(w, `"`, `""`)+`"`)
		}
	}
	return strings.Join(parts, " "+op+" ")
}

func search(db *sql.DB, q string, n int, who, since, until string, all bool) error {
	filter, extra := searchFilter(who, since, until, all)
	if ftsQuery(q, "AND") == "" { // empty, or punctuation alone (SPEC E2)
		if since == "" && until == "" {
			return fmt.Errorf("search needs words")
		}
		return windowList(db, n, filter, extra) // SPEC Q2
	}
	for i, t := range searchTries(q) {
		hits, err := searchTry(db, t, i, filter, append(append([]any{t.match}, extra...), n))
		if err != nil || hits > 0 {
			return err
		}
	}
	scope := "this memory"
	if !all {
		scope += " (default kinds; -all widens it)"
	}
	fmt.Printf("not found in %s as of %s\n", scope, time.Now().Format("2006-01-02 15:04"))
	return nil
}

// searchFilter is the where clause that narrows a search, and its arguments in order: by default only the kinds
// worth reading and no excluded session; who, a speaker; since and until, UTC bounds (searchWindow).
func searchFilter(who, since, until string, all bool) (string, []any) {
	filter := ""
	var args []any
	if !all {
		filter += " and e.kind in (" + defaultKinds + ") and s.excluded is null"
	}
	if who != "" {
		filter += " and e.speaker = ?"
		args = append(args, who)
	}
	if since != "" {
		filter += " and e.ts >= ?"
		args = append(args, since)
	}
	if until != "" {
		filter += " and e.ts < ?"
		args = append(args, until)
	}
	return filter, args
}

type searchTryKind struct{ table, match string }

// searchTries are the searches tried in turn until one finds something: every word, any word, then the words as
// one exact substring (three characters or more).
func searchTries(q string) []searchTryKind {
	tries := []searchTryKind{{"event_fts", ftsQuery(q, "AND")}, {"event_fts", ftsQuery(q, "OR")}}
	if len([]rune(strings.TrimSpace(q))) >= 3 {
		tries = append(tries, searchTryKind{"event_tri", `"` + strings.ReplaceAll(strings.TrimSpace(q), `"`, `""`) + `"`})
	}
	return tries
}

// searchTry runs one search and prints its hits, saying first when it is not the every-word one; it returns how
// many it printed.
func searchTry(db *sql.DB, t searchTryKind, i int, filter string, args []any) (hits int, err error) {
	rows, err := db.Query(`select e.ts, s.role, e.speaker, e.kind, e.session_id, e.seq, snippet(`+t.table+`, 0, '»', '«', '…', 40)
		from `+t.table+` f join event e on e.id = f.rowid join session s on s.id = e.session_id
		where `+t.table+` match ?`+filter+` order by bm25(`+t.table+`) / (1 + max(0, julianday('now') - julianday(e.ts)) / 90.0) limit ?`, args...) // bm25 is negative: older moves toward 0 (SPEC Q4)
	if err != nil {
		return hits, err // none: search reads the error, not the count
	}
	defer rows.Close()
	for rows.Next() {
		var ts, role, speaker, kind, sid, snip string
		var seq int
		rows.Scan(&ts, &role, &speaker, &kind, &sid, &seq, &snip)
		if hits == 0 && i > 0 {
			fmt.Printf("(no hit with every word; showing the %s)\n", map[int]string{1: "best partial matches", 2: "exact substring matches"}[i])
		}
		hits++
		hitLine(ts, role, speaker, kind, sid, seq, strings.Join(strings.Fields(snip), " "))
	}
	if hits > 0 {
		fmt.Println(showHint)
	}
	return hits, nil
}

func short(s string) string {
	return s[:min(len(s), 8)]
}

func stats(db *sql.DB, home string) error {
	q := func(title, sql string) {
		rows, err := db.Query(sql)
		if err != nil {
			fmt.Println(title, "error:", err)
			return
		}
		defer rows.Close()
		fmt.Println("--", title)
		cols, _ := rows.Columns()
		fmt.Println(strings.Join(cols, "  ")) // each table names its columns (SPEC E4)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			var parts []string
			for _, v := range vals {
				if v == nil {
					v = "-" // an empty max() or a null column, not Go's <nil> (SPEC E4)
				}
				parts = append(parts, fmt.Sprint(v))
			}
			fmt.Println(strings.Join(parts, "  "))
		}
	}
	q("sessions by host and role", "select host, role, count(*) sessions, sum(excluded is not null) excluded, min(started_at) first, max(ended_at) last from session group by 1, 2 order by 3 desc")
	q("events by speaker and kind", "select speaker, kind, count(*) events, sum(length(text)) characters from event group by 1, 2 order by 3 desc")
	q("anchors by type", "select type, count(*) anchors, count(distinct value) distinct_values from anchor group by 1 order by 2 desc")
	q("redacted events", "select redacted, count(*) events from event where redacted is not null group by 1")
	q("rows no rule stores (a new format shows up here)", "select signature, count, last_seen from unmapped order by count desc limit 15")
	q("files skipped", "select host, skipped, count(*) files from source_file where skipped is not null group by 1, 2")
	q("last runs", "select started_at started, ended_at ended, trigger, files_seen files, events_added events, error is not null error from ingest_run order by id desc limit 15")
	// Claude Code swaps hook output over 10,000 characters for a 2 KB preview, and the agent never sees the rest.
	q("hook output that never reached the session", "select count(*) count, max(ts) last from event where kind = 'injected' and ltrim(text) like '<persisted-output>%'")
	q("checks, last 14 days: runs, failures, last failure", "select name, count(*) runs, sum(ok = 0) failures, max(case when ok = 0 then ts end) last_failure from check_run where ts >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-14 days') group by 1 order by 1")
	q("rule card, last 14 days: reason, times, sessions, characters injected", "select reason, count(*) times, count(distinct session_id) sessions, sum(chars) characters from injection where ts >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-14 days') group by 1 order by 1")
	// What TRACE itself put in the agent's context (SPEC K2): since the last compaction is what is still there.
	const lastFive = `(select session_id from hook_out group by 1 order by max(ts) desc limit 5)`
	q("context TRACE wrote, last 5 sessions, by event (about 4 characters a token)", "select substr(session_id, 1, 8) session, event, count(*) calls, sum(chars) characters from hook_out where session_id in "+lastFive+" group by session_id, event order by max(ts) desc, 4 desc")
	q("context TRACE wrote, last 5 sessions", `select substr(h.session_id, 1, 8) session, count(*) calls, sum(h.chars) characters,
		sum(case when h.ts >= coalesce((select max(c.ts) from hook_out c where c.session_id = h.session_id and c.event = 'SessionStart:compact'), '') then h.chars else 0 end) since_last_compaction
		from hook_out h where h.session_id in `+lastFive+` group by h.session_id order by max(h.ts) desc`)
	// A failure a deliverer gave starts with its name (SPEC A2), so an update that breaks one shows up here (A7).
	q("alarm, each deliverer's last failure", "select substr(fire_reason, 1, instr(fire_reason, ':') - 1) deliverer, max(announced_at) last, fire_reason reason from task where fire_state = 'failed' and instr(fire_reason, ':') > 0 group by 1 order by 1")
	foldersWithoutHooks(db, home, os.Stdout)
	q("files with errors", "select path, last_error from source_file where last_error is not null")
	return nil
}
