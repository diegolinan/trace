package main

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// zoneMust is a zone the test needs; a failure to read it is the test's own failure.
func zoneMust(t *testing.T, s string) *time.Location {
	t.Helper()
	loc, err := parseZone(s)
	if err != nil {
		t.Fatalf("zone %q: %v", s, err)
	}
	return loc
}

// SPEC A10: -zone takes an offset or a zone name; the offset of a name is the one at the task's date.
func TestTaskAddZone(t *testing.T) {
	for zone, want := range map[string]int{"UTC": 0, "UTC-3": -3 * 3600, "UTC+5:30": 5*3600 + 1800, "UTC-03:00": -3 * 3600, "utc+2": 2 * 3600} {
		_, off := time.Date(2026, 11, 18, 11, 0, 0, 0, zoneMust(t, zone)).Zone()
		if off != want {
			t.Errorf("-zone %s: offset %d, want %d", zone, off, want)
		}
	}
	la := zoneMust(t, "America/Los_Angeles")
	if _, off := time.Date(2026, 11, 18, 11, 0, 0, 0, la).Zone(); off != -8*3600 {
		t.Errorf("Los Angeles in November: offset %d, want UTC-8", off)
	}
	if _, off := time.Date(2026, 4, 18, 11, 0, 0, 0, la).Zone(); off != -7*3600 {
		t.Errorf("Los Angeles in April: offset %d, want UTC-7 (daylight saving)", off)
	}
	root := tkRoot(t, mutLines(mutUser("u1", 0, "avisame el miércoles a las 11, estoy en Los Ángeles")))
	out := tkTrace(t, root, "task", "add", "-text", "call Bob", "-at", "2026-11-18 11:00", "-zone", "America/Los_Angeles")
	if !strings.Contains(out, "Wednesday 2026-11-18 11:00 (UTC-08:00)") {
		t.Errorf("the echo does not show the time in the zone given: %q", out)
	}
	if got := mutStr(t, tkStore(t, root), "select due || ' ' || zone from task"); got != "2026-11-18T19:00:00Z UTC-08:00" {
		t.Errorf("stored %q, want the UTC instant and the offset it was said in", got)
	}
}

// SPEC A10: 11:00 said at UTC-3 is 14:00 UTC wherever the user is when it fires.
func TestOneTimeTaskKeepsItsInstantAcrossZones(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "avisame")))
	tkTrace(t, root, "task", "add", "-text", "x", "-at", "2026-11-18 11:00", "-zone", "UTC-3")
	if got := mutStr(t, tkStore(t, root), "select due || ' ' || zone from task"); got != "2026-11-18T14:00:00Z UTC-03:00" {
		t.Errorf("stored %q", got)
	}
}

// SPEC A10: a zone that misleads or cannot be read is refused, and so is -zone without -at; nothing is stored.
func TestTaskAddRefusesBadZone(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "avisame")))
	for _, args := range [][]string{
		{"-at", "2026-11-18 11:00", "-zone", ""},
		{"-at", "2026-11-18 11:00", "-zone", "Local"},
		{"-at", "2026-11-18 11:00", "-zone", "Etc/GMT-3"},
		{"-at", "2026-11-18 11:00", "-zone", "Mars/Olympus"},
		{"-at", "2026-11-18 11:00", "-zone", "UTC-15"},
		{"-at", "2026-11-18 11:00", "-zone", "UTC-3:7"},
		{"-at", "2026-11-18 11:00", "-zone", "GMT-3"},
		{"-zone", "UTC-3"},
	} {
		args = append([]string{"task", "add", "-text", "x"}, args...)
		if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, args...); code == 0 || !strings.Contains(errOut, "nothing stored") {
			t.Errorf("trace %v: exit %d, stderr %q; want a refusal that stores nothing", args, code, errOut)
		}
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task"); n != "0" {
		t.Errorf("%s tasks stored by refused adds", n)
	}
	for _, empty := range []string{"", " "} {
		if _, err := parseZone(empty); err == nil {
			t.Errorf("an empty zone %q is read", empty)
		}
	}
}

