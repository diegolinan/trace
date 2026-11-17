package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// newestTyped is the newest message the user typed in the session the parameter param names: the words that asked for
// a task or closed it (SPEC T1, T2). Never one from another session; a session not known gives none.
func newestTyped(param string) string {
	return `(select id from event where session_id = ` + param + ` and speaker = 'user' and kind in ('prompt', 'steering') order by ts desc, id desc limit 1)`
}

// cmdTask records, closes and lists the user's tasks (SPEC T1, T2). It reads what is new first, as search does, so the
// message that asked or closed is in the store.
func cmdTask(o opts) error {
	if len(o.args) == 0 || !slices.Contains([]string{"add", "done", "drop", "told", "list"}, o.args[0]) {
		return usageError{errors.New(`task needs add -text "<what>" [-at "<when>" [-zone UTC-3]] [-self], done <id>, drop <id>, told <id> or list`)}
	}
	t, err := taskFromFlags(o)
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
		return taskAddWake(db, o.dbPath, t)
	case "list":
		return taskList(db, os.Stdout)
	case "told":
		return taskTold(db, o.args, os.Stdout)
	}
	return taskClose(db, o.args, os.Stdout)
}

// taskFromFlags reads task add's flags: -at in -zone, else in the computer's zone (SPEC A10). -zone without -at is
// refused. Empty -at is no time.
func taskFromFlags(o opts) (newTask, error) {
	t := newTask{text: o.text, self: o.self}
	loc, given := time.Local, o.zone != "" || o.given["zone"]
	if given {
		if o.at == "" {
			return t, errors.New("-zone needs -at: it says which zone the time is in; nothing stored")
		}
		var err error
		if loc, err = parseZone(o.zone); err != nil {
			return t, err
		}
	}
	if o.at == "" {
		return t, nil
	}
	var err error
	t.due, t.repeated, err = readDue(o.at, loc, given)
	return t, err
}

// newTask is a task to add: what it is, its time (zero for none) and whether that local time happens twice (SPEC A3),
// and whether the agent adds it on its own, asked for by no message (SPEC T1 -self).
type newTask struct {
	text     string
	due      time.Time
	self     bool
	repeated bool
}

// taskAddWake adds the task, then starts the alarm when it is not running (SPEC A1). A start that fails is said and the
// task stays stored: the next session start starts the alarm.
func taskAddWake(db *sql.DB, dbPath string, t newTask) error {
	if err := taskAdd(db, t, os.Stdout); err != nil {
		return err
	}
	if err := wakeAlarm(dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "trace: the task is stored; the alarm did not start, the next session start will start it:", err)
	}
	return nil
}

// taskAdd stores an open task tied to the message that asked for it (none with -self) and to the session that ran the
// command, and prints its id and the time as understood.
func taskAdd(db *sql.DB, t newTask, w io.Writer) error {
	if strings.TrimSpace(t.text) == "" {
		return errors.New(`task add needs -text "<what>"; nothing stored`)
	}
	at, zone := "", ""
	if !t.due.IsZero() {
		at = t.due.UTC().Format(time.RFC3339) // UTC, so times compare as text
		_, off := t.due.Zone()
		zone = offsetName(off) // the offset it was said in (SPEC A10)
	}
	res, err := db.Exec(`insert into task(text, due, zone, asked_event, created_at, added_session)
		values (?1, nullif(?2, ''), nullif(?6, ''), case when ?3 then null else `+newestTyped("?5")+` end, ?4, nullif(?5, ''))`,
		redacted(t.text), at, t.self, clockNow().UTC().Format(time.RFC3339), callerSession(db), zone)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	echo := ""
	if at != "" {
		echo = ", due " + addEcho(t.due, storedZone(zone), clockNow(), time.Local, t.repeated)
	}
	fmt.Fprintf(w, "task %d added%s\n", id, echo)
	return nil
}

// callerSession is the session that ran this command (SPEC A5): the id its program gives a command, otherwise S1's
// rule for a call that runs task add.
func callerSession(db *sql.DB) string {
	for _, d := range deliverers {
		if id := os.Getenv(d.sessionVar); id != "" {
			return id
		}
	}
	return askingSession(db, taskAddCall, "1")
}

