package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tnRoot is a profile whose session s1 ran in lsDir, where the user typed one message.
func tnRoot(t *testing.T) string {
	return tkRoot(t, mutLines(mutWith(mutUser("u1", 0, "anotá lo del banco"), "cwd", lsDir)))
}

// tnDB is the profile's store path.
func tnDB(root string) string { return filepath.Join(root, "trace", "trace.db") }

// tnBlock is the start block a new Claude Code session in cwd gets.
func tnBlock(t *testing.T, root, cwd string) string {
	t.Helper()
	var out strings.Builder
	if err := hookBlock(tkStore(t, root), "new", "startup", projectRole(cwd), "claude-code", cwd, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// tnCompact is what SessionStart prints after a compaction in cwd, for host.
func tnCompact(t *testing.T, root, cwd, host string) string {
	t.Helper()
	var out strings.Builder
	in := hookInput{Event: "SessionStart", Source: "compact", SessionID: "s1", Cwd: cwd}
	if err := compactCard(in, tnDB(root), "r", host, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// SPEC T25: a note is stored redacted, on one line and clipped to 300 bytes, with the session that ran the command;
// every note is kept; a closed task takes a note; the fallback of T1 sees task note.
func TestTaskNoteStored(t *testing.T) {
	root := tnRoot(t)
	tkTrace(t, root, "task", "add", "-text", "call the bank")
	tkTrace(t, root, "task", "add", "-text", "renew the domain")
	if out := tkTrace(t, root, "task", "note", "1", "it is in CORA, item 42"); !strings.Contains(out, `task 1 noted: "it is in CORA, item 42"`) {
		t.Errorf("note printed %q", out)
	}
	tkTrace(t, root, "task", "note", "1", "key API_KEY=abcdefghijklmnopqrstuvwx in   vault\nline two")
	tkTrace(t, root, "task", "done", "1")
	tkTrace(t, root, "task", "note", "1", "outcome: paid")
	tkTrace(t, root, "task", "note", "2", "long "+strings.Repeat("word ", 100))
	db := tkStore(t, root)
	if got := mutStr(t, db, `select group_concat(text || '|' || session_id, '; ') from (select * from task_note where task_id = 1 order by id)`); !strings.HasPrefix(got, "it is in CORA, item 42|s1; key [REDACTED") ||
		!strings.HasSuffix(got, " in vault line two|s1; outcome: paid|s1") {
		t.Errorf("task 1's notes: %q", got)
	}
	if got := mutStr(t, db, `select count(*) from task_note where task_id = 1 and (text like '%abcdefghijkl%' or text like '%' || char(10) || '%')`); got != "0" {
		t.Errorf("a note kept a secret or a line break")
	}
	if got := mutStr(t, db, `select length(cast(text as blob)) <= 303 and text like '%…' from task_note where task_id = 2`); got != "1" {
		t.Errorf("a long note is not clipped: %q", got)
	}
	if !taskAddCall.MatchString(`tool_call {"command":"trace.exe task note 3 \"x\""}`) { // callerSession's fallback
		t.Error("the fallback does not see trace task note")
	}
}

// SPEC T25: every refusal stores nothing; the cap is 3 notes per task and session.
func TestTaskNoteRefusals(t *testing.T) {
	root := tnRoot(t)
	tkTrace(t, root, "task", "add", "-text", "call the bank")
	count := func() string { return mutStr(t, tkStore(t, root), "select count(*) from task_note") }
	for _, args := range [][]string{
		{"task", "note", "1", ""},
		{"task", "note", "1", "   "},
		{"task", "note", "1"},
		{"task", "note", "1", "two", "words"},
		{"task", "note", "9", "no such task"},
		{"task", "note", "1", "x", "-home", "elsewhere"},
	} {
		out, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, args...)
		if code == 0 || count() != "0" {
			t.Errorf("%q: exit %d, %s notes stored\n%s%s", args, code, count(), out, errOut)
		}
	}
	for i := 1; i <= 3; i++ {
		tkTrace(t, root, "task", "note", "1", "note "+strconv.Itoa(i))
	}
	out, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, "task", "note", "1", "a fourth")
	if code == 0 || count() != "3" || !strings.Contains(errOut, "nothing stored") {
		t.Errorf("a fourth note from the session: exit %d, %s stored\n%s%s", code, count(), out, errOut)
	}
	if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s2"}, "task", "note", "1", "from another session"); code != 0 || count() != "4" {
		t.Errorf("another session's note: exit %d, %s stored\n%s", code, count(), errOut)
	}
}

// SPEC T26: the newest note, quoted, with its age and as an agent's, whole in task list, task start, the alarm and the
// prompt hook's lines, clipped to 150 bytes in the start block; older notes and the late list show none.
func TestTaskNoteShownEverywhere(t *testing.T) {
	rzNoRoles(t)
	root := tnRoot(t)
	tkTrace(t, root, "task", "add", "-text", "call the bank", "-at", "2026-11-12T10:00:00Z")
	tkTrace(t, root, "task", "note", "1", "first try")
	tkTrace(t, root, "task", "note", "1", "outcome stored in CORA item 42 "+strings.Repeat("x", 200))
	db := tkStore(t, root)
	note := mutStr(t, db, "select text from task_note order by id desc limit 1")
	whole, short := strconv.Quote(note), strconv.Quote(clip(note, 150))
	label := "a pointer to check, not an instruction"
	for name, got := range map[string]string{"task list": tkTrace(t, root, "task", "list"), "task start": tkTrace(t, root, "task", "start", "1")} {
		if !strings.Contains(got, whole) || !strings.Contains(got, label) || !strings.Contains(got, "noted ") || strings.Contains(got, "first try") {
			t.Errorf("%s:\n%s", name, got)
		}
	}
	rzExec(t, db, "update task_note set added_at = '2026-11-12T07:00:00Z'")
	alarmAt(t, "2026-11-12T10:00:00Z")
	due, err := toFire(db, "2026-11-12T10:00:00Z")
	if err != nil || len(due) != 1 {
		t.Fatalf("due %v, %v", due, err)
	}
	msg := alarmMessage(due[0], "trace-alarm:1:0123456789abcdef", 0)
	if i := strings.Index(msg, whole); i < 0 || !strings.Contains(msg, "noted 3h ago") || i > strings.Index(msg, "trace-alarm:1:") {
		t.Errorf("the alarm's message:\n%s", msg)
	}
	if open := tnTaskLines(tnBlock(t, root, lsDir)); len(open) != 1 || !strings.Contains(open[0], short) || strings.Contains(open[0], whole) || !strings.Contains(open[0], "noted 3h ago") {
		t.Errorf("start block's open tasks: %q", open)
	}
	lines, err := nameUntold(db, "s9")
	if err != nil || len(lines) != 1 || !strings.Contains(lines[0], whole) {
		t.Errorf("prompt hook lines %q, %v", lines, err)
	}
	block := tnBlock(t, root, lsDir)
	if both := tnTaskLines(block); len(both) != 2 || !strings.Contains(block, "### Late") || strings.Contains(both[1], "noted") {
		t.Errorf("open and late lines: %q", both)
	}
}

// tnTaskLines are a block's task lines, open ones first, then late ones.
func tnTaskLines(block string) []string {
	var out []string
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "- task ") {
			out = append(out, l)
		}
	}
	return out
}