// SPEC A10: with RFC 3339 the offset fixes the instant; with -zone too, a different offset is refused.
func TestRFC3339OffsetMustMatchZone(t *testing.T) {
	at := "2026-11-18T11:00:00-03:00"
	if _, repeated, err := readDue(at, zoneMust(t, "America/Los_Angeles"), true); err == nil || repeated {
		t.Errorf("an offset of -03:00 with a Los Angeles zone: %v (repeated %v); want a refusal", err, repeated)
	}
	for _, c := range []struct {
		zone  string
		given bool
	}{{"UTC-3", true}, {"America/Argentina/Buenos_Aires", true}, {"UTC+9", false}} {
		due, _, err := readDue(at, zoneMust(t, c.zone), c.given)
		if err != nil || !due.Equal(time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)) {
			t.Errorf("zone %s (given %v): %v, %v; want 14:00 UTC", c.zone, c.given, due, err)
		}
	}
}

// SPEC A3: the hour a daylight-saving change skips does not exist and is refused.
func TestNonexistentLocalTimeRefused(t *testing.T) {
	for _, at := range []string{"2026-03-08 02:30", "tomorrow at 9"} {
		if due, repeated, err := readDue(at, zoneMust(t, "America/New_York"), true); err == nil || repeated {
			t.Errorf("%q read as %v (repeated %v); want a refusal", at, due, repeated)
		}
	}
	root := tkRoot(t, mutLines(mutUser("u1", 0, "avisame")))
	if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"},
		"task", "add", "-text", "x", "-at", "2026-03-08 02:30", "-zone", "America/New_York"); code == 0 || !strings.Contains(errOut, "nothing stored") {
		t.Errorf("exit %d, stderr %q; want a refusal", code, errOut)
	}
}

// SPEC A3: the hour a change repeats is the earlier instant, and the echo says the hour happens twice.
func TestRepeatedLocalTimeTakesEarlier(t *testing.T) {
	ny := zoneMust(t, "America/New_York")
	due, repeated, err := readDue("2026-11-01 01:30", ny, true)
	if err != nil || !repeated || !due.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) {
		t.Errorf("01:30 on the day New York repeats it: %v, repeated %v, %v; want 05:30 UTC (the first, at UTC-04:00)", due, repeated, err)
	}
	if _, repeated, _ := readDue("2026-11-01 03:30", ny, true); repeated {
		t.Error("03:30, which happens once, is said to repeat")
	}
	echo := addEcho(due, ny, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), ny, true)
	if !strings.Contains(echo, "01:30 happens twice") || !strings.Contains(echo, "RFC 3339") {
		t.Errorf("the echo does not say the hour repeats: %q", echo)
	}
}

// SPEC A10: the echo shows the time in its offset, now in that offset and how far away it is, and the computer's time
// when the computer's offset differs.
func TestTaskAddEchoesNowAndDistance(t *testing.T) {
	ar := zoneMust(t, "UTC-3")
	due := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC)
	if got, want := addEcho(due, ar, now, ar, false), "Wednesday 2026-11-18 11:00 (UTC-03:00); now Tuesday 23:00, in 12h"; got != want {
		t.Errorf("echo\n got %q\nwant %q", got, want)
	}
	got := addEcho(due, ar, now.Add(-90*time.Minute+30*time.Second), zoneMust(t, "America/Los_Angeles"), false)
	if want := "Wednesday 2026-11-18 11:00 (UTC-03:00); now Tuesday 21:30, in 13h30m; here 06:00 (UTC-08:00)"; got != want {
		t.Errorf("echo away from the task's zone\n got %q\nwant %q", got, want)
	}
	if got := addEcho(due, ar, due.Add(5*time.Minute), ar, false); !strings.HasSuffix(got, "5m ago") {
		t.Errorf("a time already past: %q", got)
	}
	for d, want := range map[time.Duration]string{0: "now", 25 * time.Second: "now", -25 * time.Second: "now", time.Hour: "in 1h",
		45 * time.Minute: "in 45m", 61 * time.Minute: "in 1h1m", -90 * time.Minute: "1h30m ago", -3 * time.Hour: "3h ago", -time.Minute: "1m ago"} {
		if got := distance(d); got != want {
			t.Errorf("distance(%v) = %q, want %q", d, got, want)
		}
	}
}

