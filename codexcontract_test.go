package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// codexOutput mirrors every field Codex 0.169 accepts in a hook's JSON output on SessionStart, SubagentStart,
// UserPromptSubmit and PostToolUse (schema.rs: the universal fields plus hookSpecificOutput with hookEventName and
// additionalContext). Decoding with DisallowUnknownFields fails on anything else, which Codex would reject.
type codexOutput struct {
	Continue           *bool   `json:"continue"`
	StopReason         *string `json:"stopReason"`
	SuppressOutput     *bool   `json:"suppressOutput"`
	SystemMessage      *string `json:"systemMessage"`
	HookSpecificOutput *struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func decodeCodex(t *testing.T, event string, out []byte) string {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(out))
	d.DisallowUnknownFields()
	var o codexOutput
	if err := d.Decode(&o); err != nil {
		t.Fatalf("%s output is not what Codex accepts: %v\n%s", event, err, out)
	}
	if o.HookSpecificOutput == nil || o.HookSpecificOutput.HookEventName != event || o.Continue != nil {
		t.Fatalf("%s output: %s", event, out)
	}
	return o.HookSpecificOutput.AdditionalContext
}

func rolloutLine(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// A cwd in the Windows long-path form (\\?\D:\x, how Codex records some) is the same folder: same scope, same role.
func TestLongPathPrefix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path form")
	}
	dir := t.TempDir()
	old := cfg
	defer func() { cfg = old }()
	cfg.Scope, cfg.Roles = scopeList(filepath.Base(dir)), nil
	long := `\\?\` + dir
	if ok, _ := admitted(hookInput{Cwd: long}, "codex", ""); !ok {
		t.Error("a \\\\?\\ cwd in scope was refused")
	}
	if a, b := roleFor(long, projectRole(long)), roleFor(dir, projectRole(dir)); a != b {
		t.Errorf("roles differ by the prefix: %s and %s", a, b)
	}
}

// Each Codex event: what reaches stdout, and whether it imports. Stop, Interrupt, SessionEnd, SessionStart, the
// subagent and UserPromptSubmit payloads are shaped like ones captured from Codex 0.169.0 (a disposable
// profile, a localhost model; field names and nesting as captured, paths replaced). The PostToolUse one is not
// captured: the capture profile refused the shell call, so its Bash name and tool_input follow a fixture in Codex's
// post_tool_use.rs. Stop, Interrupt, SessionEnd and PreCompact print nothing (Codex fails a Stop that prints text);
// the others print one JSON object Codex accepts.
func TestCodexHookContract(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "shop")
	os.MkdirAll(proj, 0o755)
	rollout := filepath.Join(dir, "rollout-2026-11-14T21-33-14-01a1dc9f.jsonl")
	instructions := "# AGENTS.md instructions for " + proj + "\n\n<INSTRUCTIONS>\nNEVER_PUSH_RULE: never push without asking.\n</INSTRUCTIONS>"
	write := func(total int) {
		var b strings.Builder
		b.WriteString(rolloutLine(t, map[string]any{"type": "session_meta", "payload": map[string]any{"id": "01a1dc9f", "cwd": proj}}))
		b.WriteString(rolloutLine(t, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": instructions}, map[string]any{"type": "input_text", "text": "<environment_context/>"}}}}))
		b.WriteString(rolloutLine(t, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": nil, "rate_limits": map[string]any{}}}))
		if total > 0 {
			b.WriteString(rolloutLine(t, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{
				"total_token_usage": map[string]any{"total_tokens": 900000}, "last_token_usage": map[string]any{"total_tokens": total}, "model_context_window": 258400}}}))
		}
		if err := os.WriteFile(rollout, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldCfg, oldSpawn := cfg, spawnImport
	defer func() { cfg, spawnImport = oldCfg, oldSpawn }()
	cfg.Scope, cfg.Roles, cfg.Reinject = "shop", nil, &reinjectCfg{}
	var spawned []string
	spawnImport = func(_ []string, trigger string) error { spawned = append(spawned, trigger); return nil }
	dbPath := filepath.Join(dir, "m.db")
	run := func(e map[string]any) []byte {
		t.Helper()
		e["session_id"], e["cwd"], e["model"], e["permission_mode"] = "01a1dc9f", proj, "gpt-5.4", "bypassPermissions"
		if _, ok := e["transcript_path"]; !ok {
			e["transcript_path"] = rollout
		}
		b, _ := json.Marshal(e)
		var out bytes.Buffer
		if err := hook(readHookInput(bytes.NewReader(b)), nil, dbPath, "codex", "", func(*sql.DB, string) error { return nil }, &out); err != nil {
			t.Fatalf("%v: %v", e["hook_event_name"], err)
		}
		return out.Bytes()
	}

	write(0)
	for _, e := range []map[string]any{
		{"hook_event_name": "Stop", "turn_id": "t1", "stop_hook_active": false, "last_assistant_message": "done"},
		{"hook_event_name": "Interrupt", "turn_id": "t2"},
		{"hook_event_name": "SessionEnd", "reason": "other"},
		{"hook_event_name": "PreCompact", "turn_id": "t3", "trigger": "manual"},
	} {
		n := len(spawned)
		if out := run(e); len(out) != 0 {
			t.Errorf("%v printed %q; Codex must get nothing", e["hook_event_name"], out)
		}
		if len(spawned) != n+1 {
			t.Errorf("%v did not import", e["hook_event_name"])
		}
	}

	// Startup, resume and clear: the resume block (empty store: nothing to show, so nothing printed).
	for _, src := range []string{"startup", "resume", "clear"} {
		if out := run(map[string]any{"hook_event_name": "SessionStart", "source": src}); len(out) != 0 {
			decodeCodex(t, "SessionStart", out)
		}
	}

	// After a compaction: the rule card, with the AGENTS.md text Codex loaded, read from the rollout (here through
	// the \\?\ form Codex gives transcript_path after a resume).
	tp := rollout
	if runtime.GOOS == "windows" {
		tp = `\\?\` + rollout
	}
	got := decodeCodex(t, "SessionStart", run(map[string]any{"hook_event_name": "SessionStart", "source": "compact", "transcript_path": tp}))
	if !strings.Contains(got, "NEVER_PUSH_RULE") || strings.Contains(got, "for this summary") {
		t.Errorf("compact card:\n%s", got)
	}

	// The card by tokens: no token count on disk yet (Codex writes the response's count after PostToolUse runs)
	// gives nothing; the first count is the baseline; a jump of factor × the card's size brings the card.
	prompt := map[string]any{"hook_event_name": "UserPromptSubmit", "turn_id": "t4", "prompt": "go on"}
	if out := run(prompt); len(out) != 0 {
		t.Errorf("no token count yet, but printed %s", out)
	}
	write(1000)
	if out := run(prompt); len(out) != 0 {
		t.Errorf("the baseline call printed %s", out)
	}
	write(1000 + 200000)
	// Inside a subagent (as captured: session_id the parent's, agent_id and transcript_path the child's,
	// tool_response an object) the card never comes, even past the threshold.
	if out := run(map[string]any{"hook_event_name": "PostToolUse", "turn_id": "t5c", "agent_id": "01a1dca0-child", "agent_type": "default",
		"tool_name": "get_goal", "tool_input": map[string]any{}, "tool_response": map[string]any{"goal": nil}, "tool_use_id": "c0"}); len(out) != 0 {
		t.Errorf("a subagent's PostToolUse got %s", out)
	}
	if got := decodeCodex(t, "PostToolUse", run(map[string]any{"hook_event_name": "PostToolUse", "turn_id": "t5", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "ls"}, "tool_response": "ok", "tool_use_id": "c1"})); !strings.Contains(got, "NEVER_PUSH_RULE") {
		t.Errorf("PostToolUse card:\n%s", got)
	}

	// SubagentStart: session_id is the parent's, agent_id and transcript_path the child's.
	if got := decodeCodex(t, "SubagentStart", run(map[string]any{"hook_event_name": "SubagentStart", "turn_id": "t6",
		"agent_id": "01a1dca0-child", "agent_type": "default"})); !strings.Contains(got, "helper agent") {
		t.Errorf("SubagentStart: %s", got)
	}

	// Outside the memory's scope nothing happens at all.
	n := len(spawned)
	e := map[string]any{"hook_event_name": "Stop", "turn_id": "t7"}
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "x", "cwd": filepath.Join(dir, "other"), "transcript_path": rollout})
	var out bytes.Buffer
	hook(readHookInput(bytes.NewReader(b)), nil, dbPath, "codex", "", nil, &out)
	if out.Len() != 0 || len(spawned) != n {
		t.Errorf("out of scope %v: printed %q, imports %d", e, out.String(), len(spawned)-n)
	}
}

// The instructions are the last ones Codex recorded anywhere in the rollout (found in review: OLD at the start, more
// than 8 MB of events, CURRENT at the end gave OLD). Later calls read only what was appended; a replaced, shorter
// rollout is read again from its start.
func TestCodexSourcesLatest(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	msg := func(rule string) string {
		return rolloutLine(t, map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "# AGENTS.md instructions for D:/p\n\n<INSTRUCTIONS>\n" + rule + "\n</INSTRUCTIONS>"}}}})
	}
	rollout := filepath.Join(dir, "rollout.jsonl")
	var b strings.Builder
	b.WriteString(msg("OLD_RULE"))
	filler := rolloutLine(t, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": strings.Repeat("x", 4000)}})
	for b.Len() < 9<<20 {
		b.WriteString(filler)
	}
	b.WriteString(msg("CURRENT_RULE"))
	os.WriteFile(rollout, []byte(b.String()), 0o644)
	text := func(db *sql.DB) string {
		var parts []string
		for _, s := range codexSources(db, rollout) {
			parts = append(parts, s.text)
		}
		return strings.Join(parts, "|")
	}
	if got := text(nil); got != "CURRENT_RULE" {
		t.Errorf("without a store: %q", got)
	}
	if got := text(db); got != "CURRENT_RULE" {
		t.Errorf("first read: %q", got)
	}
	var off int64
	db.QueryRow("select offset from codex_instructions where rollout = ?", rollout).Scan(&off)
	if off != int64(b.Len()) {
		t.Errorf("offset %d, file %d", off, b.Len())
	}
	f, _ := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(filler + msg("NEWEST_RULE") + `{"type":"event_msg","payload":{"ty`) // the last line is still being written
	f.Close()
	if got := text(db); got != "NEWEST_RULE" {
		t.Errorf("after an append: %q", got)
	}
	os.WriteFile(rollout, []byte(msg("REPLACED_RULE")), 0o644)
	if got := text(db); got != "REPLACED_RULE" {
		t.Errorf("after the rollout was replaced: %q", got)
	}
}
