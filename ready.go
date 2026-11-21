package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// SPEC T5 to T7: conditions of ready. A task can wait on another task (-after) or on anything else, in words
// (-needs). Conditions are numbered once per task, in the order given, and fixed when the task is added.

// listFlag is a flag that repeats: each use adds one value.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ", ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// exitCodeError ends the command with its own exit code (task start: 3 when not ready).
type exitCodeError struct {
	code int
	error
}

// condition is one condition to add: the task it waits on, the words of what it needs, or a criterion of done (then).
type condition struct {
	after       int64
	needs, then string
}

// conditionsFromFlags reads -after (task ids, in order), then -needs, then -then (SPEC T8); an id that is not a number
// and an empty criterion are refused.
func conditionsFromFlags(after, needs, then []string) ([]condition, error) {
	var out []condition
	for _, a := range after {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("-after %q is not a task id; nothing stored", a)
		}
		out = append(out, condition{after: id})
	}
	for _, n := range needs {
		out = append(out, condition{needs: n})
	}
	for _, c := range then {
		if strings.TrimSpace(c) == "" {
			return nil, errors.New("-then needs a criterion in words; nothing stored")
		}
		out = append(out, condition{then: c})
	}
	return out, nil
}

// checkAfter refuses an -after on an unknown or dropped task, and says whether the task it names is done (SPEC T5).
func checkAfter(db *sql.DB, conds []condition) (done map[int64]bool, err error) {
	done = map[int64]bool{}
	for _, c := range conds {
		if c.after == 0 {
			continue
		}
		var how sql.NullString
		if err := db.QueryRow(`select closed_how from task where id = ?`, c.after).Scan(&how); err != nil {
			return nil, fmt.Errorf("-after %d: no such task; nothing stored", c.after)
		}
		if how.String == "drop" {
			return nil, fmt.Errorf("-after %d: that task was dropped; nothing stored", c.after)
		}
		done[c.after] = how.String == "done"
	}
	return done, nil
}

// addConditions stores a new task's conditions; an -after on a task already done is stored as met.
func addConditions(db *sql.DB, task int64, conds []condition, done map[int64]bool) error {
	now := clockNow().UTC().Format(time.RFC3339)
	for i, c := range conds {
		met := ""
		if done[c.after] { // done holds -after ids only
			met = now
		}
		if _, err := db.Exec(`insert into task_condition(task_id, n, after_id, needs, done_when, met_at) values (?, ?, nullif(?, 0), nullif(?, ''), nullif(?, ''), nullif(?, ''))`,
			task, i+1, c.after, redacted(c.needs), redacted(c.then), met); err != nil {
			return err
		}
	}
	return nil
}

// metOnDone meets every -after condition on a task just closed as done (SPEC T5).
func metOnDone(db *sql.DB, task string) error {
	_, err := db.Exec(`update task_condition set met_at = ? where after_id = ? and met_at is null`, clockNow().UTC().Format(time.RFC3339), task)
	return err
}

