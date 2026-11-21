package main

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// storedTS is how every event time is stored (UTC, milliseconds), so a bound in the same form compares as text.
const storedTS = "2006-01-02T15:04:05.000Z"

var spanBack = regexp.MustCompile(`^(\d+)([mhd])$`)

// readBound reads -since or -until (SPEC Q1) as the UTC bounds of the stretch it names: a day, a minute (both in
// loc), an RFC 3339 instant or a span back from now. since keeps e.ts >= from, until keeps e.ts < to.
func readBound(flag, s string, loc *time.Location, now time.Time) (from, to string, err error) {
	var start, end time.Time
	if m := spanBack.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		start = now.Add(-time.Duration(n) * unit)
		end = start.Add(time.Millisecond)
	} else if t, e := time.ParseInLocation("2006-01-02", s, loc); e == nil {
		start, end = t, t.AddDate(0, 0, 1)
	} else if t, e := time.ParseInLocation("2006-01-02 15:04", s, loc); e == nil {
		start, end = t, t.Add(time.Minute)
	} else if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
		start, end = t, t.Add(time.Millisecond)
	} else {
		return "", "", exitCodeError{2, fmt.Errorf("-%s %q: give a day (2026-11-21), a day and time (2026-11-21 11:30), RFC 3339, or a span back from now (90m, 3h, 2d)", flag, s)}
	}
	return start.UTC().Format(storedTS), end.UTC().Format(storedTS), nil
}

// searchWindow is the UTC bounds of -since and -until, empty when not given.
func searchWindow(o opts, now time.Time) (since, until string, err error) {
	loc := time.Local
	if o.zone != "" {
		if loc, err = parseZone(o.zone); err != nil {
			return "", "", exitCodeError{2, err}
		}
	}
	if o.since != "" {
		if since, _, err = readBound("since", o.since, loc, now); err != nil {
			return
		}
	}
	if o.until != "" {
		_, until, err = readBound("until", o.until, loc, now)
	}
	return
}

// eventText is what an event says: its text, or for a tool call its name and input.
const eventText = `coalesce(nullif(e.text, ''), coalesce(e.tool_name, '') || ' ' || coalesce(e.tool_input, ''))`

// hitLine prints one event as search does: local time, speaker/kind, role, [session #seq], then the text.
func hitLine(ts, role, speaker, kind, sid string, seq int, text string) {
	when := ts
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		when = t.Local().Format("2006-01-02 15:04")
	}
	fmt.Printf("%s  %s/%s  %s  [%s #%d]\n  %s\n", when, speaker, kind, role, short(sid), seq, text)
}

const showHint = "open a hit with: trace show <session>#<seq>"

// windowList is a search with no words (SPEC Q2): what was said in the window, oldest first.
func windowList(db *sql.DB, n int, filter string, args []any) error {
	rows, err := db.Query(`select e.ts, s.role, e.speaker, e.kind, e.session_id, e.seq, `+eventText+`
		from event e join session s on s.id = e.session_id where 1=1`+filter+` order by e.ts, e.seq limit ?`, append(args, n)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	hits := 0
	for rows.Next() {
		var ts, role, speaker, kind, sid, text string
		var seq int
		rows.Scan(&ts, &role, &speaker, &kind, &sid, &seq, &text)
		hitLine(ts, role, speaker, kind, sid, seq, clip(strings.Join(strings.Fields(text), " "), 300))
		hits++
	}
	if hits == 0 {
		fmt.Println("nothing said in that window")
	} else {
		fmt.Println(showHint)
	}
	return rows.Err()
}

// show prints a hit and the n events before and after it in its session (SPEC Q3).
func show(db *sql.DB, ref string, n int, all bool) error {
	sid, seq, err := hitRef(db, ref)
	if err != nil {
		return err
	}
	kinds, args := "", []any{sid, seq - n, seq + n}
	if !all {
		kinds, args = " and (e.seq = ? or e.kind in ("+defaultKinds+"))", append(args, seq)
	}
	rows, err := db.Query(`select e.ts, s.role, e.speaker, e.kind, e.seq, `+eventText+`
		from event e join session s on s.id = e.session_id
		where e.session_id = ? and e.seq between ? and ?`+kinds+` order by e.seq`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var ts, role, speaker, kind, text string
		var s int
		rows.Scan(&ts, &role, &speaker, &kind, &s, &text)
		mark := ""
		if s == seq {
			mark, found = "▶ ", true
		}
		fmt.Print(mark)
		hitLine(ts, role, speaker, kind, sid, s, strings.ReplaceAll(clip(text, 2000), "\n", "\n  "))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session %s has no event #%d", short(sid), seq)
	}
	return nil
}

// hitRef reads <session>#<seq> into the one session whose id starts with it and the event number.
func hitRef(db *sql.DB, ref string) (sid string, seq int, err error) {
	prefix, num, ok := strings.Cut(strings.ReplaceAll(ref, " ", ""), "#")
	seq, err = strconv.Atoi(num)
	if !ok || err != nil || len(prefix) < 8 {
		return "", 0, fmt.Errorf("show needs <session>#<seq>, the session as search prints it (8 characters or more), with any flag before it: trace show -n 5 %s", "2fb287f6#120")
	}
	ids, err := sessionsStarting(db, prefix)
	switch {
	case err != nil:
		return "", 0, err
	case len(ids) == 0:
		return "", 0, fmt.Errorf("no session starts with %s", prefix)
	case len(ids) > 1:
		return "", 0, fmt.Errorf("several sessions start with %s: %s; give more of the id", prefix, strings.Join(ids, ", "))
	}
	return ids[0], seq, nil
}

func sessionsStarting(db *sql.DB, prefix string) ([]string, error) {
	rows, err := db.Query(`select id from session where substr(id, 1, ?) = ? order by id limit 10`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
