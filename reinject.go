package main

import (
	"bufio"
	"bytes"
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

// Re-injection (queue Q50). A rule the agent loaded at the start stays in context but stops being attended to as
// context grows: in Gamage's raw data (arXiv 2604.20911) a rule falls under 50% compliance once the context is about
// 7 to 12 times its size. So TRACE re-injects a short card of the project's rules each time factor × the card's
// tokens of new context have entered, measured from the host's own token counts in the transcript. Opt-in: without
// "reinject" in config.json no hook is added for it. Claude Code and the Codex CLI/TUI (hooks mode); Codex Desktop
// runs no hooks.

type reinjectCfg struct {
	Factor float64 `json:"factor"` // new context, in card sizes, before the card comes again; 0: 7
	Budget int     `json:"budget"` // most characters of the card; 0: 8000, so it fits one hook output with room for checks
	// Alternate withholds the card on odd UTC days, a trial of whether it helps (SPEC K1).
	Alternate bool `json:"alternate"`
}

// limits are the card's factor and budget, with their defaults (7, 8000) for an absent, zero or negative setting.
func (r *reinjectCfg) limits() (factor float64, budget int) {
	factor, budget = 7, 8000
	if r != nil && r.Factor > 0 {
		factor = r.Factor
	}
	if r != nil && r.Budget > 0 {
		budget = r.Budget
	}
	return factor, budget
}

const rulesOpen, rulesClose = "<!-- trace:rules -->", "<!-- /trace:rules -->"

type source struct{ label, text string }

// loadedFile is one file of Claude Code's "instructions" record: what it loaded for the session, and as what
// (Project, Local, User, AutoMem...).
type loadedFile struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// claudeSources are the instruction files Claude Code says it loaded for the session whose transcript this is: the
// last "instructions" record in it (CLAUDE.md files, their imports, CLAUDE.local.md, auto memory's MEMORY.md), so
// they are what the host loaded and not a re-run of its discovery (SPEC G8). Each file is read again from disk, so a
// change shows; a file that is gone keeps the text recorded. Like codexSources, the store keeps how far the transcript
// was read and what it held; a transcript shorter than that was replaced and is read again from the start.
func claudeSources(db *sql.DB, transcript string) []source {
	var loaded []loadedFile
	if json.Unmarshal([]byte(lastRecord(db, "claude_instructions", "transcript", "files", transcript, claudeInstructions)), &loaded) != nil {
		return nil
	}
	var out []source
	for _, l := range loaded {
		text := l.Content
		if b, err := os.ReadFile(l.Path); err == nil {
			text = string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
		}
		out = append(out, source{filepath.ToSlash(l.Path), strings.ReplaceAll(text, "\r\n", "\n")})
	}
	return out
}

// claudeInstructions returns the files of one transcript line, as stored JSON, when it is an instructions record.
func claudeInstructions(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"instructions"`)) {
		return "", false
	}
	var rec struct {
		Attachment struct {
			Type  string       `json:"type"`
			Files []loadedFile `json:"files"`
		} `json:"attachment"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Attachment.Type != "instructions" || len(rec.Attachment.Files) == 0 {
		return "", false
	}
	for i := range rec.Attachment.Files { // stored redacted (SPEC P1); the card reads the file again from disk
		rec.Attachment.Files[i].Content = redacted(rec.Attachment.Files[i].Content)
	}
	b, _ := json.Marshal(rec.Attachment.Files)
	return string(b), true
}

// lastRecord is the last line of path that match recognizes, as match returns it. The store keeps, in table, how
// far path was read and what that line held, so each call reads only what the host appended since; a file shorter
// than that offset was replaced and is read again from the start. Without a writable store (db nil or read-only) the
// whole file is read. A last line without its newline is still being written: it is read next time.
func lastRecord(db *sql.DB, table, key, val, path string, match func(line []byte) (string, bool)) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	var off int64
	last := ""
	if db != nil {
		db.QueryRow("select offset, "+val+" from "+table+" where "+key+" = ?", path).Scan(&off, &last)
	}
	if off > st.Size() {
		off, last = 0, ""
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	read, last := matchLines(f, off, last, match)
	if db != nil && read != off {
		db.Exec("insert into "+table+"("+key+", offset, "+val+") values (?, ?, ?) on conflict("+key+") do update set offset = excluded.offset, "+val+" = excluded."+val, path, read, last)
	}
	return last
}

// matchLines reads the whole lines of r, which starts at offset off of its file, to the end: it returns the offset
// after the last whole line and the last value match recognized (last when none did).
func matchLines(r io.Reader, off int64, last string, match func(line []byte) (string, bool)) (int64, string) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return off, last
		}
		off += int64(len(line))
		if v, ok := match(line); ok {
			last = v
		}
	}
}

