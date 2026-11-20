package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// row is one line of a Claude Code transcript; only the fields trace reads.
type row struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	UUID             string          `json:"uuid"`
	Timestamp        string          `json:"timestamp"`
	SessionID        string          `json:"sessionId"`
	Cwd              string          `json:"cwd"`
	GitBranch        string          `json:"gitBranch"`
	Version          string          `json:"version"`
	Entrypoint       string          `json:"entrypoint"`
	PromptSource     string          `json:"promptSource"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	ToolDenialKind   string          `json:"toolDenialKind"`
	Message          *message        `json:"message"`
	ToolUseResult    json.RawMessage `json:"toolUseResult"`
	Attachment       json.RawMessage `json:"attachment"`
	Content          json.RawMessage `json:"content"`
	CompactMetadata  *struct {
		Trigger   string `json:"trigger"`
		PreTokens int    `json:"preTokens"`
	} `json:"compactMetadata"`
	AiTitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
}

type message struct {
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	ID        string          `json:"id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type attachment struct {
	Type    string          `json:"type"`
	Prompt  json.RawMessage `json:"prompt"`
	Content json.RawMessage `json:"content"`
	Origin  *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Files []struct {
		Path    string `json:"path"`
		Type    string `json:"type"`
		Content string `json:"content"`
	} `json:"files"`
	HookEvent string `json:"hookEvent"`
}

// ev is one layer-0 event before it gets a session, a seq and a row id.
type ev struct {
	speaker, kind, text          string
	toolName, toolInput, toolUse string
	isError                      *bool
	exitCode                     *int
	loadedSha                    string
}

// parsed is what one transcript row yields.
type parsed struct {
	events    []ev
	loaded    map[string]string // sha -> text of rule files the harness re-sent
	title     string
	custom    bool // title came from /rename, which beats the harness's ai-title
	compacted bool
}

// asText returns a content field's text: a plain string (true), or the text blocks of an array joined.
func asText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return blockText(raw), false
}

