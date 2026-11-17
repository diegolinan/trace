package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aiMarker is a marker as the alarm makes them; the nonce is hex, long enough that a redaction rule could take it.
const aiMarker = "trace-alarm:7:9f86d081884c7d65"

// aiRollout writes one Codex rollout of a main thread in the folder tree, with these records after its session_meta.
func aiRollout(t *testing.T, home, id string, records ...map[string]any) {
	t.Helper()
	day := filepath.Join(home, "sessions", "2026", "11", "12")
	os.MkdirAll(day, 0o755)
	var b strings.Builder
	b.WriteString(rolloutLine(t, map[string]any{"timestamp": "2026-11-12T09:00:00Z", "ordinal": 0, "type": "session_meta",
		"payload": map[string]any{"id": id, "timestamp": "2026-11-12T09:00:00Z", "cwd": `D:\Work\acme`, "source": "vscode"}}))
	for i, r := range records {
		r["timestamp"], r["ordinal"] = "2026-11-12T09:00:01Z", i+1
		b.WriteString(rolloutLine(t, r))
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-11-12-"+id+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// aiCodexUser is a Codex user message in both shapes a rollout records it in.
func aiCodexUser(text string) []map[string]any {
	return []map[string]any{
		{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "UserMessage", "content": []any{map[string]any{"type": "text", "text": text}}}}},
	}
}

// aiImport imports the Claude Code transcripts and the Codex rollouts of a fixture.
func aiImport(t *testing.T, f *mutFix, codexHome string) {
	t.Helper()
	if err := runImport(f.db, filepath.Join(f.dir, "projects"), f.history(), codexHome, filepath.Join(f.dir, "raw"), "acme", "test"); err != nil {
		t.Fatal(err)
	}
}

// aiStored is every stored event of a session that has words, as speaker/kind, one per line.
func aiStored(t *testing.T, f *mutFix, sid string) string {
	t.Helper()
	return mutStr(t, f.db, "select coalesce(group_concat(speaker || '/' || kind, ' '), '') from (select speaker, kind from event where session_id = ? order by seq)", sid)
}

// SPEC A9: a row carrying an alarm marker is the alarm's, never the user's, in every shape a program stores it: a
// Claude Code user message (a string, text blocks, a meta row, the cross-session envelope as Claude Code shows it
// idle), a queued message (a peer's, as it shows it mid-turn, or one it files as typed), and a Codex user message in
// both of its records. The marker is the match, and it survives redaction. What the agent says, quoting a marker, stays
// the agent's; a prompt without one stays the user's.
func TestAlarmMessageStoredAsProgramInEveryShape(t *testing.T) {
	f := newMutFix(t)
	envelope := "<cross-session-message from-name=\"TRACE alarm\" from-mode=\"bypass\">\nTRACE alarm: task 7.\n" + aiMarker + "\n</cross-session-message>"
	queued := func(uuid string, min int, kind string) mutObj {
		return mutObj{"type": "attachment", "uuid": uuid, "timestamp": mutAt(min), "attachment": mutObj{"type": "queued_command", "prompt": envelope, "origin": mutObj{"kind": kind}}}
	}
	f.write(t, "c1.jsonl", mutLines(
		mutUser("u1", 0, "TRACE alarm: task 7.\n"+aiMarker),
		mutUser("u2", 1, envelope, "isMeta", true),
		mutUser("u3", 2, envelope),
		queued("q1", 3, "peer"),
		queued("q2", 4, "human"),
		queued("q3", 5, "task-notification"),
		mutUser("u4", 6, "", "message", mutObj{"role": "user", "content": []mutObj{{"type": "text", "text": envelope}}}),
		mutUser("u5", 7, "Stop hook feedback:\n"+aiMarker, "isMeta", true),
		mutObj{"type": "assistant", "uuid": "a1", "timestamp": mutAt(8), "message": mutObj{"content": []mutObj{{"type": "text", "text": "Ana, task 7 is due (" + aiMarker + ")"}}}},
		mutUser("u6", 9, "ok, thanks"),
	))
	codex := t.TempDir()
	aiRollout(t, codex, "t1", append(aiCodexUser("TRACE alarm: task 7.\n"+aiMarker),
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "content": []any{map[string]any{"type": "text", "text": "done, " + aiMarker}}}}})...)
	aiImport(t, f, codex)
	want := strings.Repeat("program/alarm ", 8) + "assistant/reply user/prompt"
	if got := aiStored(t, f, "c1"); got != want {
		t.Errorf("Claude Code rows stored as\n%s\nwant\n%s", got, want)
	}
	if got := aiStored(t, f, "t1"); got != "program/alarm program/alarm assistant/reply" {
		t.Errorf("Codex rows stored as %s", got)
	}
	if n := mutStr(t, f.db, "select count(*) from event where kind = 'alarm' and text like ?", "%"+aiMarker+"%"); n != "10" {
		t.Errorf("%s alarm rows keep their marker, want 10", n)
	}
}

// SPEC A9: the stop hook's reason carries the task's marker, so however a program stores it, it is the alarm's and
// never the user's words: in particular it is never the message a later task add takes as the one that asked.
func TestStopHookReasonIsNotTheUsers(t *testing.T) {
	_, dbPath := shStore(t)
	reason := stopReason(dbPath, "s1")
	if !strings.Contains(reason, "trace-alarm:1:00112233aabbccdd") {
		t.Fatalf("the reason carries no marker: %q", reason)
	}
	f := newMutFix(t)
	f.write(t, "c2.jsonl", mutLines(
		mutUser("u1", 0, "remind me to call Bob at 9"),
		mutUser("u2", 1, "Stop hook feedback:\n"+reason),
		mutUser("u3", 2, reason, "isMeta", true),
	))
	codex := t.TempDir()
	aiRollout(t, codex, "t2", aiCodexUser(reason)...)
	aiImport(t, f, codex)
	if got := aiStored(t, f, "c2") + " | " + aiStored(t, f, "t2"); got != "user/prompt program/alarm program/alarm | program/alarm program/alarm" {
		t.Errorf("stored as %s", got)
	}
	if got := mutStr(t, f.db, "select text from event where id = "+newestTyped); got != "remind me to call Bob at 9" {
		t.Errorf("the newest typed message is %q, not the user's", got)
	}
}