// ruleBlocks returns the text between rulesOpen and rulesClose markers, each block in full.
func ruleBlocks(text string) string {
	var blocks []string
	for rest := text; ; {
		i := strings.Index(rest, rulesOpen)
		if i < 0 {
			break
		}
		rest = rest[i+len(rulesOpen):]
		j := strings.Index(rest, rulesClose)
		if j < 0 {
			j = len(rest)
		}
		if b := strings.TrimSpace(rest[:j]); b != "" { // an empty block adds nothing, not even blank lines
			blocks = append(blocks, b)
		}
		rest = rest[j:]
	}
	return strings.Join(blocks, "\n\n")
}

// card is the text re-injected. All the instruction files when they fit the budget; otherwise only their marked
// blocks (a file with none adds nothing, and note says so). Then the rules the user set in conversation, verbatim
// (refusals), and one line on TRACE itself. Code only: nothing is reworded (rewording rule pages lost 34
// qualifiers on 2026-10-30).
func card(srcs []source, rules string, budget int) (text, note string) {
	files, note := cardFiles(srcs, budget)
	body := rules + files // the user's rules first, so the budget cuts the files (SPEC D4)
	if body == "" {
		return "", note
	}
	if len(body) > budget {
		body = clip(body, budget)
		note = strings.TrimPrefix(note+"; the card was cut at the budget", "; ")
	}
	return "## Rules in force (re-injected by trace as the session grows; the files and the user's words are the source)\n\n" + body +
		fmt.Sprintf("\nEverything said before: %s. Search before saying you cannot confirm something; if nothing comes up, search again in the other language and with synonyms.\n", traceCmd("search", " <words>")), note
}

// cardFiles is the card's part from the instruction files: each file whole when all of them fit the budget, else only
// its marked blocks, with a note that says so and names the files that have none.
func cardFiles(srcs []source, budget int) (text, note string) {
	total := 0
	for _, s := range srcs {
		total += len(s.text)
	}
	whole := total <= budget
	var b strings.Builder
	var unmarked []string
	for _, s := range srcs {
		t := strings.TrimSpace(s.text)
		if !whole {
			if t = ruleBlocks(s.text); t == "" {
				unmarked = append(unmarked, s.label)
				continue
			}
		}
		if t != "" {
			fmt.Fprintf(&b, "### From %s\n%s\n\n", s.label, t)
		}
	}
	if whole {
		return b.String(), ""
	}
	note = fmt.Sprintf("the instruction files hold %d characters, over the card's %d, so only text between %s and %s goes in", total, budget, rulesOpen, rulesClose)
	if len(unmarked) > 0 {
		note += "; no marked rules in: " + strings.Join(unmarked, ", ")
	}
	return b.String(), note
}

// codexInstructionsRe is the message a Codex rollout records with the AGENTS.md text it loaded (global and project,
// after its trust check, overrides, fallback names and byte budget): "# AGENTS.md instructions for <cwd>", then the
// text between <INSTRUCTIONS> and </INSTRUCTIONS>.
var codexInstructionsRe = regexp.MustCompile(`(?s)\A# AGENTS\.md instructions for [^\n]*\n\n<INSTRUCTIONS>\n?(.*?)\n?</INSTRUCTIONS>\s*\z`)

// codexSources are the instructions Codex loaded for the session whose rollout this is, read from the rollout, so they
// are what Codex showed the model and not a re-run of its discovery (agents_md.rs), which depends on its trust state
// and config layers. The last such message in the whole rollout wins: Codex records the instructions again when they
// change or after a compaction. The store keeps how far the rollout was read and what it held, so each call reads
// only what Codex appended since; a rollout shorter than that offset was replaced and is read again from the start.
// Without a writable store (db nil or read-only) the whole rollout is read.
func codexSources(db *sql.DB, rollout string) []source {
	text := lastRecord(db, "codex_instructions", "rollout", "text", rollout, func(line []byte) (string, bool) {
		t, ok := codexInstructions(line)
		return redacted(t), ok // stored and shown redacted (SPEC P1)
	})
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []source{{"AGENTS.md (as Codex loaded it)", text}}
}

