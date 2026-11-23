package main

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// settingsFor is the settings file install writes: the user settings, or with -folder that folder's
// .claude/settings.local.json, which must be an existing absolute folder in scope (SPEC H1).
func settingsFor(folder string) (string, error) {
	if folder == "" {
		return claudeSettings(), nil
	}
	if fi, err := os.Stat(folder); !filepath.IsAbs(folder) || err != nil || !fi.IsDir() {
		return "", fmt.Errorf("-folder %q must be an existing absolute folder; nothing done", folder)
	}
	if !inScope(folder, string(cfg.Scope)) {
		return "", fmt.Errorf("-folder %s is outside this home's scope (%s), so TRACE would do nothing there; nothing done", folder, cfg.Scope)
	}
	return filepath.Join(folder, ".claude", "settings.local.json"), nil
}

// runsHome says whether the settings file at path runs hooks of home.
func runsHome(path, home string) bool {
	s, _, _ := readSettings(path) // a missing or unreadable file is no settings, so no hooks
	own, _, _ := traceHooks(s, home)
	return own > 0
}

// foldersWithoutHooks lists the folders in scope where main Claude Code sessions started in the last 30 days and no
// settings file Claude Code reads there runs this home's hooks (SPEC H2).
func foldersWithoutHooks(db *sql.DB, home string, w io.Writer) {
	fmt.Fprintln(w, "-- folders where Claude Code sessions started in the last 30 days without this home's hooks")
	fmt.Fprintln(w, "folder  sessions  last")
	if runsHome(claudeSettings(), home) {
		return
	}
	rows, err := db.Query(`select cwd, count(*), max(started_at) from session where host = 'claude-code' and parent_id is null
		and role != 'subagent' and excluded is null and cwd is not null and started_at >= strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-30 days')
		group by cwd order by 3 desc`)
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var cwd, last string
		var n int
		rows.Scan(&cwd, &n, &last)
		if inScope(cwd, string(cfg.Scope)) && !runsHome(filepath.Join(cwd, ".claude", "settings.json"), home) &&
			!runsHome(filepath.Join(cwd, ".claude", "settings.local.json"), home) {
			fmt.Fprintf(w, "%s  %d  %s\n", cwd, n, last)
		}
	}
}
