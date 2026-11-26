package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// tempRoots are the system temp folders: a session that started inside one is excluded, kept out of what other
// sessions recall (SPEC I4, I17). Tests replace them.
var tempRoots = systemTempRoots()

// systemTempRoots are os.TempDir() as given and resolved (a Windows 8.3 short name expanded, macOS /var read as
// /private/var), and the Unix temp folders whatever TMPDIR says.
func systemTempRoots() []string {
	roots := []string{os.TempDir()}
	if r, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		roots = append(roots, r)
	}
	return append(roots, "/tmp", "/private/tmp", "/var/folders", "/private/var/folders")
}

// tempVerdict is the temp root a folder lies inside, and the scope or keep entry that exempts it, if any. folder is a
// path or a Claude Code transcript folder name; both are compared by their normalized name (folderName).
func tempVerdict(folder string) (root, exempt string) {
	name := folderName(folder)
	if root = firstInside(name, tempRoots); root != "" {
		exempt = firstInside(name, explicitFolders())
	}
	return root, exempt
}

// excludedFor says why a session that started in folder is kept out of recall, or "" (SPEC I4).
func excludedFor(folder string) string {
	if root := tempRootOf(folder); root != "" {
		return "inside the system temp folder " + root
	}
	return ""
}

// tempRootOf is the temp root that excludes folder: "" when it lies inside none, or a scope or keep entry exempts it.
func tempRootOf(folder string) string {
	if root, exempt := tempVerdict(folder); exempt == "" {
		return root
	}
	return ""
}

// firstInside is the first of folders that name (a normalized folder name) is or lies inside.
// shortcut: a normalized name has lost its separators, so a sibling whose name extends a folder's (D:\Temp-x next to
// D:\Temp, proj-x next to proj) counts as inside it; upgrade to real path segments if a user hits it.
func firstInside(name string, folders []string) string {
	for _, f := range folders {
		if n := folderName(filepath.Clean(f)); name == n || strings.HasPrefix(name, n+"-") {
			return f
		}
	}
	return ""
}

// explicitFolders are the whole folders the person named: absolute scope entries (install -folder saves one) and keep
// entries, cleaned. A fragment of a name or a volume root names no folder.
// shortcut: inScope matches a scope entry anywhere in a name, so a fragment entry still keeps a temp-folder session in
// scope (stored, excluded) without exempting it; upgrade if fragments should exempt too.
func explicitFolders() (out []string) {
	for _, e := range append(strings.Split(string(cfg.Scope), ","), cfg.Keep...) {
		if e = filepath.Clean(strings.TrimSpace(e)); filepath.IsAbs(e) && filepath.Dir(e) != e {
			out = append(out, e)
		}
	}
	return out
}

// markExcluded decides I4 again, on every import, for every session with a folder, from its stored cwd alone: a keep
// entry or a scope reaches the sessions stored before it, and rows from the old name rules are mended. Only rows whose
// value changes are written; a session with no folder keeps what it has (SPEC I18).
func markExcluded(db *sql.DB) error {
	changed, err := excludedChanges(db)
	if err != nil || len(changed) == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, why := range changed {
		if _, err := tx.Exec("update session set excluded = nullif(?, '') where id = ?", why, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// excludedChanges are the sessions whose stored I4 value differs from what their cwd gives now, with the new value.
func excludedChanges(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("select id, cwd, coalesce(excluded, '') from session where coalesce(cwd, '') <> ''")
	if err != nil {
		return nil, err
	}
	changed, verdict := map[string]string{}, map[string]string{} // many sessions share a few folders: decide each once
	for rows.Next() {
		var id, cwd, was string
		if rows.Scan(&id, &cwd, &was) != nil {
			continue
		}
		why, seen := verdict[cwd]
		if !seen {
			why = excludedFor(cwd)
			verdict[cwd] = why
		}
		if why != was {
			changed[id] = why
		}
	}
	return changed, errors.Join(rows.Err(), rows.Close())
}

// keepHint is the line a session that started inside the system temp folder gets in its start block and in trace
// resume, in a memory of every project, unless a program started it: the way out when it is a project (SPEC S7).
// shortcut: a Codex thread a program started (exec, mcp) is known only from its rollout, not at hook time, so it gets
// the line too; upgrade by storing the thread's source on the session.
func keepHint(cwd string) string {
	root := tempRootOf(cwd)
	if root == "" || strings.TrimSpace(string(cfg.Scope)) != "" || programEntry(os.Getenv("CLAUDE_CODE_ENTRYPOINT")) {
		return ""
	}
	return fmt.Sprintf("%s is inside the system temp folder %s, so other sessions do not recall it; if %s says it is a project, run %s.\n",
		cwd, root, userName(), keepCmd(cwd))
}

func keepCmd(folder string) string { return traceCmd("keep", ` "`+folder+`"`) }

// cmdKeep saves a folder as config.json "keep", so sessions inside it are recalled although it lies inside the system
// temp folder (SPEC I19). A person runs it, or an agent on the person's word; no hook does.
func cmdKeep(o opts) error {
	if len(o.args) != 1 {
		return usageError{errors.New("keep takes one folder: trace keep <folder>")}
	}
	folder := filepath.Clean(o.args[0])
	if err := keepable(folder); err != nil {
		return err
	}
	keep := cfg.Keep
	if !slices.ContainsFunc(keep, func(k string) bool { return folderName(filepath.Clean(k)) == folderName(folder) }) {
		keep = append(slices.Clone(keep), folder)
	}
	plan, err := planConfig(o.home, map[string]any{"keep": keep})
	if err != nil {
		return err
	}
	return writeConfig(plan, os.Stdout)
}

// keepable refuses a folder keep cannot mean: a relative path, a volume root, or a whole system temp root.
func keepable(folder string) error {
	switch {
	case !filepath.IsAbs(folder):
		return fmt.Errorf("keep needs an absolute folder, not %q; nothing written", folder)
	case filepath.Dir(folder) == folder || isTempRoot(folder):
		return fmt.Errorf("%s is a volume root or the system temp folder itself, not a project; keep the project folder inside it; nothing written", folder)
	}
	return nil
}

// isTempRoot says whether folder is one of the system temp roots itself.
func isTempRoot(folder string) bool {
	return slices.ContainsFunc(tempRoots, func(r string) bool { return folderName(filepath.Clean(r)) == folderName(filepath.Clean(folder)) })
}

// cmdCheckIgnore says whether a session that starts in a folder is excluded, and why (SPEC I20). It writes nothing.
// -v is git's form; the answer always says why.
func cmdCheckIgnore(o opts) error {
	if len(o.args) != 1 {
		return usageError{errors.New("check-ignore takes one folder: trace check-ignore -v <folder>")}
	}
	folder, err := filepath.Abs(o.args[0])
	if err != nil {
		return err
	}
	fmt.Println(ignoreVerdict(folder))
	return nil
}

// ignoreVerdict is check-ignore's answer for an absolute folder.
func ignoreVerdict(folder string) string {
	root, exempt := tempVerdict(folder)
	switch {
	case root == "":
		return folder + ": recalled: inside no system temp folder"
	case exempt != "":
		return fmt.Sprintf("%s: recalled: inside the system temp folder %s, but kept by the scope or keep entry %s", folder, root, exempt)
	}
	return fmt.Sprintf("%s: excluded: inside the system temp folder %s, so other sessions do not recall it; to recall it, run %s", folder, root, keepCmd(folder))
}
