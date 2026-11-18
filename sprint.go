package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// openSprintSQL is the open sprint's id; at most one is open (SPEC T18).
const openSprintSQL = `(select id from sprint where closed_at is null)`

// outsideSprintSQL is true when the sprint id of is not the open sprint's: outside it, kanban's one story in progress
// holds (SPEC T16, T18).
func outsideSprintSQL(of string) string {
	return `coalesce(` + of + `, 0) != coalesce(` + openSprintSQL + `, -1)`
}

// taskSprint opens a sprint with -at, -text and story ids, or runs add, out or close on the open one (SPEC T18).
func taskSprint(db *sql.DB, dbPath string, args []string, t newTask, w io.Writer) error {
	if len(args) > 1 {
		if mark, ok := sprintMarks[args[1]]; ok {
			return mark.run(db, args[1:], w)
		}
		if args[1] == "close" {
			return sprintClose(db, w)
		}
	}
	if err := sprintOpen(db, args[1:], t, w); err != nil {
		return err
	}
	if err := wakeAlarm(dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "trace: the sprint is stored; the alarm did not start, the next session start will start it:", err)
	}
	return nil
}

// sprintMarks put an open story into the open sprint or take one out of it, back to the backlog (SPEC T18).
var sprintMarks = map[string]taskMark{
	"add": {`update task set sprint_id = ` + openSprintSQL + ` where id = ?3 and closed_at is null and level = 'story' and ` +
		outsideSprintSQL("sprint_id") + ` and ` + openSprintSQL + ` is not null`, "an open story outside the open sprint", "in the sprint"},
	"out": {`update task set sprint_id = null where id = ?3 and closed_at is null and sprint_id = ` + openSprintSQL,
		"a story in the open sprint", "back to the backlog"},
}

// sprintOpen stores a sprint: its end, its goal in the user's words and its open stories, and adds its review as a
// task due at the end, so the alarm fires it (SPEC T18).
func sprintOpen(db *sql.DB, ids []string, t newTask, w io.Writer) error {
	var open int64
	var goal string
	if db.QueryRow(`select id, goal from sprint where closed_at is null`).Scan(&open, &goal) == nil {
		return fmt.Errorf("sprint %d is open (%q); close it first (task sprint close); nothing stored", open, goal)
	}
	if err := sprintNeeds(db, ids, t); err != nil {
		return fmt.Errorf("%v; nothing stored", err)
	}
	var added strings.Builder
	if err := taskAdd(db, newTask{text: "review the sprint: " + t.text, due: t.due, self: true, repeated: t.repeated}, &added); err != nil {
		return err
	}
	res, err := db.Exec(`insert into sprint(goal, review_task, opened_at, opened_session) values (?, (select max(id) from task), ?, nullif(?, ''))`,
		redacted(t.text), clockNow().UTC().Format(time.RFC3339), callerSession(db))
	if err != nil {
		return err
	}
	sprint, _ := res.LastInsertId()
	for _, id := range ids {
		if _, err := db.Exec(`update task set sprint_id = ? where id = ?`, sprint, id); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "sprint %d open: %d stories; its review is %s", sprint, len(ids), strings.Replace(added.String(), " added", "", 1))
	return nil
}

// sprintNeeds says what a sprint to open lacks: an end, a goal, and stories, each an open story.
func sprintNeeds(db *sql.DB, ids []string, t newTask) error {
	switch {
	case t.due.IsZero():
		return errors.New("a sprint needs its end (-at)")
	case strings.TrimSpace(t.text) == "":
		return errors.New("a sprint needs its goal in the user's words (-text)")
	case len(ids) == 0:
		return errors.New("a sprint needs its stories (their ids)")
	}
	for _, id := range ids {
		var n int
		if db.QueryRow(`select count(*) from task where id = ? and closed_at is null and level = 'story'`, id).Scan(&n); n == 0 {
			return fmt.Errorf("task %s is not an open story", id)
		}
	}
	return nil
}

// sprintTally is a sprint's stories and points done and in all (dropped ones left out), and its open stories as
// "task <id>" (SPEC T18).
func sprintTally(db *sql.DB, sprint int64) (done, all, donePoints, allPoints int, left []string) {
	db.QueryRow(`select count(*) filter (where closed_how = 'done'), count(*), coalesce(sum(points) filter (where closed_how = 'done'), 0),
		coalesce(sum(points), 0) from task where sprint_id = ? and coalesce(closed_how, '') != 'drop'`, sprint).Scan(&done, &all, &donePoints, &allPoints)
	rows, err := db.Query(`select id, level, text from task where sprint_id = ? and closed_at is null order by rank, id`, sprint)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var level, text string
		rows.Scan(&id, &level, &text)
		left = append(left, fmt.Sprintf("task %d (%s): %q", id, level, clip(text, 300)))
	}
	return
}

// tallyText is "<d> of <n> stories done, <p> of <q> points".
func tallyText(done, all, donePoints, allPoints int) string {
	return fmt.Sprintf("%d of %d stories done, %d of %d points", done, all, donePoints, allPoints)
}

// sprintClose closes the open sprint and its review once no story in it is open: the agent first goes over each one
// with the user (SPEC T18).
func sprintClose(db *sql.DB, w io.Writer) error {
	var sprint, review int64
	if err := db.QueryRow(`select id, review_task from sprint where closed_at is null`).Scan(&sprint, &review); err != nil {
		return errors.New("no sprint is open; nothing changed")
	}
	done, all, donePoints, allPoints, left := sprintTally(db, sprint)
	if len(left) > 0 {
		return fmt.Errorf("sprint %d has open stories: %s; ask the user about each one: done, back to the backlog (task sprint out <id>) or into the next sprint; nothing changed",
			sprint, strings.Join(left, "; "))
	}
	now, session := clockNow().UTC().Format(time.RFC3339), callerSession(db)
	if _, err := db.Exec(`update sprint set closed_at = ?, closed_session = nullif(?, '') where id = ?`, now, session, sprint); err != nil {
		return err
	}
	if _, err := db.Exec(`update task set closed_at = ?1, told_at = coalesce(told_at, ?1), closed_how = 'done' where id = ?2 and closed_at is null`, now, review); err != nil {
		return err
	}
	fmt.Fprintf(w, "sprint %d closed: %s\n", sprint, tallyText(done, all, donePoints, allPoints))
	return nil
}

// sprintSection is the start block's open sprint: its goal, its end, what is done and what is left (SPEC T18).
func sprintSection(db *sql.DB, w io.Writer) {
	var sprint int64
	var goal, due, zone string
	if db.QueryRow(`select s.id, s.goal, coalesce(r.due, ''), coalesce(r.zone, '') from sprint s left join task r on r.id = s.review_task
		where s.closed_at is null`).Scan(&sprint, &goal, &due, &zone) != nil {
		return
	}
	done, all, donePoints, allPoints, left := sprintTally(db, sprint)
	var ids []string
	for _, l := range left {
		ids = append(ids, l[:strings.Index(l, " (")])
	}
	fmt.Fprintf(w, "### Sprint: %q, until %s\n%s; left: %s\n", goal, dueText(due, zone, ""), tallyText(done, all, donePoints, allPoints), strings.Join(ids, ", "))
}