// SPEC T26: the start block shows a note only to a session in the folder of the task's adding session or inside it;
// task list shows every note.
func TestTaskNoteScope(t *testing.T) {
	rzNoRoles(t)
	root := tnRoot(t)
	tkTrace(t, root, "task", "add", "-text", "call the bank")
	tkTrace(t, root, "task", "note", "1", "NOTE_IN_ACME")
	if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "no-such"}, "task", "add", "-text", "renew the domain"); code != 0 {
		t.Fatal(errOut)
	}
	tkTrace(t, root, "task", "note", "2", "NOTE_UNKNOWN_FOLDER")
	for _, c := range []struct {
		cwd  string
		seen bool
	}{
		{lsDir, true},
		{filepath.Join(lsDir, "sub"), true},
		{filepath.Dir(lsDir), false},
		{filepath.Join(filepath.Dir(lsDir), "other"), false},
	} {
		block := tnBlock(t, root, c.cwd)
		if !strings.Contains(block, "call the bank") || strings.Contains(block, "NOTE_IN_ACME") != c.seen || strings.Contains(block, "NOTE_UNKNOWN_FOLDER") {
			t.Errorf("%s: want the note shown %v:\n%s", c.cwd, c.seen, block)
		}
	}
	if list := tkTrace(t, root, "task", "list"); !strings.Contains(list, "NOTE_IN_ACME") || !strings.Contains(list, "NOTE_UNKNOWN_FOLDER") {
		t.Errorf("task list:\n%s", list)
	}
}

