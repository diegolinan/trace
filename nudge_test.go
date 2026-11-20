package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC T23: from the second commit with no task in progress the agent is told; a task in progress, a subagent's
// call or a command that is not a commit says nothing.
func TestCommitNudge(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	call := func(session, agent, cmd string) string {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		return commitNudge(db, hookInput{Event: "PostToolUse", SessionID: session, AgentID: agent, ToolName: "Bash", ToolInput: in})
	}
	if got := call("s1", "", `git add . && git commit -m "first"`); got != "" {
		t.Errorf("first commit: %q", got)
	}
	if got := call("s1", "", "git status"); got != "" {
		t.Errorf("not a commit: %q", got)
	}
	if got := call("s1", "", `git -C "D:/repo" commit -q -m second`); !strings.Contains(got, "2 commits") || !strings.Contains(got, "task start") {
		t.Errorf("second commit: %q", got)
	}
	if got := call("s2", "", "git commit -m other"); got != "" {
		t.Errorf("another session's first commit: %q", got)
	}
	if got := call("s1", "agent-1", "git commit -m sub"); got != "" {
		t.Errorf("a subagent's commit: %q", got)
	}
	in, _ := json.Marshal(map[string]string{"file_path": "git commit"})
	if got := commitNudge(db, hookInput{Event: "PostToolUse", SessionID: "s1", ToolName: "Write", ToolInput: in}); got != "" {
		t.Errorf("another tool: %q", got)
	}
	db.Exec(`insert into task(text, created_at, started_at, level) values ('the work', '2026-11-20T10:00:00Z', '2026-11-20T10:00:00Z', 'story')`)
	if got := call("s1", "", "git commit -m third"); got != "" {
		t.Errorf("with a task in progress: %q", got)
	}
}

// SPEC T23: a Claude Code install always watches Bash, alongside what checks or the rule card need.
func TestClaudeHooksAlwaysWatchBash(t *testing.T) {
	for _, c := range []struct {
		cfg  config
		want string
	}{
		{config{}, "Bash"},
		{config{Checks: []check{{On: "Write|Edit"}}}, "Write|Edit|Bash"},
		{config{Checks: []check{{On: "Bash"}}}, "Bash"},
		{config{Reinject: &reinjectCfg{}}, "*"},
	} {
		if got := claudeHooks(c.cfg)["PostToolUse"]; got != c.want {
			t.Errorf("%+v: PostToolUse matcher %q, want %q", c.cfg, got, c.want)
		}
	}
	if _, ok := optionalHooks(config{})["PostToolUse"]; ok {
		t.Error("Codex's optional hooks changed: no PostToolUse without checks or the rule card")
	}
}

// SPEC T23: the hook itself shows the line on a Claude Code session's second commit, and not on Codex.
func TestCommitNudgeThroughTheHook(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, nil
	dbPath := filepath.Join(t.TempDir(), "m.db")
	commit := hookInput{Event: "PostToolUse", SessionID: "s", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"git commit -m x"}`), Cwd: t.TempDir()}
	if got := smHook(t, commit, dbPath, false); got != "" {
		t.Fatalf("first commit: %q", got)
	}
	if got := smHook(t, commit, dbPath, false); !strings.Contains(got, "2 commits with no task in progress") {
		t.Fatalf("second commit: %q", got)
	}
	codexDB := filepath.Join(t.TempDir(), "c.db")
	for i := 0; i < 2; i++ {
		var w strings.Builder
		if err := ruleHook(commit, "r", "codex", codexDB, false, &w); err != nil || w.Len() != 0 {
			t.Fatalf("Codex commit %d got output: %q %v", i+1, w.String(), err)
		}
	}
}
