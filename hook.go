package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hookInput is the part of the host's hook payload trace reads (Claude Code and Codex share these names).
type hookInput struct {
	Event      string          `json:"hook_event_name"`
	SessionID  string          `json:"session_id"`
	Source     string          `json:"source"` // SessionStart: startup, resume, clear, compact
	Transcript string          `json:"transcript_path"`
	Cwd        string          `json:"cwd"`
	ToolName   string          `json:"tool_name"`        // PostToolUse
	ToolInput  json.RawMessage `json:"tool_input"`       // PostToolUse
	AgentID    string          `json:"agent_id"`         // set inside a subagent, whose payload names the parent's transcript
	StopActive bool            `json:"stop_hook_active"` // Stop: a stop hook already kept this turn going
}

func readHookInput(r io.Reader) hookInput {
	var in hookInput
	// PostToolUse carries the tool's whole result, often megabytes; a cut payload would not decode at all.
	json.NewDecoder(io.LimitReader(r, 256<<20)).Decode(&in) // an unreadable payload still triggers an import
	return in
}

// admitted says whether a hook call belongs to this memory, before anything is imported, opened, run or printed.
// Its cwd is in scope; or, for Claude Code only, its transcript lies inside the configured transcripts folder (projects)
// under a project folder in scope and is this session's own (its session id, or its parent's inside a subagent), so a
// session that cd's elsewhere keeps its memory. A missing or relative cwd, a Codex call
// outside scope, and a transcript anywhere else admit nothing. cwdIn tells the caller whether the cwd itself is in
// scope: checks run commands there, so they need it.
func admitted(in hookInput, host, projects string) (ok, cwdIn bool) {
	// The host always sends a cwd; a payload without it is not trusted with anything.
	if !filepath.IsAbs(in.Cwd) {
		return false, false
	}
	if inScope(in.Cwd, string(cfg.Scope)) {
		return true, true
	}
	return host == "claude-code" && ownTranscriptInScope(in, projects), false
}

// ownTranscriptInScope says whether a Claude Code call's transcript is its session's own and lies in the transcripts
// folder (projects) under a project folder in scope.
func ownTranscriptInScope(in hookInput, projects string) bool {
	if projects == "" || in.Transcript == "" || in.SessionID == "" {
		return false
	}
	// The transcript must be this session's own (or, inside a subagent, its parent's), not a path borrowed from another.
	if id, _, parent, _ := sessionFor(in.Transcript); id != in.SessionID && parent != in.SessionID {
		return false
	}
	// On an error rel is "", which fails nested below like "." and "..".
	rel, _ := filepath.Rel(filepath.Clean(projects), filepath.Clean(in.Transcript))
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	project, _, nested := strings.Cut(filepath.ToSlash(rel), "/")
	return nested && inScope(project, string(cfg.Scope))
}

// hook handles one host hook call. Calls admitted refuses get nothing: no import, no store, no output. SessionStart
// (except after a compaction) imports in the foreground, so the session that just ended is stored, then prints
// where it stopped; Claude Code waits for SessionStart hooks before Claude's first reply, not before the prompt box.
// Every other event starts a detached import. host is the agent that called (claude-code or codex); projects is the
// Claude Code transcripts folder in use.
func hook(in hookInput, flags []string, dbPath, host, projects string, run func(*sql.DB, string) error, w io.Writer) error {
	ok, cwdIn := admitted(in, host, projects)
	if !ok {
		return nil
	}
	role, mine := hookRole(in, host)
	// One dispatch for both hosts. What differs is only how output is written: Codex rejects plain stdout on Stop and
	// ignores it on PreCompact (schema.rs, stop.rs, compact.rs, rust-v0.169.0), so it gets context only as JSON, and
	// only on the events that carry it (contextOut).
	switch {
	case in.Event == "PostToolUse" || in.Event == "UserPromptSubmit" || in.Event == "SubagentStart":
		if !mine {
			return nil
		}
		return contextHook(in, role, host, dbPath, cwdIn, w)
	case in.Event == "SessionStart" && in.Source != "compact":
		return sessionStart(in, dbPath, role, host, mine, run, w)
	}
	return detachedHook(in, flags, dbPath, role, host, mine, w)
}

// contextHook answers the events that import nothing: a tool call or a prompt (an import runs at every Stop) and a
// subagent's start (the parent's Stop covers it).
func contextHook(in hookInput, role, host, dbPath string, cwdIn bool, w io.Writer) error {
	if in.Event == "SubagentStart" {
		return contextOut(w, "SubagentStart", fmt.Sprintf(helperLine, roleLabel(role, "your parent agent"), userName()))
	}
	return ruleHook(in, role, host, dbPath, cwdIn, w)
}

// sessionStart imports in the foreground, so the session that just ended is stored, then prints where it stopped.
func sessionStart(in hookInput, dbPath, role, host string, mine bool, run func(*sql.DB, string) error, w io.Writer) error {
	trigger := hookTrigger(in)
	if err := importOnce(dbPath, 5*time.Second, func(db *sql.DB) error { return run(db, trigger) }); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import failed, showing what is stored:", err)
	}
	if err := wakeAlarm(dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "trace: the alarm did not start:", err)
	}
	if !mine {
		return nil
	}
	return startBlock(in, dbPath, role, host, w)
}

