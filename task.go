package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// newestTyped is the newest message the user typed, in any session of the store: the words that asked for a task or
// closed it (SPEC T1, T2).
const newestTyped = `(select id from event where speaker = 'user' and kind in ('prompt', 'steering') order by ts desc, id desc limit 1)`

// cmdTask records, closes and lists the user's tasks (SPEC T1, T2). It reads what is new first, as search does, so the
// message that asked or closed is in the store.
func cmdTask(o opts) error {
	if len(o.args) == 0 || !slices.Contains([]string{"add", "done", "drop", "list"}, o.args[0]) {
		return usageError{errors.New(`task needs add -text "<what>" [-at "<when>"], done <id>, drop <id> or list`)}
	}
	due, err := parseAt(o.at)
	if err != nil {
		return err
	}
	if err := importNow(o.dbPath, o.importFn("task")); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import before task failed, using what is stored:", err)
	}
	db, err := openDB(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	switch o.args[0] {
	case "add":
		return taskAdd(db, o.text, due, os.Stdout)
	case "list":
		return taskList(db, os.Stdout)
	}
	return taskClose(db, o.args, os.Stdout)
}

// parseAt reads -at: YYYY-MM-DD HH:MM in local time, or RFC 3339. Empty is no time.
func parseAt(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, fmt.Errorf("-at %q is not a time: give YYYY-MM-DD HH:MM (local time) or RFC 3339; nothing stored", s)
	}
	return t, nil
}

// taskAdd stores an open task tied to the message that asked for it, and prints its id and the time as understood.
func taskAdd(db *sql.DB, text string, due time.Time, w io.Writer) error {
	if strings.TrimSpace(text) == "" {
		return errors.New(`task add needs -text "<what>"; nothing stored`)
	}
	at := ""
	if !due.IsZero() {
		at = due.UTC().Format(time.RFC3339) // UTC, so times compare as text
	}
	res, err := db.Exec(`insert into task(text, due, asked_event, created_at) values (?, nullif(?, ''), `+newestTyped+`, ?)`,
		redacted(text), at, clockNow().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	fmt.Fprintf(w, "task %d added%s\n", id, dueText(at, ", due "))
	return nil
}

// taskClose closes an open task as done or dropped (args[0]), with when and the message that closed it.
func taskClose(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 2 {
		return usageError{fmt.Errorf("task %s needs a task id", args[0])}
	}
	res, err := db.Exec(`update task set closed_at = ?, closed_how = ?, closed_event = `+newestTyped+` where id = ? and closed_at is null`,
		clockNow().UTC().Format(time.RFC3339), args[0], args[1])
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %s is not an open task; nothing changed", args[1])
	}
	fmt.Fprintf(w, "task %s closed: %s\n", args[1], args[0])
	return nil
}

// taskList prints every open task, oldest first.
func taskList(db *sql.DB, w io.Writer) error {
	lines, _, err := openTasks(db, -1)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		lines = []string{"No open tasks."}
	}
	_, err = fmt.Fprintln(w, strings.Join(lines, "\n"))
	return err
}

// openTasks are the open tasks, oldest first, at most limit (-1: all), one line each, and how many more there are.
func openTasks(db *sql.DB, limit int) (lines []string, more int, err error) {
	rows, err := db.Query(`select t.id, t.created_at, coalesce(t.due, ''), t.text, coalesce(e.text, ''), count(*) over ()
		from task t left join event e on e.id = t.asked_event where t.closed_at is null order by t.id limit ?`, limit)
	if err != nil {
		return lines, more, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var id int
		var created, due, text, asked string
		rows.Scan(&id, &created, &due, &text, &asked, &total)
		c, _ := time.Parse(time.RFC3339, created)
		lines = append(lines, fmt.Sprintf("- task %d, %s ago%s: %s", id, clockNow().Sub(c).Round(time.Minute), dueText(due, ", due "), taskWords(text, asked)))
	}
	return lines, total - len(lines), rows.Err()
}

// taskSection is the start block's list of open tasks, left out when there is none like any empty section, and what
// the agent does with tasks, always, or the first task is never added (SPEC T3).
func taskSection(db *sql.DB, w io.Writer, user string) error {
	lines, more, err := openTasks(db, 5)
	if err != nil {
		return err
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("…and %d more (%s)", more, traceCmd("task list", "")))
	}
	if len(lines) > 0 {
		fmt.Fprintf(w, "### Open tasks (oldest first)\n%s\n", strings.Join(lines, "\n"))
	}
	fmt.Fprintf(w, "When %s asks to be reminded of something or to note it for later, run %s. Close a task only after asking %s: %s or %s.\n\n",
		user, traceCmd("task add", ` -text "<what>" [-at "YYYY-MM-DD HH:MM"]`), user, traceCmd("task done", " <id>"), traceCmd("task drop", " <id>"))
	return nil
}

// dueTasks names the open tasks whose time has come and not yet announced, and marks them announced, on a typed
// prompt (SPEC T4). A store that does not exist yet has none and is not created here.
func dueTasks(in hookInput, store *lazyStore) string {
	if in.Event != "UserPromptSubmit" {
		return ""
	}
	if _, err := os.Stat(store.path); err != nil {
		return ""
	}
	d := store.open()
	if d == nil {
		return ""
	}
	// One statement claims and reads, so two sessions never both announce a task.
	rows, err := d.Query(`update task set announced_at = ?1 where closed_at is null and announced_at is null and due <= ?1
		returning id, due, text, coalesce((select text from event where id = asked_event), '')`, clockNow().UTC().Format(time.RFC3339))
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace: tasks:", err)
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int
		var due, text, asked string
		rows.Scan(&id, &due, &text, &asked)
		lines = append(lines, fmt.Sprintf("Reminder for %s: task %d%s: %s", userName(), id, dueText(due, ", due "), taskWords(text, asked)))
	}
	return strings.Join(lines, "\n")
}

// dueText is a stored time as the user reads it, local and with its weekday, after prefix; "" for no time.
func dueText(due, prefix string) string {
	t, err := time.Parse(time.RFC3339, due)
	if err != nil {
		return ""
	}
	return prefix + t.Local().Format("Monday 2006-01-02 15:04")
}

// taskWords is a task and the words that asked for it, clipped and on one line.
func taskWords(text, asked string) string {
	return text + ` (asked: "` + strings.Join(strings.Fields(clip(asked, 300)), " ") + `")`
}
