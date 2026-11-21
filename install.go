package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

// hookEvents are the Claude Code events TRACE listens to, with their timeouts in seconds.
var hookEvents = []struct {
	name    string
	timeout int
}{{"SessionStart", 30}, {"Stop", 10}, {"PreCompact", 10}, {"SessionEnd", 5}, {"SubagentStart", 5}, {"PostToolUse", 60}, {"UserPromptSubmit", 10}, {"PreToolUse", 60}}

// optional are the events installed only when config.json asks for them, with their matcher ("" for none): checks
// need PostToolUse on their tools; the rule card needs every tool call and every prompt, since most context enters
// between prompts.
var optional = map[string]bool{"PostToolUse": true, "UserPromptSubmit": true, "PreToolUse": true}

func optionalHooks(c config) map[string]string {
	// always: the net that asks about rules not recorded (SPEC D6), and the checks before and rules at the action (C5)
	m := map[string]string{"UserPromptSubmit": "", "PreToolUse": preMatcher(c.Checks)}
	if c.Reinject != nil {
		m["PostToolUse"], m["UserPromptSubmit"] = "*", ""
	} else if mt := checkMatcher(checksWhen(c.Checks, false)); mt != "" {
		m["PostToolUse"] = mt
	}
	return m
}

// claudeSettings is Claude Code's user settings file; CLAUDE_CONFIG_DIR moves it.
func claudeSettings() string { return filepath.Join(claudeDir(), "settings.json") }

