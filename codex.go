package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Codex keeps one rollout file per thread under <codex home>/sessions/YYYY/MM/DD (and archived_sessions/). A line is
// {timestamp, ordinal, type, payload}. Files idle for a week are compressed to .jsonl.zst, and a revert writes a new
// file that continues the ordinals, so the key of a row is (thread, ordinal), never the file. Only one source per
// fact is stored: the typed words come from item_completed/UserMessage, never from the response_item copies.

func hostName(host string) string {
	switch host {
	case "codex":
		return "Codex"
	case "claude-code":
		return "Claude Code"
	}
	return host
}

// liveSession is the newest session of this host and role when it was written to in the last two minutes: the
// session asking for `trace resume` is itself, and the block should show the one before it.
func liveSession(db *sql.DB, host, role string) string {
	var id string
	cut := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	db.QueryRow(`select id from session where host = ? and role = ? and ended_at >= ? order by ended_at desc limit 1`, host, role, cut).Scan(&id)
	return id
}

func codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	u, _ := os.UserHomeDir()
	return filepath.Join(u, ".codex")
}

// codexFiles lists every rollout, plain or compressed. A missing Codex home is no Codex, not an error.
func codexFiles(home string) []string {
	var out []string
	for _, sub := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(home, sub), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "rollout-") &&
				(strings.HasSuffix(p, ".jsonl") || strings.HasSuffix(p, ".jsonl.zst")) {
				out = append(out, p)
			}
			return nil
		})
	}
	return out
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *int64          `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexMeta struct {
	ID                 string          `json:"id"`
	ParentThreadID     string          `json:"parent_thread_id"`
	Timestamp          string          `json:"timestamp"`
	Cwd                string          `json:"cwd"`
	CliVersion         string          `json:"cli_version"`
	Source             json.RawMessage `json:"source"`
	SubagentHistoryEnd *int64          `json:"subagent_history_start_ordinal"`
	Git                struct {
		Branch string `json:"branch"`
	} `json:"git"`
}

type codexText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func joinTexts(ts []codexText) string {
	var parts []string
	for _, t := range ts {
		if t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// outputText reads a tool output that is a plain string in function_call_output and a list of parts in
// custom_tool_call_output.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []codexText
	json.Unmarshal(raw, &parts)
	return joinTexts(parts)
}

// codexSkip are the rows that carry nothing to store, or only a duplicate of what another row already stored.
var codexSkip = map[string]bool{
	"event_msg/token_count": true, "token_usage_record": true, "event_msg/task_started": true,
	"event_msg/task_complete": true, "event_msg/thread_settings_applied": true, "world_state": true,
	"response_item/reasoning": true, "response_item/message/assistant": true, "response_item/additional_tools": true,
	"response_item/compaction": true, "inter_agent_communication_metadata": true, "session_meta": true,
	"event_msg/item_completed/CommandExecution": true, "event_msg/item_completed/McpToolCall": true,
	"event_msg/item_completed/FileChange": true, "event_msg/item_completed/Extension": true,
	"event_msg/item_completed/ImageView": true, "event_msg/item_completed/ContextCompaction": true,
	"event_msg/item_completed/SubAgentActivity": true,
}

// heavy rows are base64 blobs of hundreds of MB; they are skipped before any JSON is parsed.
var heavy = [][]byte{[]byte(`"type":"Extension"`), []byte(`"type":"ImageView"`), []byte(`"type":"image_generation`)}

// codexFirst reads the session_meta of a rollout: the thread it belongs to and the folder it ran in.
func codexFirst(r io.Reader) (codexMeta, bool) {
	br := bufio.NewReaderSize(r, 1<<20)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return codexMeta{}, false
	}
	var l codexLine
	var m codexMeta
	if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" || json.Unmarshal(l.Payload, &m) != nil || m.ID == "" {
		return codexMeta{}, false
	}
	return m, true
}

func openRollout(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".zst") {
		return f, nil
	}
	zr, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(1<<30))
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{zr.IOReadCloser(), f}, nil
}

