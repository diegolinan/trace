package main

import (
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
// "reinject" in config.json no hook is added for it. Claude Code only for now; Codex Desktop runs no hooks.

type reinjectCfg struct {
	Factor float64 `json:"factor"` // new context, in card sizes, before the card comes again; 0: 7
	Budget int     `json:"budget"` // most characters of the card; 0: 8000, so it fits one hook output with room for checks
}

func (r *reinjectCfg) factor() float64 {
	if r == nil || r.Factor <= 0 {
		return 7
	}
	return r.Factor
}

func (r *reinjectCfg) budget() int {
	if r == nil || r.Budget <= 0 {
		return 8000
	}
	return r.Budget
}

const rulesOpen, rulesClose = "<!-- trace:rules -->", "<!-- /trace:rules -->"

type source struct{ label, text string }

// gitRoots walks up from dir to the checkout root and the main repository's root (they differ in a linked worktree).
// No git process: this runs on every tool call.
func gitRoots(dir string) (root, main string) {
	for d := dir; ; {
		if st, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if st.IsDir() {
				return d, d
			}
			b, _ := os.ReadFile(filepath.Join(d, ".git"))
			// "gitdir: <main>/.git/worktrees/<name>"
			if g := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:")); strings.Contains(filepath.ToSlash(g), "/.git/worktrees/") {
				return d, filepath.FromSlash(strings.SplitN(filepath.ToSlash(g), "/.git/worktrees/", 2)[0])
			}
			return d, d
		}
		up := filepath.Dir(d)
		if up == d {
			return dir, dir
		}
		d = up
	}
}

// importRe finds Claude Code's "@path" imports: at the start of a line or after a space, outside code.
var importRe = regexp.MustCompile(`(?m)(?:^|\s)@(~?[\w./\\:-]+)`)

// withImports returns the file and every file it imports, depth first, as Claude Code loads them (at most 5 deep).
func withImports(path, home string, depth int, seen map[string]bool, out *[]source, label func(string) string) {
	abs, _ := filepath.Abs(path)
	if depth > 5 || seen[strings.ToLower(abs)] {
		return
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return
	}
	seen[strings.ToLower(abs)] = true
	text := strings.ReplaceAll(string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))), "\r\n", "\n")
	*out = append(*out, source{label(abs), text})
	inCode := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		for _, m := range importRe.FindAllStringSubmatch(strings.ReplaceAll(line, "`", " ` "), -1) {
			p := m[1]
			if strings.HasPrefix(p, "~") {
				p = filepath.Join(home, p[1:])
			} else if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(abs), p)
			}
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				withImports(p, home, depth+1, seen, out, label)
			}
		}
	}
}

// claudeSources are the instruction files Claude Code loads for a session in cwd: the user's CLAUDE.md, the
// checkout's CLAUDE.md, .claude/CLAUDE.md and CLAUDE.local.md with their imports, and auto memory's MEMORY.md.
func claudeSources(cwd, home string) []source {
	root, main := gitRoots(cwd)
	label := func(p string) string {
		if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
		return filepath.ToSlash(p)
	}
	var out []source
	seen := map[string]bool{}
	for _, p := range []string{filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(root, "CLAUDE.md"),
		filepath.Join(root, ".claude", "CLAUDE.md"), filepath.Join(root, "CLAUDE.local.md")} {
		withImports(p, home, 0, seen, &out, label)
	}
	if mem := memoryFile(root, main, home); mem != "" {
		withImports(mem, home, 5, seen, &out, label) // MEMORY.md is an index: its links are not imports
	}
	return out
}