// isTraceCmd recognises a hook entry that runs a trace binary, wherever it was installed.
func isTraceCmd(h any) bool {
	m, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(cmd, `\`, "/")))
	return base == "trace" || base == "trace.exe"
}

// hookHome is the -home a trace hook entry runs with, read by the flag set main uses (the last -home wins, -home=x
// works, parsing stops at -- or the first word). named is false for a hook installed before -home existed; ok is
// false for an entry that is not a plain `trace hook` call trace can read, which install treats as another memory's
// and never touches.
func hookHome(h any) (home string, named, ok bool) {
	args, isHook := hookArgs(h)
	o := opts{cmd: "hook"}
	fs := o.flagSet(flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if !isHook || fs.Parse(args) != nil || fs.NArg() > 0 {
		return "", false, false
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "home" {
			named = true
		}
	})
	return o.home, named, !named || o.home != ""
}

// hookArgs are the flags of a hook entry that runs `trace hook`; isHook is false for any other entry, or one whose
// arguments are not all strings.
func hookArgs(h any) (args []string, isHook bool) {
	raw, _ := h.(map[string]any)["args"].([]any)
	for _, a := range raw {
		s, isStr := a.(string)
		if !isStr {
			return nil, false
		}
		args = append(args, s)
	}
	if len(args) == 0 || args[0] != "hook" {
		return nil, false
	}
	return args[1:], true
}

// sameHome compares two homes as paths; on Windows without case.
func sameHome(a, b string) bool {
	a, b = filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// traceHooks lists the homes of the trace hooks in settings: own (this home), legacy (no home) and foreign (another,
// or an entry trace cannot read, listed by its command).
func traceHooks(settings map[string]any, home string) (own, legacy int, foreign []string) {
	hooks, _ := settings["hooks"].(map[string]any)
	for _, groups := range hooks {
		gs, _ := groups.([]any)
		for _, g := range gs {
			group, _ := g.(map[string]any)
			inner, _ := group["hooks"].([]any)
			for _, h := range inner {
				if !isTraceCmd(h) {
					continue
				}
				switch hh, named, ok := hookHome(h); {
				case !ok:
					foreign = append(foreign, fmt.Sprintf("an unreadable trace entry %v", h.(map[string]any)["args"]))
				case !named:
					legacy++
				case sameHome(hh, home):
					own++
				default:
					foreign = append(foreign, hh)
				}
			}
		}
	}
	return own, legacy, foreign
}

// setHooks removes the trace hooks that run this home (and, with migrate, those that name no home), then adds one
// per event running exe with this home (none when exe is empty). Hooks of another home are kept; install refuses
// before it gets here when there are any. An optional event gets one only when opt names it (optionalHooks), so tool
// calls start nothing unless asked. Other hooks and settings are left as they are. It returns the events changed.
func setHooks(settings map[string]any, exe, home string, opt map[string]string, migrate bool) []string {
	mine := func(h any) bool { return claims(h, home, migrate) }
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	var changed []string
	for _, ev := range hookEvents {
		before, _ := hooks[ev.name].([]any)
		after := withoutMine(before, mine)
		matcher, asked := opt[ev.name]
		if exe != "" && (asked || !optional[ev.name]) {
			after = append(after, traceGroup(exe, home, matcher, ev.timeout))
		}
		if !reflect.DeepEqual(normalize(before), normalize(after)) {
			changed = append(changed, ev.name)
		}
		setOrDelete(hooks, ev.name, after, len(after) == 0)
	}
	setOrDelete(settings, "hooks", hooks, len(hooks) == 0)
	return changed
}

// claims says whether a hook entry is one setHooks replaces: a trace hook that runs this home, or, with migrate,
// one that names no home.
func claims(h any, home string, migrate bool) bool {
	if !isTraceCmd(h) {
		return false
	}
	hh, named, ok := hookHome(h)
	return ok && (!named && migrate || named && sameHome(hh, home))
}

// setOrDelete sets m[key] to v, or removes the key when v is empty, so no empty list or object is left behind.
func setOrDelete(m map[string]any, key string, v any, empty bool) {
	if empty {
		delete(m, key)
	} else {
		m[key] = v
	}
}

// withoutMine is an event's hook groups with the hooks mine claims taken out: a group left empty goes, a group that
// lost some is copied with the rest, and anything else is kept as it was.
func withoutMine(groups []any, mine func(any) bool) []any {
	var after []any
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			after = append(after, g)
			continue
		}
		inner, _ := group["hooks"].([]any)
		var kept []any
		for _, h := range inner {
			if !mine(h) {
				kept = append(kept, h)
			}
		}
		switch {
		case len(kept) == len(inner):
			after = append(after, g)
		case len(kept) > 0:
			copyGroup := map[string]any{}
			for k, v := range group {
				copyGroup[k] = v
			}
			copyGroup["hooks"] = kept
			after = append(after, copyGroup)
		}
	}
	return after
}

// traceGroup is the hook group that runs exe for this home on one event, with the event's matcher if it has one.
func traceGroup(exe, home, matcher string, timeout int) map[string]any {
	group := map[string]any{"hooks": []any{map[string]any{
		"type": "command", "command": exe, "args": []any{"hook", "-home", home}, "timeout": float64(timeout)}}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	return group
}

// normalize round-trips a value through JSON so freshly built maps compare equal to parsed ones.
func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}

// install adds (exe != "") or removes the hooks that run this TRACE home in Claude Code's user settings; one home per
// settings file. A file that does not parse, holds another home's hooks (install) or hooks that name no home
// (without migrate) is left untouched. An existing file is copied to settings.json.trace-backup first, and nothing is
// written if the file changed while trace was working on it.
func install(path, exe, home string, opt map[string]string, migrate bool, w io.Writer) error {
	settings, b, err := readSettings(path)
	if err != nil {
		return err
	}
	if note, err := settingsFit(settings, b != nil, path, exe, home, migrate); note != "" || err != nil {
		fmt.Fprint(w, note)
		return err
	}
	changed := setHooks(settings, exe, home, opt, migrate)
	if len(changed) == 0 {
		fmt.Fprintf(w, "hooks already up to date in %s\n", path)
		return nil
	}
	if err := writeSettings(path, b, settings); err != nil {
		return err
	}
	installReport(w, path, exe, changed, b != nil)
	return nil
}

// installReport says what install did: the events whose hooks it added or removed, and the backup when there was a
// file to keep.
func installReport(w io.Writer, path, exe string, changed []string, backedUp bool) {
	verb := "added"
	if exe == "" {
		verb = "removed"
	}
	fmt.Fprintf(w, "hooks %s in %s: %s\n", verb, path, strings.Join(changed, ", "))
	if backedUp {
		fmt.Fprintf(w, "previous file kept as %s.trace-backup\n", path)
	}
	fmt.Fprintln(w, "restart open Claude Code sessions: they read hooks when they start")
}

// readSettings is Claude Code's settings and their bytes; a missing file is empty settings and nil bytes, a file
// that does not parse is an error and is left untouched.
func readSettings(path string) (map[string]any, []byte, error) {
	settings := map[string]any{}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return settings, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON, left untouched: %v", path, err)
	}
	return settings, b, nil
}

// settingsFit refuses another home's hooks on install and hooks that name no home without migrate; a note ends an
// uninstall that has nothing of this home's to remove (no settings file, or only another home's hooks).
func settingsFit(settings map[string]any, exists bool, path, exe, home string, migrate bool) (note string, err error) {
	if !exists && exe == "" {
		return "no Claude Code settings file; nothing to remove\n", nil
	}
	own, legacy, foreign := traceHooks(settings, home)
	if err := refusal(path, exe, home, legacy, foreign, migrate); err != nil {
		return "", err
	}
	if exe == "" && own+legacy == 0 && len(foreign) > 0 {
		return fmt.Sprintf("no TRACE hooks for %s in %s; those of %s are left as they are\n", home, path, foreign[0]), nil
	}
	return "", nil
}

// refusal is why install leaves the file untouched: another home's hooks (on install), or hooks that name no home
// without migrate.
func refusal(path, exe, home string, legacy int, foreign []string, migrate bool) error {
	if exe != "" && len(foreign) > 0 {
		return fmt.Errorf("%s already runs TRACE with another home (%s); one TRACE per settings file: uninstall that one with -home first; nothing done", path, foreign[0])
	}
	if legacy > 0 && !migrate {
		return fmt.Errorf("%s holds %d TRACE hook(s) that name no home (installed before -home existed); run again with -migrate to bind them to %s (the file is backed up first); nothing done", path, legacy, home)
	}
	return nil
}

// writeSettings writes the new settings over old, keeping the previous file as settings.json.trace-backup.
func writeSettings(path string, old []byte, settings map[string]any) error {
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeChecked(path, string(old), old != nil, string(append(out, '\n')), path+".trace-backup")
}

// selfPath is this binary's absolute path, with forward slashes on Windows as Claude Code's settings use them.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.ToSlash(exe), nil // a no-op on Unix, where the separator is already /
}
