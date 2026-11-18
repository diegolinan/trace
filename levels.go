package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// levels are a task's levels, top first; each goes only under the one before it (SPEC T10, T11).
var levels = []string{"epic", "story", "task", "subtask"}

// levelRank is a level's place in levels, or -1 for any other word.
func levelRank(level string) int {
	for i, l := range levels {
		if l == level {
			return i
		}
	}
	return -1
}

// fitsUnder says why a task at level cannot sit under a parent at parentLevel ("" for none), or nil when it can: a
// parent is exactly one level up, an epic has none, a subtask must have one (SPEC T11).
func fitsUnder(level, parentLevel string) error {
	rank := levelRank(level)
	switch {
	case rank < 0:
		return fmt.Errorf("%q is not a level: epic, story, task or subtask", level)
	case parentLevel == "" && level == "subtask":
		return errors.New("a subtask goes under a task (-under <id>)")
	case parentLevel == "":
		return nil
	case rank == 0:
		return errors.New("an epic goes under nothing")
	case levelRank(parentLevel) != rank-1:
		return fmt.Errorf("%s goes under %s, not under %s", level, levels[rank-1], parentLevel)
	}
	return nil
}

// taskTag is what shows a task's level, points, parent and progress to the agent (SPEC T13, T15, T16), scanned from
// tagSQL.
type taskTag struct {
	level, parentLevel string
	parent             int64
	points             int
	started            bool
}

// dest is where a row's tagSQL columns are scanned into.
func (g *taskTag) dest() []any {
	return []any{&g.level, &g.parentLevel, &g.parent, &g.points, &g.started}
}

// String is "story, 3 points, in epic 3, in progress", "epic, 8 points", "epic", "in progress", or "" for a task with
// no parent not started, the only kind there was before levels.
func (g taskTag) String() string {
	var parts []string
	if g.parent != 0 || g.level != "task" {
		parts = append(parts, g.level)
	}
	if g.points > 0 {
		parts = append(parts, pointsText(g.points))
	}
	if g.parent != 0 {
		parts = append(parts, fmt.Sprintf("in %s %d", g.parentLevel, g.parent))
	}
	if g.started {
		parts = append(parts, "in progress")
	}
	return strings.Join(parts, ", ")
}

// tagged is "task <id>" with its level tag in parentheses when it has one.
func tagged(id int64, tag string) string {
	if tag == "" {
		return fmt.Sprintf("task %d", id)
	}
	return fmt.Sprintf("task %d (%s)", id, tag)
}

// tagSQL selects a task t's taskTag: its level, its parent's level, its parent's id (0 for none), its points, an
// epic's being the sum of its stories' not dropped (SPEC T15), and whether it is in progress (SPEC T16); it needs
// "left join task p on p.id = t.parent_id".
const tagSQL = `t.level, coalesce(p.level, ''), coalesce(t.parent_id, 0), coalesce(case when t.level = 'epic'
	then (select sum(c.points) from task c where c.parent_id = t.id and coalesce(c.closed_how, '') != 'drop') else t.points end, 0), t.started_at is not null`

// openLevel is an open task's level, or an error naming it as not open.
func openLevel(db *sql.DB, id string) (string, error) {
	var level string
	if err := db.QueryRow(`select level from task where id = ? and closed_at is null`, id).Scan(&level); err != nil {
		return "", fmt.Errorf("task %s is not an open task", id)
	}
	return level, nil
}

// placeTask resolves task add's -level and -under: the level (below the parent's when not given, else task) and the
// parent's id (0 for none), or why it cannot be placed (SPEC T10, T11).
func placeTask(db *sql.DB, level, under string) (string, int64, error) {
	parentLevel := ""
	var parent int64
	if under != "" {
		if err := db.QueryRow(`select id, level from task where id = ? and closed_at is null`, under).Scan(&parent, &parentLevel); err != nil {
			return level, parent, fmt.Errorf("-under %s is not an open task; nothing stored", under)
		}
	}
	if level == "" {
		level = below[parentLevel]
	}
	if err := fitsUnder(level, parentLevel); err != nil {
		return level, parent, fmt.Errorf("%v; nothing stored", err)
	}
	return level, parent, nil
}

