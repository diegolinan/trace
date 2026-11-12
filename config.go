package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cfg is read once by main from <home>/config.json. Tests set it directly.
var cfg config

// config holds every setting that belongs to one user or one setup, so none of it is written in the code.
// Flags override data, repo and scope.
type config struct {
	Data       string     `json:"data"`        // where trace.db and raw/ live; empty: the home itself
	Repo       string     `json:"repo"`        // git repository for checking commit ids; empty: no checks
	Scope      string     `json:"scope"`       // only project folders whose name contains this; empty: all
	User       string     `json:"user"`        // the person's name in the text the agents read; empty: "the user"
	Roles      []roleRule `json:"roles"`       // named checkouts; empty: every git checkout is its own project
	RelayTools []string   `json:"relay_tools"` // tool-name prefixes whose results are another agent's words
	// Anchors adds tags beside commit, path and url: name -> regexp, for example a ticket id. Matches are kept as
	// written.
	Anchors map[string]string `json:"anchors"`
	// PinPhrases mark a typed message as pinned, in any language; empty: "remember this".
	PinPhrases []string `json:"pin_phrases"`
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
	setRepo(cfg.Repo)
	extraAnchors = nil
	for name, p := range cfg.Anchors {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "trace: config anchor %q skipped: %v\n", name, err)
			continue
		}
		extraAnchors = append(extraAnchors, namedRe{name, re})
	}
	sort.Slice(extraAnchors, func(i, j int) bool { return extraAnchors[i].name < extraAnchors[j].name })
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

// roleRule names a checkout by a file in its own root, so a moved or renamed folder keeps its role (the id-not-
// name rule: agentmemory's git-dir test, engram #1717). Folder is the fallback when the cwd no longer exists.
type roleRule struct {
	Name     string `json:"name"`
	Label    string `json:"label"`    // how the resume block and helper agents name it
	Git      string `json:"git"`      // "file": a linked worktree; "dir": a main checkout; empty: either
	Marker   string `json:"marker"`   // a file at the checkout root...
	Contains string `json:"contains"` // ...that holds this text
	Folder   string `json:"folder"`   // transcript folder name suffix, compared without case
}

// traceHome holds config.json, and the data unless config moves it: $TRACE_HOME, else ~/.trace. A junction cannot
// move the data: the SQLite driver fails to open a file through one on Windows ("cannot find the path
// specified"), and Go 1.23+ does not follow junctions, so "data" names the real folder instead.
func traceHome() string {
	if h := os.Getenv("TRACE_HOME"); h != "" {
		return h
	}
	u, _ := os.UserHomeDir()
	return filepath.Join(u, ".trace")
}

func dataDir(home string, c config) string {
	if c.Data != "" {
		return c.Data
	}
	return home
}

func loadConfig(home string) (c config) {
	b, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err == nil && json.Unmarshal(b, &c) != nil {
		fmt.Fprintln(os.Stderr, "trace: config.json is not valid JSON; using defaults")
		c = config{}
	}
	return c
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

func folderName(path string) string { return strings.ToLower(notAlnum.ReplaceAllString(path, "-")) }

// projectRole is the role of a checkout when no roles are configured: its own folder name, so the resume block
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

// roleFor reads the role from the checkout's own files. A cwd that no longer exists keeps the fallback.
func roleFor(cwd, fallback string) string {
	for d := cwd; d != ""; {
		if st, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if len(cfg.Roles) == 0 {
				return projectRole(d)
			}
			for _, r := range cfg.Roles {
				if r.Marker == "" || r.Git == "file" && st.IsDir() || r.Git == "dir" && !st.IsDir() {
					continue
				}
				if b, _ := os.ReadFile(filepath.Join(d, r.Marker)); bytes.Contains(b, []byte(r.Contains)) {
					return r.Name
				}
			}
			return "other"
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	return fallback
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

// repoRe strips the repository prefix, and any worktree under .claude/worktrees, so a path means the same thing
// from every checkout. Nil when no repo is set.
var repoRe *regexp.Regexp

func setRepo(repo string) {
	repoRe = nil
	if r := strings.TrimSuffix(filepath.ToSlash(repo), "/"); r != "" {
		repoRe = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(r) + `/(?:\.claude/worktrees/[^/]+/)?`)
	}
}