// codexInstructions returns the instructions text of one rollout line, when it is the AGENTS.md message.
func codexInstructions(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte("AGENTS.md instructions for")) {
		return "", false
	}
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Type, Role string
			Content    []struct{ Text string }
		} `json:"payload"`
	}
	if json.Unmarshal(line, &r) != nil || r.Type != "response_item" || r.Payload.Type != "message" || r.Payload.Role != "user" {
		return "", false
	}
	for _, c := range r.Payload.Content {
		if m := codexInstructionsRe.FindStringSubmatch(c.Text); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// fileTail is the last 512 KB of a file, split into lines (the first may be cut).
func fileTail(path string) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, _ := f.Stat()
	f.Seek(max(st.Size()-512<<10, 0), io.SeekStart) // a failed seek reads the whole file, which holds the same last lines
	b, _ := io.ReadAll(f)
	return bytes.Split(b, []byte("\n"))
}

// contextTokens is the context size of the last model call in a transcript, read from the file's tail. It lags at
// least one step: the tool result just returned is not in it yet, which a threshold tolerates.
// Claude Code: the call's input, cached or not. Codex: last_token_usage.total_tokens of the last token_count event,
// what Codex itself counts as in the window (tokens_in_context_window, protocol.rs); total_token_usage is the sum over
// the session, and after a compaction last_token_usage is Codex's own estimate of what is left, so the drop resets
// the baseline (reinjection). A token_count with no info (rate limits only) is skipped.
func contextTokens(transcript, host string) int {
	lines := fileTail(transcript)
	if host == "codex" {
		return codexContextTokens(lines)
	}
	return claudeContextTokens(lines)
}

// codexContextTokens is last_token_usage.total_tokens of the last token_count event with one, 0 with none.
func codexContextTokens(lines [][]byte) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"token_count"`)) {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Info *struct {
					Last struct {
						Total int `json:"total_tokens"`
					} `json:"last_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(lines[i], &e) == nil && e.Type == "event_msg" && e.Payload.Type == "token_count" && e.Payload.Info != nil && e.Payload.Info.Last.Total > 0 {
			return e.Payload.Info.Last.Total
		}
	}
	return 0
}

