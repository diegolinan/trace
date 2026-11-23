package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// bgHook is the SessionStart block a new Claude Code session in cwd gets from the hook.
func bgHook(t *testing.T, db *sql.DB, cwd string) string {
	t.Helper()
	var out strings.Builder
	if err := hookBlock(db, "new", "startup", projectRole(cwd), "claude-code", cwd, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// bgBigStore fills a store with everything the start block shows, each at its longest: 8 rules and 5 decisions that
// answer long replies, 8 long user prompts, 4 long replies, 5 pins, 9 tasks, 3 of them late, 5 lessons.
func bgBigStore(t *testing.T, cwd string) *sql.DB {
	t.Helper()
	db := rzDB(t)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	old := clockNow
	clockNow = func() time.Time { return now }
	t.Cleanup(func() { clockNow = old })
	long := func(tag string, n int) string { return tag + " " + strings.Repeat("palabra ", n/8) }
	at := func(m int) string { return now.Add(time.Duration(m-600) * time.Minute).Format(time.RFC3339) }
	rzSession(t, db, "s", projectRole(cwd), cwd, at(0), at(500), 3600)
	seq := 0
	ev := func(speaker, kind, text string) int {
		seq++
		rzEvent(t, db, "s", seq, at(seq), speaker, kind, text)
		return seq
	}
	for i := 0; i < 13; i++ {
		ev("assistant", "reply", long(fmt.Sprintf("reply before rule %d", i), 400))
		s := ev("user", "prompt", long(fmt.Sprintf("rule %d", i), 260))
		rzExec(t, db, `insert into rule(event_id, quote, decision, session_id, added_at) values ((select id from event where session_id = 's' and seq = ?), ?, ?, 's', ?)`,
			s, long(fmt.Sprintf("rule %d", i), 260), i >= 8, at(s))
	}
	for i := 0; i < 8; i++ {
		ev("user", "prompt", long(fmt.Sprintf("last words %d", i), 500))
	}
	for i := 0; i < 5; i++ {
		p := ev("user", "prompt", long(fmt.Sprintf("pin %d remember this", i), 300))
		rzExec(t, db, `insert into anchor(event_id, type, value) values ((select id from event where session_id = 's' and seq = ?), 'pin', 'pin')`, p)
	}
	for i := 0; i < 4; i++ {
		ev("assistant", "reply", long(fmt.Sprintf("last reply %d", i), 600))
	}
	for i := 0; i < 9; i++ {
		tkTask(t, db, long(fmt.Sprintf("task %d", i), 160), "", at(i), "s", 1)
	}
	rzExec(t, db, `update task set due = ?, announced_at = ? where id <= 5`, at(10), at(11))
	rzExec(t, db, `update event set compaction_no = 1`) // so the first words after the last compaction show
	for i := 0; i < 5; i++ {
		rzExec(t, db, `insert into lesson(text, evidence, event_id, machine, cwd, session_id, added_at) values (?, 'the evidence of it', 1, 1, ?, 's', ?)`,
			long(fmt.Sprintf("lesson %d", i), 280), cwd, at(i))
	}
	return db
}

// SPEC B2, B3: a block over the limit is shortened, in its fixed order, to at most 9,500 bytes, keeps what no step
// cuts, and ends saying what was shortened; trace resume (B4) still prints it whole.
func TestStartBlockFitsTheHookLimit(t *testing.T) {
	rzNoRoles(t)
	cwd := t.TempDir()
	db := bgBigStore(t, cwd)
	if whole := rzBlock(t, db, cwd); len(whole) <= 9500 || strings.Contains(whole, "Shortened to fit") {
		t.Fatalf("trace resume: %d bytes, want the whole block, over 9,500 and not shortened", len(whole))
	}
	b := bgHook(t, db, cwd)
	if len(b) > 9500 {
		t.Fatalf("the hook's block is %d bytes, over 9,500", len(b))
	}
	if b != bgHook(t, db, cwd) {
		t.Fatal("two runs on the same store give different blocks")
	}
	for _, want := range []string{
		"### What Ana set as rules and decisions", `"rule 3 palabra`, `"rule 12 palabra`, "and 5 more: ", "record it with",
		"lesson add -text", "## Where", "Everything else said or done", "Shortened to fit Claude Code's 10,000-character hook limit: no pins", "the newest 5 rules and 3 decisions, and the end",
		"resume -host claude-code",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the shortened block lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "### Pinned") {
		t.Errorf("pins kept in a shortened block:\n%s", b)
	}
	if !strings.HasSuffix(b, "\n") || strings.Count(b, "Shortened to fit") != 1 {
		t.Errorf("want one closing line:\n%s", b[max(0, len(b)-400):])
	}

	// A task too long for any step: cut at a whole line, and the closing line says the end was cut.
	tkTask(t, db, long2(20000), "", "2026-11-23T11:00:00Z", "s", 1)
	rzExec(t, db, `update task set rank = case when length(text) > 10000 then -1 else id end`)
	b = bgHook(t, db, cwd)
	if len(b) > 9500 || !strings.Contains(b, "and the end of the data above") || strings.Contains(b, "xxxxxxxxxx") {
		t.Fatalf("over the limit after every step: %d bytes, ends %q", len(b), b[max(0, len(b)-300):])
	}
}

func long2(n int) string { return strings.Repeat("x", n) }

// SPEC B2: a block under the limit is exactly what it was, with no closing line.
func TestSmallStartBlockUnchanged(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	cwd := t.TempDir()
	rzSession(t, db, "s", projectRole(cwd), cwd, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "s", 1, "2026-11-10T10:00:00Z", "user", "prompt", "hola")
	rzEvent(t, db, "s", 2, "2026-11-10T10:01:00Z", "assistant", "reply", "listo")
	var whole strings.Builder
	if err := startText(db, "new", "startup", projectRole(cwd), "claude-code", cwd, true, fitNone, &whole, &whole); err != nil {
		t.Fatal(err)
	}
	if b := bgHook(t, db, cwd); b != whole.String() || strings.Contains(b, "Shortened") {
		t.Fatalf("a small block changed:\n%s\n--- want ---\n%s", b, whole.String())
	}
}

// SPEC B1: the full board guide is trace task help's.
func TestTaskHelpHasTheGuide(t *testing.T) {
	out, errOut, code := runTrace(t, t.TempDir(), "", nil, "task", "help")
	if code != 0 {
		t.Fatalf("task help: exit %d %s", code, errOut)
	}
	for _, want := range []string{"-after", "-needs", "task met", "task start", "-then", "task then", "-level", "-under",
		"task under", "task level", "scrum master", "never name", "task stop", "one story in progress", "task sprint", "-points"} {
		if !strings.Contains(out, want) {
			t.Errorf("task help lacks %q:\n%s", want, out)
		}
	}
}

// SPEC B1: the start block keeps T3 and names trace task help, without the guide itself.
func TestStartBlockNamesTaskHelp(t *testing.T) {
	var b strings.Builder
	if err := taskSection(rzDB(t), &b, "Ana"); err != nil {
		t.Fatal(err)
	}
	s := b.String()
	for _, want := range []string{"task add", "-self", "Most conversations put nothing on the board", "Close a task only after asking Ana", "task help"} {
		if !strings.Contains(s, want) {
			t.Errorf("the block lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "scrum master") || len(s) > 1200 {
		t.Errorf("the block still holds the guide (%d bytes):\n%s", len(s), s)
	}
}

// bgAt is the block startText writes at step f, body and tail together.
func bgAt(t *testing.T, db *sql.DB, cwd string, f fit) string {
	t.Helper()
	var b strings.Builder
	if err := startText(db, "new", "startup", projectRole(cwd), "claude-code", cwd, true, f, &b, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// bgSection is the lines of the section titled title (from "### title" to the next blank line), without its title.
func bgSection(b, title string) []string {
	_, rest, ok := strings.Cut(b, "### "+title)
	if !ok {
		return nil
	}
	sec, _, _ := strings.Cut(rest, "\n\n")
	sec, _, _ = strings.Cut(sec, "\n### ") // the late tasks follow the open ones with no blank line
	lines := strings.Split(sec, "\n")[1:]
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "and ") || strings.HasPrefix(l, "…") {
			out = append(out, l)
		}
	}
	return out
}

func bgLongest(lines []string) int {
	n := 0
	for _, l := range lines {
		n = max(n, len(l))
	}
	return n
}

// SPEC B2: each step takes effect at its own step and not before, and keeps the steps before it.
func TestFitStepsInOrder(t *testing.T) {
	rzNoRoles(t)
	cwd := t.TempDir()
	db := bgBigStore(t, cwd)
	type probe func(b string) int
	count := func(title string) probe { return func(b string) int { return len(bgSection(b, title)) } }
	longest := func(title string) probe { return func(b string) int { return bgLongest(bgSection(b, title)) } }
	has := func(s string) probe {
		return func(b string) int {
			if strings.Contains(b, s) {
				return 1
			}
			return 0
		}
	}
	for _, c := range []struct {
		step   fit
		probe  probe
		before int // the probe's value one step before; at the step it must be less
	}{
		{fitNoPins, has("### Pinned"), 1},
		{fitFewTasks, count("Open tasks"), 6},
		{fitFewTasks, count("Late"), 5},
		{fitFewWords, has("first words in that stretch"), 1},
		{fitFewWords, count("Ana's last words"), 8},
		{fitShortReplies, longest("The last replies"), 0},
		{fitNoAnswering, has("(answering:"), 1},
		{fitFewLessons, count("What agents learned"), 5},
		{fitShorterWords, longest("Ana's last words"), 0},
		{fitShorterWords, longest("The last replies"), 0},
		{fitFewRules, count("What Ana set as rules"), 13},
	} {
		prev, at := c.probe(bgAt(t, db, cwd, c.step-1)), c.probe(bgAt(t, db, cwd, c.step))
		if c.before != 0 && prev != c.before {
			t.Errorf("step %d (%s): %d one step before, want %d", c.step, fitSteps[c.step], prev, c.before)
		}
		if at >= prev {
			t.Errorf("step %d (%s): %d at the step, %d before; want fewer", c.step, fitSteps[c.step], at, prev)
		}
		if last := c.probe(bgAt(t, db, cwd, fitLast)); last > at {
			t.Errorf("step %d (%s): %d at the last step, %d at its own; a later step undid it", c.step, fitSteps[c.step], last, at)
		}
	}
	if n := count("What Ana set as rules")(bgAt(t, db, cwd, fitLast)); n != 9 { // 5 rules, 3 decisions, "and 5 more"
		t.Errorf("rules at the last step: %d lines, want 9", n)
	}
}

// SPEC B2, B3: a block that one step brings under 9,500 stops there; when only the closing line does not fit, the data
// is cut at a whole line, and the tail still starts on its own line.
func TestFitStopsAtTheFirstStepThatFits(t *testing.T) {
	rzNoRoles(t)
	cwd := t.TempDir()
	db := rzDB(t)
	rzSession(t, db, "s", projectRole(cwd), cwd, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	for i := 0; i < 5; i++ {
		rzEvent(t, db, "s", i+1, fmt.Sprintf("2026-11-10T10:0%d:00Z", i), "user", "prompt", "pin "+strings.Repeat("p", 290))
		rzExec(t, db, `insert into anchor(event_id, type, value) values ((select id from event where seq = ?), 'pin', 'pin')`, i+1)
	}
	rzEvent(t, db, "s", 9, "2026-11-10T10:09:00Z", "assistant", "reply", "listo")
	tkTask(t, db, "x", "", "2026-11-10T10:00:00Z", "s", 0)
	// The one task's text sets the size of the block without pins to exactly target bytes.
	size := func(target int) {
		base := len(bgAt(t, db, cwd, fitNoPins))
		var text string
		rzExec(t, db, `select 1`)
		db.QueryRow(`select text from task`).Scan(&text)
		rzExec(t, db, `update task set text = ?`, strings.Repeat("x", len(text)+target-base))
		if got := len(bgAt(t, db, cwd, fitNoPins)); got != target {
			t.Fatalf("fixture: %d bytes without pins, want %d", got, target)
		}
	}

	closing := "Shortened to fit Claude Code's 10,000-character hook limit: no pins; run " + traceCmd("resume", " -host claude-code") + " for the whole block.\n"
	size(9500 - len(closing)) // with the closing line, exactly at the limit
	b := bgHook(t, db, cwd)
	if len(b) != 9500 || !strings.HasSuffix(b, closing) {
		t.Errorf("one step is enough: %d bytes, ends %q", len(b), b[max(0, len(b)-250):])
	}

	size(9500) // fits without pins, exactly, but not with the closing line
	b = bgHook(t, db, cwd)
	if len(b) > 9500 || !strings.Contains(b, "no pins, and the end of the data above") || !strings.Contains(b, "\nWhen Ana asks to be reminded") {
		t.Errorf("only the closing line is over: %d bytes, ends %q", len(b), b[max(0, len(b)-600):])
	}
}

// The other host keeps the first session's instructions (SPEC T3) and is never shortened (B4).
func TestCodexStartBlockNotShortened(t *testing.T) {
	rzNoRoles(t)
	var out strings.Builder
	f := t.TempDir()
	if err := hookBlock(rzDB(t), "new", "startup", projectRole(f), "codex", f, &out); err != nil {
		t.Fatal(err)
	}
	if b := out.String(); !strings.Contains(b, "task add") || strings.Contains(b, "Shortened") {
		t.Errorf("codex first block:\n%s", b)
	}
}
