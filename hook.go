package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// hookInput is the part of the host's hook payload trace reads (Claude Code and Codex share these names).
type hookInput struct {
	Event      string          `json:"hook_event_name"`
	SessionID  string          `json:"session_id"`
	Source     string          `json:"source"` // SessionStart: startup, resume, clear, compact
	Transcript string          `json:"transcript_path"`
	Cwd        string          `json:"cwd"`
	ToolName   string          `json:"tool_name"`  // PostToolUse
	ToolInput  json.RawMessage `json:"tool_input"` // PostToolUse
	AgentID    string          `json:"agent_id"`   // set inside a subagent, whose payload names the parent's transcript
}

func readHookInput(r io.Reader) hookInput {
	var in hookInput
	// PostToolUse carries the tool's whole result, often megabytes; a cut payload would not decode at all.
	json.NewDecoder(io.LimitReader(r, 256<<20)).Decode(&in) // an unreadable payload still triggers an import
	return in
}

// hook handles one host hook call. SessionStart (except after a compaction) imports in the foreground, so the
// session that just ended is stored, then prints where it stopped; Claude Code waits for SessionStart hooks before
// Claude's first reply, not before the prompt box. Every other event starts a detached import.
func hook(in hookInput, flags []string, dbPath string, run func(*sql.DB, string) error, w io.Writer) error {
	trigger := "hook"
	if in.Event != "" {
		trigger = "hook:" + in.Event
	}
	if in.Source != "" {
		trigger += ":" + in.Source
	}
	if in.SessionID != "" { // which session fired it, so a hook that misfires can be traced
		trigger += " " + short(in.SessionID)
	}
	_, role, _, excluded := sessionFor(in.Transcript)
	if in.Cwd != "" {
		role = roleFor(in.Cwd, role)
	}
	mine := ours(role) && excluded == ""
	switch {
	case in.Event == "PostToolUse" || in.Event == "UserPromptSubmit": // no import: one runs at every Stop
		if !mine {
			return nil
		}
		return ruleHook(in, role, dbPath, w)
	case in.Event == "SubagentStart": // no import: the parent's Stop covers it
		if !mine {
			return nil
		}
		return json.NewEncoder(w).Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "SubagentStart", "additionalContext": fmt.Sprintf(helperLine, roleLabel(role, "your parent agent"), userName())}})
	case in.Event == "PreCompact":
		err := spawnImport(flags, trigger)
		if mine {
			fmt.Fprintf(w, compactSteer, userName())
		}
		return err
	case in.Event == "SessionStart" && in.Source == "compact":
		err := spawnImport(flags, trigger)
		// Claude Code reloads the instruction files after a compaction, but not the user's rules from conversation.
		if mine && cfg.Reinject != nil {
			db, _ := openReadOnly(dbPath)
			text, _ := sessionCard(db, in.Cwd, role)
			if db != nil {
				db.Close()
			}
			fmt.Fprint(w, text)
		}
		return err
	case in.Event != "SessionStart":
		return spawnImport(flags, trigger)
	}
	if err := importOnce(dbPath, 5*time.Second, func(db *sql.DB) error { return run(db, trigger) }); err != nil {
		fmt.Fprintln(os.Stderr, "trace: import failed, showing what is stored:", err)
	}
	if !mine {
		return nil
	}
	db, err := openReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return resumeBlock(db, in.SessionID, in.Source, role, "claude-code", w)
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
// flags are this call's own flags, passed on to the import unchanged.
func spawnImport(flags []string, trigger string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, append(append([]string{"import"}, flags...), "-trigger", trigger)...)
	if err := startDetached(cmd); err != nil {
		return err
	}
	return cmd.Process.Release()
}
