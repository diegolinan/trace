package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names load on a computer that has no zone database (SPEC A10)
)

// SPEC A10: only the UTC offset matters. A task stores its UTC instant and the offset it was said in; every time
// shown carries its offset. TRACE never looks a zone up anywhere: the computer's clock, or the zone the agent passes.

var utcOffset = regexp.MustCompile(`(?i)^UTC(?:([+-])(\d{1,2})(?::?(\d{2}))?)?$`)

// parseZone reads -zone: an offset (UTC, UTC-3, UTC+5:30) or a zone name. Refused: empty, Local, any Etc/ name (its
// sign is the reverse of what it reads as), and what cannot be read.
func parseZone(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	if m := utcOffset.FindStringSubmatch(s); m != nil {
		return fixedOffset(m)
	}
	if s == "" || strings.EqualFold(s, "Local") || strings.HasPrefix(strings.ToLower(s), "etc/") {
		return nil, fmt.Errorf("-zone %q: give an offset such as UTC-3 or a zone name such as America/Los_Angeles; nothing stored", s)
	}
	loc, err := time.LoadLocation(s)
	if err != nil {
		return nil, fmt.Errorf("-zone %q is not a zone: give an offset such as UTC-3 or a zone name such as America/Los_Angeles; nothing stored", s)
	}
	return loc, nil
}

// fixedOffset is the zone of a UTC±H[:MM] match; offsets run from UTC-12 to UTC+14.
func fixedOffset(m []string) (*time.Location, error) {
	h, _ := strconv.Atoi(m[2])
	min, _ := strconv.Atoi(m[3])
	secs := h*3600 + min*60
	if m[1] == "-" {
		secs = -secs
	}
	if min >= 60 || secs < -12*3600 || secs > 14*3600 {
		return nil, fmt.Errorf("-zone %q is not an offset between UTC-12 and UTC+14; nothing stored", m[0])
	}
	return time.FixedZone(offsetName(secs), secs), nil
}

// offsetName is an offset as TRACE shows and stores it: UTC-03:00, UTC+05:30, UTC+00:00.
func offsetName(secs int) string {
	sign := "+"
	if secs < 0 {
		sign, secs = "-", -secs
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, secs/3600, secs%3600/60)
}

const atLayout = "2006-01-02 15:04"

// readDue reads -at in loc: YYYY-MM-DD HH:MM, or RFC 3339, whose offset fixes the instant and, with a zone given,
// must be that zone's at that instant (SPEC A10). A local time a daylight-saving change skips is refused; one it
// repeats is the earlier instant, and repeated says so (SPEC A3).
func readDue(at string, loc *time.Location, zoneGiven bool) (due time.Time, repeated bool, err error) {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t, false, offsetMatches(at, t, loc, zoneGiven)
	}
	t, err := time.ParseInLocation(atLayout, at, loc)
	if err != nil {
		return t, false, fmt.Errorf("-at %q is not a time: give YYYY-MM-DD HH:MM or RFC 3339; nothing stored", at)
	}
	if t.In(loc).Format(atLayout) != at {
		return t, false, fmt.Errorf("-at %q does not exist there: a daylight-saving change skips it; nothing stored", at)
	}
	due, repeated = firstOf(t, loc, at)
	return due, repeated, nil
}

// offsetMatches refuses an RFC 3339 time whose offset is not the given zone's at that instant (SPEC A10).
func offsetMatches(at string, t time.Time, loc *time.Location, zoneGiven bool) error {
	_, got := t.Zone()
	if _, want := t.In(loc).Zone(); zoneGiven && got != want {
		return fmt.Errorf("-at %q has the offset %s, but -zone is at %s then; nothing stored", at, offsetName(got), offsetName(want))
	}
	return nil
}

// firstOf is the earlier of the instants a local time names in loc, and whether there are two: a daylight-saving
// change repeats an hour, or half an hour in a few zones (SPEC A3).
func firstOf(t time.Time, loc *time.Location, at string) (time.Time, bool) {
	for _, step := range []time.Duration{time.Hour, 30 * time.Minute} {
		if earlier := t.Add(-step); earlier.In(loc).Format(atLayout) == at {
			return earlier, true
		}
		if t.Add(step).In(loc).Format(atLayout) == at {
			return t, true
		}
	}
	return t, false
}

// showAt is an instant as the user reads it, in loc, with its weekday and its offset.
func showAt(t time.Time, loc *time.Location) string {
	t = t.In(loc)
	_, off := t.Zone()
	return t.Format("Monday 2006-01-02 15:04") + " (" + offsetName(off) + ")"
}

// addEcho is what task add prints after the id: the time in the zone it was read in, now in that zone and how far
// away it is, and the computer's time when its offset then differs (SPEC A10); a repeated hour says so (SPEC A3).
func addEcho(due time.Time, loc *time.Location, now time.Time, here *time.Location, repeated bool) string {
	s := showAt(due, loc) + "; now " + now.In(loc).Format("Monday 15:04") + ", " + distance(due.Sub(now))
	_, off := due.In(loc).Zone()
	if _, hereOff := due.In(here).Zone(); hereOff != off {
		s += "; here " + due.In(here).Format("15:04") + " (" + offsetName(hereOff) + ")"
	}
	if repeated {
		s += fmt.Sprintf("; %s happens twice there: this is the first, at %s; for the second give -at in RFC 3339 with its offset",
			due.In(loc).Format("15:04"), offsetName(off))
	}
	return s
}

// distance is how far away a time is, to the minute: "in 12h", "in 13h30m", "5m ago", "now".
func distance(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d == 0:
		return "now"
	case d < 0:
		return hoursMinutes(-d) + " ago"
	}
	return "in " + hoursMinutes(d)
}

// hoursMinutes is a whole number of minutes as 12h, 13h30m or 45m.
func hoursMinutes(d time.Duration) string {
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// storedZone is the zone a task's time is shown in: its stored offset, or the computer's for a task stored before
// offsets were kept (SPEC A10).
func storedZone(zone string) *time.Location {
	if loc, err := parseZone(zone); err == nil {
		return loc
	}
	return time.Local
}