// claudeContextTokens is the input of the last assistant call with a usage, cached or not, 0 with none.
func claudeContextTokens(lines [][]byte) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"usage"`)) {
			continue
		}
		var u struct {
			Type    string `json:"type"`
			Message struct {
				Usage struct {
					In     int `json:"input_tokens"`
					Read   int `json:"cache_read_input_tokens"`
					Create int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &u) != nil || u.Type != "assistant" {
			continue
		}
		return u.Message.Usage.In + u.Message.Usage.Read + u.Message.Usage.Create
	}
	return 0
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:8]) }

// reinjection decides, for one hook call, what to inject: the card when factor × its tokens of new context entered
// since the last injection (or since the session's first call), only its new lines when it changed, nothing
// otherwise. A smaller context than at the last record means a compaction or a clear: the baseline restarts there.
// It records each decision except "nothing", so the common call writes nothing.
func reinjection(db *sql.DB, sid string, ctx int, text string, factor float64, now time.Time) (out, reason string, err error) {
	if text == "" || ctx == 0 || sid == "" {
		return "", "", nil
	}
	// Redacted before anything: the card is stored, and the stored and the new text must compare alike (SPEC P1).
	text = redacted(text)
	// Parallel tool calls fire their hooks at once: the read and the record are one write transaction, so only one
	// of them injects. openDB keeps a single connection, so these statements all run on it.
	if _, err := db.Exec("begin immediate"); err != nil {
		return "", "", err
	}
	defer func() {
		if err == nil {
			_, err = db.Exec("commit")
		} else {
			db.Exec("rollback")
		}
	}()
	if out, reason, err = injectionDue(db, sid, ctx, text, factor); err != nil || reason == "" {
		return "", "", err
	}
	if withheld(now) {
		out, reason = "", "withheld-"+reason
	}
	return out, reason, recordInjection(db, sid, ctx, text, out, reason, now)
}

// withheld says whether the card is held back on this day of an alternate-days trial (SPEC K1): odd days since
// 1970-01-01 in UTC.
func withheld(now time.Time) bool {
	return cfg.Reinject != nil && cfg.Reinject.Alternate && now.Unix()/86400%2 == 1
}

// injectionDue decides against the session's last record: "start" (none yet: the host loaded the files itself, this
// is the baseline), "reset" (a smaller context), "change" (the card changed: only its new lines), "threshold" (enough
// new context: the whole card), or "" for nothing.
func injectionDue(db *sql.DB, sid string, ctx int, text string, factor float64) (out, reason string, err error) {
	var lastCtx int
	var lastSha string
	switch err := db.QueryRow("select ctx_tokens, card_sha from injection where session_id = ? order by id desc limit 1", sid).Scan(&lastCtx, &lastSha); {
	case err == sql.ErrNoRows:
		return "", "start", nil
	case err != nil:
		return "", "", err
	case ctx < lastCtx:
		return "", "reset", nil
	case lastSha != sha(text):
		var old string
		db.QueryRow("select text from card where sha = ?", lastSha).Scan(&old)
		if out = cardChange(old, text); out != "" {
			out = "## Rules changed since the last time trace showed them (new or changed lines only)\n" + out
		}
		return out, "change", nil
	case float64(ctx-lastCtx) >= factor*float64(len(text))/4: // about 4 characters per token
		return text, "threshold", nil
	}
	return "", "", nil
}

// recordInjection keeps the card's text by its hash and the decision, so the next call compares against it.
func recordInjection(db *sql.DB, sid string, ctx int, text, out, reason string, now time.Time) error {
	if _, err := db.Exec("insert or ignore into card(sha, first_seen, text) values (?, ?, ?)", sha(text), now.UTC().Format(time.RFC3339), text); err != nil {
		return err
	}
	_, err := db.Exec("insert into injection(session_id, ts, ctx_tokens, reason, card_sha, chars) values (?, ?, ?, ?, ?, ?)",
		sid, now.UTC().Format(time.RFC3339), ctx, reason, sha(text), len(out))
	return err
}

// cardChange is the card's new lines, then "out of force: #id" for each recorded rule that left it (SPEC D5).
func cardChange(old, cur string) string {
	out := newLines(old, cur)
	in := map[string]bool{}
	for _, m := range recordLine.FindAllStringSubmatch(cur, -1) {
		in[m[1]] = true
	}
	for _, m := range recordLine.FindAllStringSubmatch(old, -1) {
		if !in[m[1]] {
			out = strings.TrimPrefix(out+"\nout of force: #"+m[1], "\n")
		}
	}
	return out
}

// recordLine matches a recorded rule's line in a card and takes its id.
var recordLine = regexp.MustCompile(`(?m)^- .*?#(\d+) (?:rule|decision): `)

func newLines(old, cur string) string {
	have := map[string]bool{}
	for _, l := range strings.Split(old, "\n") {
		have[l] = true
	}
	var add []string
	for _, l := range strings.Split(cur, "\n") {
		if strings.TrimSpace(l) != "" && !have[l] {
			add = append(add, l)
		}
	}
	return strings.Join(add, "\n")
}

// sessionCard builds the card for a session of host in cwd (transcript is its transcript or rollout), with the user's
// rules from this host's sessions in the store when it is readable.
func sessionCard(db *sql.DB, cwd, transcript, role, host string) (string, string) {
	var rules strings.Builder
	if db != nil {
		if recorded, _, _ := ruleSection(db, &rules, userName(), role, cwd); !recorded {
			refusals(db, &rules, userName(), host, role, cwd)
		}
	}
	if host == "codex" {
		return card(codexSources(db, transcript), rules.String(), reinjectBudget())
	}
	return card(claudeSources(db, transcript), rules.String(), reinjectBudget())
}

func reinjectFactor() float64 { f, _ := cfg.Reinject.limits(); return f }
func reinjectBudget() int     { _, b := cfg.Reinject.limits(); return b }
