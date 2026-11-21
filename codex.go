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
	"regexp"
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

// resumeCall matches a tool call that runs trace resume (trace or trace.exe, quoted or not; the input is stored as
// JSON, so a quote may come as \"), not a file named resume.go. taskAddCall matches one that runs trace task add or trace rule add.
var resumeCall = regexp.MustCompile(`(?i)trace(?:\.exe)?\\?["']?\s+resume\b`)
var taskAddCall = regexp.MustCompile(`(?i)trace(?:\.exe)?\\?["']?\s+(?:task|rule)\s+add\b`)

// liveSession is the session asking for `trace resume`, so the block shows the one before it (SPEC S1): among the
// sessions of this host and role written to in the last two minutes, the one whose last stored event is that call
// (the host writes the call before running it, and resume imports first); with none, the newest of them.
func liveSession(db *sql.DB, host, role string) string {
	return askingSession(db, resumeCall, "s.host = ? and s.role = ?", host, role)
}

// askingSession is S1's rule for any command: among the sessions that meet where and were written to in the last two
// minutes, the one whose last stored event is a tool call that call matches; with none, the newest of them.
func askingSession(db *sql.DB, call *regexp.Regexp, where string, args ...any) string {
	cut := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
	rows, err := db.Query(`select s.id, coalesce((select e.kind || ' ' || coalesce(e.text, '') from event e where e.session_id = s.id
		order by e.seq desc limit 1), '') from session s where `+where+` and s.ended_at >= ? order by s.ended_at desc`, append(args, cut)...)
	if err != nil {
		return ""
	}
	defer rows.Close()
	newest := ""
	for rows.Next() {
		var id, last string
		rows.Scan(&id, &last)
		if newest == "" {
			newest = id
		}
		if strings.HasPrefix(last, "tool_call ") && call.MatchString(last) {
			return id
		}
	}
	return newest
}

func codexHome() string { return envOrHome("CODEX_HOME", ".codex") }

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
	line, _ := bufio.NewReader(r).ReadBytes('\n') // nothing read does not parse below, so an empty or failed read is no rollout
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
	tx, err := im.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // a run that neither marks nor reads the rollout changes nothing, a cleared mark included
	return im.codexRollout(tx, path, st.Size(), st.ModTime().UTC().Format(time.RFC3339Nano))
}

// codexRollout reads what a rollout of this size and time holds that the store has not read yet, inside tx, and
// commits tx once it marked or read it.
func (im *importer) codexRollout(tx *sql.Tx, path string, size int64, mtime string) error {
	f, err := loadRolloutRow(tx, path)
	if err != nil || f.skipped.String == "scope:"+im.scope { // the mark holds only for the scope that set it (SPEC I1)
		return err
	}
	cleared, err := f.clearMark(tx)
	if err != nil {
		return err
	}
	zst := strings.HasSuffix(path, ".zst")
	if zst || size < f.from { // a compressed copy is read whole, a plain file rewritten shorter again from its start
		f.from = 0
	}
	if f.unchanged(zst, cleared, size, mtime) {
		return nil
	}
	return im.newRollout(tx, path, zst, f, size, mtime)
}

