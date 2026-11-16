package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type importer struct {
	db         *sql.DB
	archiveDir string // raw copies, one .gz per transcript, appended as new gzip members
	scope      string // only history.jsonl prompts whose project folder contains this
	added      int
	touched    map[string]bool // sessions that got events this run
	unmapped   map[string]int  // row kinds no rule stores, counted so a new format shows up
}

// transcriptFiles lists every session and subagent transcript in scope: project folders whose name
// contains scope.
// It also returns the files under <session>/tool-results, relative to projectsDir: the full output the
// transcript keeps only as a 2 KB preview, which Claude Code's cleanup deletes with the session.
func transcriptFiles(projectsDir, scope string) (out, results []string, err error) {
	if st, serr := os.Stat(projectsDir); serr == nil && !st.IsDir() { // a wrong -projects says so (SPEC E1)
		return nil, nil, fmt.Errorf("%s is a file, not a Claude Code projects folder", projectsDir)
	}
	dirs, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return nil, nil, nil // no Claude Code on this machine (a Codex-only user)
	}
	if err != nil {
		return nil, nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() || !inScope(d.Name(), scope) {
			continue
		}
		root := filepath.Join(projectsDir, d.Name())
		filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			parts := strings.Split(filepath.ToSlash(rel), "/")
			switch {
			case len(parts) >= 3 && parts[1] == "tool-results":
				results = append(results, filepath.Join(d.Name(), rel))
			case !strings.HasSuffix(p, ".jsonl") && !rawCopy.MatchString(p):
			// Workflow subagents sit deeper, in subagents/workflows/<wf>/ (lore claude_code.rs:237-260).
			case len(parts) == 1 || (len(parts) >= 3 && parts[1] == "subagents"):
				out = append(out, p)
			}
			return nil
		})
	}
	return out, results, nil
}

