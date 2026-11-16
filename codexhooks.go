package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"
)

// Codex hooks mode: TRACE's hooks in <CODEX_HOME>/hooks.json, the user layer, so they fire in every project and the
// hook's scope gate keeps the others out. Codex runs `command` through a shell, never as argv: on Windows the shell
// may be cmd or PowerShell (shell_detect.rs, rust-v0.169.0), so commandWindows starts PowerShell itself with the call
// encoded, which both shells pass through unchanged; on Unix, command is POSIX-quoted for sh, bash and zsh.
// Measured on Windows (PowerShell 5.1 and cmd, emulating Codex's runner, a path with spaces and ñ): argv, UTF-8 stdin
// and UTF-8 stdout pass intact; about 260 ms more per call; under an outer PowerShell a failing exit code arrives as 1,
// not its own value (still a failure). Each call starts PowerShell and trace before the scope gate runs, in every Codex
// session on the machine, so with checks or the rule card on, every tool call in every session pays that.

// codexHookEvent is one event registered in hooks.json: its matcher ("" for none), its timeout in seconds, and
// whether its output reaches the model. Those set additionalContextLimit, so Codex does not cut them at its
// 2,500-token default and spill the rest to a temp file (output_spill.rs, rust-v0.169.0).
type codexHookEvent struct {
	name, matcher string
	timeout       int
	context       bool
}

// codexContextLimit is the additionalContextLimit TRACE sets, in tokens: above the largest output it gives (the rule
// card and checks are clipped at 9,500 characters, the resume block is about 6 KB).
const codexContextLimit = 4000

// codexHookEvents are the events registered for config c, the functions of the Claude Code hooks (hookEvents):
// SessionStart (every source: startup, resume, clear, compact, fork) shows where the last session stopped, or the rule
// card after a compaction; Stop imports; Interrupt imports a turn cut short (Codex only: Claude Code has no such
// event); SessionEnd imports at a clean shutdown, best effort (1 s by default, 3 s at most in Codex); SubagentStart
// tells a subagent it is not its parent. PostToolUse and UserPromptSubmit come only when c asks for checks or the rule
// card (optionalHooks), as for Claude Code. PreCompact is not registered: Codex ignores its stdout, so it cannot steer
// the summary (compact.rs tests pre_compact_ignores_plain_stdout). None of them runs on a kill or a crash.
func codexHookEvents(c config) []codexHookEvent {
	evs := []codexHookEvent{{"SessionStart", "", 30, true}, {"Stop", "", 10, false}, {"Interrupt", "", 3, false},
		{"SessionEnd", "", 3, false}, {"SubagentStart", "", 5, true}}
	opt := optionalHooks(c)
	if m, ok := opt["PostToolUse"]; ok {
		evs = append(evs, codexHookEvent{"PostToolUse", m, 60, true})
	}
	if _, ok := opt["UserPromptSubmit"]; ok {
		evs = append(evs, codexHookEvent{"UserPromptSubmit", "", 10, true})
	}
	return evs
}

// codexHookMark names the home in each entry's statusMessage, which is how install and uninstall find their own.
const codexHookMark = "trace memory: "

func posixQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// psScript is the PowerShell that commandWindows runs: the call, then its exit code (0 success, non-zero failure).
func psScript(exe, home string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return "& " + q(exe) + " hook -host codex -home " + q(home) + "; exit $LASTEXITCODE"
}

func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func codexHookEntry(exe, home string, ev codexHookEvent) map[string]any {
	e := map[string]any{
		"type":           "command",
		"command":        posixQuote(exe) + " hook -host codex -home " + posixQuote(home),
		"commandWindows": "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + encodePS(psScript(exe, home)),
		"timeout":        float64(ev.timeout),
		"statusMessage":  codexHookMark + home,
	}
	if ev.context {
		e["additionalContextLimit"] = float64(codexContextLimit)
	}
	return e
}

// codexHookOwner is the home an entry belongs to ("" when it is not TRACE's).
func codexHookOwner(h any) string {
	m, _ := h.(map[string]any)
	s, _ := m["statusMessage"].(string)
	if strings.HasPrefix(s, codexHookMark) {
		return strings.TrimPrefix(s, codexHookMark)
	}
	return ""
}

// parseCodexHooks reads hooks.json strictly enough to edit it safely: an object whose "hooks", when present, maps
// each event to a list of objects whose "hooks", when present, is a list. Anything else is left untouched.
func parseCodexHooks(b []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("is not valid JSON, left untouched: %v", err)
	}
	file, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("is not a JSON object, left untouched")
	}
	bad := fmt.Errorf("does not have the shape Codex hooks have, left untouched")
	if h, present := file["hooks"]; present {
		hooks, ok := h.(map[string]any)
		if !ok {
			return nil, bad
		}
		for _, groups := range hooks {
			gs, ok := groups.([]any)
			if !ok {
				return nil, bad
			}
			for _, g := range gs {
				group, ok := g.(map[string]any)
				if !ok {
					return nil, bad
				}
				if inner, present := group["hooks"]; present {
					if _, ok := inner.([]any); !ok {
						return nil, bad
					}
				}
			}
		}
	}
	return file, nil
}

