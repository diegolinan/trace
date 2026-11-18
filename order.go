package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// rankSQL is the rank a task added now takes: after every other, so last among the open tasks under its parent
// (SPEC T17).
const rankSQL = `(select coalesce(max(rank), 0) + 1 from task)`

// taskOrder moves an open task to place n among the open tasks under the same parent, shifting the rest (SPEC T17).
func taskOrder(db *sql.DB, args []string, w io.Writer) error {
	if len(args) < 3 {
		return usageError{errors.New("task order needs a task id and a place")}
	}
	var id int64
	var parent sql.NullInt64
	if err := db.QueryRow(`select id, parent_id from task where id = ? and closed_at is null`, args[1]).Scan(&id, &parent); err != nil {
		return fmt.Errorf("task %s is not an open task; nothing changed", args[1])
	}
	ids, err := siblings(db, parent, id)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(args[2])
	if err != nil || n < 1 || n > len(ids)+1 {
		return fmt.Errorf("place %s is not 1 to %d, the open tasks there; nothing changed", args[2], len(ids)+1)
	}
	ids = append(ids[:n-1], append([]int64{id}, ids[n-1:]...)...)
	for i, s := range ids {
		if _, err := db.Exec(`update task set rank = ? where id = ?`, i, s); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "task %d: %d of %d\n", id, n, len(ids))
	return nil
}

// siblings are the open tasks under parent (none when not valid), in order, but task.
func siblings(db *sql.DB, parent sql.NullInt64, task int64) ([]int64, error) {
	rows, err := db.Query(`select id from task where closed_at is null and parent_id is ? and id != ? order by rank, id`, parent, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