// newRollout reads a rollout that changed, or marks it when its thread is outside scope.
func (im *importer) newRollout(tx *sql.Tx, path string, zst bool, f rolloutRow, size int64, mtime string) error {
	meta, ok, err := rolloutMeta(path)
	if err != nil || !ok { // not ok: still being created, or not a rollout; the next run tries again
		return err
	}
	if !inScope(meta.Cwd, im.scope) {
		err = f.mark(tx, im.scope, size, mtime)
	} else {
		err = im.readRollout(tx, path, meta, zst, f, size, mtime)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// rolloutRow is what the store remembers of a rollout file.
type rolloutRow struct {
	id, from       int64
	size           sql.NullInt64
	mtime, skipped sql.NullString
}

func loadRolloutRow(tx *sql.Tx, path string) (rolloutRow, error) {
	var f rolloutRow
	if _, err := tx.Exec("insert or ignore into source_file(host, path) values ('codex', ?)", path); err != nil {
		return f, err
	}
	err := tx.QueryRow("select id, read_to_byte, size, mtime, skipped from source_file where path = ?", path).Scan(&f.id, &f.from, &f.size, &f.mtime, &f.skipped)
	return f, err
}

// clearMark drops a mark set under another scope, or before marks named one, so the rollout is read again from
// the start (SPEC I1); true when there was one.
func (f *rolloutRow) clearMark(tx *sql.Tx) (bool, error) {
	if !f.skipped.Valid {
		return false, nil
	}
	f.from = 0
	_, err := tx.Exec("update source_file set skipped = null, read_to_byte = 0 where id = ?", f.id)
	return true, err
}

// mark keeps a rollout outside scope out of later runs under the same scope, with the size and time it had.
func (f rolloutRow) mark(tx *sql.Tx, scope string, size int64, mtime string) error {
	_, err := tx.Exec("update source_file set skipped = ?, size = ?, mtime = ? where id = ?", "scope:"+scope, size, mtime, f.id)
	return err
}

// unchanged says there is nothing new to read: a compressed copy with the size and time it had when read (unless a
// mark was just cleared: they were stored with the mark), or a plain file read to its end.
func (f rolloutRow) unchanged(zst, cleared bool, size int64, mtime string) bool {
	if zst {
		return !cleared && f.size.Int64 == size && f.mtime.String == mtime
	}
	return size == f.from
}

// rolloutMeta is a rollout's first record, its session_meta; false while it is not there.
func rolloutMeta(path string) (m codexMeta, ok bool, err error) {
	rc, err := openRollout(path)
	if err == nil {
		defer rc.Close()
		m, ok = codexFirst(rc)
	}
	return m, ok, err
}

// readRollout reads a rollout from f.from (a compressed one from its start) and records how far it was read.
func (im *importer) readRollout(tx *sql.Tx, path string, meta codexMeta, zst bool, f rolloutRow, size int64, mtime string) error {
	rc, err := openRollout(path)
	if err != nil {
		return err
	}
	defer rc.Close()
	if !zst {
		if _, err := rc.(*os.File).Seek(f.from, io.SeekStart); err != nil {
			return err
		}
	}
	end, bad, err := im.readCodex(tx, rc, path, meta, f.from, f.id)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if zst {
		end = size
	}
	_, err = tx.Exec("update source_file set size = ?, mtime = ?, read_to_byte = ?, bad_lines = bad_lines + ?, last_error = null where id = ?",
		size, mtime, end, bad, f.id)
	return err
}

// codexPayload is the part of a rollout line's payload trace reads, for every record kind.
type codexPayload struct {
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

type codexItem struct {
	Type        string      `json:"type"`
	Content     []codexText `json:"content"`
	SummaryText []string    `json:"summary_text"`
}

// codexSig names a rollout record's kind: the line type, then the payload's, the item's or the message role.
func codexSig(l codexLine, p codexPayload) (string, codexItem) {
	sig := l.Type
	if p.Type != "" && l.Type != "turn_context" && l.Type != "compacted" {
		sig += "/" + p.Type
	}
	var item codexItem
	if p.Type == "item_completed" {
		json.Unmarshal(p.Item, &item)
		sig += "/" + item.Type
	}
	if l.Type == "response_item" && p.Type == "message" {
		sig += "/" + p.Role
	}
	return sig, item
}

// codexEvents are the events one rollout record of kind sig yields, in order; known is false for a kind trace does
// not map. A compaction yields its boundary and then its summary. A pure mapping: storing is readCodex's job.
func codexEvents(sig string, p codexPayload, item codexItem) (events []ev, known bool) {
	if f, known := codexMaps[sig]; known {
		return f(p, item), true
	}
	return nil, false
}

// codexMap turns one rollout record into its events.
type codexMap func(p codexPayload, item codexItem) []ev

var (
	codexToolCall = func(p codexPayload, _ codexItem) []ev { return []ev{codexCall(p)} }
	codexResult   = func(p codexPayload, _ codexItem) []ev {
		return []ev{{speaker: "tool", kind: "tool_result", text: headTail(outputText(p.Output)), toolUse: p.CallID}}
	}
	codexInjected = func(p codexPayload, _ codexItem) []ev {
		return []ev{{speaker: "harness", kind: "injected", text: joinTexts(p.Content)}}
	}
)

// codexMaps maps each record kind trace stores to its events; a turn_context is known and yields none.
var codexMaps = map[string]codexMap{
	"event_msg/item_completed/UserMessage": func(_ codexPayload, item codexItem) []ev {
		return []ev{{speaker: "user", kind: "prompt", text: joinTexts(item.Content)}}
	},
	"event_msg/user_message": func(p codexPayload, _ codexItem) []ev { // before item_completed existed
		return []ev{{speaker: "user", kind: "prompt", text: p.Message}}
	},
	"event_msg/item_completed/AgentMessage": func(_ codexPayload, item codexItem) []ev {
		return []ev{{speaker: "assistant", kind: "reply", text: joinTexts(item.Content)}}
	},
	"event_msg/item_completed/Reasoning": func(_ codexPayload, item codexItem) []ev {
		return []ev{{speaker: "assistant", kind: "thinking", text: strings.Join(item.SummaryText, "\n")}}
	},
	"response_item/custom_tool_call":        codexToolCall,
	"response_item/function_call":           codexToolCall,
	"response_item/custom_tool_call_output": codexResult,
	"response_item/function_call_output":    codexResult,
	"response_item/agent_message": func(p codexPayload, _ codexItem) []ev {
		return []ev{{speaker: "relay", kind: "relay", text: p.Author + " -> " + p.Recipient + ": " + joinTexts(p.Content)}}
	},
	"response_item/message/user":      codexInjected,
	"response_item/message/developer": codexInjected,
	"compacted": func(p codexPayload, _ codexItem) []ev {
		return []ev{{speaker: "harness", kind: "boundary", text: "compacted"}, {speaker: "model-summary", kind: "summary", text: p.Message}}
	},
	"event_msg/turn_aborted": func(p codexPayload, _ codexItem) []ev {
		return []ev{{speaker: "harness", kind: "boundary", text: "turn aborted: " + p.Reason}}
	},
	"turn_context": func(codexPayload, codexItem) []ev { return nil },
}

// codexCall is a tool call: a namespaced tool is named namespace.name; a function call's input is its arguments.
func codexCall(p codexPayload) ev {
	name, in := p.Name, p.Input
	if p.Namespace != "" {
		name = p.Namespace + "." + p.Name
	}
	if in == "" {
		in = p.Arguments
	}
	return ev{speaker: "assistant", kind: "tool_call", text: name + " " + clip(in, 4000), toolName: name, toolInput: in, toolUse: p.CallID}
}

// codexRead is one pass over a rollout: the thread's session and what is needed to store its events in order.
type codexRead struct {
	im            *importer
	tx            *sql.Tx
	sid, path     string
	meta          codexMeta
	sub, program  bool
	fileID        int64
	seq, comp     int
	bad           int
	lastTs, model string
}

func (im *importer) readCodex(tx *sql.Tx, r io.Reader, path string, meta codexMeta, from, fileID int64) (int64, int, error) {
	c := &codexRead{im: im, tx: tx, sid: meta.ID, fileID: fileID, path: path, meta: meta}
	c.sub = meta.ParentThreadID != "" || bytes.Contains(meta.Source, []byte(`"subagent"`))
	c.program = string(meta.Source) == `"exec"` || string(meta.Source) == `"mcp"`
	role, parent := "subagent", meta.ParentThreadID
	if !c.sub {
		role, parent = roleFor(meta.Cwd, projectRole(meta.Cwd)), ""
	}
	if _, err := tx.Exec(`insert or ignore into session(id, host, role, parent_id, cwd, branch_first, branch_last, version_first, started_at, ended_at, excluded)
		values (?, 'codex', ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
		c.sid, role, parent, meta.Cwd, meta.Git.Branch, meta.Git.Branch, meta.CliVersion, meta.Timestamp, meta.Timestamp, excludedFor(folderName(meta.Cwd))); err != nil {
		return from, c.bad, err // nothing read yet
	}
	tx.QueryRow("select coalesce(max(seq), 0) from event where session_id = ?", c.sid).Scan(&c.seq)
	if from != 0 { // continuing a read: from is never negative
		tx.QueryRow("select compactions from session where id = ?", c.sid).Scan(&c.comp)
	}
	end, err := lines(r, from, c.line)
	if err != nil {
		return end, c.bad, err
	}
	_, err = tx.Exec(`update session set ended_at = max(coalesce(ended_at, ''), ?), model = coalesce(nullif(?, ''), model), compactions = max(compactions, ?) where id = ?`,
		c.lastTs, c.model, c.comp, c.sid)
	return end, c.bad, err
}

// line reads one rollout line: a blob or a broken line is skipped (a broken one counted), the parent's copied
// history too, and any other record goes through codexEvents into the store.
func (c *codexRead) line(line []byte, at int64) error {
	if codexHeavy(line) {
		return nil
	}
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		c.bad++
		return nil
	}
	if c.parentHistory(l) {
		return nil
	}
	if l.Timestamp != "" {
		c.lastTs = l.Timestamp
	}
	var p codexPayload
	json.Unmarshal(l.Payload, &p)
	sig, item := codexSig(l, p)
	c.note(sig, p)
	events, known := codexEvents(sig, p, item)
	events = c.speakers(events)
	if !known && !codexSkip[sig] {
		c.im.unmapped["codex "+sig]++
	}
	key := codexKey(c.sid, c.path, l, at)
	for i, e := range events {
		if err := c.put(fmt.Sprintf("%s:%d", key, i), at, l.Timestamp, e); err != nil {
			return err
		}
	}
	return nil
}

// speakers gives a thread a program started its own prompts: the user did not type them (SPEC I11).
func (c *codexRead) speakers(events []ev) []ev {
	if !c.program {
		return events
	}
	for i := range events {
		if events[i].speaker == "user" {
			events[i].speaker = "program"
		}
	}
	return events
}

// parentHistory says a subagent's line is the parent's history, copied into the subagent's file.
func (c *codexRead) parentHistory(l codexLine) bool {
	end := c.meta.SubagentHistoryEnd
	return c.sub && l.Ordinal != nil && end != nil && *l.Ordinal < *end
}

// note keeps what a record says about the thread: the model a turn ran with, and a compaction.
func (c *codexRead) note(sig string, p codexPayload) {
	if sig == "turn_context" && p.Model != "" {
		c.model = p.Model
	}
	if sig == "compacted" {
		c.comp++
	}
}

// codexHeavy says a line is one of the base64 blobs of hundreds of MB, from its first 400 bytes, before any parse.
func codexHeavy(line []byte) bool {
	head := line[:min(len(line), 400)]
	for _, h := range heavy {
		if bytes.Contains(head, h) {
			return true
		}
	}
	return false
}

// codexKey keys a record by thread and ordinal. Rows written before ordinals existed are keyed by the plain file's
// name and the byte offset, so its compressed copy (same bytes, same offsets) keys alike (SPEC I3).
func codexKey(sid, path string, l codexLine, at int64) string {
	if l.Ordinal != nil {
		return fmt.Sprintf("codex:%s:%d", sid, *l.Ordinal)
	}
	return fmt.Sprintf("codex:%s:@%s:%d", sid, strings.TrimSuffix(filepath.Base(path), ".zst"), at)
}

// put stores one event with words, naming the tool a result answers; a relay tool's result is another agent's words.
func (c *codexRead) put(key string, at int64, ts string, e ev) error {
	if e.text == "" && e.toolInput == "" {
		return nil
	}
	if e.kind == "tool_result" && e.toolUse != "" {
		// its own session's latest call with that id, stored by this run or an earlier one (SPEC I5, I6)
		c.tx.QueryRow("select tool_name from event where session_id = ? and tool_use_id = ? and kind = 'tool_call' order by seq desc limit 1", c.sid, e.toolUse).Scan(&e.toolName)
		if relayTool(e.toolName) {
			e.speaker, e.kind = "relay", "relay"
		}
	}
	c.seq++
	err := c.im.insertEvent(c.tx, key, c.sid, c.seq, ts, "", c.comp, c.fileID, at, e)
	return err
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