// blockText joins the text blocks of a content array; anything that is not one has no text.
func blockText(raw json.RawMessage) string {
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// headTail keeps tool output searchable without storing megabytes; the raw copy keeps the rest (ReMe's head/tail).
func headTail(s string) string {
	const keep = 1500
	if len(s) <= 2*keep+200 {
		return s
	}
	// A character cut at either end is left out; callers pass text decoded from JSON, which is valid UTF-8.
	h, t := strings.ToValidUTF8(s[:keep], ""), strings.ToValidUTF8(s[len(s)-keep:], "")
	return h + fmt.Sprintf("\n[… %d chars cut, full text in the raw copy …]\n", len(s)-len(h)-len(t)) + t
}

var exitRe = regexp.MustCompile(`^(?:Error: )?Exit code (\d+)`)

func boolp(b bool) *bool { return &b }

// parseRow maps one transcript row to events. sub is true for a subagent's own transcript, where the
// user-side prompt was written by the parent agent, not by the user.
func parseRow(r *row, sub bool) parsed {
	rp := sides(sub, r.Entrypoint)
	rp.suggested = r.PromptSource == "suggestion_accepted" // SPEC I12
	switch r.Type {
	case "ai-title":
		rp.p.title = r.AiTitle
	case "custom-title":
		rp.p.title, rp.p.custom = r.CustomTitle, true
	case "agent-name": // /rename writes both rows; an older name may survive only here
		rp.p.title, rp.p.custom = r.AgentName, true
	case "system":
		rp.system(r)
	case "user":
		rp.user(r)
	case "assistant":
		rp.assistant(r)
	case "attachment":
		rp.attachment(r)
	}
	return rp.p
}

// sides is who speaks on each side of a transcript: in a subagent's, the parent agent asks; in a session a program
// started, the program does (SPEC I9); else the user.
func sides(sub bool, entrypoint string) rowParser {
	switch {
	case sub:
		return rowParser{asker: "assistant", answerer: "subagent"}
	case programEntry(entrypoint):
		return rowParser{asker: "program", answerer: "assistant"}
	}
	return rowParser{asker: "user", answerer: "assistant"}
}

// programEntry says a program started the session: `claude -p` and the SDKs (sdk-cli, sdk-ts, sdk-py) or an MCP
// client. The terminal, the editors and the desktop app are a person typing.
func programEntry(entrypoint string) bool {
	return strings.HasPrefix(entrypoint, "sdk") || entrypoint == "mcp"
}

// rowParser collects one row's events; asker and answerer are who speaks on each side of this transcript.
type rowParser struct {
	p               parsed
	asker, answerer string
	suggested       bool // the user sent a suggestion the model worded: its prompt is a suggestion
}

// add keeps an event that has words.
func (rp *rowParser) add(e ev) {
	if rp.suggested && e.kind == "prompt" {
		e.kind = "suggestion"
	}
	if strings.TrimSpace(e.text) != "" {
		rp.p.events = append(rp.p.events, e)
	}
}

func (rp *rowParser) system(r *row) {
	switch r.Subtype {
	case "compact_boundary":
		rp.p.compacted = true
		t := "conversation compacted"
		if m := r.CompactMetadata; m != nil {
			t = fmt.Sprintf("conversation compacted (%s, %d tokens before)", m.Trigger, m.PreTokens)
		}
		rp.add(ev{speaker: "harness", kind: "boundary", text: t})
	case "away_summary":
		s, _ := asText(r.Content)
		rp.add(ev{speaker: "model-summary", kind: "summary", text: s})
	}
}

func (rp *rowParser) user(r *row) {
	if r.Message == nil {
		return
	}
	s, isStr := asText(r.Message.Content)
	switch {
	case r.IsCompactSummary:
		rp.add(ev{speaker: "model-summary", kind: "summary", text: s})
	case r.IsMeta:
		rp.meta(s)
	case isStr:
		rp.userText(s)
	default:
		var bs []block
		json.Unmarshal(r.Message.Content, &bs)
		for _, b := range bs {
			switch b.Type {
			case "text":
				rp.add(typed(rp.asker, b.Text))
			case "tool_result":
				rp.p.events = append(rp.p.events, toolResult(r, b, rp.asker)...)
			}
		}
	}
}

// meta is a harness-written user row: only a subagent's hand-back and a relayed message are words.
func (rp *rowParser) meta(s string) {
	switch {
	case strings.Contains(s, "<agent-message"):
		rp.add(ev{speaker: "subagent", kind: "handback", text: s})
	case strings.Contains(s, "<cross-session-message"), alarmMarker.MatchString(s): // the alarm's: insertEvent files it (SPEC A9)
		rp.add(ev{speaker: "relay", kind: "relay", text: s})
	}
}

// userText is a user row whose content is one string: a harness notice, a command's output, a hand-back, or typed.
func (rp *rowParser) userText(s string) {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "<task-notification"):
		rp.add(ev{speaker: "harness", kind: "boundary", text: s})
	case strings.HasPrefix(t, "<local-command-"):
	case strings.HasPrefix(t, "<bash-stdout"):
		rp.add(ev{speaker: "tool", kind: "tool_result", text: headTail(s)})
	case strings.HasPrefix(t, "<agent-message"):
		rp.add(ev{speaker: "subagent", kind: "handback", text: s})
	default: // typed text
		rp.add(typed(rp.asker, s))
	}
}

func (rp *rowParser) assistant(r *row) {
	if r.Message == nil {
		return
	}
	var bs []block
	json.Unmarshal(r.Message.Content, &bs)
	for _, b := range bs {
		switch b.Type {
		case "text":
			rp.add(ev{speaker: rp.answerer, kind: "reply", text: b.Text})
		case "thinking":
			rp.add(ev{speaker: rp.answerer, kind: "thinking", text: b.Thinking})
		case "tool_use", "server_tool_use":
			in := string(b.Input)
			rp.add(ev{speaker: rp.answerer, kind: "tool_call", text: b.Name + " " + clip(in, 4000), toolName: b.Name, toolInput: in, toolUse: b.ID})
		case "advisor_tool_result":
			s, _ := asText(b.Content)
			if s == "" {
				s = string(b.Content)
			}
			rp.add(ev{speaker: "advisor", kind: "reply", text: s})
		}
	}
}

func (rp *rowParser) attachment(r *row) {
	var a attachment
	if json.Unmarshal(r.Attachment, &a) != nil {
		return
	}
	switch a.Type {
	case "queued_command":
		rp.queued(a)
	case "hook_additional_context":
		s, _ := asText(a.Content)
		rp.add(ev{speaker: "harness", kind: "injected", text: s})
	case "hook_success":
		if injectingHooks[a.HookEvent] {
			s, _ := asText(a.Content)
			rp.add(ev{speaker: "harness", kind: "injected", text: s})
		}
	case "instructions":
		rp.instructions(a)
	}
}

// injectingHooks are the hook events whose output the model sees, so it is text the session was given.
var injectingHooks = map[string]bool{"SessionStart": true, "UserPromptSubmit": true, "SubagentStart": true}

