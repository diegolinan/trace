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

// kinds are a story's kinds; story is the default, stored as none (SPEC T14, kindSQL).
var kinds = map[string]bool{"story": true, "bug": true, "chore": true, "spike": true, "impediment": true}

// kindSQL stores a kind given as ?: story, the default, as none.
const kindSQL = "nullif(nullif(?, ''), 'story')"

// fibonacci are the points a story can carry (SPEC T15).
var fibonacci = map[int]bool{1: true, 2: true, 3: true, 5: true, 8: true, 13: true}

// sized says why a task at level cannot have kind ("" for none) and points (0 for none), or nil when it can: only a
// story has a kind or points, and a spike or an impediment has no points (SPEC T14, T15).
func sized(level, kind string, points int) error {
	switch {
	case kind != "" && !kinds[kind]:
		return fmt.Errorf("%q is not a kind: story, bug, chore, spike or impediment", kind)
	case kind != "" && level != "story":
		return errors.New("only a story has a kind")
	case points == 0:
		return nil
	}
	return pointed(level, kind, points)
}

// pointed says why a task at level with kind cannot carry points, not 0, or nil when it can (SPEC T15).
func pointed(level, kind string, points int) error {
	switch {
	case !fibonacci[points]:
		return fmt.Errorf("%d is not a point: 1, 2, 3, 5, 8 or 13", points)
	case level != "story":
		return errors.New("only a story carries points")
	case kind == "spike":
		return errors.New("a spike has no points: bound it by its time (-at)")
	case kind == "impediment":
		return errors.New("an impediment has no points")
	}
	return nil
}

// readPoints reads -points or task points' number: "" is none, and a word is not a point.
func readPoints(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n == 0 {
		return n, fmt.Errorf("%s is not a point: 1, 2, 3, 5, 8 or 13", s)
	}
	return n, nil
}

// openSize is an open task's level, kind ("" for story or none) and points (0 for none).
func openSize(db *sql.DB, id string) (level, kind string, points int, err error) {
	err = db.QueryRow(`select level, coalesce(kind, ''), coalesce(points, 0) from task where id = ? and closed_at is null`, id).Scan(&level, &kind, &points)
	if err != nil {
		err = fmt.Errorf("task %s is not an open task", id)
	}
	return level, kind, points, err
}

// taskKind changes an open story's kind (SPEC T14).
func taskKind(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task kind needs a task id and a kind")}
	}
	return resize(db, args[1], w, func(level, _ string, points int) (string, any, string, error) {
		return "kind = " + kindSQL, args[2], args[2], sized(level, args[2], points)
	})
}

// taskPoints changes an open story's points (SPEC T15).
func taskPoints(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task points needs a task id and points")}
	}
	return resize(db, args[1], w, func(level, kind string, _ int) (string, any, string, error) {
		n, err := readPoints(args[2])
		if err == nil {
			err = sized(level, kind, n)
		}
		return "points = ?", n, pointsText(n), err
	})
}

// resize sets one size column of an open task as change says (set, "<column> = <expression of ?>", with value), unless
// change refuses, and prints what it said.
func resize(db *sql.DB, id string, w io.Writer, change func(level, kind string, points int) (set string, value any, said string, err error)) error {
	level, kind, points, err := openSize(db, id)
	if err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	set, value, said, err := change(level, kind, points)
	if err != nil {
		return fmt.Errorf("%v; nothing changed", err)
	}
	if _, err := db.Exec(`update task set `+set+` where id = ?`, value, id); err != nil {
		return err
	}
	fmt.Fprintf(w, "task %s: %s\n", id, said)
	return nil
}

// pointsText is "1 point" or "<n> points".
func pointsText(n int) string {
	if n == 1 {
		return "1 point"
	}
	return fmt.Sprintf("%d points", n)
}

// taskSizes prints the last 20 stories closed as done with points, for the agent to estimate against (SPEC T15).
func taskSizes(db *sql.DB, w io.Writer) error {
	rows, err := db.Query(`select id, points, coalesce(kind, 'story'), text, coalesce(started_at, created_at), closed_at,
		case when started_at is null then 'added' else 'started' end from task
		where level = 'story' and closed_how = 'done' and points is not null order by closed_at desc, id desc limit 20`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id int64
		var points int
		var kind, text, created, closed, from string
		rows.Scan(&id, &points, &kind, &text, &created, &closed, &from)
		c, _ := time.Parse(time.RFC3339, created)
		d, _ := time.Parse(time.RFC3339, closed)
		lines = append(lines, fmt.Sprintf("- task %d (%s, %s): %q, %s from %s to done", id, kind, pointsText(points), clip(text, 300), d.Sub(c).Round(time.Minute), from))
	}
	if len(lines) == 0 {
		lines = []string{"No story closed as done with points yet."}
	}
	_, err = fmt.Fprintln(w, strings.Join(lines, "\n"))
	return err
}