// waitersOf names the open tasks with an unmet condition on task, for task drop (SPEC T6).
func waitersOf(db *sql.DB, task string) ([]string, error) {
	rows, err := db.Query(`select distinct c.task_id from task_condition c join task t on t.id = c.task_id
		where c.after_id = ? and c.met_at is null and t.closed_at is null order by c.task_id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, fmt.Sprintf("task %d waits on it", id))
	}
	return out, rows.Err()
}

// taskMet meets condition n of an open task: a -needs, or an -after whose task was dropped. It records when, the
// session and the user's newest typed message in it (SPEC T6).
func taskMet(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task met needs a task id and a condition number")}
	}
	var afterHow sql.NullString
	var after sql.NullInt64
	err := db.QueryRow(`select c.after_id, a.closed_how from task_condition c join task t on t.id = c.task_id
		left join task a on a.id = c.after_id where c.task_id = ? and c.n = ? and c.met_at is null and t.closed_at is null`,
		args[1], args[2]).Scan(&after, &afterHow)
	if err != nil {
		return fmt.Errorf("task %s has no open condition %s; nothing changed", args[1], args[2])
	}
	if after.Valid && afterHow.String != "drop" {
		return fmt.Errorf("condition %s of task %s waits on task %d, still open: it is met when that task is done; nothing changed", args[2], args[1], after.Int64)
	}
	if _, err := db.Exec(`update task_condition set met_at = ?1, met_session = nullif(?2, ''), met_event = `+newestTyped("?2")+`
		where task_id = ?3 and n = ?4`, clockNow().UTC().Format(time.RFC3339), callerSession(db), args[1], args[2]); err != nil {
		return err
	}
	fmt.Fprintf(w, "task %s: condition %s met\n", args[1], args[2])
	return nil
}

// taskStart checks a task before work on it starts: ready, and then in progress (SPEC T16), or what it waits on with
// exit 3, recording nothing (SPEC T7).
func taskStart(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 2 {
		return usageError{errors.New("task start needs a task id")}
	}
	var id int64
	var g taskTag
	if err := db.QueryRow(`select t.id, `+tagSQL+` from task t left join task p on p.id = t.parent_id where t.id = ? and t.closed_at is null`, args[1]).Scan(append([]any{&id}, g.dest()...)...); err != nil {
		return fmt.Errorf("task %s is not an open task", args[1])
	}
	name := tagged(id, g.String())
	waiting := waitingOn(db, id, 0)
	if waiting == "" {
		if err := startWork(db, id, g.level); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s is ready; now in progress\n", name)
	} else {
		fmt.Fprintf(w, "%s is not ready: %s\n", name, waiting)
	}
	if done := criteriaText(db, args[1]); done != "" {
		fmt.Fprintf(w, "done when: %s\n", done)
	}
	if waiting != "" {
		return exitCodeError{3, fmt.Errorf("task %d is not ready", id)}
	}
	return nil
}

// taskThen adds a criterion of done to an open task, with the task's next condition number (SPEC T8).
func taskThen(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 || strings.TrimSpace(args[2]) == "" {
		return usageError{errors.New(`task then needs a task id and a criterion in words; nothing stored`)}
	}
	res, err := db.Exec(`insert into task_condition(task_id, n, done_when) select id, (select coalesce(max(n), 0) + 1 from task_condition where task_id = task.id), ?
		from task where id = ? and closed_at is null`, redacted(args[2]), args[1])
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %s is not an open task; nothing stored", args[1])
	}
	id, _ := res.LastInsertId()
	var n int64
	db.QueryRow(`select n from task_condition where rowid = ?`, id).Scan(&n)
	fmt.Fprintf(w, "task %s: criterion %d added\n", args[1], n)
	return nil
}

// criteriaText is a task's unmet criteria of done as then <n>: "<words>", joined, or "" when none is unmet (SPEC T9).
func criteriaText(db *sql.DB, task string) string {
	rows, err := db.Query(`select n, done_when from task_condition where task_id = ? and done_when is not null and met_at is null order by n`, task)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var n int64
		var words string
		rows.Scan(&n, &words)
		parts = append(parts, fmt.Sprintf("then %d: %s", n, strconv.Quote(strings.Join(strings.Fields(clip(words, 300)), " "))))
	}
	return strings.Join(parts, "; ")
}

// waitingOn is "waiting on: ..." with a task's unmet conditions, at most limit (0: all) and how many more, or "" when
// it waits on nothing (SPEC T7). -needs words are clipped to one line and quoted.
func waitingOn(db *sql.DB, task int64, limit int) string {
	parts := unmetConditions(db, task, conditionText)
	if len(parts) == 0 {
		return ""
	}
	if limit > 0 && len(parts) > limit {
		parts = append(parts[:limit], fmt.Sprintf("and %d more", len(parts)-limit))
	}
	return "waiting on: " + strings.Join(parts, "; ")
}

// unmetConditions is each unmet condition of a task, in order, as show writes it (SPEC T7, T24).
func unmetConditions(db *sql.DB, task int64, show func(n, after int64, needs, how string, waits bool) string) []string {
	rows, err := db.Query(`select c.n, coalesce(c.after_id, 0), coalesce(c.needs, ''), coalesce(a.closed_how, ''),
		exists (select 1 from task_condition x where x.task_id = c.after_id and x.met_at is null)
		from task_condition c left join task a on a.id = c.after_id where c.task_id = ? and c.met_at is null and c.done_when is null order by c.n`, task)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var n, after int64
		var needs, how string
		var waits bool
		rows.Scan(&n, &after, &needs, &how, &waits)
		parts = append(parts, show(n, after, needs, how, waits))
	}
	return parts
}

// conditionText is one unmet condition as shown: need n: "<words>", or after task <id> and its state.
func conditionText(n, after int64, needs, how string, waits bool) string {
	if after == 0 {
		return fmt.Sprintf("need %d: %s", n, strconv.Quote(strings.Join(strings.Fields(clip(needs, 300)), " ")))
	}
	state := "open"
	switch {
	case how == "drop":
		state = "dropped"
	case waits:
		state = "open, itself waiting"
	}
	return fmt.Sprintf("after task %d (%s)", after, state)
}

// withWaiting adds to each task line what its task waits on, at most three conditions (SPEC T7).
func withWaiting(db *sql.DB, lines []string, ids []int64) []string {
	for i, id := range ids {
		if w := waitingOn(db, id, 3); w != "" {
			lines[i] += " " + w
		}
	}
	return lines
}
