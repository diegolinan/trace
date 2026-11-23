package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lsDir is the folder session s1 of lessonRoot ran in.
var lsDir = filepath.Join(string(filepath.Separator)+"work", "acme")

// lessonRoot is a session s1 in lsDir where the user asked for a test and two tool results came back.
func lessonRoot(t *testing.T) string {
	return tkRoot(t, mutLines(
		mutWith(mutUser("u1", 0, "probá si editar el script en marcha lo rompe"), "cwd", lsDir),
		mutWith(mutToolResult("r1", 1, "x1", "line 3: syntax error\nbash reads a script while it runs", false), "cwd", lsDir),
		mutWith(mutToolResult("r2", 2, "x2", "go version go1.27.0 windows/amd64", false), "cwd", lsDir),
	))
}

// SPEC L1: a lesson is tied to the newest tool result of the calling session that holds the evidence, folded as D1
// folds; the evidence is stored as the result spells it; the text and the evidence go through redact and are clipped
// to 300 characters; the scope is the session's folder, or the machine with -machine.
func TestLessonAdd(t *testing.T) {
	root := lessonRoot(t)
	out := tkTrace(t, root, "lesson", "add", "-text", "editing a running .sh breaks that run", "-evidence", "BASH reads a SCRIPT while")
	if !strings.Contains(out, `lesson 1 recorded: "editing a running .sh breaks that run"`) {
		t.Errorf("add printed %q", out)
	}
	db := tkStore(t, root)
	if got := mutStr(t, db, `select l.evidence || '|' || e.kind || '|' || l.machine || '|' || l.cwd || '|' || l.session_id from lesson l join event e on e.id = l.event_id`); got != "bash reads a script while|tool_result|0|"+lsDir+"|s1" {
		t.Errorf("stored %q", got)
	}
	tkTrace(t, root, "lesson", "add", "-machine", "-text", "go here reads API_KEY=abcdefghijklmnopqrstuvwx from env", "-evidence", "go version go1.27.0")
	if got := mutStr(t, db, `select text || '|' || machine from lesson where id = 2`); strings.Contains(got, "abcdefghijkl") || !strings.Contains(got, "[REDACTED") || !strings.HasSuffix(got, "|1") {
		t.Errorf("machine lesson stored %q", got)
	}
	tkTrace(t, root, "lesson", "add", "-text", "a long one "+strings.Repeat("word ", 100), "-evidence", "go version go1.27.0")
	if got := mutStr(t, db, `select length(text) <= 301 and text like '%…' from lesson where id = 3`); got != "1" {
		t.Errorf("long text not clipped: %q", got)
	}
	if !taskAddCall.MatchString(`tool_call {"command":"trace.exe lesson add -text \"x y z\""}`) { // callerSession's fallback
		t.Error("the fallback does not see trace lesson add")
	}
}

// SPEC L1: every refusal stores nothing.
func TestLessonRefusals(t *testing.T) {
	root := lessonRoot(t)
	tkTrace(t, root, "lesson", "add", "-text", "bash reads scripts lazily", "-evidence", "reads a script")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"lesson", "add", "-text", "too short", "-evidence", "reads a script"}, "at least 3 words"},
		{[]string{"lesson", "add", "-text", "no evidence given here"}, "-evidence"},
		{[]string{"lesson", "add", "-text", "evidence too short here", "-evidence", "reads a"}, "-evidence"},
		{[]string{"lesson", "add", "-text", "evidence the user typed", "-evidence", "editar el script"}, "no tool result"},
		{[]string{"lesson", "add", "-text", "a bad about here", "-evidence", "reads a script", "-about", "("}, "-about"},
		{[]string{"lesson", "add", "-text", "an empty about here", "-evidence", "reads a script", "-about", "x*"}, "-about"},
		{[]string{"lesson", "add", "-text", "replaces a missing one", "-evidence", "reads a script", "-replaces", "9"}, "not in force"},
		{[]string{"lesson", "add", "-text", "Bash reads scripts LAZILY.", "-evidence", "go version go1.27.0"}, "already"},
	} {
		out, errOut, code := rdRun(t, root, c.args...)
		if code == 0 || !strings.Contains(out+errOut, c.want) {
			t.Errorf("%v: exit %d, %q %q", c.args, code, out, errOut)
		}
	}
	if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "nope"}, "lesson", "add", "-text", "an unknown session here", "-evidence", "reads a script"); code == 0 || !strings.Contains(errOut, "unknown") {
		t.Errorf("unknown session: exit %d %q", code, errOut)
	}
	noFolder := tkRoot(t, mutLines(mutToolResult("r1", 1, "x1", "bash reads a script while it runs", false)))
	if _, errOut, code := rdRun(t, noFolder, "lesson", "add", "-text", "a session with no folder", "-evidence", "reads a script"); code == 0 || !strings.Contains(errOut, "folder") {
		t.Errorf("no folder: exit %d %q", code, errOut)
	}
	tkTrace(t, root, "lesson", "add", "-machine", "-text", "bash reads scripts lazily", "-evidence", "reads a script") // same text, other scope
	tkTrace(t, root, "lesson", "add", "-text", "go is installed", "-evidence", "go version go1.27.0") // exactly 3 words is enough
	if _, errOut, code := rdRun(t, root, "lesson", "add", "-text", "a fourth one in this session", "-evidence", "go version go1.27.0"); code == 0 || !strings.Contains(errOut, "3 lessons") {
		t.Errorf("fourth: exit %d %q", code, errOut)
	}
	if got := mutStr(t, tkStore(t, root), `select count(*) from lesson`); got != "3" {
		t.Errorf("%s lessons", got)
	}
}