// keepResults copies tool-results files into the raw folder once; they are written once and never change.
func keepResults(projectsDir, archive string, results []string) error {
	if archive == "" {
		return nil
	}
	for _, rel := range results {
		src, dst := filepath.Join(projectsDir, rel), filepath.Join(archive, rel)
		st, err := os.Stat(src)
		if err != nil {
			continue // deleted since the walk
		}
		if d, err := os.Stat(dst); err == nil && d.Size() == st.Size() {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst+".tmp", b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(dst+".tmp", dst); err != nil {
			return err
		}
	}
	return nil
}

// sessionFor names the session a transcript file belongs to and its role.
// rawCopy matches a transcript's raw copy (archive): <session>.jsonl.gz, or a copy set aside when the transcript was
// rewritten, <session>.jsonl.replaced-<UTC time>.gz. `trace restore` reads them as transcripts.
var rawCopy = regexp.MustCompile(`\.jsonl(\.replaced-\d{8}T\d{6})?\.gz$`)

func sessionFor(path string) (id, role, parent, excluded string) {
	base := rawCopy.ReplaceAllString(filepath.Base(path), ".jsonl")
	base = strings.TrimSuffix(base, ".jsonl")
	dir := filepath.Dir(path)
	for d := dir; filepath.Dir(d) != d; d = filepath.Dir(d) { // subagents/agent-x.jsonl or subagents/workflows/<wf>/agent-x.jsonl
		if filepath.Base(d) == "subagents" {
			parent = filepath.Base(filepath.Dir(d))
			return base, "subagent", parent, excludedFor(filepath.Base(filepath.Dir(filepath.Dir(d))))
		}
	}
	// Fallback only: the folder is named after the path, which moves (claude-code #101522). Windows launchers
	// write C-- or c-- (episodic-memory #202), so compare without case.
	project := filepath.Base(dir)
	return base, folderRole(project), "", excludedFor(project)
}

// excludedFor keeps probe and test folders out of recall (mnemos polluted its own store with test runs). project is
// a Claude Code transcript folder name or a Codex cwd; both are compared by their normalized name (SPEC I4).
// shortcut: a Claude Code folder name has lost its separators, so a project folder whose name holds "temp" or "tmp"
// between separators (my-temp-tool) is left out too; upgrade to real path segments if a user hits it.
func excludedFor(project string) string {
	name := folderName(project)
	for _, w := range []string{"-temp-", "-tmp-", "-var-folders-", "scratch"} {
		if strings.Contains(name, w) {
			return "probe or test folder: " + project
		}
	}
	return ""
}

func (im *importer) importFile(path, host string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Everything from reading the offset to storing the new one happens in one IMMEDIATE transaction (the
	// driver's default since v0.37.0), so two importers cannot both read the same bytes (concurrency hunt).
	tx, err := im.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := loadTranscriptRow(tx, host, path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := strings.HasSuffix(path, ".gz")
	restart := row.rewritten(f, st.Size(), gz)
	if !restart && st.Size() == row.from {
		return nil
	}
	from := row.from
	if restart {
		from = 0 // uuids keep the re-read idempotent
	}
	r, err := readerAt(f, path, from, gz)
	if err != nil {
		return err
	}
	bad, end, err := im.readFile(tx, host, r, path, from, row.id)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if gz {
		end = st.Size()
	}
	archived, archBytes := "", row.archBytes
	if end > from && im.archiveDir != "" {
		if archived, archBytes, err = im.archive(path, from, end, restart, archBytes); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("update source_file set size = ?, mtime = ?, read_to_byte = ?, tail_sha = ?, bad_lines = bad_lines + ?, archived_as = coalesce(nullif(?, ''), archived_as), archive_bytes = ?, last_error = null where id = ?",
		st.Size(), st.ModTime().UTC().Format(time.RFC3339), end, tailHash(f, end), bad, archived, archBytes, row.id); err != nil {
		return err
	}
	return tx.Commit()
}

// transcriptRow is what the store remembers of a transcript or prompt-log file.
type transcriptRow struct {
	id, from, archBytes int64
	tailSha             sql.NullString
}

func loadTranscriptRow(tx *sql.Tx, host, path string) (transcriptRow, error) {
	var row transcriptRow
	if _, err := tx.Exec("insert or ignore into source_file(host, path) values (?, ?)", host, path); err != nil {
		return row, err
	}
	err := tx.QueryRow("select id, read_to_byte, tail_sha, archive_bytes from source_file where path = ?", path).Scan(&row.id, &row.from, &row.tailSha, &row.archBytes)
	return row, err
}

// rewritten says the file must be read again from its start. Claude Code rewrites transcripts in place (compaction
// temp-file swap #81843, Remote Control resume #104123), so a file that did not shrink can still be a different
// file: the bytes before the offset are checked. A raw copy (.gz, `trace restore`) cannot be read from a byte offset
// inside its compressed stream: when its size changed it is read whole again; read_to_byte stores its compressed size.
func (row transcriptRow) rewritten(f *os.File, size int64, gz bool) bool {
	if size < row.from || (row.from > 0 && row.tailSha.Valid && tailHash(f, row.from) != row.tailSha.String) {
		return true
	}
	return gz && size != row.from
}

// readerAt reads f from the byte offset from; a raw copy is read through gzip, multistream since the archive
// appends one member per import.
func readerAt(f *os.File, path string, from int64, gz bool) (io.Reader, error) {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	if !gz {
		return f, nil
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cutTail{zr}, nil
}

// readFile reads a prompt log or a transcript, by host.
func (im *importer) readFile(tx *sql.Tx, host string, r io.Reader, path string, from, fileID int64) (bad int, end int64, err error) {
	if host == "prompt-log" {
		end, err = im.readHistory(tx, r, from, fileID)
		return 0, end, err
	}
	return im.readTranscript(tx, r, path, from, fileID)
}

// cutTail ends a raw copy at a member cut short (a run killed mid-archive, which the next archive run trims):
// what came before it is read, the half member is not.
type cutTail struct{ r io.Reader }

func (c cutTail) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return n, err
}

// tailHash hashes up to 4 KB ending at offset, enough to tell a rewritten file from the one read before.
func tailHash(f *os.File, offset int64) string {
	start := max(offset-4096, 0)
	buf := make([]byte, offset-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return ""
	}
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:])
}

var recordStart = regexp.MustCompile(`\{"(?:parentUuid|type)":`)

// records returns the JSON records in one line. Claude Code sometimes splices two records onto one line or
// cuts one short (#81843): decode one record at a time; after a failure, resume at the next record start.
// Starts are tried left to right, so an outer record is always tried before the blocks nested in it.
func records(line []byte) (out []row, bad int) {
	var r row
	if json.Unmarshal(line, &r) == nil {
		return []row{r}, 0
	}
	start := 0
	for {
		rest := bytes.TrimLeft(line[start:], " \t\r\n")
		if len(rest) == 0 {
			return out, bad
		}
		start = len(line) - len(rest)
		dec := json.NewDecoder(bytes.NewReader(rest))
		var r row
		if dec.Decode(&r) == nil {
			out = append(out, r)
			start += int(dec.InputOffset())
			continue
		}
		bad++
		next := recordStart.FindIndex(line[start+1:])
		if next == nil {
			return out, bad
		}
		start += 1 + next[0]
	}
}

// lines yields complete lines from r starting at offset; a last line without a newline is still being
// written and is left for the next run.
func lines(r io.Reader, offset int64, fn func(line []byte, at int64) error) (int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	pos := offset
	for {
		b, err := br.ReadBytes('\n')
		if err == io.EOF {
			return pos, nil
		}
		if err != nil {
			return pos, err
		}
		if err := fn(b, pos); err != nil {
			return pos, err
		}
		pos += int64(len(b))
	}
}

type sessionState struct {
	id         string
	seq        int
	compaction int
	known      bool
}

// transcriptRead is one pass over a Claude Code transcript: the session it belongs to and what the rows read so far
// say about it.
type transcriptRead struct {
	im                                       *importer
	tx                                       *sql.Tx
	sid, role, parent, excluded              string
	sub                                      bool
	from, fileID                             int64
	ss                                       sessionState
	toolNames                                map[string]string
	boundaries                               map[string]bool
	lastBranch, lastVersion, firstTs, lastTs string
	title, model                             string
	customTitle                              bool
}

func (im *importer) readTranscript(tx *sql.Tx, f io.Reader, path string, from, fileID int64) (int, int64, error) {
	t := &transcriptRead{im: im, tx: tx, from: from, fileID: fileID, toolNames: map[string]string{}, boundaries: map[string]bool{}}
	t.sid, t.role, t.parent, t.excluded = sessionFor(path)
	t.sub = t.role == "subagent"
	t.ss = sessionState{id: t.sid}
	tx.QueryRow("select coalesce(max(seq), 0) from event where session_id = ?", t.sid).Scan(&t.ss.seq)
	var comp sql.NullInt64
	if tx.QueryRow("select compactions from session where id = ?", t.sid).Scan(&comp) == nil {
		t.ss.known, t.ss.compaction = true, int(comp.Int64)
	}
	if t.sub && !t.ss.known {
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil {
			var m struct {
				AgentType string `json:"agentType"`
			}
			json.Unmarshal(b, &m)
			defer tx.Exec("update session set agent_type = ? where id = ?", m.AgentType, t.sid)
		}
	}
	if from == 0 {
		t.ss.compaction = 0 // a full re-read counts compactions again from the start
	}
	bad := 0
	end, err := lines(f, from, func(line []byte, at int64) error {
		rs, b := records(line)
		bad += b // unrecoverable pieces are counted; the raw copy still has their bytes
		for i := range rs {
			if err := t.row(&rs[i], at); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return bad, end, err
	}
	if err := t.finish(); err != nil {
		return bad, end, err
	}
	return bad, end, nil
}

// row reads one transcript row into the session and its events.
func (t *transcriptRead) row(r *row, at int64) error {
	t.note(r)
	if err := t.ensureSession(r); err != nil {
		return err
	}
	p := parseRow(r, t.sub)
	if err := t.names(r, p); err != nil {
		return err
	}
	// Compaction replays old rows with their uuids (#92089): count each boundary once.
	if p.compacted && !t.boundaries[r.UUID] && (t.from == 0 || !seen(t.tx, r.UUID+":0")) {
		t.boundaries[r.UUID] = true
		t.ss.compaction++
	}
	if r.UUID == "" || r.Timestamp == "" || !t.ss.known {
		return nil
	}
	return t.store(r, p.events, at)
}

// store inserts a row's events in order, each keyed by the row's uuid and its place in the row.
func (t *transcriptRead) store(r *row, events []ev, at int64) error {
	for i, e := range events {
		t.resolveTool(&e)
		t.ss.seq++
		if err := t.im.insertEvent(t.tx, fmt.Sprintf("%s:%d", r.UUID, i), t.sid, t.ss.seq, r.Timestamp, r.GitBranch, t.ss.compaction, t.fileID, at, e); err != nil {
			return err
		}
	}
	return nil
}

// note keeps what the session's record takes from its rows: first and last time, branch, version, model.
func (t *transcriptRead) note(r *row) {
	if r.Timestamp != "" {
		if t.firstTs == "" {
			t.firstTs = r.Timestamp
		}
		t.lastTs = r.Timestamp
	}
	if r.GitBranch != "" {
		t.lastBranch = r.GitBranch
	}
	if r.Version != "" {
		t.lastVersion = r.Version
	}
	if r.Message != nil && r.Message.Model != "" && r.Message.Model != "<synthetic>" {
		t.model = r.Message.Model
	}
}

// ensureSession stores the session at its first timed row, with the role its folder gives it.
func (t *transcriptRead) ensureSession(r *row) error {
	if t.ss.known || r.Timestamp == "" {
		return nil
	}
	wrole := t.role
	if !t.sub {
		wrole = roleFor(r.Cwd, t.role)
	}
	if strings.HasPrefix(r.Entrypoint, "sdk") && t.role != "subagent" {
		wrole = "worker"
	}
	if _, err := t.tx.Exec(`insert or ignore into session(id, host, role, parent_id, cwd, branch_first, branch_last, version_first, started_at, ended_at, excluded)
		values (?, 'claude-code', ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
		t.sid, wrole, t.parent, r.Cwd, r.GitBranch, r.GitBranch, r.Version, r.Timestamp, r.Timestamp, t.excluded); err != nil {
		return err
	}
	t.ss.known = true
	return nil
}

// names keeps the session's title (a /rename beats the harness's), every name it was given, and the rule files
// the harness loaded.
func (t *transcriptRead) names(r *row, p parsed) error {
	if p.title != "" && (p.custom || !t.customTitle) {
		t.title, t.customTitle = p.title, t.customTitle || p.custom
	}
	if p.custom && p.title != "" { // name rows carry no timestamp: the last timestamp seen dates them
		if _, err := t.tx.Exec("insert or ignore into session_name(session_id, name, first_seen) values (?, ?, nullif(?, ''))", t.sid, p.title, t.lastTs); err != nil {
			return err
		}
	}
	for sha, text := range p.loaded {
		if _, err := t.tx.Exec("insert or ignore into loaded_text(sha, first_seen, text) values (?, ?, ?)", sha, r.Timestamp, redacted(text)); err != nil {
			return err
		}
	}
	return nil
}

// resolveTool names the tool a result answers, from this read or the store; a relay tool's result is another
// agent's words, and a shell's result that is not an error exited 0.
func (t *transcriptRead) resolveTool(e *ev) {
	if e.kind == "tool_call" && e.toolUse != "" {
		t.toolNames[e.toolUse] = e.toolName
	}
	if e.kind != "tool_result" || e.toolUse == "" {
		return
	}
	e.toolName = t.toolNames[e.toolUse]
	if e.toolName == "" {
		t.tx.QueryRow("select tool_name from event where session_id = ? and tool_use_id = ? and kind = 'tool_call'", t.sid, e.toolUse).Scan(&e.toolName) // its own session's call (SPEC I5)
	}
	if relayTool(e.toolName) { // another agent's words, never this one's (Wu 2026)
		e.speaker, e.kind = "relay", "relay"
	}
	if e.exitCode == nil && shellSucceeded(e) {
		zero := 0
		e.exitCode = &zero
	}
}

// shellSucceeded says a shell tool's result is marked not an error: it exited 0.
func shellSucceeded(e *ev) bool {
	return e.isError != nil && !*e.isError && (e.toolName == "Bash" || e.toolName == "PowerShell")
}

// finish updates the session's record with what the rows said.
func (t *transcriptRead) finish() error {
	if !t.ss.known {
		return nil
	}
	_, err := t.tx.Exec(`update session set ended_at = max(coalesce(ended_at, ''), ?), branch_last = coalesce(nullif(?, ''), branch_last),
		version_first = coalesce(version_first, nullif(?, '')), model = coalesce(nullif(?, ''), model), compactions = ?,
		title = case when ? <> '' then ? else title end,
		started_at = min(coalesce(started_at, ?), coalesce(nullif(?, ''), started_at)) where id = ?`,
		t.lastTs, t.lastBranch, t.lastVersion, t.model, t.ss.compaction, t.title, t.title, t.firstTs, t.firstTs, t.sid)
	return err
}

func seen(tx *sql.Tx, uuid string) bool {
	var one int
	return tx.QueryRow("select 1 from event where uuid = ?", uuid).Scan(&one) == nil
}

// insertEvent stores one event with its anchors; false when the uuid was already there.
func (im *importer) insertEvent(tx *sql.Tx, uuid, sid string, seq int, ts, branch string, comp int, fileID, at int64, e ev) error {
	text, red := redact(e.text)
	input, red2 := redact(e.toolInput)
	red = kindsIn(red + "," + red2) // every kind removed, from the text and the input alike (SPEC P2)
	res, err := tx.Exec(`insert or ignore into event(uuid, session_id, seq, ts, speaker, kind, text, tool_name, tool_input, tool_use_id, is_error, exit_code, branch, compaction_no, loaded_sha, file_id, byte_offset, redacted)
		values (?, ?, ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), ?, nullif(?, ''), ?, ?, nullif(?, ''))`,
		uuid, sid, seq, ts, e.speaker, e.kind, text, e.toolName, input, e.toolUse, e.isError, e.exitCode, branch, comp, e.loadedSha, fileID, at, red)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	id, _ := res.LastInsertId()
	im.touched[sid] = true
	for _, a := range anchors(text, e.kind, e.toolName) {
		if _, err := tx.Exec("insert into anchor(event_id, type, value) values (?, ?, ?)", id, a.typ, a.value); err != nil {
			return err
		}
	}
	im.added++
	return nil
}

// archive appends the bytes just read to <archiveDir>/<project>/<file>.gz as a new gzip member (gunzip
// reads concatenated members as one stream) and returns the copy's new size. It first cuts the copy back
// to size, the length recorded with the last committed import, so a member left half-written by a killed
// run is dropped instead of breaking every later read.
func (im *importer) archive(path string, from, end int64, restart bool, size int64) (string, int64, error) {
	dst, err := archivePath(im.archiveDir, path)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	if restart {
		if err := keepReplaced(dst); err != nil {
			return "", 0, err
		}
	}
	if restart || from == 0 {
		size = 0
	}
	n, err := appendMember(dst, path, from, end, size)
	return dst, n, err
}

// archivePath is where path's raw copy goes: the same place under archiveDir as under the projects folder, a
// subagent's (<project>/<sid>/subagents/[workflows/<wf>/]agent-x.jsonl) beside its parent's, and ~/.claude/history.jsonl
// at .claude/history.jsonl.gz.
func archivePath(archiveDir, path string) (string, error) {
	projectsDir := filepath.Dir(filepath.Dir(path)) // <projects>/<project>/<sid>.jsonl
	for d := filepath.Dir(path); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if filepath.Base(d) == "subagents" {
			projectsDir = filepath.Dir(filepath.Dir(filepath.Dir(d)))
			break
		}
	}
	rel, err := filepath.Rel(projectsDir, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(archiveDir, rel+".gz"), nil
}

// keepReplaced moves the copy of a transcript that was rewritten (possibly losing history, #104123) beside the new
// one, named by when it was replaced.
func keepReplaced(dst string) error {
	if _, err := os.Stat(dst); err != nil {
		return nil
	}
	return os.Rename(dst, strings.TrimSuffix(dst, ".gz")+".replaced-"+time.Now().UTC().Format("20060102T150405")+".gz")
}

// appendMember cuts dst back to size, then appends path's bytes from from to end as one gzip member; it returns
// dst's new size.
func appendMember(dst, path string, from, end, size int64) (int64, error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	if err := out.Truncate(size); err != nil {
		return 0, err
	}
	if _, err := out.Seek(size, io.SeekStart); err != nil {
		return 0, err
	}
	src, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	src.Seek(from, io.SeekStart)
	zw := gzip.NewWriter(out)
	if _, err := io.CopyN(zw, src, end-from); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return out.Seek(0, io.SeekCurrent)
}

var pastedRe = regexp.MustCompile(`\[Pasted text #(\d+)[^\]]*\]`)

// readHistory imports ~/.claude/history.jsonl prompts whose session transcript no longer exists:
// the user's typed words from sessions Claude Code already deleted.
func (im *importer) readHistory(tx *sql.Tx, f io.Reader, from, fileID int64) (int64, error) {
	end, err := lines(f, from, func(line []byte, at int64) error {
		var h struct {
			Display        string `json:"display"`
			Timestamp      int64  `json:"timestamp"`
			Project        string `json:"project"`
			SessionID      string `json:"sessionId"`
			PastedContents map[string]struct {
				Content string `json:"content"`
			} `json:"pastedContents"`
		}
		if json.Unmarshal(line, &h) != nil || h.SessionID == "" || !inScope(h.Project, im.scope) {
			return nil
		}
		var host string
		if tx.QueryRow("select host from session where id = ?", h.SessionID).Scan(&host) == nil && host == "claude-code" {
			return nil // the transcript survives and already holds this prompt
		}
		text := pastedRe.ReplaceAllStringFunc(h.Display, func(m string) string {
			if pc, ok := h.PastedContents[pastedRe.FindStringSubmatch(m)[1]]; ok && pc.Content != "" {
				return pc.Content
			}
			return m
		})
		ts := time.UnixMilli(h.Timestamp).UTC().Format("2006-01-02T15:04:05.000Z")
		role := folderRole(folderName(h.Project))
		if _, err := tx.Exec(`insert or ignore into session(id, host, role, cwd, started_at, ended_at) values (?, 'prompt-log', ?, ?, ?, ?)`,
			h.SessionID, role, h.Project, ts, ts); err != nil {
			return err
		}
		tx.Exec("update session set ended_at = max(ended_at, ?), started_at = min(started_at, ?) where id = ?", ts, ts, h.SessionID)
		var seq int
		tx.QueryRow("select coalesce(max(seq), 0) + 1 from event where session_id = ?", h.SessionID).Scan(&seq)
		return im.insertEvent(tx, fmt.Sprintf("hist:%s:%d", h.SessionID, h.Timestamp), h.SessionID, seq, ts, "", 0, fileID, at,
			ev{speaker: "user", kind: "prompt", text: text})
	})
	return end, err
}

// activeTime recomputes wall time minus gaps over 5 minutes (chronos) for the sessions touched.
func activeTime(db *sql.DB, touched map[string]bool) error {
	for sid := range touched {
		if _, err := db.Exec(`update session set active_secs = (
			select coalesce(cast(sum(case when d <= 300 then d else 0 end) as integer), 0) from (
				select (julianday(ts) - julianday(lag(ts) over (order by ts))) * 86400 as d from event where session_id = ?))
			where id = ?`, sid, sid); err != nil {
			return err
		}
	}
	return nil
}