// detachedHook starts a detached import for Stop, SessionEnd, Interrupt (Codex only), PreCompact and the
// SessionStart after a compaction, and prints only what those last two carry: the summary steer and the rule card.
// A Stop may also hold the turn for a task not told (SPEC A8).
func detachedHook(in hookInput, flags []string, dbPath, role, host string, mine bool, w io.Writer) error {
	err := spawnImport(flags, hookTrigger(in))
	if !mine {
		return err
	}
	switch {
	case in.Event == "PreCompact" && host != "codex": // Codex drops PreCompact stdout, and its hooks.json gets no PreCompact
		fmt.Fprintf(w, compactSteer, userName())
	case in.Event == "SessionStart" && cfg.Reinject != nil && !withheld(clockNow()):
		return errors.Join(err, compactCard(in, dbPath, role, host, w))
	case in.Event == "Stop":
		return errors.Join(err, holdStop(in, dbPath, host, w))
	}
	return err
}

// hookTrigger names an import by the hook that started it, and which session fired it, so a hook that misfires can
// be traced.
func hookTrigger(in hookInput) string {
	trigger := "hook"
	if in.Event != "" {
		trigger = "hook:" + in.Event
	}
	if in.Source != "" {
		trigger += ":" + in.Source
	}
	if in.SessionID != "" {
		trigger += " " + short(in.SessionID)
	}
	return trigger
}

// hookRole is the calling session's role, and whether this memory speaks to it (a role of its own, not excluded).
// A Codex payload names no Claude transcript folder, so the fallback is the folder itself, the role the importer
// gives a Codex thread (projectRole of its cwd).
func hookRole(in hookInput, host string) (role string, mine bool) {
	_, role, _, excluded := sessionFor(in.Transcript)
	if host == "codex" {
		role, excluded = roleFor(in.Cwd, projectRole(in.Cwd)), ""
	} else if in.Cwd != "" {
		role = roleFor(in.Cwd, role)
	}
	return role, ours(role) && excluded == ""
}

// compactCard prints the rule card after a compaction: the host reloads the instruction files then, but not the
// user's rules from conversation.
func compactCard(in hookInput, dbPath, role, host string, w io.Writer) error {
	db, _ := openReadOnly(dbPath)
	text, _ := sessionCard(db, in.Cwd, in.Transcript, role, host)
	if db != nil {
		db.Close()
	}
	if host == "codex" {
		return contextOut(w, "SessionStart", text)
	}
	_, err := fmt.Fprint(w, text)
	return err
}

// startBlock prints where the last session stopped. Codex reads SessionStart context from JSON on stdout
// (hooks/src/schema.rs, rust-v0.169.0); never continue:false.
func startBlock(in hookInput, dbPath, role, host string, w io.Writer) error {
	db, err := openReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if host != "codex" {
		return resumeBlock(db, in.SessionID, in.Source, role, host, in.Cwd, w)
	}
	var buf strings.Builder
	if err := resumeBlock(db, in.SessionID, in.Source, role, host, in.Cwd, &buf); err != nil {
		return err
	}
	return contextOut(w, "SessionStart", buf.String())
}

// contextOut writes text as hookSpecificOutput.additionalContext of event, the shape both hosts read on SessionStart,
// SubagentStart, UserPromptSubmit and PostToolUse; Codex rejects any other field there. Empty text writes nothing.
func contextOut(w io.Writer, event, text string) error {
	if text == "" {
		return nil
	}
	return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": text}})
}

// helperLine reaches every subagent through SubagentStart (subagents get no SessionStart context). They load the
// same role files and memory as their parent, so without it they take themselves for that agent.
const helperLine = "You are a helper agent started by %[1]s, not %[1]s itself. The role files and memory you loaded describe %[1]s, not you. " +
	"Do the task you were given and return the result. Do not commit, push, fast-forward main, write %[1]s's memory or message %[2]s or other agents unless the task says so."

// compactSteer is PreCompact stdout, which Claude Code appends to its summary prompt under "Additional
// Instructions:" (2.1.322 strings). It steers the summary only, never reaches the model afterwards, and the user
// sees it on screen (#101309). Fixed text, so it cannot grow (#101057: 2.2 MB of output thrashed autocompact).
// Codex ignores it. Rules from compact-plus (open work as fact) and vectr (specific statements).
const compactSteer = `trace, for this summary:
- Quote %s's corrections, rejections and approvals word for word, in their language, with what each applies to. Never paraphrase them.
- Keep every commit SHA, file path and issue or ticket id exactly.
- State open work as facts (done, not done, decided), never as orders for the next step.
- Give specific statements (the command, the flag, the path), not a list of topics.
- Mark any claim of success that has no passing tool result as unverified.
`

// spawnImport starts a detached copy of trace that imports, and returns at once. Used for Stop, PreCompact and
// SessionEnd, so nothing waits on it: SessionEnd gets 1.5 s in Claude Code and 1-3 s in Codex, a Codex hook timeout kills the hook's whole process tree, and a
// minimal SessionEnd fired 21/21 where a heavy one fired about half (#98075).
// flags are this call's own flags, passed on to the import unchanged. A variable so tests do not start the test binary.
var spawnImport = startImport

func startImport(flags []string, trigger string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Not released or waited for: the hook exits right after, and the system frees the handle then.
	return startDetached(exec.Command(exe, append(append([]string{"import"}, flags...), "-trigger", trigger)...))
}