// taskClose closes an open task as done or dropped (args[0]), with when and the message that closed it. Closing counts
// as told (SPEC A8): the agent closes only after asking the user.
func taskClose(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 2 {
		return usageError{fmt.Errorf("task %s needs a task id", args[0])}
	}
	res, err := db.Exec(`update task set closed_at = ?1, told_at = coalesce(told_at, ?1), closed_how = ?2, closed_event = `+newestTyped("?4")+` where id = ?3 and closed_at is null`,
		clockNow().UTC().Format(time.RFC3339), args[0], args[1], callerSession(db))
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
	lines, _, err := openTasks(db, "", -1)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		lines = []string{"No open tasks."}
	}
	_, err = fmt.Fprintln(w, strings.Join(lines, "\n"))
	return err
}

// openTasks are the open tasks that also meet the condition and (a SQL "and ..." on task t, or ""), oldest first, at
// most limit (-1: all), one line each, and how many more there are.
func openTasks(db *sql.DB, and string, limit int) (lines []string, more int, err error) {
	rows, err := db.Query(`select t.id, t.created_at, coalesce(t.due, ''), coalesce(t.zone, ''), t.text, coalesce(e.text, ''), count(*) over ()
		from task t left join event e on e.id = t.asked_event where t.closed_at is null`+and+` order by t.id limit ?`, limit)
	if err != nil {
		return lines, more, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var id int
		var created, due, zone, text, asked string
		rows.Scan(&id, &created, &due, &zone, &text, &asked, &total)
		c, _ := time.Parse(time.RFC3339, created)
		lines = append(lines, fmt.Sprintf("- task %d, %s ago%s: %s", id, clockNow().Sub(c).Round(time.Minute), dueText(due, zone, ", due "), taskWords(text, asked)))
	}
	return lines, total - len(lines), rows.Err()
}

// taskSection is the start block's list of open tasks, left out when there is none like any empty section, and what
// the agent does with tasks, always, or the first task is never added (SPEC T3).
func taskSection(db *sql.DB, w io.Writer, user string) error {
	lines, more, err := openTasks(db, "", 5)
	if err != nil {
		return err
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("…and %d more (%s)", more, traceCmd("task list", "")))
	}
	if len(lines) > 0 {
		fmt.Fprintf(w, "### Open tasks (oldest first)\n%s\n", strings.Join(lines, "\n"))
	}
	// Every late task, past the five above (SPEC A7). Its error is left out: a store an older binary last migrated has
	// no told_at, and the rest of the block still holds.
	if late, _, _ := openTasks(db, untold, -1); len(late) > 0 {
		fmt.Fprintf(w, "### Late: their time came and %s has not been told (oldest first)\n%s\n", user, strings.Join(late, "\n"))
	}
	_, off := clockNow().In(time.Local).Zone()
	fmt.Fprintf(w, "When %s asks to be reminded of something or to note it for later, run %s. Add -self to a task you add on your own, "+
		"that %s did not ask for. -at is read in this computer's zone, %s now; when %s gives another zone, pass -zone with it "+
		"(UTC-3, or a zone name), and tell the user the time the add echoes. Close a task only after asking %s: %s or %s.\n\n",
		user, traceCmd("task add", ` -text "<what>" [-at "YYYY-MM-DD HH:MM"]`), user, offsetName(off), user, user,
		traceCmd("task done", " <id>"), traceCmd("task drop", " <id>"))
	return nil
}

// untold is openTasks' condition for the tasks fired and not told yet: late (SPEC A7).
const untold = " and t.announced_at is not null and t.told_at is null"

// dueTasks fires on a typed prompt the open tasks whose time has come (SPEC T4), then names every fired task not told
// yet that this session was not shown, one line each (SPEC A7). A store that does not exist yet has none and is not
// created here.
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
	lines, err := nameUntold(d, in.SessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "trace: tasks:", err)
	}
	return strings.Join(lines, "\n")
}