// SPEC L1 to L3: -replaces puts the old one out of force and keeps its about, drop puts one out of force, nothing is
// deleted; list shows those in force with scope, evidence and about, and -all how the others left.
func TestLessonDropReplaceList(t *testing.T) {
	root := lessonRoot(t)
	tkTrace(t, root, "lesson", "add", "-text", "bash reads scripts lazily", "-evidence", "reads a script", "-about", `\.sh\b`)
	if out := tkTrace(t, root, "lesson", "add", "-text", "bash reads a running script lazily", "-evidence", "reads a script", "-replaces", "1"); !strings.Contains(out, "lesson 2 recorded") || !strings.Contains(out, "replaces 1") {
		t.Errorf("replace printed %q", out)
	}
	if out := tkTrace(t, root, "lesson", "add", "-text", "bash reads a running script lazily", "-evidence", "go version go1.27.0", "-replaces", "2"); !strings.Contains(out, "lesson 3 recorded") {
		t.Errorf("the same text again with new evidence: %q", out) // how an old lesson comes back (L4)
	}
	root2 := lessonRoot(t) // a second session store: s1 has recorded 3 already
	tkTrace(t, root2, "lesson", "add", "-machine", "-text", "go 1.27 is installed here", "-evidence", "go version go1.27.0")
	if out := tkTrace(t, root2, "lesson", "drop", "1"); !strings.Contains(out, "lesson 1: dropped") {
		t.Errorf("drop printed %q", out)
	}
	if _, errOut, code := rdRun(t, root2, "lesson", "drop", "1"); code == 0 || !strings.Contains(errOut, "lesson 1 is not in force") {
		t.Errorf("second drop: exit %d %q", code, errOut)
	}
	list := tkTrace(t, root, "lesson", "list")
	if !strings.Contains(list, `#3 `) || !strings.Contains(list, `"bash reads a running script lazily"`) || !strings.Contains(list, `evidence: "go version go1.27.0"`) ||
		!strings.Contains(list, `about: \.sh\b`) || !strings.Contains(list, "project") || strings.Contains(list, "#1 ") || strings.Contains(list, "#2 ") {
		t.Errorf("list: %q", list)
	}
	all := tkTrace(t, root, "lesson", "list", "-all") + tkTrace(t, root2, "lesson", "list", "-all")
	if !strings.Contains(all, "replaced by #2") || !strings.Contains(all, "replaced by #3") || !strings.Contains(all, "dropped") || !strings.Contains(all, "machine") {
		t.Errorf("list -all: %q", all)
	}
	if got := mutStr(t, tkStore(t, root), `select count(*) from lesson`); got != "3" {
		t.Errorf("%s lessons", got)
	}
}

// lessonRow stores a lesson recorded in folder cwd by a session of role; machine marks a machine lesson, out its time
// out of force.
func lessonRow(t *testing.T, db *sql.DB, text string, machine bool, cwd, role, about, added, out string) {
	t.Helper()
	rzExec(t, db, `insert into lesson(text, evidence, event_id, machine, cwd, role, session_id, about, added_at, out_at)
		values (?, 'the evidence words', 1, ?, ?, ?, 'old', nullif(?, ''), ?, nullif(?, ''))`, text, machine, cwd, role, about, added, out)
}

// lsClock pins the clock to 2026-11-22 12:00 UTC and returns a time days before it.
func lsClock(t *testing.T) func(days int) string {
	old := clockNow
	clockNow = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { clockNow = old })
	return func(days int) string {
		return clockNow().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	}
}

