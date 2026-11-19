package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// cfg is read once by main from <home>/config.json. Tests set it directly.
var cfg config

// config holds every setting that belongs to one user or one setup, so none of it is written in the code.
// Flags override data and scope.
type config struct {
	Data       string     `json:"data"`        // where trace.db and raw/ live; empty: the home itself
	Scope      scopeList  `json:"scope"`       // a folder or a list of folders (inScope); empty: all
	Claude     *bool      `json:"claude"`      // false: never read Claude Code transcripts or history.jsonl
	User       string     `json:"user"`        // the person's name in the text the agents read; empty: "the user"
	Roles      []roleRule `json:"roles"`       // named folders; empty: every folder is its own project
	RelayTools []string   `json:"relay_tools"` // tool-name prefixes whose results are another agent's words
	// Anchors adds tags beside path and url: name -> regexp, for example a ticket id. Matches are kept as
	// written.
	Anchors map[string]string `json:"anchors"`
	// PinPhrases mark a typed message as pinned, in any language; empty: "remember this".
	PinPhrases []string `json:"pin_phrases"`
	// Checks are the project's rules a command can verify, run after matching tool calls (checks.go).
	Checks []check `json:"checks"`
	// Reinject turns on the rule card (reinject.go); absent, nothing is re-injected and no hook is added for it.
	Reinject *reinjectCfg `json:"reinject"`
	// NotTypedByUser are periods when someone else typed in the user's terminal (SPEC I10).
	NotTypedByUser []period `json:"not_typed_by_user"`
}

// period is a span of UTC dates, both included, with a note saying why.
type period struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Session string `json:"session"`
	Note    string `json:"note"`
}

type namedRe struct {
	name string
	re   *regexp.Regexp
}

var (
	extraAnchors []namedRe
	pinRe        *regexp.Regexp
)

