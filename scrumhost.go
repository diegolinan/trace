package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostScrum runs the scrum master on its host's command line. A variable so tests never start a paid model run; it
// also refuses from a test binary, which fences every host off besides (TestMain).
var hostScrum scrumHost = func(host, model, prompt string) (string, error) {
	if testing.Testing() {
		return "", errors.New("no scrum master host runs from a test binary")
	}
	return runScrumHost(scrumExe[host], host, model, prompt)
}

// scrumExe is each host's command line.
var scrumExe = map[string]string{claudeName: "claude", codexName: "codex"}

// scrumTimeout is how long one run may take before it is ended and recorded as failed.
const scrumTimeout = 10 * time.Minute

// runScrumHost runs exe, host's command line, headless: the prompt on stdin, closed; no tools; no session kept; an
// empty folder; TRACE's hooks quiet (scrumChildVar); no session id of the user's carried over (SPEC T19).
func runScrumHost(exe, host, model, prompt string) (string, error) {
	dir, err := os.MkdirTemp("", "trace-scrum-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), scrumTimeout)
	defer cancel()
	last := filepath.Join(dir, "answer.txt")
	cmd := exec.CommandContext(ctx, exe, scrumArgs(host, model, last)...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, scrumEnv(os.Environ()), strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", host, err)
	}
	return scrumAnswer(host, string(out), last)
}

// scrumAnswer is the run's answer: Codex's last message, written to last, or what Claude Code printed.
func scrumAnswer(host, out, last string) (string, error) {
	if host == codexName {
		b, err := os.ReadFile(last)
		return string(b), err
	}
	return out, nil
}

// scrumArgs are the host's headless flags: Codex read-only and ephemeral, writing its last message to last; Claude
// Code in print mode with no tools, no MCP servers, no user settings (so no user hooks) and no session saved.
func scrumArgs(host, model, last string) []string {
	var args []string
	if host == codexName {
		args = []string{"exec", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-o", last}
	} else {
		args = []string{"-p", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--setting-sources", "project", "--output-format", "text"}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if host == codexName {
		args = append(args, "-")
	}
	return args
}

// scrumEnv is env for the scrum master's session: marked as the child, without the session ids that would make its
// moves look like the user's session, and without the advisor (a headless worker inherits it).
func scrumEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == claudeCode.sessionVar || name == codexDeliverer.sessionVar || name == scrumChildVar {
			continue
		}
		out = append(out, kv)
	}
	return append(out, scrumChildVar+"=1", "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1")
}

// scrumTrigger is the scrum run a hook event starts: after a turn, which may hold a hand-off; "" for none (SPEC T19).
func scrumTrigger(in hookInput) string {
	if in.Event == "Stop" {
		return "stop"
	}
	return ""
}

// scrumHook starts the run a hook event of a session TRACE speaks to calls for, when the scrum master is on; a start
// that fails is said and never fails the hook (SPEC T19).
func scrumHook(in hookInput, flags []string, host string) {
	if trigger := scrumTrigger(in); scrumOn() && trigger != "" {
		if err := spawnScrum(flags, trigger, host); err != nil {
			fmt.Fprintln(os.Stderr, "trace: the scrum master did not start:", err)
		}
	}
}

// spawnScrum starts a detached scrum run and returns at once, so no hook waits on a model. A variable so tests do not
// start the test binary.
var spawnScrum = startScrum

// startScrum starts trace scrum with the hook's own flags, its trigger and its host.
func startScrum(flags []string, trigger, host string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return startDetached(exec.Command(exe, append(append([]string{"scrum"}, flags...), "-trigger", trigger, "-host", host)...))
}

// scrumImportWait is how long a run waits for an import that is going, so it reads the turn that just ended.
const scrumImportWait = 30 * time.Second

// cmdScrum is one scrum run, started detached by a hook: it stores what is new, then, holding the store's scrum lock
// so one runs at a time, runs the scrum master when a run is due (SPEC T19).
func cmdScrum(o opts) error {
	if !scrumOn() {
		return nil
	}
	importOnce(o.dbPath, scrumImportWait, o.importFn("scrum")) // a failed import leaves what is stored, and records itself
	lock, err := tryLock(o.dbPath + ".scrum.lock")
	if lock == nil {
		return err // held: a run is going; or the lock cannot be taken
	}
	defer lock.Close()
	db, err := openDB(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if !scrumDue(db, o.host) {
		return nil
	}
	return scrumRun(db, o.trigger, o.host, hostScrum)
}