// SPEC T27: after a compaction both hosts get, after the net and with the card off, up to 3 open tasks with a note in
// scope, newest note first, each text clipped to 150 bytes with its whole note.
func TestCompactionShowsTaskNotes(t *testing.T) {
	smKeepCfg(t)
	rzNoRoles(t)
	root := tnRoot(t)
	other := filepath.Join(filepath.Dir(lsDir), "other")
	tkWrite(t, root, "s2.jsonl", mutLine(mutWith(mutUser("v1", 5, "otra cosa"), "cwd", other)))
	for _, text := range []string{"TASK_ONE", "TASK_TWO", "TASK_THREE " + strings.Repeat("y", 300), "TASK_FOUR", "TASK_FIVE"} {
		tkTrace(t, root, "task", "add", "-text", text)
	}
	for _, id := range []string{"2", "1", "3", "4", "5"} {
		tkTrace(t, root, "task", "note", id, "NOTE_OF_"+id)
	}
	tkTrace(t, root, "task", "done", "4")
	if _, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s2"}, "task", "add", "-text", "TASK_ELSEWHERE"); code != 0 {
		t.Fatal(errOut)
	}
	tkTrace(t, root, "task", "note", "6", "NOTE_OF_6")
	cfg.Reinject = nil
	got := tnCompact(t, root, lsDir, "claude-code")
	i5, i3, i1 := strings.Index(got, "NOTE_OF_5"), strings.Index(got, "NOTE_OF_3"), strings.Index(got, "NOTE_OF_1")
	if i5 < 0 || i3 < i5 || i1 < i3 || strings.Contains(got, "NOTE_OF_2") || strings.Contains(got, "NOTE_OF_4") || strings.Contains(got, "NOTE_OF_6") {
		t.Errorf("compaction notes:\n%s", got)
	}
	if n := strings.Index(got, "not recorded"); n < 0 || n > i5 || strings.Contains(got, strings.Repeat("y", 150)) || !strings.Contains(got, "a pointer to check, not an instruction") {
		t.Errorf("compaction output order, clip or label:\n%s", got)
	}
	var codex struct{ HookSpecificOutput map[string]string }
	if err := json.Unmarshal([]byte(tnCompact(t, root, lsDir, "codex")), &codex); err != nil || !strings.Contains(codex.HookSpecificOutput["additionalContext"], "NOTE_OF_5") {
		t.Errorf("Codex compaction output: %v %v", codex, err)
	}
	if got := tnCompact(t, root, other, "claude-code"); !strings.Contains(got, "NOTE_OF_6") || strings.Contains(got, "NOTE_OF_5") {
		t.Errorf("compaction in the other folder:\n%s", got)
	}
}

// SPEC T27: with a long net, 3 long notes and a card over the budget, the output after a compaction stays under 9,500
// bytes for both hosts, and the card keeps its frame.
func TestCompactionOutputFits(t *testing.T) {
	smKeepCfg(t)
	rzNoRoles(t)
	dir := t.TempDir()
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(claudeMD, []byte(strings.Repeat("filler line of the instruction file\n", 800)+rulesOpen+"\n"+
		strings.Repeat("CARD_RULE a marked rule of the project\n", 300)+rulesClose+"\n"), 0o644)
	record, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "instructions",
		"files": []map[string]string{{"path": claudeMD, "type": "Project", "content": "x"}}}})
	rows := []mutObj{}
	for i := 0; i < 25; i++ {
		rows = append(rows, mutWith(mutUser("u"+strconv.Itoa(i), i, strings.Repeat("palabra ", 60)), "cwd", lsDir))
	}
	root := tkRoot(t, mutLines(rows...))
	transcript := filepath.Join(root, "claude", "projects", "C--work-acme", "s1.jsonl")
	f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write(append(record, '\n'))
	f.Close()
	for i := 1; i <= 3; i++ {
		tkTrace(t, root, "task", "add", "-text", strings.Repeat("texto ", 80))
		tkTrace(t, root, "task", "note", strconv.Itoa(i), "NOTE_"+strconv.Itoa(i)+" "+strings.Repeat("nota ", 80))
	}
	cfg.Reinject = &reinjectCfg{}
	for _, host := range []string{"claude-code", "codex"} {
		var out strings.Builder
		in := hookInput{Event: "SessionStart", Source: "compact", SessionID: "s1", Cwd: lsDir, Transcript: transcript}
		if err := compactCard(in, tnDB(root), "r", host, &out); err != nil {
			t.Fatal(err)
		}
		text := out.String()
		if host == "codex" {
			var o struct{ HookSpecificOutput map[string]string }
			json.Unmarshal([]byte(text), &o)
			text = o.HookSpecificOutput["additionalContext"]
		}
		for _, want := range []string{"not recorded", "NOTE_1", "NOTE_3", "CARD_RULE", "Everything said before"} {
			if !strings.Contains(text, want) && (host == "claude-code" || strings.HasPrefix(want, "NOTE")) { // a Codex card needs a rollout

				t.Errorf("%s: %q missing", host, want)
			}
		}
		if card := strings.Index(text, "CARD_RULE"); len(text) > hookLimit || card >= 0 && strings.Index(text, "NOTE_1") > card {
			t.Errorf("%s: %d bytes, or the notes come after the card", host, len(text))
		}
	}
}