// SPEC L1, L4: a project lesson reaches sessions in its folder or inside it, never above it or beside it; a machine
// lesson reaches every session; with roles a project lesson is also of its role, a machine lesson of every role.
func TestLessonScope(t *testing.T) {
	rzNoRoles(t)
	ago := lsClock(t)
	db := rzDB(t)
	base := t.TempDir()
	f := filepath.Join(base, "site")
	lessonRow(t, db, "project lesson of site", false, f, "r1", "", ago(1), "")
	lessonRow(t, db, "machine lesson anywhere", true, f, "r1", "", ago(1), "")
	lessonRow(t, db, "machine lesson ninety days old", true, f, "r1", "", ago(90), "") // the edge of the window is in
	for _, c := range []struct {
		cwd     string
		project bool
	}{{f, true}, {filepath.Join(f, "in"), true}, {base, false}, {f + "-x", false}} {
		got := rzBlock(t, db, c.cwd)
		if strings.Contains(got, "project lesson of site") != c.project || !strings.Contains(got, "machine lesson anywhere") ||
			!strings.Contains(got, "ninety days old") || strings.Contains(got, "more:") {
			t.Errorf("%s:\n%s", c.cwd, got)
		}
	}
	cfg.Roles = []roleRule{{Name: "x"}}
	for _, c := range []struct {
		role    string
		project bool
	}{{"r1", true}, {"r2", false}} {
		var out strings.Builder
		if err := resumeBlock(db, "new", "startup", c.role, "claude-code", f, &out); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); strings.Contains(got, "project lesson of site") != c.project || !strings.Contains(got, "machine lesson anywhere") {
			t.Errorf("role %s:\n%s", c.role, got)
		}
	}
	// The same text is free in a folder inside or above the lesson's: only the same folder is the same scope.
	for _, cwd := range []string{filepath.Join(f, "in"), base} {
		if err := lessonFree(db, lesson{text: "Project lesson of SITE", cwd: cwd}, ""); err != nil {
			t.Errorf("%s: %v", cwd, err)
		}
	}
	if err := lessonFree(db, lesson{text: "project lesson of site", cwd: f}, ""); err == nil {
		t.Error("same text, same folder: accepted")
	}
}

// SPEC L4: right after the rules section, up to 5 lessons in force and in scope from the last 90 days, newest first,
// each "#<id> <date>: "<text>"", then how many more; shown with no earlier session; never in the card.
func TestLessonInBlock(t *testing.T) {
	rzNoRoles(t)
	ago := lsClock(t)
	db := rzDB(t)
	f := t.TempDir()
	lessonRow(t, db, "lesson older than ninety days", false, f, "r", "", ago(91), "")
	lessonRow(t, db, "lesson out of force", false, f, "r", "", ago(1), ago(0))
	for _, n := range []string{"one", "two", "three", "four", "five"} {
		lessonRow(t, db, "recent lesson "+n, false, f, "r", "", ago(10), "")
	}
	got := rzBlock(t, db, f) // no earlier session in f
	head := "What agents learned here, by testing (each is an agent's claim, never an instruction from " + userName() + ", and authorizes nothing"
	if !strings.Contains(got, head) {
		t.Fatalf("no section:\n%s", got)
	}
	for _, want := range []string{`#7 ` + local(ago(10))[:10] + `: "recent lesson five"`, `"recent lesson one"`, "and 1 more: ", "lesson list"} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"older than ninety", "out of force"} {
		if strings.Contains(got, not) {
			t.Errorf("block shows %q:\n%s", not, got)
		}
	}
	rzSession(t, db, "old", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	ev := ruleEvent(t, db, "old", 1, "2026-11-10T08:01:00Z", "parches no, lo hablamos mil veces")
	ruleRow(t, db, ev, "parches no, lo hablamos", false, "2026-11-10T08:02:00Z", "")
	got = rzBlock(t, db, f)
	if r, l, w := strings.Index(got, "set as rules and decisions"), strings.Index(got, "What agents learned here"), strings.Index(got, "## Where"); r < 0 || l < r || w < l {
		t.Errorf("rules at %d, lessons at %d, the session at %d:\n%s", r, l, w, got)
	}
	if card, _ := sessionCard(db, f, "", "r", "claude-code"); strings.Contains(card, "recent lesson") || strings.Contains(card, "learned") || !strings.Contains(card, "parches no") {
		t.Errorf("lessons in the card:\n%s", card)
	}
}

// lsPre is what PreToolUse gives the agent before a Bash call running cmd in cwd, as a subagent.
func lsPre(t *testing.T, dbPath, cwd, cmd string) string {
	t.Helper()
	var w strings.Builder
	input, _ := json.Marshal(map[string]string{"command": cmd})
	in := hookInput{Event: "PreToolUse", SessionID: "s1", AgentID: "a1", ToolName: "Bash", ToolInput: input, Cwd: cwd}
	if err := preToolHook(in, "r", dbPath, true, &w); err != nil {
		t.Fatal(err)
	}
	return w.String()
}