// importCodex reads one rollout. Plain files are read from the stored byte offset, like Claude Code transcripts;
// compressed files are read whole, and only when their size or time changed. A thread outside scope is marked so
// later runs skip it without opening it again.
func (im *importer) importCodex(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	mtime := st.ModTime().UTC().Format(time.RFC3339Nano)
	tx, err := im.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("insert or ignore into source_file(host, path) values ('codex', ?)", path); err != nil {
		return err
	}
	var fileID, from int64
	var size sql.NullInt64
	var oldMtime, skipped sql.NullString
	if err := tx.QueryRow("select id, read_to_byte, size, mtime, skipped from source_file where path = ?", path).Scan(&fileID, &from, &size, &oldMtime, &skipped); err != nil {
		return err
	}
	if skipped.Valid {
		return nil
	}
	zst := strings.HasSuffix(path, ".zst")
	if zst && size.Int64 == st.Size() && oldMtime.String == mtime {
		return nil
	}
	if !zst && st.Size() == from {
		return nil
	}
	rc, err := openRollout(path)
	if err != nil {
		return err
	}
	meta, ok := codexFirst(rc)
	rc.Close()
	if !ok {
		return nil // still being created, or not a rollout: the next run tries again
	}
	if im.scope != "" && !strings.Contains(folderName(meta.Cwd), strings.ToLower(im.scope)) {
		_, err := tx.Exec("update source_file set skipped = 'scope', size = ?, mtime = ? where id = ?", st.Size(), mtime, fileID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if zst || st.Size() < from {
		from = 0
	}
	rc, err = openRollout(path)
	if err != nil {
		return err
	}
	defer rc.Close()
	if !zst {
		if _, err := rc.(*os.File).Seek(from, io.SeekStart); err != nil {
			return err
		}
	}
	end, bad, err := im.readCodex(tx, rc, path, meta, from, fileID)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if zst {
		end = st.Size()
	}
	if _, err := tx.Exec("update source_file set size = ?, mtime = ?, read_to_byte = ?, bad_lines = bad_lines + ?, last_error = null where id = ?",
		st.Size(), mtime, end, bad, fileID); err != nil {
		return err
	}
	return tx.Commit()
}

func (im *importer) readCodex(tx *sql.Tx, r io.Reader, path string, meta codexMeta, from, fileID int64) (int64, int, error) {
	sid := meta.ID
	sub := meta.ParentThreadID != "" || bytes.Contains(meta.Source, []byte(`"subagent"`))
	role, parent := "subagent", meta.ParentThreadID
	if !sub {
		role, parent = roleFor(meta.Cwd, projectRole(meta.Cwd)), ""
	}
	if _, err := tx.Exec(`insert or ignore into session(id, host, role, parent_id, cwd, branch_first, branch_last, version_first, started_at, ended_at, excluded)
		values (?, 'codex', ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
		sid, role, parent, meta.Cwd, meta.Git.Branch, meta.Git.Branch, meta.CliVersion, meta.Timestamp, meta.Timestamp, excludedFor(folderName(meta.Cwd))); err != nil {
		return from, 0, err
	}
	var seq, comp int
	tx.QueryRow("select coalesce(max(seq), 0) from event where session_id = ?", sid).Scan(&seq)
	if from > 0 {
		tx.QueryRow("select compactions from session where id = ?", sid).Scan(&comp)
	}
	var lastTs, model string
	toolNames := map[string]string{}
	bad := 0
	put := func(key string, at int64, ts string, e ev) error {
		if e.text == "" && e.toolInput == "" {
			return nil
		}
		if e.kind == "tool_call" && e.toolUse != "" {
			toolNames[e.toolUse] = e.toolName
		}
		if e.kind == "tool_result" && e.toolUse != "" {
			e.toolName = toolNames[e.toolUse]
			if e.toolName == "" {
				tx.QueryRow("select tool_name from event where tool_use_id = ? and kind = 'tool_call'", e.toolUse).Scan(&e.toolName)
			}
			if relayTool(e.toolName) {
				e.speaker, e.kind = "relay", "relay"
			}
		}
		seq++
		_, err := im.insertEvent(tx, key, sid, seq, ts, "", comp, fileID, at, e)
		return err
	}
	end, err := lines(r, from, func(line []byte, at int64) error {
		head := line[:min(len(line), 400)]
		for _, h := range heavy {
			if bytes.Contains(head, h) {
				return nil
			}
		}
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			bad++
			return nil
		}
		if l.Ordinal != nil && sub && meta.SubagentHistoryEnd != nil && *l.Ordinal < *meta.SubagentHistoryEnd {
			return nil // the parent's history, copied into the subagent's file
		}
		key := fmt.Sprintf("codex:%s:@%s:%d", sid, filepath.Base(path), at) // rows written before ordinals existed
		if l.Ordinal != nil {
			key = fmt.Sprintf("codex:%s:%d", sid, *l.Ordinal)
		}
		if l.Timestamp != "" {
			lastTs = l.Timestamp
		}
		var p struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   []codexText     `json:"content"`
			Item      json.RawMessage `json:"item"`
			Name      string          `json:"name"`
			Namespace string          `json:"namespace"`
			Input     string          `json:"input"`
			Arguments string          `json:"arguments"`
			CallID    string          `json:"call_id"`
			Output    json.RawMessage `json:"output"`
			Author    string          `json:"author"`
			Recipient string          `json:"recipient"`
			Message   string          `json:"message"`
			Reason    string          `json:"reason"`
			Model     string          `json:"model"`
		}
		json.Unmarshal(l.Payload, &p)
		sig := l.Type
		if p.Type != "" && l.Type != "turn_context" && l.Type != "compacted" {
			sig += "/" + p.Type
		}
		var item struct {
			Type        string      `json:"type"`
			Content     []codexText `json:"content"`
			SummaryText []string    `json:"summary_text"`
		}
		if p.Type == "item_completed" {
			json.Unmarshal(p.Item, &item)
			sig += "/" + item.Type
		}
		if l.Type == "response_item" && p.Type == "message" {
			sig += "/" + p.Role
		}
		if l.Type == "response_item" && p.Type == "additional_tools" {
			sig = "response_item/additional_tools"
		}
		ts := l.Timestamp
		switch sig {
		case "turn_context":
			if p.Model != "" {
				model = p.Model
			}
			return nil
		case "event_msg/item_completed/UserMessage":
			return put(key+":0", at, ts, ev{speaker: "user", kind: "prompt", text: joinTexts(item.Content)})
		case "event_msg/user_message": // before item_completed existed
			return put(key+":0", at, ts, ev{speaker: "user", kind: "prompt", text: p.Message})
		case "event_msg/item_completed/AgentMessage":
			return put(key+":0", at, ts, ev{speaker: "assistant", kind: "reply", text: joinTexts(item.Content)})
		case "event_msg/item_completed/Reasoning":
			return put(key+":0", at, ts, ev{speaker: "assistant", kind: "thinking", text: strings.Join(item.SummaryText, "\n")})
		case "response_item/custom_tool_call", "response_item/function_call":
			name, in := p.Name, p.Input
			if p.Namespace != "" {
				name = p.Namespace + "." + p.Name
			}
			if in == "" {
				in = p.Arguments
			}
			return put(key+":0", at, ts, ev{speaker: "assistant", kind: "tool_call", text: name + " " + clip(in, 4000), toolName: name, toolInput: in, toolUse: p.CallID})
		case "response_item/custom_tool_call_output", "response_item/function_call_output":
			return put(key+":0", at, ts, ev{speaker: "tool", kind: "tool_result", text: headTail(outputText(p.Output)), toolUse: p.CallID})
		case "response_item/agent_message":
			return put(key+":0", at, ts, ev{speaker: "relay", kind: "relay", text: p.Author + " -> " + p.Recipient + ": " + joinTexts(p.Content)})
		case "response_item/message/user", "response_item/message/developer":
			return put(key+":0", at, ts, ev{speaker: "harness", kind: "injected", text: joinTexts(p.Content)})
		case "compacted":
			comp++
			if err := put(key+":0", at, ts, ev{speaker: "harness", kind: "boundary", text: "compacted"}); err != nil {
				return err
			}
			return put(key+":1", at, ts, ev{speaker: "model-summary", kind: "summary", text: p.Message})
		case "event_msg/turn_aborted":
			return put(key+":0", at, ts, ev{speaker: "harness", kind: "boundary", text: "turn aborted: " + p.Reason})
		}
		if !codexSkip[sig] {
			im.unmapped["codex "+sig]++
		}
		return nil
	})
	if err != nil {
		return end, bad, err
	}
	_, err = tx.Exec(`update session set ended_at = max(coalesce(ended_at, ''), ?), model = coalesce(nullif(?, ''), model), compactions = max(compactions, ?) where id = ?`,
		lastTs, model, comp, sid)
	return end, bad, err
}

// codexNames copies thread names from session_index.jsonl, for threads already stored (so names of threads
// outside scope never enter the database).
func codexNames(db *sql.DB, home string) error {
	b, err := os.ReadFile(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return nil
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
			UpdatedAt  string `json:"updated_at"`
		}
		if json.Unmarshal(line, &r) != nil || r.ID == "" || r.ThreadName == "" {
			continue
		}
		if _, err := db.Exec(`insert or ignore into session_name(session_id, name, first_seen)
			select ?, ?, nullif(?, '') where exists (select 1 from session where id = ? and host = 'codex')`,
			r.ID, r.ThreadName, r.UpdatedAt, r.ID); err != nil {
			return err
		}
	}
	return nil
}