// memoryFile is auto memory's MEMORY.md: autoMemoryDirectory from the checkout's local settings, its shared
// settings or the user's (in that order), else the folder Claude Code derives from the main repository's path.
func memoryFile(root, main, home string) string {
	enabled, dir := true, ""
	for _, p := range []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".claude", "settings.local.json")} {
		var s struct {
			Enabled *bool  `json:"autoMemoryEnabled"`
			Dir     string `json:"autoMemoryDirectory"`
		}
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &s) == nil {
			if s.Enabled != nil {
				enabled = *s.Enabled
			}
			if s.Dir != "" {
				dir = s.Dir
			}
		}
	}
	if !enabled {
		return ""
	}
	if dir == "" {
		dir = filepath.Join(home, ".claude", "projects", notAlnum.ReplaceAllString(main, "-"), "memory")
	}
	return filepath.Join(dir, "MEMORY.md")
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
		blocks = append(blocks, strings.TrimSpace(rest[:j]))
		rest = rest[j:]
	}
	return strings.Join(blocks, "\n\n")
}

// card is the text re-injected. All the instruction files when they fit the budget; otherwise only their marked
// blocks (a file with none adds nothing, and note says so). Then the rules the user set in conversation, verbatim
// (refusals), and one line on TRACE itself. Code only: nothing is reworded (rewording rule pages lost 34
// qualifiers on 2026-10-30).
func card(srcs []source, rules string, budget int) (text, note string) {
	var b strings.Builder
	total := 0
	for _, s := range srcs {
		total += len(s.text)
	}
	whole := total <= budget
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
	if !whole {
		note = fmt.Sprintf("the instruction files hold %d characters, over the card's %d, so only text between %s and %s goes in", total, budget, rulesOpen, rulesClose)
		if len(unmarked) > 0 {
			note += "; no marked rules in: " + strings.Join(unmarked, ", ")
		}
	}
	b.WriteString(rules)
	if b.Len() == 0 {
		return "", note
	}
	body := b.String()
	if len(body) > budget {
		body = clip(body, budget)
		note = strings.TrimPrefix(note+"; the card was cut at the budget", "; ")
	}
	exe, _ := os.Executable()
	return "## Rules in force (re-injected by trace as the session grows; the files and the user's words are the source)\n\n" + body +
		fmt.Sprintf("\nEverything said before: `%s search <words>`. Search before saying you cannot confirm something.\n", exe), note
}

// contextTokens is the context size of the last model call in a Claude Code transcript (its input, cached or not),
// read from the file's tail. It lags one step: the tool result just returned is not in it yet.
func contextTokens(transcript string) int {
	f, err := os.Open(transcript)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, _ := f.Stat()
	off := st.Size() - 512<<10
	if off < 0 {
		off = 0
	}
	b, _ := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	lines := bytes.Split(b, []byte("\n"))
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
	var lastCtx int
	var lastSha string
	switch err := db.QueryRow("select ctx_tokens, card_sha from injection where session_id = ? order by id desc limit 1", sid).Scan(&lastCtx, &lastSha); {
	case err == sql.ErrNoRows:
		reason = "start" // the host loaded the files itself; this is the baseline
	case err != nil:
		return "", "", err
	case ctx < lastCtx:
		reason = "reset"
	case lastSha != sha(text):
		var old string
		db.QueryRow("select text from card where sha = ?", lastSha).Scan(&old)
		if out = newLines(old, text); out != "" {
			out = "## Rules changed since the last time trace showed them (new or changed lines only)\n" + out
		}
		reason = "change"
	case float64(ctx-lastCtx) >= factor*float64(len(text))/4: // about 4 characters per token
		out, reason = text, "threshold"
	default:
		return "", "", nil
	}
	if _, err := db.Exec("insert or ignore into card(sha, first_seen, text) values (?, ?, ?)", sha(text), now.UTC().Format(time.RFC3339), text); err != nil {
		return "", "", err
	}
	_, err = db.Exec("insert into injection(session_id, ts, ctx_tokens, reason, card_sha, chars) values (?, ?, ?, ?, ?, ?)",
		sid, now.UTC().Format(time.RFC3339), ctx, reason, sha(text), len(out))
	return out, reason, err
}

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

// sessionCard builds the card for a session in cwd, with the user's rules from the store when it is readable.
func sessionCard(db *sql.DB, cwd, role string) (string, string) {
	home, _ := os.UserHomeDir()
	var rules strings.Builder
	if db != nil {
		refusals(db, &rules, userName(), "claude-code", role)
	}
	return card(claudeSources(cwd, home), rules.String(), cfg.Reinject.budget())
}
