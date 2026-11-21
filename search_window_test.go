package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// searchStore makes a store with one session and the given events (ts, kind, text), numbered from 1.
func searchStore(t *testing.T, sessions map[string][][3]string) string {
	t.Helper()
	root := t.TempDir()
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for sid, events := range sessions {
		mustExec(t, db, `insert into session(id, host, role, cwd, started_at, ended_at) values (?, 'claude-code', 'r', 'C:\w', ?, ?)`, sid, events[0][0], events[0][0])
		for i, e := range events {
			mustExec(t, db, `insert into event(session_id, seq, ts, speaker, kind, text) values (?, ?, ?, 'user', ?, ?)`, sid, i+1, e[0], e[1], e[2])
		}
	}
	return root
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func traceOut(t *testing.T, root string, want int, args ...string) (string, string) {
	t.Helper()
	out, errOut, code := runTrace(t, root, "", nil, args...)
	if code != want {
		t.Fatalf("%v: exit %d, want %d\n%s\n%s", args, code, want, out, errOut)
	}
	return out, errOut
}

// SPEC Q1, Q2: -since and -until take a day, a day and a minute, RFC 3339 or a span back from now, read in -zone;
// with no words the window lists what was said, oldest first, cut to 300 characters.
func TestSearchTimeWindow(t *testing.T) {
	long := "alpha three " + strings.Repeat("x", 400)
	root := searchStore(t, map[string][][3]string{"sessionid-long": {
		{"2026-11-10T10:29:59.999Z", "prompt", "alpha one"},
		{"2026-11-10T10:30:00.000Z", "prompt", "alpha two"},
		{"2026-11-10T10:30:59.999Z", "prompt", long},
		{"2026-11-10T10:31:00.000Z", "prompt", "alpha four"},
		{"2026-11-11T00:00:00.000Z", "prompt", "alpha five"},
	}})
	words := []string{"one", "two", "three", "four", "five"}
	has := func(out string, want ...string) {
		t.Helper()
		out = strings.NewReplacer("»", "", "«", "").Replace(out) // search marks the words it matched
		for _, w := range words {
			in := strings.Contains(out, "alpha "+w)
			wanted := false
			for _, x := range want {
				wanted = wanted || x == w
			}
			if in != wanted {
				t.Fatalf("%q shown %v, want %v:\n%s", w, in, wanted, out)
			}
		}
	}
	minute := []string{"search", "-zone", "UTC", "-since", "2026-11-10 10:30", "-until", "2026-11-10 10:30"}
	out, _ := traceOut(t, root, 0, append(minute, "alpha")...)
	has(out, "two", "three")
	out, _ = traceOut(t, root, 0, "search", "-zone", "UTC-3", "-since", "2026-11-10 07:30", "-until", "2026-11-10 07:30", "alpha")
	has(out, "two", "three")
	out, _ = traceOut(t, root, 0, "search", "-zone", "UTC", "-since", "2026-11-11", "alpha")
	has(out, "five")
	out, _ = traceOut(t, root, 0, "search", "-zone", "UTC", "-until", "2026-11-10", "alpha")
	has(out, "one", "two", "three", "four")
	out, _ = traceOut(t, root, 0, "search", "-since", "2026-11-10T10:30:00Z", "-until", "2026-11-10T10:31:00Z", "alpha")
	has(out, "two", "three", "four")
	out, _ = traceOut(t, root, 0, "search", "-since", "1000d", "alpha")
	has(out, "one", "two", "three", "four", "five")
	if out, _ = traceOut(t, root, 0, "search", "-since", "1m", "alpha"); !strings.Contains(out, "not found in") {
		t.Fatalf("a span of a minute:\n%s", out)
	}

	// No words: the window, oldest first, cut.
	out, _ = traceOut(t, root, 0, minute...)
	has(out, "two", "three")
	if strings.Index(out, "alpha two") > strings.Index(out, "alpha three") || strings.Contains(out, strings.Repeat("x", 300)) ||
		!strings.Contains(out, strings.Repeat("x", 250)) {
		t.Fatalf("window without words:\n%s", out)
	}
	out, _ = traceOut(t, root, 0, "search", "-zone", "UTC", "-n", "1", "-until", "2026-11-10")
	has(out, "one")
	if out, _ = traceOut(t, root, 0, "search", "-zone", "UTC", "-since", "2026-11-12"); !strings.Contains(out, "nothing said in that window") || strings.Contains(out, "trace show") {
		t.Fatalf("an empty window:\n%s", out)
	}

	for _, bad := range []string{"yesterday", "2026-11-10 25:00", "3w"} {
		if _, errOut := traceOut(t, root, 2, "search", "-since", bad, "alpha"); !strings.Contains(errOut, "90m") {
			t.Fatalf("-since %q:\n%s", bad, errOut)
		}
	}
	if _, errOut := traceOut(t, root, 2, "search", "-until", "nope"); !strings.Contains(errOut, "-until") {
		t.Fatalf("-until nope:\n%s", errOut)
	}
}

// SPEC Q3: trace show opens a hit with the events around it in its session; search names the command.
func TestShowAroundAHit(t *testing.T) {
	ev := func(kind, text string) [3]string { return [3]string{"2026-11-10T10:30:00.000Z", kind, text} }
	root := searchStore(t, map[string][][3]string{
		"aaaaaaaa-long": {ev("prompt", "line one"), ev("prompt", "line two"), ev("prompt", "line three"),
			ev("prompt", "line four "+strings.Repeat("y", 2500)), ev("thinking", "line hidden"), ev("prompt", "line six"),
			ev("prompt", "line seven"), ev("prompt", "line eight")},
		"aaaaaaaa-other": {ev("prompt", "other one")},
		"bbbbbbbb-1":     {ev("thinking", "line lonely")},
	})
	shown := func(out string, want ...string) {
		t.Helper()
		for _, w := range []string{"one", "two", "three", "four", "hidden", "six", "seven", "eight"} {
			in := strings.Contains(out, "line "+w)
			wanted := false
			for _, x := range want {
				wanted = wanted || x == w
			}
			if in != wanted {
				t.Fatalf("%q shown %v, want %v:\n%s", w, in, wanted, out)
			}
		}
	}
	out, _ := traceOut(t, root, 0, "show", "aaaaaaaa-l#4")
	shown(out, "one", "two", "three", "four", "six", "seven")
	if strings.Index(out, "line one") > strings.Index(out, "line seven") || strings.Contains(out, strings.Repeat("y", 2001)) ||
		!strings.Contains(out, strings.Repeat("y", 1900)) {
		t.Fatalf("order or cut:\n%s", out)
	}
	out, _ = traceOut(t, root, 0, "show", "-all", "-n", "1", "aaaaaaaa-long", "#4")
	shown(out, "three", "four", "hidden")
	if out, _ = traceOut(t, root, 0, "show", "bbbbbbbb#1"); !strings.Contains(out, "line lonely") {
		t.Fatalf("the hit itself, outside the default kinds:\n%s", out)
	}
	if _, errOut := traceOut(t, root, 1, "show", "aaaaaaaa#1"); !strings.Contains(errOut, "aaaaaaaa-long") || !strings.Contains(errOut, "aaaaaaaa-other") {
		t.Fatalf("ambiguous:\n%s", errOut)
	}
	for bad, want := range map[string]string{"cccccccc#1": "no session starts with", "aaaa#1": "show needs", "aaaaaaaa-long": "show needs",
		"aaaaaaaa-long#x": "show needs", "aaaaaaaa-long#99": "has no event #99"} {
		if _, errOut := traceOut(t, root, 1, "show", bad); !strings.Contains(errOut, want) {
			t.Fatalf("show %s: %q, want %q", bad, errOut, want)
		}
	}
	if out, _ = traceOut(t, root, 0, "search", "seven"); !strings.Contains(out, "trace show <session>#<seq>") {
		t.Fatalf("search names show:\n%s", out)
	}
}

// SPEC Q4: of two equal matches the newer comes first, whatever order they were stored in.
func TestSearchPrefersRecent(t *testing.T) {
	root := searchStore(t, map[string][][3]string{"sessionid-long": {
		{"2026-08-01T10:00:00.000Z", "prompt", "alpha bravo old"},
		{"2026-11-10T10:00:00.000Z", "prompt", "alpha bravo new"},
		{"2026-09-01T10:00:00.000Z", "prompt", "alpha bravo mid"},
	}})
	out, _ := traceOut(t, root, 0, "search", "alpha", "bravo")
	if n, m, o := strings.Index(out, "new"), strings.Index(out, "mid"), strings.Index(out, "old"); n < 0 || n > m || m > o {
		t.Fatalf("newest first:\n%s", out)
	}
}