// below is the level a task takes under a parent at each level when -level is not given: task with no parent, and
// subtask under a subtask, which fitsUnder then refuses.
var below = map[string]string{"": "task", "epic": "story", "story": "task", "task": "subtask", "subtask": "subtask"}

// recordMove sets what a move or level change changed on an open task, with when and the session that ran it
// (SPEC T11).
func recordMove(db *sql.DB, set string, value any, id string) error {
	_, err := db.Exec(`update task set `+set+` = ?1, moved_at = ?2, moved_session = nullif(?3, '') where id = ?4`,
		value, clockNow().UTC().Format(time.RFC3339), callerSession(db), id)
	return err
}

// taskUnder moves an open task under another open task one level up, or out of its parent with none (SPEC T11).
func taskUnder(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task under needs a task id and a parent id or none")}
	}
	level, err := openLevel(db, args[1])
	if err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	var parent any
	parentLevel := ""
	if args[2] != "none" {
		if parentLevel, err = openLevel(db, args[2]); err != nil {
			return fmt.Errorf("%v; nothing changed", err)
		}
		parent = args[2]
	}
	if err := fitsUnder(level, parentLevel); err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	if err := recordMove(db, "parent_id", parent, args[1]); err != nil {
		return err
	}
	where := "none"
	if parent != nil {
		where = "task " + args[2]
	}
	fmt.Fprintf(w, "task %s: under %s\n", args[1], where)
	return nil
}

// taskLevel changes an open task's level, keeping its parent one level up and its open children one level down
// (SPEC T10).
func taskLevel(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task level needs a task id and a level")}
	}
	_, kind, points, err := openSize(db, args[1])
	if err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	var parentLevel string
	db.QueryRow(`select coalesce(p.level, '') from task t left join task p on p.id = t.parent_id where t.id = ?`, args[1]).Scan(&parentLevel)
	if err := fitsUnder(args[2], parentLevel); err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	if err := sized(args[2], kind, points); err != nil {
		return fmt.Errorf("%v; nothing changed", err) // a kind or points only a story has (SPEC T14, T15)
	}
	var child string
	db.QueryRow(`select level from task where parent_id = ? and closed_at is null`, args[1]).Scan(&child)
	if err := fitsUnder(child, args[2]); child != "" && err != nil {
		return fmt.Errorf("an open task under it is a %s: %v; nothing changed", child, err)
	}
	if err := recordMove(db, "level", args[2], args[1]); err != nil {
		return err
	}
	fmt.Fprintf(w, "task %s: %s\n", args[1], args[2])
	return nil
}

// openChildren is the open tasks directly under a task as task <n> (<level>): "<words>", joined, or "" (SPEC T12).
func openChildren(db *sql.DB, task string) string {
	rows, err := db.Query(`select id, level, text from task where parent_id = ? and closed_at is null order by id`, task)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id int64
		var level, text string
		rows.Scan(&id, &level, &text)
		parts = append(parts, fmt.Sprintf("task %d (%s): %q", id, level, clip(text, 300)))
	}
	return strings.Join(parts, "; ")
}

// treeOrder puts tasks in tree order: each top task (no open parent) in the order given, then its children the same
// way, depth first; depth says how deep each one sits (SPEC T13).
func treeOrder(ids, parents []int64) (order []int, depth []int) {
	at := map[int64]int{}
	for i, id := range ids {
		at[id] = i
	}
	kids := map[int64][]int{}
	var walk func(i, d int)
	walk = func(i, d int) {
		order, depth = append(order, i), append(depth, d)
		for _, k := range kids[ids[i]] {
			walk(k, d+1)
		}
	}
	var tops []int
	for i, p := range parents {
		if _, open := at[p]; open { // no task has id 0
			kids[p] = append(kids[p], i)
		} else {
			tops = append(tops, i)
		}
	}
	for _, i := range tops {
		walk(i, 0)
	}
	return order, depth
}