// codexHooksPlan returns hooks.json's new content with this home's entries replaced by exe's (removed when exe is
// empty; the same bytes when there were none). Every other key and hook is kept, other TRACE homes' entries too:
// several memories can share a Codex home, each hook leaving out what is outside its own scope.
func codexHooksPlan(old []byte, exe, home string) ([]byte, error) {
	file, err := parseCodexHooks(old)
	if err != nil {
		return nil, err
	}
	hooks, _ := file["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	removed := false
	mine := func(h any) bool { // another memory's are kept: installCodex checked that the scopes do not overlap
		owner := codexHookOwner(h)
		if owner != "" && sameHome(owner, home) {
			removed = true
			return true
		}
		return false
	}
	for ev, groups := range hooks {
		gs, _ := groups.([]any)
		if kept := withoutMine(gs, mine); len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if exe == "" && !removed {
		return old, nil // nothing of this home's: the file is not rewritten
	}
	if exe != "" {
		addCodexHooks(hooks, exe, home)
	}
	if len(hooks) == 0 {
		delete(file, "hooks")
	} else {
		file["hooks"] = hooks
	}
	b, _ := json.MarshalIndent(file, "", "  ")
	return append(b, '\n'), nil
}

// addCodexHooks appends this home's group to every event TRACE hooks in Codex, with the event's matcher if any.
func addCodexHooks(hooks map[string]any, exe, home string) {
	for _, ev := range codexHookEvents(cfg) {
		g := map[string]any{"hooks": []any{codexHookEntry(exe, home, ev)}}
		if ev.matcher != "" {
			g["matcher"] = ev.matcher
		}
		list, _ := hooks[ev.name].([]any)
		hooks[ev.name] = append(list, g)
	}
}

// codexHookHomes lists the TRACE homes whose hooks hooks.json holds.
func codexHookHomes(b []byte) []string {
	file, err := parseCodexHooks(b)
	if err != nil {
		return nil
	}
	var out []string
	hooks, _ := file["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			group, _ := g.(map[string]any)
			inner, _ := group["hooks"].([]any)
			for _, h := range inner {
				if o := codexHookOwner(h); o != "" && !slices.ContainsFunc(out, func(x string) bool { return sameHome(x, o) }) {
					out = append(out, o)
				}
			}
		}
	}
	return out
}

// scopesOverlap says whether two memories could both keep one conversation: an empty scope keeps every folder, and
// two entries overlap when either folder is in the other's scope (inScope, both ways).
func scopesOverlap(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return true
	}
	for _, x := range strings.Split(a, ",") {
		for _, y := range strings.Split(b, ",") {
			if x, y = strings.TrimSpace(x), strings.TrimSpace(y); x != "" && y != "" && (inScope(x, y) || inScope(y, x)) {
				return true
			}
		}
	}
	return false
}

// codexHooksHave says whether hooks.json holds this home's entries; a file it cannot read is an error, never "no".
func codexHooksHave(b []byte, home string) (bool, error) {
	file, err := parseCodexHooks(b)
	if err != nil {
		return false, err
	}
	hooks, _ := file["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			group, _ := g.(map[string]any)
			inner, _ := group["hooks"].([]any)
			for _, h := range inner {
				if o := codexHookOwner(h); o != "" && sameHome(o, home) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// filePlan is one file's planned change, worked out before anything is written so a run can check every file first.
type filePlan struct {
	path      string
	old, next []byte
	existed   bool
}

func (p filePlan) changed() bool { return p.next != nil && string(p.next) != string(p.old) }

func (p filePlan) write() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	return writeChecked(p.path, string(p.old), p.existed, string(p.next), "")
}

// undo puts the old content back, only while the file still holds what write put there.
func (p filePlan) undo() error {
	if !p.existed {
		if now, err := os.ReadFile(p.path); err != nil || string(now) != string(p.next) {
			return fmt.Errorf("%s changed after this run wrote it; left as it is", p.path)
		}
		return os.Remove(p.path)
	}
	return writeChecked(p.path, string(p.next), true, string(p.old), "")
}

// planCodexHooks works out codexDir/hooks.json with this home's hooks added (exe != "") or removed.
func planCodexHooks(codexDir, exe, home string) (filePlan, error) {
	p := filePlan{path: filepath.Join(codexDir, "hooks.json")}
	old, err := os.ReadFile(p.path)
	if err != nil && !os.IsNotExist(err) {
		return p, err
	}
	p.old, p.existed = old, err == nil
	if p.next, err = codexHooksPlan(old, exe, home); err != nil {
		return p, fmt.Errorf("%s %w; nothing done", p.path, err)
	}
	return p, nil
}

// setCodexHooks adds (exe != "") or removes this home's hooks in codexDir/hooks.json and says whether it changed it.
func setCodexHooks(codexDir, exe, home string, w io.Writer) (changed bool, err error) {
	p, err := planCodexHooks(codexDir, exe, home)
	if err != nil || !p.changed() {
		return false, err
	}
	if exe != "" {
		fmt.Fprintln(w, "Codex will run, on Windows, this PowerShell (sent encoded so cmd and PowerShell both pass it unchanged):")
		fmt.Fprintln(w, "  "+psScript(exe, home))
		fmt.Fprintln(w, "and elsewhere:", posixQuote(exe)+" hook -host codex -home "+posixQuote(home))
	}
	return true, p.write()
}