// compileConfig builds the patterns cfg asks for. A bad pattern is reported and skipped, never fatal: the hooks
// must not fail a turn.
func compileConfig() {
	extraAnchors = nil
	for _, name := range slices.Sorted(maps.Keys(cfg.Anchors)) {
		re, err := regexp.Compile(cfg.Anchors[name])
		if err != nil {
			fmt.Fprintf(os.Stderr, "trace: config anchor %q skipped: %v\n", name, err)
			continue
		}
		extraAnchors = append(extraAnchors, namedRe{name, re})
	}
	phrases := cfg.PinPhrases
	if len(phrases) == 0 {
		phrases = []string{"remember this"}
	}
	quoted := make([]string, len(phrases))
	for i, p := range phrases {
		quoted[i] = regexp.QuoteMeta(p)
	}
	pinRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`)
}

// roleRule names a folder by a file in it, so a moved or renamed folder keeps its role (the id-not-name rule,
// engram #1717). Folder is the fallback when the cwd no longer exists. A "git" key in config.json is ignored (SPEC G3).
type roleRule struct {
	Name     string `json:"name"`
	Label    string `json:"label"`    // how the resume block and helper agents name it
	Marker   string `json:"marker"`   // a file in the session's folder or one above it...
	Contains string `json:"contains"` // ...that holds this text
	Folder   string `json:"folder"`   // transcript folder name suffix, compared without case
}

// traceHome holds config.json, and the data unless config moves it: $TRACE_HOME, else ~/.trace. A junction cannot
// move the data: the SQLite driver fails to open a file through one on Windows ("cannot find the path
// specified"), and Go 1.23+ does not follow junctions, so "data" names the real folder instead.
func traceHome() string { return envOrHome("TRACE_HOME", ".trace") }

// claudeDir is Claude Code's folder: $CLAUDE_CONFIG_DIR, else ~/.claude (SPEC I13).
func claudeDir() string { return envOrHome("CLAUDE_CONFIG_DIR", ".claude") }

// envOrHome is the folder the variable names, else that folder under the user's home.
func envOrHome(variable, folder string) string {
	if d := os.Getenv(variable); d != "" {
		return d
	}
	u, _ := os.UserHomeDir()
	return filepath.Join(u, folder)
}

func dataDir(home string, c config) string {
	if c.Data != "" {
		return c.Data
	}
	return home
}

// loadConfig fails on a file it cannot read: running on defaults would widen the scope to every project.
// A UTF-8 BOM (Windows PowerShell writes one) is accepted.
func loadConfig(home string) (c config, err error) {
	path := filepath.Join(home, "config.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return config{}, nil
	}
	if err == nil {
		err = json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &c)
	}
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func userName() string {
	if cfg.User != "" {
		return cfg.User
	}
	return "the user"
}

// folderName is how Claude Code names a project's transcript folder: every character that is not a letter or a
// digit becomes "-". Launchers differ in drive-letter case (episodic-memory #202), so it is lowered.
var notAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// The Windows long-path prefix (\\?\D:\x, as Codex sends cwd and transcript_path after a resume) names the same
// folder, so it is dropped: otherwise a role or scope would differ by the prefix alone.
func folderName(path string) string {
	return strings.ToLower(notAlnum.ReplaceAllString(strings.TrimPrefix(path, `\\?\`), "-"))
}

// scopeList is the scope setting: one string or a list, kept comma-joined so the -scope flag takes the same form.
// shortcut: a folder whose path holds a comma splits in two, both still matching it; upgrade if that ever bites.
type scopeList string

func (s *scopeList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = scopeList(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("scope: a string or a list of strings: %w", err)
	}
	*s = scopeList(strings.Join(many, ","))
	return nil
}

// inScope says whether a folder (a path, or Claude Code's transcript folder name) is in scope: its folderName contains
// the folderName of any comma-separated entry, so a full path, a folder name and either case all work. Empty: all.
func inScope(folder, scope string) bool {
	if strings.TrimSpace(scope) == "" {
		return true
	}
	name := folderName(folder)
	for _, s := range strings.Split(scope, ",") {
		if s = strings.TrimSpace(s); s != "" && strings.Contains(name, folderName(s)) {
			return true
		}
	}
	return false
}

// projectRole is the role of a folder when no roles are configured: its own folder name, so the resume block
// shows the last session of the same project.
func projectRole(dir string) string { return "project:" + folderName(dir) }

// folderRole is the fallback role, from the transcript folder name alone.
func folderRole(folder string) string {
	f := strings.ToLower(folder)
	for _, r := range cfg.Roles {
		if r.Folder != "" && strings.HasSuffix(f, strings.ToLower(r.Folder)) {
			return r.Name
		}
	}
	if len(cfg.Roles) > 0 {
		return "other"
	}
	return "project:" + f
}

// roleFor reads the role from the files in the session.s folder and the folders above it. A cwd that no longer exists keeps the fallback.
func roleFor(cwd, fallback string) string {
	// A path from another system (D:\x read on Linux) is not absolute here; walking up it would reach ".".
	if !filepath.IsAbs(cwd) {
		return fallback
	}
	// No roles: the session's own folder (SPEC G2). Roles: the nearest folder, from cwd up, holding a role's marker
	// (SPEC G3). A .git is one more file and is never consulted.
	if len(cfg.Roles) == 0 {
		return projectRole(cwd)
	}
	for d := cwd; ; {
		for _, r := range cfg.Roles {
			if r.Marker == "" {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(d, r.Marker)); err == nil && bytes.Contains(b, []byte(r.Contains)) {
				return r.Name
			}
		}
		up := filepath.Dir(d)
		if up == d {
			return fallback
		}
		d = up
	}
}

// ours says whether a role gets the resume block, the summary steer and the helper line. The hooks are user-wide,
// so with roles configured, other projects get nothing.
func ours(role string) bool {
	if len(cfg.Roles) == 0 {
		return strings.HasPrefix(role, "project:")
	}
	for _, r := range cfg.Roles {
		if r.Name == role {
			return true
		}
	}
	return false
}

func roleLabel(role, fallback string) string {
	for _, r := range cfg.Roles {
		if r.Name == role && r.Label != "" {
			return r.Label
		}
	}
	return fallback
}

func relayTool(name string) bool {
	for _, p := range cfg.RelayTools {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
