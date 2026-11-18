package main

import (
	"database/sql"
	"fmt"
	"time"
)

// startWork puts a ready task in progress with when and the session, keeping an earlier start; without a sprint a
// story is refused while another story is in progress (SPEC T16).
func startWork(db *sql.DB, id int64, level string) error {
	if level == "story" {
		var other int64
		var g taskTag
		err := db.QueryRow(`select t.id, `+tagSQL+` from task t left join task p on p.id = t.parent_id
			where t.level = 'story' and t.closed_at is null and t.started_at is not null and t.id != ? limit 1`, id).Scan(append([]any{&other}, g.dest()...)...)
		if err == nil {
			return fmt.Errorf("%s is in progress: finish it, stop it (task stop %d) or ask the user first; nothing changed", tagged(other, g.String()), other)
		}
	}
	_, err := db.Exec(`update task set started_session = case when started_at is null then nullif(?2, '') else started_session end,
		started_at = coalesce(started_at, ?1) where id = ?3`, clockNow().UTC().Format(time.RFC3339), callerSession(db), id)
	return err
}
