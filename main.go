// TRACE, Transcript Recall And Continuity Engine: a coding agent's own memory. A memory trace is neuroscience's
// name for the physical mark a memory leaves, the engram. Every Claude Code session in scope is copied verbatim
// into one SQLite file, by code, with no model in the loop; hooks show the next session where the last one stopped.
// Settings live in <home>/config.json (config.go).
//
// Usage:
//
//	trace install           add the hooks to Claude Code's user settings, then import (uninstall removes them)
//	trace install -host codex   a Codex-only memory for the current folder: config.json and an AGENTS.md line
//	trace import            read new bytes of every transcript in scope; safe to run any time
//	trace hook              for host hooks: starts a detached import and returns at once, always exit 0
//	trace resume            where the last session in this checkout stopped, for agents without hooks (-host codex)
//	trace search <words>    [-n 10] [-who user|assistant|subagent] [-all] [-since 2026-11-01]
//	trace rules             the rule card a session in this folder gets (re-injection, opt-in)
//	trace stats
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: trace install | uninstall | import | hook | resume | search <words> | rules | stats")
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	userHome, _ := os.UserHomeDir()
	home := traceHome()
	var err error
	if cfg, err = loadConfig(home); err != nil {
		fmt.Fprintln(os.Stderr, "trace: config.json is not valid JSON, nothing done:", err)
		if cmd == "hook" {
			return // fail open: never block the host's turn
		}
		os.Exit(1)
	}
	data := dataDir(home, cfg)
	dbPath := fs.String("db", filepath.Join(data, "trace.db"), "database file")
	claudeProjects, claudeHistory := filepath.Join(userHome, ".claude", "projects"), filepath.Join(userHome, ".claude", "history.jsonl")
	if cfg.Claude != nil && !*cfg.Claude {
		claudeProjects, claudeHistory = "", ""
	}
	projects := fs.String("projects", claudeProjects, "Claude Code transcripts folder (empty: none)")
	history := fs.String("history", claudeHistory, "Claude Code prompt log (empty: none)")
	codexDir := fs.String("codex", codexHome(), "Codex home, for its session rollouts (empty: no Codex)")
	host := fs.String("host", "codex", "resume: which agent's sessions to show (codex or claude-code)")
	notID := fs.String("not", "", "resume: a session id to leave out (default: the session written to in the last two minutes)")
	archive := fs.String("archive", filepath.Join(data, "raw"), "raw copies folder (empty: no copy)")
	repo := fs.String("repo", cfg.Repo, "git repository for commit checks (empty: none)")
	scope := fs.String("scope", string(cfg.Scope), "folders in scope, comma-separated (empty: all)")
	trigger := fs.String("trigger", "manual", "what started this import (hook name)")
	n := fs.Int("n", 10, "hits")
	who := fs.String("who", "", "only this speaker")
	all := fs.Bool("all", false, "also search thinking, tool results, summaries, injected text and excluded sessions")
	since := fs.String("since", "", "only events on or after this date (YYYY-MM-DD)")
	fs.Parse(os.Args[2:])
	cfg.Repo = *repo
	compileConfig()

	switch cmd {
	case "hook":
		// Fail open: a hook error must never block or fail the host's turn.
		if err := hook(readHookInput(os.Stdin), os.Args[2:], *dbPath, func(db *sql.DB, trigger string) error {
			return runImport(db, *projects, *history, *codexDir, *archive, *repo, *scope, trigger)
		}, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "trace hook:", err)
		}
		return
	case "install", "uninstall":
		hostSet := false // -host defaults to codex for resume; install only means Codex when asked for
		fs.Visit(func(f *flag.Flag) { hostSet = hostSet || f.Name == "host" })
		if cmd == "install" && hostSet && *host == "codex" {
			cwd, _ := os.Getwd()
			if err = installCodex(home, cwd, *codexDir, os.Stdout); err != nil {
				break
			}
			fmt.Println("first import (later ones run from trace resume)...")
			err = importOnce(*dbPath, time.Minute, func(db *sql.DB) error {
				return runImport(db, "", "", *codexDir, *archive, *repo, cwd, "install")
			})
			break
		}
		exe := ""
		if cmd == "install" {
			if exe, err = selfPath(); err != nil {
				break
			}
		}
		if err = install(claudeSettings(), exe, optionalHooks(cfg), os.Stdout); err != nil || cmd == "uninstall" {
			break
		}
		fmt.Println("first import (later ones run from the hooks)...")
		err = importOnce(*dbPath, time.Minute, func(db *sql.DB) error {
			return runImport(db, *projects, *history, *codexDir, *archive, *repo, *scope, "install")
		})
	case "resume":
		// For hosts that run no hooks (Codex Desktop): the agent runs this itself when a session starts.
		if ierr := importOnce(*dbPath, 5*time.Second, func(db *sql.DB) error {
			return runImport(db, *projects, *history, *codexDir, *archive, *repo, *scope, "resume")
		}); ierr != nil {
			fmt.Fprintln(os.Stderr, "trace: import failed, showing what is stored:", ierr)
		}
		cwd, _ := os.Getwd()
		role := roleFor(cwd, projectRole(cwd))
		var db *sql.DB
		if db, err = openReadOnly(*dbPath); err == nil {
			skip := *notID
			if skip == "" {
				skip = liveSession(db, *host, role)
			}
			var buf strings.Builder
			if err = resumeBlock(db, skip, "startup", role, *host, &buf); err == nil {
				if buf.Len() == 0 {
					fmt.Printf("No earlier %s session in this checkout.\n", hostName(*host))
				}
				fmt.Print(buf.String())
			}
			db.Close()
		}
	case "import":
		// Waits for a running import: the hook that fired last must still read the newest bytes.
		err = importOnce(*dbPath, time.Minute, func(db *sql.DB) error {
			return runImport(db, *projects, *history, *codexDir, *archive, *repo, *scope, *trigger)
		})
	case "search", "stats":
		if cmd == "search" {
			// Read what is new first, so the live session is searchable without a per-turn hook (cass).
			if ierr := importOnce(*dbPath, 0, func(db *sql.DB) error {
				return runImport(db, *projects, *history, *codexDir, *archive, *repo, *scope, "search")
			}); ierr != nil {
				fmt.Fprintln(os.Stderr, "trace: import before search failed, searching what is stored:", ierr)
			}
		}
		var db *sql.DB
		if db, err = openReadOnly(*dbPath); err == nil {
			if cmd == "search" {
				err = search(db, strings.Join(fs.Args(), " "), *n, *who, *since, *all)
			} else {
				err = stats(db)
			}
			db.Close()
		}
	case "rules":
		// The rule card a Claude Code session in this folder gets, and why it holds what it holds.
		cwd, _ := os.Getwd()
		db, _ := openReadOnly(*dbPath)
		text, note := sessionCard(db, cwd, roleFor(cwd, projectRole(cwd)))
		if db != nil {
			db.Close()
		}
		home, _ := os.UserHomeDir()
		for _, s := range claudeSources(cwd, home) {
			fmt.Fprintf(os.Stderr, "%7d  %s\n", len(s.text), s.label)
		}
		if note != "" {
			fmt.Fprintln(os.Stderr, "note:", note)
		}
		if cfg.Reinject == nil {
			fmt.Fprintln(os.Stderr, `re-injection is off; "reinject": {} in config.json turns it on (then run trace install)`)
		}
		fmt.Fprintf(os.Stderr, "card: %d characters, about %d tokens; it comes back every %.0f tokens of new context\n\n",
			len(text), len(text)/4, cfg.Reinject.factor()*float64(len(text))/4)
		fmt.Print(text)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fail(err)
	}
}

