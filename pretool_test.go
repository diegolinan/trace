package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// preOut is what PreToolUse wrote: the decision, its reason, and the context for the agent.
type preOut struct{ decision, reason, context string }

func runPre(t *testing.T, tool, input, dbPath string) preOut {
	t.Helper()
	var w bytes.Buffer
	in := hookInput{Event: "PreToolUse", SessionID: "s1", ToolName: tool, ToolInput: json.RawMessage(input)} // no folder: every record is in scope
	if err := preToolHook(in, "r", dbPath, true, &w); err != nil {
		t.Fatal(err)
	}
	if w.Len() == 0 {
		return preOut{}
	}
	var out struct {
		Hook struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
			Context  string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(w.Bytes(), &out); err != nil || out.Hook.Event != "PreToolUse" {
		t.Fatalf("PreToolUse output %q: %v", w.String(), err)
	}
	return preOut{out.Hook.Decision, out.Hook.Reason, out.Hook.Context}
}

// SPEC C3, C4: a check before the call stops it when it fails, with its name and output as the reason, sees the tool
// and its target in its environment, never runs after the call, and a broken one lets the call through.
func TestCheckBeforeStopsTheCall(t *testing.T) {
	smKeepCfg(t)
	echoTarget := `echo "target=$TRACE_TARGET tool=$TRACE_TOOL"; exit 1`
	if runtime.GOOS == "windows" {
		echoTarget = "echo target=%TRACE_TARGET% tool=%TRACE_TOOL% & exit /b 1"
	}
	cfg.Reinject = nil
	cfg.Checks = []check{
		{Name: "changelog", On: "Bash", Match: `publish`, Run: echoTarget, Before: true},
		{Name: "after", On: "Bash", Run: smFailing("AFTER_RAN")},
	}
	dbPath := filepath.Join(t.TempDir(), "p.db")
	got := runPre(t, "Bash", `{"command":"sh publish.sh v1"}`, dbPath)
	if got.decision != "deny" || !strings.Contains(got.reason, `"changelog"`) || !strings.Contains(got.reason, "target=sh publish.sh v1 tool=Bash") ||
		strings.Contains(got.reason, "AFTER_RAN") {
		t.Fatalf("before check: %+v", got)
	}
	if got := runPre(t, "Bash", `{"command":"ls"}`, dbPath); got != (preOut{}) {
		t.Fatalf("no match: %+v", got)
	}
	post := smHook(t, hookInput{Event: "PostToolUse", SessionID: "s1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"sh publish.sh"}`), Cwd: t.TempDir()}, dbPath, true)
	if !strings.Contains(post, "AFTER_RAN") || strings.Contains(post, "target=") {
		t.Fatalf("after the call: %q", post)
	}
	db, _ := openDB(dbPath)
	defer db.Close()
	if n := count(t, db, "select count(*) from check_run where name = 'changelog' and ok = 0"); n != 1 {
		t.Fatalf("before runs logged: %d", n)
	}
	cfg.Checks = []check{{Name: "broken", On: "(", Run: "x", Before: true}}
	if got := runPre(t, "Bash", `{"command":"sh publish.sh"}`, dbPath); got.decision == "deny" || !strings.Contains(got.context, `"broken" is misconfigured`) {
		t.Fatalf("broken check: %+v", got)
	}
}

// SPEC D11: a record with -about comes back before a tool call it is about, verbatim; a replacement keeps it, a
// dropped record and an unrelated call get nothing, and an -about that does not compile is refused.
func TestRuleAboutAtTheAction(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject, cfg.Roles = nil, nil, nil // no roles: a record of any session is in scope
	root := ruleRoot(t)
	dbPath := filepath.Join(root, "trace", "trace.db")
	if out := tkTrace(t, root, "rule", "add", "-quote", "trabajamos TDD y SDD", "-about", `publish\.sh|git push`); !strings.Contains(out, "rule 1 recorded") {
		t.Fatalf("add: %q", out)
	}
	got := runPre(t, "Bash", `{"command":"sh system/tools/trace-publish/PUBLISH.sh v2"}`, dbPath)
	if got.decision != "" || !strings.Contains(got.context, "about what you are about to do") || !strings.Contains(got.context, `#1 rule: "trabajamos TDD y SDD,"`) {
		t.Fatalf("at the action: %+v", got)
	}
	if got := runPre(t, "Write", `{"file_path":"notes.md"}`, dbPath); got != (preOut{}) {
		t.Fatalf("unrelated call: %+v", got)
	}
	tkTrace(t, root, "rule", "add", "-quote", "de acá en más", "-replaces", "1")
	got = runPre(t, "Bash", `{"command":"git push mac"}`, dbPath)
	if !strings.Contains(got.context, `#2 rule: "de acá en más"`) || strings.Contains(got.context, "#1 ") {
		t.Fatalf("replacement keeps -about: %+v", got)
	}
	if list := tkTrace(t, root, "rule", "list"); !strings.Contains(list, `about: publish\.sh|git push`) {
		t.Fatalf("list: %q", list)
	}
	tkTrace(t, root, "rule", "drop", "2")
	if got := runPre(t, "Bash", `{"command":"git push"}`, dbPath); got != (preOut{}) {
		t.Fatalf("dropped: %+v", got)
	}
	if _, errOut, code := rdRun(t, root, "rule", "add", "-quote", "no depende de git", "-about", "("); code == 0 || !strings.Contains(errOut, "-about") {
		t.Fatalf("bad -about: exit %d %q", code, errOut)
	}
}

// SPEC C5: PreToolUse is installed on the tools that change something and the tools the checks before name.
func TestInstallPreToolUse(t *testing.T) {
	if m := optionalHooks(config{})["PreToolUse"]; m != "Bash|Edit|Write|MultiEdit|NotebookEdit" {
		t.Fatalf("default matcher %q", m)
	}
	if m := optionalHooks(config{Checks: []check{{On: "Read", Before: true}, {On: "Grep"}}})["PreToolUse"]; m != "Bash|Edit|Write|MultiEdit|NotebookEdit|Read" {
		t.Fatalf("with a check before %q", m)
	}
	if m := optionalHooks(config{Checks: []check{{Before: true}}})["PreToolUse"]; m != "*" {
		t.Fatalf("a check before on every tool %q", m)
	}
	if m := optionalHooks(config{Checks: []check{{On: "Read", Before: true}}})["PostToolUse"]; m != "" {
		t.Fatalf("a check before needs no PostToolUse: %q", m)
	}
}

// SPEC C3: a check before the call runs only in a folder in scope, and one the limit cuts lets the call through.
func TestCheckBeforeScopeAndLimit(t *testing.T) {
	smKeepCfg(t)
	cfg.Reinject = nil
	cfg.Checks = []check{{Name: "deny", On: "Bash", Run: smFailing("NO"), Before: true}}
	dbPath := filepath.Join(t.TempDir(), "p.db")
	var w bytes.Buffer
	in := hookInput{Event: "PreToolUse", SessionID: "s1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)}
	if err := preToolHook(in, "r", dbPath, false, &w); err != nil || w.Len() != 0 {
		t.Fatalf("outside scope: %v %q", err, w.String())
	}
	old := checkLimit
	t.Cleanup(func() { checkLimit = old })
	checkLimit = 300 * time.Millisecond
	slow := "sleep 5; exit 1"
	if runtime.GOOS == "windows" {
		slow = "ping -n 6 127.0.0.1 >nul & exit /b 1"
	}
	cfg.Checks = []check{{Name: "slow", On: "Bash", Run: slow, Before: true}}
	if got := runPre(t, "Bash", `{"command":"ls"}`, dbPath); got.decision == "deny" || !strings.Contains(got.context, `"slow" was not run`) {
		t.Fatalf("cut by the limit: %+v", got)
	}
}