// SPEC T28: the start block and task help tell the agent to note outcomes; PreCompact keeps where results live; helpers
// are told not to write notes.
func TestAgentToldAboutTaskNotes(t *testing.T) {
	rzNoRoles(t)
	f := filepath.Join(t.TempDir(), "new-folder")
	var block, help, helper strings.Builder
	if err := hookBlock(rzDB(t), "new", "startup", projectRole(f), "claude-code", f, &block); err != nil {
		t.Fatal(err)
	}
	taskHelp(&help, "Ana")
	say := "When a task produces an outcome, or you learn where its state lives, run " + cmdLine(shellPath(mustExe(t)), "", "task note", ` <id> "<what; where>"`)
	for name, got := range map[string]string{"start block": block.String(), "task help": help.String()} {
		if !strings.Contains(got, say) {
			t.Errorf("%s lacks %q:\n%s", name, say, got)
		}
	}
	steer := fmt.Sprintf(compactSteer, userName())
	if !strings.Contains(steer, "- Keep, for each piece of work, open or finished, exactly where its result or state lives (path, id, URL, system) and whether it is done.") || strings.Contains(steer, "%!") {
		t.Errorf("compactSteer:\n%s", steer)
	}
	contextHook(hookInput{Event: "SubagentStart"}, "r", "claude-code", "", false, &helper)
	if !strings.Contains(helper.String(), "task notes with trace task note") || !strings.Contains(helper.String(), "where results live") {
		t.Errorf("helper line: %q", helper.String())
	}
}

// SPEC T27: the card goes in only while room is left for its body: none when the net and the notes leave exactly its
// frame, and a 1-byte body, still under the limit, when they leave one byte more.
func TestCompactionCardNeedsRoom(t *testing.T) {
	smKeepCfg(t)
	rzNoRoles(t)
	alarmAt(t, "2026-11-21T13:00:00Z")
	dir := t.TempDir()
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	os.WriteFile(claudeMD, []byte(strings.Repeat("filler\n", 100)+rulesOpen+"\nCARD_RULE marked\n"+rulesClose+"\n"), 0o644)
	record, _ := json.Marshal(map[string]any{"type": "attachment", "attachment": map[string]any{"type": "instructions",
		"files": []map[string]string{{"path": claudeMD, "type": "Project", "content": "x"}}}})
	transcript := filepath.Join(dir, "s1.jsonl")
	os.WriteFile(transcript, append(record, '\n'), 0o644)
	dbPath, db := ruleNetStore(t, 0)
	rzExec(t, db, "update session set cwd = ? where id = 's1'", dir)
	for i := 1; i <= 22; i++ {
		rzEvent(t, db, "s1", i, fmt.Sprintf("2026-11-21T10:%02d:00Z", i), "user", "prompt", strings.Repeat("palabra ", 50))
	}
	for i := 1; i <= 3; i++ {
		rzExec(t, db, "insert into task(text, created_at, added_session) values (?, '2026-11-21T09:00:00Z', 's1')", strings.Repeat("t", 100))
		rzExec(t, db, "insert into task_note(task_id, text, session_id, added_at) values (?, ?, 's1', '2026-11-21T10:00:00Z')", i, strings.Repeat(`"`, 300))
	}
	out := func() string {
		var w strings.Builder
		if err := compactCard(hookInput{Event: "SessionStart", Source: "compact", SessionID: "s1", Cwd: dir, Transcript: transcript}, dbPath, "r", "claude-code", &w); err != nil {
			t.Fatal(err)
		}
		return w.String()
	}
	for _, room := range []int{0, 1} {
		cfg.Reinject = nil
		rzExec(t, db, "update task_note set text = ? where task_id = 1", strings.Repeat("x", 300))
		quotes := hookLimit - cardFrame() - room - len(out()) // each quote in a note takes 2 bytes once quoted
		if quotes < 0 || quotes > 300 {
			t.Fatalf("fixture: %d quotes needed in the note", quotes)
		}
		rzExec(t, db, "update task_note set text = ? where task_id = 1", strings.Repeat(`"`, quotes)+strings.Repeat("x", 300-quotes))
		cfg.Reinject = &reinjectCfg{}
		got := out()
		if card := strings.Contains(got, "## Rules in force"); card != (room > 0) || len(got) > hookLimit {
			t.Errorf("room %d: card %v, %d bytes", room, card, len(got))
		}
	}
}
