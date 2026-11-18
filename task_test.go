package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tkRoot is a throwaway profile for runTrace with one Claude Code transcript, s1, holding lines.
func tkRoot(t *testing.T, lines string) string {
	t.Helper()
	root := t.TempDir()
	tkWrite(t, root, "s1.jsonl", lines)
	return root
}

// tkWrite writes a transcript into the profile's Claude Code projects folder.
func tkWrite(t *testing.T, root, name, lines string) {
	t.Helper()
	dir := filepath.Join(root, "user", ".claude", "projects", "C--work-acme")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tkStore opens the profile's store.
func tkStore(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(root, "trace", "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// tkTrace runs trace in the profile as session s1 (the transcript tkRoot writes) runs a command, and fails the test on
// a non-zero exit.
func tkTrace(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"}, args...)
	if code != 0 || strings.Contains(errOut, "import before task failed") {
		t.Fatalf("trace %s: exit %d\n%s\n%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

// SPEC T1: the task is tied to the newest message the user typed, read in by the add itself; a later prompt a program
// wrote or a suggestion the user accepted is not that message.
func TestTaskAddTiesToAskingMessage(t *testing.T) {
	root := tkRoot(t, mutLines(
		mutUser("u1", 0, "acordate de llamar al banco"),
		mutUser("u2", 1, "dale, seguí", "promptSource", "suggestion_accepted")))
	tkWrite(t, root, "s2.jsonl", mutLine(mutUser("p1", 2, "a program wrote this", "entrypoint", "sdk-cli")))
	out := tkTrace(t, root, "task", "add", "-text", "call the bank")
	if !strings.Contains(out, "task 1 added") {
		t.Errorf("the add does not print the task's id: %q", out)
	}
	db := tkStore(t, root)
	got := mutStr(t, db, "select t.text || ' | ' || coalesce(e.text, '<none>') || ' | ' || coalesce(t.closed_at, 'open') from task t left join event e on e.id = t.asked_event")
	if got != "call the bank | acordate de llamar al banco | open" {
		t.Errorf("task, asking words, state = %q", got)
	}
}

// SPEC T1, T2: the ask and the close are the newest typed message of the session that ran the command, never a newer
// one typed in another session; a session not known gives no message.
func TestTaskAddAskedInItsOwnSession(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "acordate de llamar al banco")))
	tkWrite(t, root, "s2.jsonl", mutLine(mutUser("u2", 5, "dale")))
	run := func(session string, args ...string) {
		t.Helper()
		if out, errOut, code := runTrace(t, root, "", map[string]string{"CLAUDE_CODE_SESSION_ID": session}, args...); code != 0 {
			t.Fatalf("trace %v in %s: exit %d\n%s\n%s", args, session, code, out, errOut)
		}
	}
	run("s1", "task", "add", "-text", "call the bank")
	run("s2", "task", "add", "-text", "renew the domain")
	run("no-such-session", "task", "add", "-text", "pay the rent")
	run("s2", "task", "done", "2")
	got := mutStr(t, tkStore(t, root), `select group_concat(t.text || ' | ' || coalesce(a.text, '<none>') || ' | ' || coalesce(c.text, '<open>'), '; ')
		from task t left join event a on a.id = t.asked_event left join event c on c.id = t.closed_event`)
	if want := "call the bank | acordate de llamar al banco | <open>; renew the domain | dale | dale; pay the rent | <none> | <open>"; got != want {
		t.Errorf("task, ask, close:\n got %s\nwant %s", got, want)
	}
	// SPEC T1: tied to no message without -self is not passed off as the agent's own.
	list := tkTrace(t, root, "task", "list")
	if !strings.Contains(list, "pay the rent (the message that asked for it is not known)") || strings.Contains(list, "on its own") {
		t.Errorf("a task whose ask is not known:\n%s", list)
	}
	msg := alarmMessage(dueTask{id: 3, text: "pay the rent"}, "trace-alarm:3:0123456789abcdef", 0)
	if !strings.Contains(msg, "TRACE does not know which message of "+userName()+" asked for it.") || strings.Contains(msg, "on its own") {
		t.Errorf("the alarm's message for a task whose ask is not known:\n%s", msg)
	}
}

// SPEC T1: with -self no message asked for the task, so it is tied to none and every listing says an agent added it,
// instead of quoting the user's newest words as the ask.
func TestTaskAddSelfTiesToNoMessage(t *testing.T) {
	root := tkRoot(t, mutLines(mutUser("u1", 0, "explicate")))
	if out := tkTrace(t, root, "task", "add", "-text", "test the alarm", "-self"); !strings.Contains(out, "task 1 added") {
		t.Errorf("the add does not print the task's id: %q", out)
	}
	if got := mutStr(t, tkStore(t, root), "select coalesce(asked_event, '<none>') from task where id = 1"); got != "<none>" {
		t.Errorf("a self-added task is tied to message %s", got)
	}
	list := tkTrace(t, root, "task", "list")
	if strings.Contains(list, "explicate") || !strings.Contains(list, "test the alarm (added by an agent on its own, not asked by ") {
		t.Errorf("the list does not say an agent added the task:\n%s", list)
	}
}

// SPEC T1, A10: -at is echoed as understood, with its weekday and its offset: the computer's for YYYY-MM-DD HH:MM, its
// own for RFC 3339; it is stored in UTC.
func TestTaskAddEchoesTheTime(t *testing.T) {
	root := t.TempDir()
	localAt := time.Date(2026, 11, 16, 15, 0, 0, 0, time.Local)
	utcAt := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	for at, want := range map[string]time.Time{"2026-11-16 15:00": localAt, "2026-11-17T09:30:00Z": utcAt} {
		out := tkTrace(t, root, "task", "add", "-text", "x "+at, "-at", at)
		_, off := want.Zone()
		if echo := want.Format("Monday 2006-01-02 15:04") + " (" + offsetName(off) + ")"; !strings.Contains(out, echo) {
			t.Errorf("-at %q: output %q does not echo %q", at, out, echo)
		}
		if got := mutStr(t, tkStore(t, root), "select due from task where text = ?", "x "+at); got != want.UTC().Format(time.RFC3339) {
			t.Errorf("-at %q stored as %s", at, got)
		}
	}
	if out := tkTrace(t, root, "task", "add", "-text", "no time"); strings.Contains(out, "2026") {
		t.Errorf("a task without -at echoes a time: %q", out)
	}
}

// SPEC T1: an -at that is neither form, or a missing -text, is refused and nothing is stored.
func TestTaskAddRefusesABadTime(t *testing.T) {
	root := t.TempDir()
	for _, c := range []struct {
		args []string
		says string
	}{
		{[]string{"task", "add", "-text", "x", "-at", "tomorrow"}, `-at "tomorrow"`},
		{[]string{"task", "add", "-text", "x", "-at", "16/11/2026 15:00"}, "YYYY-MM-DD HH:MM"},
		{[]string{"task", "add", "-text", "x", "-at", "2026-11-16"}, "RFC 3339"},
		{[]string{"task", "add", "-at", "2026-11-16 15:00"}, "-text"},
		{[]string{"task"}, "task needs add"},
		{[]string{"task", "remind"}, "task needs add"},
	} {
		if _, errOut, code := runTrace(t, root, "", nil, c.args...); code == 0 || !strings.Contains(errOut, c.says) {
			t.Errorf("trace %s: exit %d, stderr %q; want a refusal that says %q", strings.Join(c.args, " "), code, errOut, c.says)
		}
	}
	if n := mutStr(t, tkStore(t, root), "select count(*) from task"); n != "0" {
		t.Errorf("%s task(s) stored after refusals", n)
	}
}

// SPEC T2: done and drop close an open task with when and the words that closed it; closing again or an unknown id
// fails and changes nothing; list shows the open ones, oldest first, with id, age, time and the asking words.
func TestTaskCloseAndList(t *testing.T) {
	asking := mutLines(mutUser("u1", 0, "anotá que hay que revisar el contrato"))
	root := tkRoot(t, asking)
	tkTrace(t, root, "task", "add", "-text", "review the contract")
	tkTrace(t, root, "task", "add", "-text", "call the bank", "-at", "2026-11-16 15:00")
	tkTrace(t, root, "task", "add", "-text", "renew the domain")
	tkWrite(t, root, "s1.jsonl", asking+mutLine(mutUser("u2", 5, "listo, ya lo revisé")))
	if out := tkTrace(t, root, "task", "done", "1"); out != "task 1 closed: done\n" {
		t.Errorf("done prints %q", out)
	}
	db := tkStore(t, root)
	const closed = "select closed_how || ' | ' || coalesce(closed_at, '') || ' | ' || coalesce((select text from event where id = closed_event), '<none>') from task where id = ?"
	first := mutStr(t, db, closed, 1)
	if !strings.HasPrefix(first, "done | 20") || !strings.HasSuffix(first, " | listo, ya lo revisé") {
		t.Errorf("task 1 closed as %q", first)
	}
	tkWrite(t, root, "s1.jsonl", asking+mutLines(mutUser("u2", 5, "listo, ya lo revisé"), mutUser("u3", 6, "otra cosa")))
	for args, says := range map[string]string{"done 1": "task 1 is not an open task", "drop 1": "task 1 is not an open task",
		"drop 99": "task 99 is not an open task", "done x": "task x is not an open task", "done": "task done needs a task id"} {
		if _, errOut, code := runTrace(t, root, "", nil, append([]string{"task"}, strings.Fields(args)...)...); code == 0 || !strings.Contains(errOut, says) {
			t.Errorf("trace task %s: exit %d, stderr %q; want a failure that says %q", args, code, errOut, says)
		}
	}
	if again := mutStr(t, db, closed, 1); again != first {
		t.Errorf("closing a closed task changed it: %q, was %q", again, first)
	}
	list := tkTrace(t, root, "task", "list")
	due := time.Date(2026, 11, 16, 15, 0, 0, 0, time.Local).Format("Monday 2006-01-02 15:04")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "- task 2, ") || !strings.HasPrefix(lines[1], "- task 3, ") {
		t.Fatalf("list is not the open tasks, oldest first:\n%s", list)
	}
	for _, want := range []string{" ago", "due " + due, "call the bank", `asked: "anotá que hay que revisar el contrato"`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("list line %q lacks %q", lines[0], want)
		}
	}
	if strings.Contains(lines[1], "due ") {
		t.Errorf("a task without a time shows one: %q", lines[1])
	}
	tkTrace(t, root, "task", "drop", "2")
	if got := mutStr(t, db, closed, 2); !strings.HasPrefix(got, "drop | 20") || !strings.HasSuffix(got, " | otra cosa") {
		t.Errorf("task 2 dropped as %q", got)
	}
	if list := tkTrace(t, root, "task", "list"); strings.Contains(list, "task 2") || !strings.Contains(list, "task 3") {
		t.Errorf("list after the drop:\n%s", list)
	}
	tkTrace(t, root, "task", "drop", "3")
	if list := tkTrace(t, root, "task", "list"); strings.TrimSpace(list) != "No open tasks." {
		t.Errorf("list with no open task: %q", list)
	}
}

// tkTask stores a task asked by the typed message askedSeq of session sid (0: none), opened at created.
func tkTask(t *testing.T, db *sql.DB, text, due, created, sid string, askedSeq int) {
	t.Helper()
	rzExec(t, db, `insert into task(text, due, asked_event, created_at) values (?, nullif(?, ''),
		(select id from event where session_id = ? and seq = ?), ?)`, text, due, sid, askedSeq, created)
}

// SPEC T3: the start block lists the open tasks, at most 5, oldest first, with how many more and the asking words,
// and tells the agent how to add one and when to close one.
func TestStartBlockShowsOpenTasks(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	cwd := t.TempDir()
	rzSession(t, db, "s", projectRole(cwd), cwd, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "s", 1, "2026-11-10T10:00:00Z", "user", "prompt", "recordame pagar el alquiler el lunes")
	rzEvent(t, db, "s", 2, "2026-11-10T10:01:00Z", "assistant", "reply", "listo")
	for i, text := range []string{"closed one", "pay the rent", "t2", "t3", "t4", "t5"} {
		tkTask(t, db, text, "", "2026-11-10T10:0"+string(rune('0'+i))+":00Z", "s", 1)
	}
	tkTask(t, db, "with a time", "2026-11-16T15:00:00Z", "2026-11-10T10:09:00Z", "s", 0)
	rzExec(t, db, "update task set closed_at = '2026-11-10T10:30:00Z', closed_how = 'done' where text = 'closed one'")
	b := rzBlock(t, db, cwd)
	_, sec, ok := strings.Cut(b, "### Open tasks")
	if !ok {
		t.Fatalf("no open tasks section:\n%s", b)
	}
	var lines []string
	for _, l := range strings.Split(sec, "\n")[1:] {
		if strings.HasPrefix(l, "- ") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "- task 2, ") || !strings.HasPrefix(lines[4], "- task 6, ") {
		t.Fatalf("want tasks 2 to 6, oldest first:\n%s", sec)
	}
	for _, want := range []string{
		`pay the rent (asked: "recordame pagar el alquiler el lunes")`,
		"and 1 more (" + cmdLine(shellPath(mustExe(t)), "", "task list", "") + ")",
		"When Ana asks to be reminded of something or to note it for later, run " + cmdLine(shellPath(mustExe(t)), "", "task add", ` -text "<what>" [-at "YYYY-MM-DD HH:MM"]`) +
			". Add -self to a task you add on your own, that Ana did not ask for.",
		"Close a task only after asking Ana: " + cmdLine(shellPath(mustExe(t)), "", "task done", " <id>") + " or " + cmdLine(shellPath(mustExe(t)), "", "task drop", " <id>") + ".",
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("the section lacks %q:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "closed one") {
		t.Errorf("a closed task is listed:\n%s", sec)
	}
	rzExec(t, db, "delete from task")
	if b := rzBlock(t, db, cwd); strings.Contains(b, "### Open tasks") || !strings.Contains(b, "\nWhen Ana asks to be reminded") {
		t.Errorf("with no open task the block has an empty section or lacks how to add one:\n%s", b)
	}
	tkTask(t, db, "only one", "", "2026-11-11T10:00:00Z", "s", 1)
	if b := rzBlock(t, db, cwd); !strings.Contains(b, "### Open tasks (most important first, as a tree)\n- task 1, ") || strings.Contains(b, "more (") {
		t.Errorf("one open task is not listed alone:\n%s", b)
	}
}

func mustExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// SPEC T4: on the first prompt after a task's time the prompt hook adds one line for it, in the same single output
// as the rule card; the next prompt does not repeat it, and the task stays open. Tasks without a time, closed ones
// and ones not yet due are never announced.
func TestDueTaskAnnouncedOnceAtNextPrompt(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, &reinjectCfg{}
	oldClock := clockNow
	t.Cleanup(func() { clockNow = oldClock })
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "m.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rzExec(t, db, `insert into session(id, host, role) values ('s', 'claude-code', 'r')`)
	rzEvent(t, db, "s", 1, "2026-11-11T10:00:00Z", "user", "prompt", "avisame a las 10 que llame al banco")
	tkTask(t, db, "call the bank", "2026-11-12T10:00:00Z", "2026-11-11T10:00:00Z", "s", 1)
	tkTask(t, db, "no time", "", "2026-11-11T10:00:00Z", "s", 1)
	tkTask(t, db, "closed", "2026-11-12T09:00:00Z", "2026-11-11T10:00:00Z", "s", 1)
	tkTask(t, db, "later", "2026-11-13T10:00:00Z", "2026-11-11T10:00:00Z", "s", 1)
	rzExec(t, db, "update task set closed_at = '2026-11-11T11:00:00Z', closed_how = 'done' where text = 'closed'")
	rec := riClaudeRecord(t, "instructions", [2]string{filepath.Join(dir, "gone.md"), "SM-RULE keep it"})
	tr := riWrite(t, dir, "s.jsonl", rec, smUsage(t, 1))
	in := hookInput{Event: "UserPromptSubmit", SessionID: "s1", Transcript: tr, Cwd: dir}
	clockNow = func() time.Time { return time.Date(2026, 11, 12, 9, 59, 0, 0, time.UTC) }
	if got := smHook(t, in, dbPath, false); got != "" {
		t.Fatalf("output before the task's time: %q", got)
	}
	clockNow = func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC) }
	riWrite(t, dir, "s.jsonl", rec, smUsage(t, 1), smUsage(t, 1000000))
	got := smHook(t, in, dbPath, false) // smHook fails on more than one JSON object
	dueAt := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC).Local()
	_, off := dueAt.Zone()
	due := dueAt.Format("Monday 2006-01-02 15:04") + " (" + offsetName(off) + ")" // a task with no stored offset: the computer's
	want := `Reminder for Ana: task 1, due ` + due + `: call the bank (asked: "avisame a las 10 que llame al banco")`
	if !strings.HasPrefix(got, want+"\n\n") || !strings.Contains(got, "SM-RULE keep it") {
		t.Fatalf("want the reminder line first, then the card:\n%s", got)
	}
	for _, never := range []string{"no time", "closed", "later"} {
		if strings.Contains(got, never) {
			t.Errorf("task %q announced:\n%s", never, got)
		}
	}
	if got := smHook(t, in, dbPath, false); got != "" {
		t.Errorf("the task was announced twice: %q", got)
	}
	if open := mutStr(t, db, "select count(*) from task where closed_at is null"); open != "3" {
		t.Errorf("%s open tasks after the announcement, want 3", open)
	}
	var w bytes.Buffer
	cfg.Reinject = nil
	clockNow = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	if err := ruleHook(hookInput{Event: "PostToolUse", SessionID: "s1", Cwd: dir}, "r", "claude-code", dbPath, false, &w); err != nil || w.Len() != 0 {
		t.Errorf("a tool call announced a task: %q %v", w.String(), err)
	}
	if err := ruleHook(in, "r", "claude-code", dbPath, false, &w); err != nil || !json.Valid(w.Bytes()) || !strings.Contains(w.String(), "task 4, due") {
		t.Errorf("the next due task without the card: %q %v", w.String(), err)
	}
}