// importOnce runs one import at a time per database (deja-vu and memsearch #788 for the lock kind). While
// another holds the lock it retries for up to wait, then exits quietly; the next import reads everything new.
func importOnce(dbPath string, wait time.Duration, run func(*sql.DB) error) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	var lock *os.File
	for {
		var err error
		if lock, err = tryLock(dbPath + ".lock"); err != nil {
			return err
		}
		if lock != nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "another import is running; skipped")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
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
	os.Exit(1)
}

func runImport(db *sql.DB, projects, history, codex, archive, repo, scope, trigger string) error {
	start := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec("insert into ingest_run(started_at, trigger) values (?, ?)", start, trigger)
	if err != nil {
		return err
	}
	runID, _ := res.LastInsertId()
	im := &importer{db: db, archiveDir: archive, repo: repo, scope: scope, shaCache: map[string]string{}, touched: map[string]bool{}, unmapped: map[string]int{}}
	defer im.closeGit()
	files, results, err := transcriptFiles(projects, scope)
	var errs []string
	if err == nil {
		if err := keepResults(projects, archive, results); err != nil {
			errs = append(errs, err.Error())
		}
		for _, f := range files {
			if _, err := im.importFile(f, "claude-code"); err != nil {
				errs = append(errs, err.Error())
				db.Exec("update source_file set last_error = ? where path = ?", err.Error(), f)
			}
		}
		// After the transcripts, so prompts whose session survives are not imported twice.
		if _, err := os.Stat(history); err == nil {
			if _, err := im.importFile(history, "prompt-log"); err != nil {
				errs = append(errs, err.Error())
			}
		}
	} else {
		errs = append(errs, err.Error())
	}
	if codex != "" {
		cf := codexFiles(codex)
		for _, f := range cf {
			if err := im.importCodex(f); err != nil {
				errs = append(errs, err.Error())
				db.Exec("update source_file set last_error = ? where path = ?", err.Error(), f)
			}
		}
		files = append(files, cf...)
		if err := codexNames(db, codex); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for sig, n := range im.unmapped {
		db.Exec("insert into unmapped(signature, count, last_seen) values (?, ?, ?) on conflict(signature) do update set count = count + excluded.count, last_seen = excluded.last_seen", sig, n, start)
	}
	if err := activeTime(db, im.touched); err != nil {
		errs = append(errs, err.Error())
	}
	msg := strings.Join(errs, "\n")
	db.Exec("update ingest_run set ended_at = ?, files_seen = ?, events_added = ?, error = nullif(?, '') where id = ?",
		time.Now().UTC().Format(time.RFC3339), len(files), im.added, msg, runID)
	if trigger != "search" { // stderr: a SessionStart hook's stdout is the text injected into the session
		fmt.Fprintf(os.Stderr, "files %d, events added %d, sessions touched %d\n", len(files), im.added, len(im.touched))
	}
	if msg != "" {
		return fmt.Errorf("%d file(s) failed:\n%s", len(errs), msg)
	}
	return nil
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

func search(db *sql.DB, q string, n int, who, since string, all bool) error {
	if strings.TrimSpace(q) == "" {
		return fmt.Errorf("search needs words")
	}
	filter := ""
	if !all {
		filter += " and e.kind in (" + defaultKinds + ") and s.excluded is null"
	}
	args := func(match string) []any {
		a := []any{match}
		if who != "" {
			a = append(a, who)
		}
		if since != "" {
			a = append(a, since)
		}
		return append(a, n)
	}
	if who != "" {
		filter += " and e.speaker = ?"
	}
	if since != "" {
		filter += " and e.ts >= ?"
	}
	tries := []struct{ table, match string }{
		{"event_fts", ftsQuery(q, "AND")},
		{"event_fts", ftsQuery(q, "OR")},
	}
	if len([]rune(strings.TrimSpace(q))) >= 3 {
		tries = append(tries, struct{ table, match string }{"event_tri", `"` + strings.ReplaceAll(strings.TrimSpace(q), `"`, `""`) + `"`})
	}
	for i, t := range tries {
		rows, err := db.Query(`select e.ts, s.role, e.speaker, e.kind, e.session_id, e.seq, snippet(`+t.table+`, 0, '»', '«', '…', 40)
			from `+t.table+` f join event e on e.id = f.rowid join session s on s.id = e.session_id
			where `+t.table+` match ?`+filter+` order by bm25(`+t.table+`) limit ?`, args(t.match)...)
		if err != nil {
			return err
		}
		hits := 0
		for rows.Next() {
			var ts, role, speaker, kind, sid, snip string
			var seq int
			rows.Scan(&ts, &role, &speaker, &kind, &sid, &seq, &snip)
			if hits == 0 && i > 0 {
				fmt.Printf("(no hit with every word; showing the %s)\n", map[int]string{1: "best partial matches", 2: "exact substring matches"}[i])
			}
			hits++
			when := ts
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				when = t.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%s  %s/%s  %s  [%s #%d]\n  %s\n", when, speaker, kind, role, short(sid), seq, strings.Join(strings.Fields(snip), " "))
		}
		rows.Close()
		if hits > 0 {
			return nil
		}
	}
	scope := "this memory"
	if !all {
		scope += " (default kinds; -all widens it)"
	}
	fmt.Printf("not found in %s as of %s\n", scope, time.Now().Format("2006-01-02 15:04"))
	return nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func stats(db *sql.DB) error {
	q := func(title, sql string) {
		rows, err := db.Query(sql)
		if err != nil {
			fmt.Println(title, "error:", err)
			return
		}
		defer rows.Close()
		fmt.Println("--", title)
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			var parts []string
			for _, v := range vals {
				parts = append(parts, fmt.Sprint(v))
			}
			fmt.Println(strings.Join(parts, "  "))
		}
	}
	q("sessions by host and role", "select host, role, count(*), sum(excluded is not null), min(started_at), max(ended_at) from session group by 1, 2 order by 3 desc")
	q("events by speaker and kind", "select speaker, kind, count(*), sum(length(text)) from event group by 1, 2 order by 3 desc")
	q("anchors by type", "select type, count(*), count(distinct value) from anchor group by 1 order by 2 desc")
	q("redacted events", "select redacted, count(*) from event where redacted is not null group by 1")
	q("rows no rule stores (a new format shows up here)", "select signature, count, last_seen from unmapped order by count desc limit 15")
	q("files skipped", "select host, skipped, count(*) from source_file where skipped is not null group by 1, 2")
	q("last runs", "select started_at, ended_at, trigger, files_seen, events_added, error is not null from ingest_run order by id desc limit 15")
	// Claude Code swaps hook output over 10,000 characters for a 2 KB preview, and the agent never sees the rest.
	q("hook output that never reached the session", "select count(*), max(ts) from event where kind = 'injected' and ltrim(text) like '<persisted-output>%'")
	q("checks, last 14 days: runs, failures, last failure", "select name, count(*), sum(ok = 0), max(case when ok = 0 then ts end) from check_run where ts >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-14 days') group by 1 order by 1")
	q("rule card, last 14 days: reason, times, sessions, characters injected", "select reason, count(*), count(distinct session_id), sum(chars) from injection where ts >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-14 days') group by 1 order by 1")
	q("files with errors", "select path, last_error from source_file where last_error is not null")
	return nil
}
