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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type importer struct {
	db         *sql.DB
	archiveDir string // raw copies, one .gz per transcript, appended as new gzip members
	repo       string // git repository used to verify commit ids
	scope      string // only history.jsonl prompts whose project folder contains this
	shaCache   map[string]string
	git        *exec.Cmd // one `git cat-file --batch-check` per run, fed one candidate per line
	gitIn      io.WriteCloser
	gitOut     *bufio.Reader
	added      int
	touched    map[string]bool // sessions that got events this run
	unmapped   map[string]int  // row kinds no rule stores, counted so a new format shows up
}

// transcriptFiles lists every session and subagent transcript in scope: project folders whose name
// contains scope.
// It also returns the files under <session>/tool-results, relative to projectsDir: the full output the
// transcript keeps only as a 2 KB preview, which Claude Code's cleanup deletes with the session.
func transcriptFiles(projectsDir, scope string) (out, results []string, err error) {
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
			case !strings.HasSuffix(p, ".jsonl"):
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
func sessionFor(path string) (id, role, parent, excluded string) {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
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

// excludedFor keeps probe and test folders out of recall (mnemos polluted its own store with test runs).
func excludedFor(project string) string {
	if strings.Contains(project, "-Temp-") || strings.Contains(project, "scratch") {
		return "probe or test folder: " + project
	}
	return ""
}

func (im *importer) importFile(path, host string) (int, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	// Everything from reading the offset to storing the new one happens in one IMMEDIATE transaction (the
	// driver's default since v0.37.0), so two importers cannot both read the same bytes (concurrency hunt).
	tx, err := im.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var fileID, from, archBytes int64
	var tailSha sql.NullString
	if _, err := tx.Exec("insert or ignore into source_file(host, path) values (?, ?)", host, path); err != nil {
		return 0, err
	}
	if err := tx.QueryRow("select id, read_to_byte, tail_sha, archive_bytes from source_file where path = ?", path).Scan(&fileID, &from, &tailSha, &archBytes); err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// Claude Code rewrites transcripts in place (compaction temp-file swap #81843, Remote Control resume
	// #104123), so a file that did not shrink can still be a different file: check the bytes before the offset.
	restart := st.Size() < from || (from > 0 && tailSha.Valid && tailHash(f, from) != tailSha.String)
	if !restart && st.Size() == from {
		return 0, nil
	}
	if restart {
		from = 0 // uuids keep the re-read idempotent
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return 0, err
	}
	var n, bad int
	var end int64
	var archived string
	if host == "prompt-log" {
		n, end, err = im.readHistory(tx, f, from, fileID)
	} else {
		n, bad, end, err = im.readTranscript(tx, f, path, from, fileID)
	}
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if end > from && im.archiveDir != "" {
		if archived, archBytes, err = im.archive(path, from, end, restart, archBytes); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec("update source_file set size = ?, mtime = ?, read_to_byte = ?, tail_sha = ?, bad_lines = bad_lines + ?, archived_as = coalesce(nullif(?, ''), archived_as), archive_bytes = ?, last_error = null where id = ?",
		st.Size(), st.ModTime().UTC().Format(time.RFC3339), end, tailHash(f, end), bad, archived, archBytes, fileID); err != nil {
		return 0, err
	}
	return n, tx.Commit()
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

func (im *importer) readTranscript(tx *sql.Tx, f io.Reader, path string, from, fileID int64) (int, int, int64, error) {
	sid, role, parent, excluded := sessionFor(path)
	sub := role == "subagent"
	ss := sessionState{id: sid}
	tx.QueryRow("select coalesce(max(seq), 0) from event where session_id = ?", sid).Scan(&ss.seq)
	var comp sql.NullInt64
	if tx.QueryRow("select compactions from session where id = ?", sid).Scan(&comp) == nil {
		ss.known, ss.compaction = true, int(comp.Int64)
	}
	if sub && !ss.known {
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil {
			var m struct {
				AgentType string `json:"agentType"`
			}
			json.Unmarshal(b, &m)
			defer tx.Exec("update session set agent_type = ? where id = ?", m.AgentType, sid)
		}
	}
	if from == 0 {
		ss.compaction = 0 // a full re-read counts compactions again from the start
	}
	toolNames := map[string]string{}
	var lastBranch, lastVersion, firstTs, lastTs, title, model string
	customTitle := false
	added, bad := 0, 0
	boundaries := map[string]bool{}
	one := func(r *row, at int64) error {
		if r.Timestamp != "" {
			if firstTs == "" {
				firstTs = r.Timestamp
			}
			lastTs = r.Timestamp
		}
		if r.GitBranch != "" {
			lastBranch = r.GitBranch
		}
		if r.Version != "" {
			lastVersion = r.Version
		}
		if r.Message != nil && r.Message.Model != "" && r.Message.Model != "<synthetic>" {
			model = r.Message.Model
		}
		if !ss.known && r.Timestamp != "" {
			wrole := role
			if !sub {
				wrole = roleFor(r.Cwd, role)
			}
			if strings.HasPrefix(r.Entrypoint, "sdk") && role != "subagent" {
				wrole = "worker"
			}
			if _, err := tx.Exec(`insert or ignore into session(id, host, role, parent_id, cwd, branch_first, branch_last, version_first, started_at, ended_at, excluded)
				values (?, 'claude-code', ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, nullif(?, ''))`,
				sid, wrole, parent, r.Cwd, r.GitBranch, r.GitBranch, r.Version, r.Timestamp, r.Timestamp, excluded); err != nil {
				return err
			}
			ss.known = true
		}
		p := parseRow(r, sub)
		if p.title != "" && (p.custom || !customTitle) {
			title, customTitle = p.title, customTitle || p.custom
		}
		if p.custom && p.title != "" { // name rows carry no timestamp: the last timestamp seen dates them
			if _, err := tx.Exec("insert or ignore into session_name(session_id, name, first_seen) values (?, ?, nullif(?, ''))", sid, p.title, lastTs); err != nil {
				return err
			}
		}
		for sha, text := range p.loaded {
			if _, err := tx.Exec("insert or ignore into loaded_text(sha, first_seen, text) values (?, ?, ?)", sha, r.Timestamp, text); err != nil {
				return err
			}
		}
		// Compaction replays old rows with their uuids (#92089): count each boundary once.
		if p.compacted && !boundaries[r.UUID] && (from == 0 || !seen(tx, r.UUID+":0")) {
			boundaries[r.UUID] = true
			ss.compaction++
		}
		if r.UUID == "" || r.Timestamp == "" || !ss.known {
			return nil
		}
		for i, e := range p.events {
			if e.kind == "tool_call" && e.toolUse != "" {
				toolNames[e.toolUse] = e.toolName
			}
			if e.kind == "tool_result" && e.toolUse != "" {
				e.toolName = toolNames[e.toolUse]
				if e.toolName == "" {
					tx.QueryRow("select tool_name from event where tool_use_id = ? and kind = 'tool_call'", e.toolUse).Scan(&e.toolName)
				}
				if relayTool(e.toolName) { // another agent's words, never this one's (Wu 2026)
					e.speaker, e.kind = "relay", "relay"
				}
				if e.exitCode == nil && e.isError != nil && !*e.isError && (e.toolName == "Bash" || e.toolName == "PowerShell") {
					zero := 0
					e.exitCode = &zero
				}
			}
			ss.seq++
			ok, err := im.insertEvent(tx, fmt.Sprintf("%s:%d", r.UUID, i), sid, ss.seq, r.Timestamp, r.GitBranch, ss.compaction, fileID, at, e)
			if err != nil {
				return err
			}
			if ok {
				added++
			}
		}
		return nil
	}
	end, err := lines(f, from, func(line []byte, at int64) error {
		rs, b := records(line)
		bad += b // unrecoverable pieces are counted; the raw copy still has their bytes
		for i := range rs {
			if err := one(&rs[i], at); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, bad, end, err
	}
	if ss.known {
		if _, err := tx.Exec(`update session set ended_at = max(coalesce(ended_at, ''), ?), branch_last = coalesce(nullif(?, ''), branch_last),
			version_first = coalesce(version_first, nullif(?, '')), model = coalesce(nullif(?, ''), model), compactions = ?,
			title = case when ? <> '' then ? else title end,
			started_at = min(coalesce(started_at, ?), coalesce(nullif(?, ''), started_at)) where id = ?`,
			lastTs, lastBranch, lastVersion, model, ss.compaction, title, title, firstTs, firstTs, sid); err != nil {
			return 0, bad, end, err
		}
	}
	return added, bad, end, nil
}

func seen(tx *sql.Tx, uuid string) bool {
	var one int
	return tx.QueryRow("select 1 from event where uuid = ?", uuid).Scan(&one) == nil
}

// insertEvent stores one event with its anchors; false when the uuid was already there.
func (im *importer) insertEvent(tx *sql.Tx, uuid, sid string, seq int, ts, branch string, comp int, fileID, at int64, e ev) (bool, error) {
	text, red := redact(e.text)
	input, red2 := redact(e.toolInput)
	if red2 != "" && red == "" {
		red = red2
	}
	res, err := tx.Exec(`insert or ignore into event(uuid, session_id, seq, ts, speaker, kind, text, tool_name, tool_input, tool_use_id, is_error, exit_code, branch, compaction_no, loaded_sha, file_id, byte_offset, redacted)
		values (?, ?, ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), ?, nullif(?, ''), ?, ?, nullif(?, ''))`,
		uuid, sid, seq, ts, e.speaker, e.kind, text, e.toolName, input, e.toolUse, e.isError, e.exitCode, branch, comp, e.loadedSha, fileID, at, red)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	id, _ := res.LastInsertId()
	im.touched[sid] = true
	for _, a := range anchors(text, e.kind, e.toolName) {
		typ, val, verified := a.typ, a.value, sql.NullInt64{}
		if typ == "sha?" {
			full := im.commit(val)
			if full == "" {
				continue
			}
			typ, val, verified = "commit", full, sql.NullInt64{Int64: 1, Valid: true}
		}
		if _, err := tx.Exec("insert into anchor(event_id, type, value, verified) values (?, ?, ?, ?)", id, typ, val, verified); err != nil {
			return false, err
		}
	}
	im.added++
	return true, nil
}

var fullShaRe = regexp.MustCompile(`^([0-9a-f]{40}) commit`)

// commit returns the full id when git knows the candidate as a commit, "" otherwise. One git process serves the
// whole run: a process per candidate made imports take 50-110 s once agents read repos full of hashes (2026-11-10).
func (im *importer) commit(c string) string {
	if v, ok := im.shaCache[c]; ok {
		return v
	}
	v := ""
	if im.repo != "" && im.startGit() {
		fmt.Fprintln(im.gitIn, c)
		line, err := im.gitOut.ReadString('\n')
		if err != nil { // git died: stop asking it this run
			im.closeGit()
			im.repo = ""
		} else if m := fullShaRe.FindStringSubmatch(line); m != nil {
			v = m[1]
		}
	}
	im.shaCache[c] = v
	return v
}

func (im *importer) startGit() bool {
	if im.git != nil {
		return true
	}
	cmd := exec.Command("git", "-C", im.repo, "cat-file", "--batch-check")
	in, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	out, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return false
	}
	im.git, im.gitIn, im.gitOut = cmd, in, bufio.NewReader(out)
	return true
}

func (im *importer) closeGit() {
	if im.git != nil {
		im.gitIn.Close()
		im.git.Wait()
		im.git = nil
	}
}

// archive appends the bytes just read to <archiveDir>/<project>/<file>.gz as a new gzip member (gunzip
// reads concatenated members as one stream) and returns the copy's new size. It first cuts the copy back
// to size, the length recorded with the last committed import, so a member left half-written by a killed
// run is dropped instead of breaking every later read.
func (im *importer) archive(path string, from, end int64, restart bool, size int64) (string, int64, error) {
	projectsDir := filepath.Dir(filepath.Dir(path)) // <projects>/<project>/<sid>.jsonl
	for d := filepath.Dir(path); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if filepath.Base(d) == "subagents" { // <projects>/<project>/<sid>/subagents/[workflows/<wf>/]agent-x.jsonl
			projectsDir = filepath.Dir(filepath.Dir(filepath.Dir(d)))
			break
		}
	}
	rel, err := filepath.Rel(projectsDir, path)
	if err != nil {
		return "", 0, err
	}
	dst := filepath.Join(im.archiveDir, rel+".gz")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	if restart {
		// The transcript was rewritten (possibly losing history, #104123): keep the old copy beside the new one.
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, strings.TrimSuffix(dst, ".gz")+".replaced-"+time.Now().UTC().Format("20060102T150405")+".gz"); err != nil {
				return "", 0, err
			}
		}
	}
	if restart || from == 0 {
		size = 0
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()
	if err := out.Truncate(size); err != nil {
		return "", 0, err
	}
	if _, err := out.Seek(size, io.SeekStart); err != nil {
		return "", 0, err
	}
	src, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	src.Seek(from, io.SeekStart)
	zw := gzip.NewWriter(out)
	if _, err := io.CopyN(zw, src, end-from); err != nil {
		return "", 0, err
	}
	if err := zw.Close(); err != nil {
		return "", 0, err
	}
	n, err := out.Seek(0, io.SeekCurrent)
	return dst, n, err
}

var pastedRe = regexp.MustCompile(`\[Pasted text #(\d+)[^\]]*\]`)

// readHistory imports ~/.claude/history.jsonl prompts whose session transcript no longer exists:
// the user's typed words from sessions Claude Code already deleted.
func (im *importer) readHistory(tx *sql.Tx, f io.Reader, from, fileID int64) (int, int64, error) {
	added := 0
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
		ok, err := im.insertEvent(tx, fmt.Sprintf("hist:%s:%d", h.SessionID, h.Timestamp), h.SessionID, seq, ts, "", 0, fileID, at,
			ev{speaker: "user", kind: "prompt", text: text})
		if ok {
			added++
		}
		return err
	})
	return added, end, err
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
