package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// parseRowCases is one transcript row per branch of parseRow; sub marks a subagent's own transcript.
var parseRowCases = []struct {
	name string
	sub  bool
	line string
}{
	{"ai-title", false, `{"type":"ai-title","aiTitle":"Fix login"}`},
	{"custom-title", false, `{"type":"custom-title","customTitle":"launch day"}`},
	{"agent-name", false, `{"type":"agent-name","agentName":"first try"}`},
	{"compact with metadata", false, `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":1234}}`},
	{"compact bare", false, `{"type":"system","subtype":"compact_boundary"}`},
	{"away summary", false, `{"type":"system","subtype":"away_summary","content":"while you were away"}`},
	{"other system", false, `{"type":"system","subtype":"informational","content":"x"}`},
	{"user no message", false, `{"type":"user"}`},
	{"user compact summary", false, `{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"the summary"}}`},
	{"meta handback", false, `{"type":"user","isMeta":true,"message":{"role":"user","content":"<agent-message from=\"x\">done</agent-message>"}}`},
	{"meta relay", false, `{"type":"user","isMeta":true,"message":{"role":"user","content":"<cross-session-message>hi</cross-session-message>"}}`},
	{"meta other", false, `{"type":"user","isMeta":true,"message":{"role":"user","content":"caveat"}}`},
	{"task notification", false, `{"type":"user","message":{"role":"user","content":"<task-notification>done</task-notification>"}}`},
	{"local command", false, `{"type":"user","message":{"role":"user","content":"<local-command-stdout>x</local-command-stdout>"}}`},
	{"bash stdout", false, `{"type":"user","message":{"role":"user","content":"<bash-stdout>ls</bash-stdout>"}}`},
	{"string handback", false, `{"type":"user","message":{"role":"user","content":"<agent-message>back</agent-message>"}}`},
	{"typed", false, `{"type":"user","message":{"role":"user","content":"arreglá el login"}}`},
	{"typed whitespace", false, `{"type":"user","message":{"role":"user","content":"   "}}`},
	{"typed by parent", true, `{"type":"user","message":{"role":"user","content":"do the task"}}`},
	{"user blocks", false, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"and this"},{"type":"tool_result","tool_use_id":"t1","content":"Exit code 2\nboom","is_error":true},{"type":"image"}]}}`},
	{"assistant no message", false, `{"type":"assistant"}`},
	{"assistant blocks", false, `{"type":"assistant","message":{"model":"m","content":[{"type":"text","text":"done"},{"type":"thinking","thinking":"hmm"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}},{"type":"server_tool_use","id":"t2","name":"web_search","input":{"q":"x"}},{"type":"advisor_tool_result","content":[{"type":"text","text":"advice"}]},{"type":"advisor_tool_result","content":{"raw":1}}]}}`},
	{"subagent reply", true, `{"type":"assistant","message":{"content":[{"type":"text","text":"sub says"}]}}`},
	{"attachment bad", false, `{"type":"attachment","attachment":"nope"}`},
	{"queued human", false, `{"type":"attachment","attachment":{"type":"queued_command","prompt":"no, eso no","origin":{"kind":"human"}}}`},
	{"queued peer", false, `{"type":"attachment","attachment":{"type":"queued_command","prompt":"report","origin":{"kind":"peer"}}}`},
	{"queued other", false, `{"type":"attachment","attachment":{"type":"queued_command","prompt":"x"}}`},
	{"hook context", false, `{"type":"attachment","attachment":{"type":"hook_additional_context","content":["rules"]}}`},
	{"hook success start", false, `{"type":"attachment","attachment":{"type":"hook_success","hookEvent":"SessionStart","content":"block"}}`},
	{"hook success other", false, `{"type":"attachment","attachment":{"type":"hook_success","hookEvent":"Stop","content":"x"}}`},
	{"instructions none", false, `{"type":"attachment","attachment":{"type":"instructions","files":[]}}`},
	{"instructions", false, `{"type":"attachment","attachment":{"type":"instructions","files":[{"path":"C:/a/CLAUDE.md","type":"Project","content":"rule"},{"path":"C:/m/MEMORY.md","type":"AutoMem","content":"mem"}]}}`},
}

// describe is a parsed row as one comparable line.
func describe(p parsed) string {
	var b strings.Builder
	fmt.Fprintf(&b, "title=%q custom=%v compacted=%v", p.title, p.custom, p.compacted)
	var shas []string
	for k := range p.loaded {
		shas = append(shas, k[:8]+":"+fmt.Sprint(len(p.loaded[k])))
	}
	sort.Strings(shas)
	fmt.Fprintf(&b, " loaded=%v", shas)
	for _, e := range p.events {
		ec := ""
		if e.exitCode != nil {
			ec = fmt.Sprint(*e.exitCode)
		}
		ie := ""
		if e.isError != nil {
			ie = fmt.Sprint(*e.isError)
		}
		fmt.Fprintf(&b, " | %s/%s %q tool=%q in=%q use=%q err=%s exit=%s sha=%v", e.speaker, e.kind, e.text, e.toolName, e.toolInput, e.toolUse, ie, ec, e.loadedSha != "")
	}
	return b.String()
}

// Pins parseRow: every branch's events, as the code made them before it was split by record type.
func TestParseRowPinned(t *testing.T) {
	var golden map[string]string
	b, err := os.ReadFile("testdata/parserow.golden.json")
	if os.Getenv("TRACE_WRITE_GOLDEN") == "" {
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(b, &golden)
	}
	got := map[string]string{}
	for _, c := range parseRowCases {
		var r row
		if err := json.Unmarshal([]byte(c.line), &r); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got[c.name] = describe(parseRow(&r, c.sub))
	}
	if os.Getenv("TRACE_WRITE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		out, _ := json.MarshalIndent(got, "", "  ")
		os.WriteFile("testdata/parserow.golden.json", append(out, '\n'), 0o644)
		return
	}
	for _, c := range parseRowCases {
		if got[c.name] != golden[c.name] {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got[c.name], golden[c.name])
		}
	}
}
