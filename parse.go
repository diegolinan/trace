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

// asText returns a content field's text: a plain string, or the text blocks of an array joined.
func asText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return "", false
	}
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), false
}

// headTail keeps tool output searchable without storing megabytes; the raw copy keeps the rest (ReMe's head/tail).
func headTail(s string) string {
	const keep = 1500
	if len(s) <= 2*keep+200 {
		return s
	}
	h, t := s[:keep], s[len(s)-keep:]
	for len(h) > 0 && !validTail(h) {
		h = h[:len(h)-1]
	}
	for len(t) > 0 && (t[0]&0xC0) == 0x80 {
		t = t[1:]
	}
	return h + fmt.Sprintf("\n[… %d chars cut, full text in the raw copy …]\n", len(s)-len(h)-len(t)) + t
}

// validTail reports whether s does not end inside a multi-byte UTF-8 character.
func validTail(s string) bool {
	for i := len(s) - 1; i >= 0 && i >= len(s)-4; i-- {
		c := s[i]
		if c < 0x80 {
			return true
		}
		if c >= 0xC0 {
			need := 2
			if c >= 0xE0 {
				need = 3
			}
			if c >= 0xF0 {
				need = 4
			}
			return len(s)-i >= need
		}
	}
	return true
}

var exitRe = regexp.MustCompile(`^(?:Error: )?Exit code (\d+)`)

func boolp(b bool) *bool { return &b }

// parseRow maps one transcript row to events. sub is true for a subagent's own transcript, where the
// user-side prompt was written by the parent agent, not by the user.
func parseRow(r *row, sub bool) parsed {
	var p parsed
	asker, answerer := "user", "assistant"
	if sub {
		asker, answerer = "assistant", "subagent"
	}
	add := func(e ev) {
		if strings.TrimSpace(e.text) != "" {
			p.events = append(p.events, e)
		}
	}
	switch r.Type {
	case "ai-title":
		p.title = r.AiTitle
	case "custom-title":
		p.title, p.custom = r.CustomTitle, true
	case "agent-name": // /rename writes both rows; an older name may survive only here
		p.title, p.custom = r.AgentName, true
	case "system":
		switch r.Subtype {
		case "compact_boundary":
			p.compacted = true
			t := "conversation compacted"
			if m := r.CompactMetadata; m != nil {
				t = fmt.Sprintf("conversation compacted (%s, %d tokens before)", m.Trigger, m.PreTokens)
			}
			add(ev{speaker: "harness", kind: "boundary", text: t})
		case "away_summary":
			s, _ := asText(r.Content)
			add(ev{speaker: "model-summary", kind: "summary", text: s})
		}
	case "user":
		if r.Message == nil {
			return p
		}
		if r.IsCompactSummary {
			s, _ := asText(r.Message.Content)
			add(ev{speaker: "model-summary", kind: "summary", text: s})
			return p
		}
		s, isStr := asText(r.Message.Content)
		if r.IsMeta {
			switch {
			case strings.Contains(s, "<agent-message"):
				add(ev{speaker: "subagent", kind: "handback", text: s})
			case strings.Contains(s, "<cross-session-message"):
				add(ev{speaker: "relay", kind: "relay", text: s})
			}
			return p
		}
		if isStr {
			t := strings.TrimSpace(s)
			switch {
			case strings.HasPrefix(t, "<task-notification"):
				add(ev{speaker: "harness", kind: "boundary", text: s})
			case strings.HasPrefix(t, "<local-command-"):
			case strings.HasPrefix(t, "<bash-stdout"):
				add(ev{speaker: "tool", kind: "tool_result", text: headTail(s)})
			case strings.HasPrefix(t, "<agent-message"):
				add(ev{speaker: "subagent", kind: "handback", text: s})
			default: // typed text
				add(typed(asker, s))
			}
			return p
		}
		var bs []block
		json.Unmarshal(r.Message.Content, &bs)
		for _, b := range bs {
			switch b.Type {
			case "text":
				add(typed(asker, b.Text))
			case "tool_result":
				p.events = append(p.events, toolResult(r, b)...)
			}
		}
	case "assistant":
		if r.Message == nil {
			return p
		}
		var bs []block
		json.Unmarshal(r.Message.Content, &bs)
		for _, b := range bs {
			switch b.Type {
			case "text":
				add(ev{speaker: answerer, kind: "reply", text: b.Text})
			case "thinking":
				add(ev{speaker: answerer, kind: "thinking", text: b.Thinking})
			case "tool_use", "server_tool_use":
				in := string(b.Input)
				add(ev{speaker: answerer, kind: "tool_call", text: b.Name + " " + clip(in, 4000), toolName: b.Name, toolInput: in, toolUse: b.ID})
			case "advisor_tool_result":
				s, _ := asText(b.Content)
				if s == "" {
					s = string(b.Content)
				}
				add(ev{speaker: "advisor", kind: "reply", text: s})
			}
		}
	case "attachment":
		var a attachment
		if json.Unmarshal(r.Attachment, &a) != nil {
			return p
		}
		switch a.Type {
		case "queued_command":
			s, _ := asText(a.Prompt)
			if a.Origin != nil && a.Origin.Kind == "human" {
				add(ev{speaker: "user", kind: "steering", text: s})
			} else if a.Origin != nil && a.Origin.Kind == "peer" {
				add(ev{speaker: "subagent", kind: "handback", text: s})
			}
		case "hook_additional_context":
			s, _ := asText(a.Content)
			add(ev{speaker: "harness", kind: "injected", text: s})
		case "hook_success":
			if a.HookEvent == "SessionStart" || a.HookEvent == "UserPromptSubmit" || a.HookEvent == "SubagentStart" {
				s, _ := asText(a.Content)
				add(ev{speaker: "harness", kind: "injected", text: s})
			}
		case "instructions":
			if len(a.Files) == 0 {
				return p
			}
			var all strings.Builder
			var paths []string
			for _, f := range a.Files {
				fmt.Fprintf(&all, "== %s (%s)\n%s\n", f.Path, f.Type, f.Content)
				paths = append(paths, f.Path)
			}
			h := sha256.Sum256([]byte(all.String()))
			sha := hex.EncodeToString(h[:])
			p.loaded = map[string]string{sha: all.String()}
			add(ev{speaker: "harness", kind: "injected", text: "rule files loaded: " + strings.Join(paths, ", "), loadedSha: sha})
		}
	}
	return p
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

// toolResult turns one tool_result block into an answer, a refusal or a plain result.
func toolResult(r *row, b block) []ev {
	s, _ := asText(b.Content)
	if r.ToolDenialKind == "user-rejected" {
		return []ev{{speaker: "user", kind: "refusal", text: s, toolUse: b.ToolUseID, isError: boolp(true)}}
	}
	var q struct {
		Answers     map[string]string `json:"answers"`
		Annotations map[string]struct {
			Notes string `json:"notes"`
		} `json:"annotations"`
	}
	if len(r.ToolUseResult) > 0 && r.ToolUseResult[0] == '{' && json.Unmarshal(r.ToolUseResult, &q) == nil && len(q.Answers) > 0 {
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
		return []ev{{speaker: "user", kind: "answer", text: t.String(), toolUse: b.ToolUseID}}
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

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n] + "…"
}