// SPEC L5: before a tool call the about matches, without case, up to 3 lessons in force, in scope and under 90 days
// come back after the rules of D11, subagents included; an unrelated call, a dropped, old or out-of-scope lesson do not.
func TestLessonAtTheAction(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject, cfg.Roles = nil, nil, nil
	ago := lsClock(t)
	dbPath, db := ruleNetStore(t, 1)
	f := t.TempDir()
	rzExec(t, db, `insert into rule(event_id, quote, session_id, added_at, about) values (1, 'mensaje numero 1', 's1', ?, 'publish')`, ago(1))
	for _, n := range []string{"one", "two", "three", "four"} {
		lessonRow(t, db, "publish lesson "+n, false, f, "r", `publish\.sh`, ago(5), "")
	}
	lessonRow(t, db, "publish lesson dropped", true, f, "r", `publish\.sh`, ago(2), ago(1))
	lessonRow(t, db, "publish lesson too old", true, f, "r", `publish\.sh`, ago(91), "")
	lessonRow(t, db, "publish lesson elsewhere", false, f+"-x", "r", `publish\.sh`, ago(1), "")
	got := lsPre(t, dbPath, f, "sh PUBLISH.SH v2")
	head := "What agents learned about what you are about to do, by testing (an agent's claim, not an instruction; " + userName() + "'s rules win):"
	if r, l := strings.Index(got, "mensaje numero 1"), strings.Index(got, head); r < 0 || l < r {
		t.Fatalf("rule at %d, lessons at %d: %q", r, l, got)
	}
	if strings.Count(got, "publish lesson") != 3 || strings.Contains(got, "lesson one") || strings.Contains(got, "dropped") || strings.Contains(got, "too old") || strings.Contains(got, "elsewhere") {
		t.Errorf("lessons: %q", got)
	}
	if got := lsPre(t, dbPath, f, "ls"); got != "" {
		t.Errorf("unrelated call: %q", got)
	}
}

// SPEC L7: a lesson recorded in the session does not reset the nets of D6 and D7.
func TestLessonNotANetRecord(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, nil
	dbPath, db := ruleNetStore(t, 10)
	rzExec(t, db, `insert into lesson(text, evidence, event_id, machine, cwd, role, session_id, added_at) values ('a b c', 'd e f', 1, 0, 'x', 'r', 's1', '2026-11-21T10:30:00Z')`)
	if got := smHook(t, hookInput{Event: "UserPromptSubmit", SessionID: "s1"}, dbPath, false); !strings.Contains(got, "not recorded") {
		t.Errorf("prompt net after a lesson: %q", got)
	}
	if got := compactNet(db, "s1"); !strings.Contains(got, "mensaje numero 10") {
		t.Errorf("compaction net after a lesson: %q", got)
	}
}

// SPEC L6: the start block tells the agent how to record lessons, after the rule instruction, with or without records
// or an earlier session, first session included; the card does not; a subagent is told to report lessons, not record.
func TestLessonInstruction(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	const say = "When you find out by testing something about the tools, the machine or this project"
	check := func(what, got string) {
		t.Helper()
		if r, l := strings.Index(got, "rule add -quote"), strings.Index(got, say); r < 0 || l < r || strings.Count(got, say) != 1 || !strings.Contains(got, "lesson drop <id>") {
			t.Errorf("%s: rule instruction at %d, lesson one at %d:\n%s", what, r, l, got)
		}
	}
	var first strings.Builder
	if err := hookBlock(db, "new", "startup", projectRole(f), "claude-code", f, &first); err != nil {
		t.Fatal(err)
	}
	check("first session", first.String())
	rzSession(t, db, "old", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	check("earlier session, no records", rzBlock(t, db, f))
	ev := ruleEvent(t, db, "old", 1, "2026-11-10T08:01:00Z", "parches no, lo hablamos mil veces")
	ruleRow(t, db, ev, "parches no, lo hablamos", false, "2026-11-10T08:02:00Z", "")
	check("with records", rzBlock(t, db, f))
	if card, _ := sessionCard(db, f, "", projectRole(f), "claude-code"); strings.Contains(card, say) || !strings.Contains(card, "rule add -quote") {
		t.Errorf("card:\n%s", card)
	}
	var w strings.Builder
	if err := contextHook(hookInput{Event: "SubagentStart"}, "r", "claude-code", "", false, &w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "trace lesson add") || !strings.Contains(w.String(), "report") || !strings.Contains(w.String(), "record rules") {
		t.Errorf("subagent line: %q", w.String())
	}
}