// queued is a message queued while the agent worked: the user steering it, or a peer agent's hand-back.
func (rp *rowParser) queued(a attachment) {
	if a.Origin == nil {
		return
	}
	s, _ := asText(a.Prompt)
	switch a.Origin.Kind {
	case "human":
		rp.add(ev{speaker: "user", kind: "steering", text: s})
	case "peer":
		rp.add(ev{speaker: "subagent", kind: "handback", text: s})
	default:
		if alarmMarker.MatchString(s) { // the alarm's: insertEvent files it (SPEC A9)
			rp.add(ev{speaker: "relay", kind: "relay", text: s})
		}
	}
}

// instructions records which rule files the harness loaded, and their text once by its hash.
func (rp *rowParser) instructions(a attachment) {
	if len(a.Files) == 0 {
		return
	}
	var all strings.Builder
	var paths []string
	for _, f := range a.Files {
		fmt.Fprintf(&all, "== %s (%s)\n%s\n", f.Path, f.Type, f.Content)
		paths = append(paths, f.Path)
	}
	h := sha256.Sum256([]byte(all.String()))
	sha := hex.EncodeToString(h[:])
	rp.p.loaded = map[string]string{sha: all.String()}
	rp.add(ev{speaker: "harness", kind: "injected", text: "rule files loaded: " + strings.Join(paths, ", "), loadedSha: sha})
}

// typed files a user-role text as words only when a person typed words: interrupt markers, bridge launches by
// another agent and slash or ! commands are machine text (on a real store, 138 of 966 user rows). Rules for
// decisions and corrections must never read them (gitmemory: two in five user turns were not typed by a person; muninn CHANGELOG 401-411).
func typed(asker, s string) ev {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "[Request interrupted by user"):
		return ev{speaker: "harness", kind: "boundary", text: s}
	case strings.HasPrefix(t, "[[bridge-"):
		return ev{speaker: "relay", kind: "relay", text: s}
	case strings.HasPrefix(t, "<command-name>"), strings.HasPrefix(t, "<command-message>"), strings.HasPrefix(t, "<bash-input>"):
		return ev{speaker: asker, kind: "command", text: s}
	}
	return ev{speaker: asker, kind: "prompt", text: s}
}

// toolResult turns one tool_result block into an answer, a refusal or a plain result. In a program's session the
// denial or answer is the program's, as its prompts are (SPEC I16).
func toolResult(r *row, b block, asker string) []ev {
	s, _ := asText(b.Content)
	who := "user"
	if asker == "program" {
		who = asker
	}
	if r.ToolDenialKind == "user-rejected" {
		return []ev{{speaker: who, kind: "refusal", text: s, toolUse: b.ToolUseID, isError: boolp(true)}}
	}
	if t, ok := answers(r.ToolUseResult); ok {
		return []ev{{speaker: who, kind: "answer", text: t, toolUse: b.ToolUseID}}
	}
	e := ev{speaker: "tool", kind: "tool_result", text: headTail(s), toolUse: b.ToolUseID, isError: boolp(b.IsError)}
	if m := exitRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		e.exitCode = &n
	}
	if strings.TrimSpace(e.text) == "" {
		return nil
	}
	return []ev{e}
}

// answers is what the user answered the agent's questions, from a tool result: each question with its answer and the
// note the user wrote, in question order. ok is false when the result holds no answers.
func answers(toolUseResult json.RawMessage) (text string, ok bool) {
	var q struct {
		Answers     map[string]string `json:"answers"`
		Annotations map[string]struct {
			Notes string `json:"notes"`
		} `json:"annotations"`
	}
	if json.Unmarshal(toolUseResult, &q) != nil || len(q.Answers) == 0 {
		return "", false
	}
	keys := make([]string, 0, len(q.Answers))
	for k := range q.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var t strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&t, "Q: %s\nA: %s\n", k, q.Answers[k])
		if n := q.Annotations[k].Notes; n != "" {
			fmt.Fprintf(&t, "Note: %s\n", n)
		}
	}
	return t.String(), true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// alarmMarker is on every message the alarm or its stop hook writes (SPEC A4, A9).
var alarmMarker = regexp.MustCompile(`trace-alarm:[0-9]+:[0-9a-f]{16}`)

// alarmOr is e, or the alarm's message when e is words that reached the session from outside and carry a marker (SPEC
// A9), in whatever shape the program stored them: typed or queued, a command, a relay, a peer's hand-back, injected
// text. The marker is the match. What the agent itself said or ran can quote a marker and stays its own.
func alarmOr(e ev) ev {
	switch e.kind {
	case "prompt", "steering", "suggestion", "command", "relay", "handback", "injected":
		if e.speaker != "assistant" && alarmMarker.MatchString(e.text) {
			e.speaker, e.kind = "program", "alarm"
		}
	}
	return e
}