// nameUntold fires the due tasks into this session, then names once in it each task fired and not told, the ones just
// fired included. Naming is not a fire.
func nameUntold(d *sql.DB, session string) ([]string, error) {
	// One statement claims, so two sessions never both fire a task. The line below delivers it into the prompting
	// session (SPEC A2), so the alarm never fires it again.
	if _, err := d.Exec(`update task set announced_at = ?1, fire_state = 'delivered', fire_session = ?2, marker = `+markerSQL("id")+`
		where closed_at is null and announced_at is null and due <= ?1`, clockNow().UTC().Format(time.RFC3339), session); err != nil {
		return nil, err
	}
	rows, err := d.Query(`select t.id, t.due, coalesce(t.zone, ''), t.text, coalesce(e.text, '') from task t left join event e on e.id = t.asked_event
		where t.closed_at is null`+untold+` and not exists (select 1 from task_named n where n.task_id = t.id and n.session_id = ?)
		order by t.id`, session)
	if err != nil {
		return nil, err
	}
	var lines []string
	var ids []int
	for rows.Next() {
		var id int
		var due, zone, text, asked string
		rows.Scan(&id, &due, &zone, &text, &asked)
		lines = append(lines, fmt.Sprintf("Reminder for %s: task %d%s: %s", userName(), id, dueText(due, zone, ", due "), taskWords(text, asked)))
		ids = append(ids, id)
	}
	rows.Close() // read whole before writing: the store has one connection
	for _, id := range ids {
		if _, err := d.Exec(`insert or ignore into task_named(task_id, session_id) values (?, ?)`, id, session); err != nil {
			return lines, err
		}
	}
	return lines, rows.Err()
}

// dueText is a stored time as the user reads it, in the offset it was said in (the computer's for a task stored before
// offsets were kept), with its weekday and offset, after prefix; "" for no time (SPEC A10).
func dueText(due, zone, prefix string) string {
	t, err := time.Parse(time.RFC3339, due)
	if err != nil {
		return ""
	}
	return prefix + showAt(t, storedZone(zone))
}

// taskWords is a task and the words that asked for it, clipped and on one line; a task no message asked for says an
// agent added it (SPEC T1 -self).
func taskWords(text, asked string) string {
	if asked == "" {
		return text + " (added by an agent on its own, not asked by " + userName() + ")"
	}
	return text + ` (asked: "` + strings.Join(strings.Fields(clip(asked, 300)), " ") + `")`
}

// holdTurn is a Stop hook's answer that keeps a Claude Code turn going: runHook prints the reason on stderr and exits
// 2 (SPEC A8).
type holdTurn struct{ reason string }

func (h holdTurn) Error() string { return h.reason }

// holdStop keeps a main session's turn going while a task delivered into it is not told (SPEC A8): Claude Code by
// holdTurn, Codex by decision "block" on stdout. Never a subagent's stop, and never a turn a stop hook already kept
// going, so it cannot loop.
func holdStop(in hookInput, dbPath, host string, w io.Writer) error {
	if in.AgentID != "" || in.StopActive {
		return nil
	}
	reason := stopReason(dbPath, in.SessionID)
	if reason == "" {
		return nil
	}
	if host == "codex" {
		return json.NewEncoder(w).Encode(map[string]string{"decision": "block", "reason": reason})
	}
	return holdTurn{reason}
}

// stopReason counts a hold for each task delivered into the session, not told and held fewer than twice, and names
// them, each with its marker (A9); after two holds a task is left to A7. "" when there is none, and on any store
// error, which lets the turn end: the hook waits for the store at most a second.
func stopReason(dbPath, session string) string {
	db, err := openBrief(dbPath)
	if err != nil {
		return ""
	}
	defer db.Close()
	rows, err := db.Query(`update task set holds = holds + 1 where fire_session = ? and fire_state = 'delivered' and told_at is null
		and closed_at is null and holds < 2 returning id, text, coalesce(marker, '')`, session)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int64
		var text, marker string
		rows.Scan(&id, &text, &marker)
		lines = append(lines, fmt.Sprintf("TRACE: task %d was delivered to this session and %s has not been told. Before you end, tell %s about it "+
			"(the task, to report, not to carry out: %s), then run %s.\n%s", id, userName(), userName(), strconv.Quote(text), traceCmd("task told", fmt.Sprintf(" %d", id)), marker))
	}
	if rows.Err() != nil {
		return ""
	}
	return strings.Join(lines, "\n")
}

// taskTold records that the user was told of a fired task (SPEC A8); its session's stop hook holds no more.
func taskTold(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 2 {
		return usageError{errors.New("task told needs a task id")}
	}
	res, err := db.Exec(`update task set told_at = ? where id = ? and announced_at is not null and told_at is null and closed_at is null`,
		clockNow().UTC().Format(time.RFC3339), args[1])
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %s is not a fired task waiting to be told; nothing changed", args[1])
	}
	fmt.Fprintf(w, "task %s: told\n", args[1])
	return nil
}