// SPEC A10: offsets run from UTC-12 to UTC+14, minutes under 60, and UTC itself is shown as UTC+00:00.
func TestOffsetBounds(t *testing.T) {
	for _, ok := range []string{"UTC+14", "UTC-12", "UTC-3:59", "UTC+0"} {
		if _, err := parseZone(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"UTC+14:30", "UTC-12:30", "UTC-3:60", "UTC-15"} {
		if _, err := parseZone(bad); err == nil || !strings.Contains(err.Error(), `"`+bad+`"`) {
			t.Errorf("%s: %v; want a refusal that names it", bad, err)
		}
	}
	for secs, want := range map[int]string{0: "UTC+00:00", -10800: "UTC-03:00", 19800: "UTC+05:30"} {
		if got := offsetName(secs); got != want {
			t.Errorf("offsetName(%d) = %q, want %q", secs, got, want)
		}
	}
}

// SPEC A10: list, start block, prompt hook and alarm show a task in its stored offset; a task stored before offsets
// were kept shows in the computer's offset.
func TestTimesShownWithOffset(t *testing.T) {
	if got := dueText("2026-11-18T19:00:00Z", "UTC-08:00", ", due "); got != ", due Wednesday 2026-11-18 11:00 (UTC-08:00)" {
		t.Errorf("stored offset: %q", got)
	}
	old := time.Date(2026, 11, 18, 19, 0, 0, 0, time.UTC).Local()
	_, off := old.Zone()
	if got := dueText("2026-11-18T19:00:00Z", "", ""); got != old.Format("Monday 2006-01-02 15:04")+" ("+offsetName(off)+")" {
		t.Errorf("no stored offset: %q, want the computer's", got)
	}
	if got := dueText("", "UTC-08:00", ", due "); got != "" {
		t.Errorf("no time: %q", got)
	}
	root := tkRoot(t, mutLines(mutUser("u1", 0, "avisame")))
	tkTrace(t, root, "task", "add", "-text", "call Bob", "-at", "2026-11-18 11:00", "-zone", "UTC-8")
	if list := tkTrace(t, root, "task", "list"); !strings.Contains(list, "due Wednesday 2026-11-18 11:00 (UTC-08:00): call Bob") {
		t.Errorf("task list: %q", list)
	}
	msg := alarmMessage(dueTask{id: 1, due: "2026-11-18T19:00:00Z", zone: "UTC-08:00", text: "call Bob", asked: "avisame"}, "trace-alarm:1:0123456789abcdef", 0)
	if !strings.Contains(msg, "due Wednesday 2026-11-18 11:00 (UTC-08:00)") {
		t.Errorf("the alarm's message: %q", msg)
	}
}

// SPEC A10: the start block names the computer's offset and tells the agent about -zone and the echo.
func TestStartBlockNamesTheZone(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	cwd := t.TempDir()
	rzSession(t, db, "s", projectRole(cwd), cwd, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	_, off := clockNow().In(time.Local).Zone()
	b := rzBlock(t, db, cwd)
	for _, want := range []string{"-at is read in this computer's zone, " + offsetName(off) + " now", "pass -zone",
		"tell the user the time the add echoes"} {
		if !strings.Contains(b, want) {
			t.Errorf("the start block lacks %q:\n%s", want, b)
		}
	}
}

// SPEC A10: TRACE never looks a zone up on the internet: no source file speaks HTTP, and only the Unix inbox opens a
// socket.
func TestNoZoneLookupOnline(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		ast, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range ast.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "net/") || (path == "net" && f != "inbox_unix.go") {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
