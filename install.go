package main

import (
	"encoding/json"
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
}{{"SessionStart", 30}, {"Stop", 10}, {"PreCompact", 10}, {"SessionEnd", 5}, {"SubagentStart", 5}, {"PostToolUse", 60}}

// claudeSettings is Claude Code's user settings file; CLAUDE_CONFIG_DIR moves it.
func claudeSettings() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	u, _ := os.UserHomeDir()
	return filepath.Join(u, ".claude", "settings.json")
}

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

// setHooks removes every trace hook from settings, then adds one per event running exe (none when exe is empty).
// PostToolUse gets one only when checks are configured, matching their tools, so other tool calls start nothing.
// Other hooks and settings are left as they are. It returns the events whose hooks changed.
func setHooks(settings map[string]any, exe, matcher string) []string {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	var changed []string
	for _, ev := range hookEvents {
		before, _ := hooks[ev.name].([]any)
		var after []any
		for _, g := range before {
			group, ok := g.(map[string]any)
			if !ok {
				after = append(after, g)
				continue
			}
			inner, _ := group["hooks"].([]any)
			var kept []any
			for _, h := range inner {
				if !isTraceCmd(h) {
					kept = append(kept, h)
				}
			}
			if len(kept) == len(inner) {
				after = append(after, g)
			} else if len(kept) > 0 {
				copyGroup := map[string]any{}
				for k, v := range group {
					copyGroup[k] = v
				}
				copyGroup["hooks"] = kept
				after = append(after, copyGroup)
			}
		}
		if exe != "" && ev.name == "PostToolUse" && matcher != "" {
			after = append(after, map[string]any{"matcher": matcher, "hooks": []any{map[string]any{
				"type": "command", "command": exe, "args": []any{"hook"}, "timeout": float64(ev.timeout)}}})
		} else if exe != "" && ev.name != "PostToolUse" {
			after = append(after, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": exe, "args": []any{"hook"}, "timeout": float64(ev.timeout)}}})
		}
		if !reflect.DeepEqual(normalize(before), normalize(after)) {
			changed = append(changed, ev.name)
		}
		if len(after) == 0 {
			delete(hooks, ev.name)
		} else {
			hooks[ev.name] = after
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
	return changed
}

// normalize round-trips a value through JSON so freshly built maps compare equal to parsed ones.
func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	json.Unmarshal(b, &out)
	return out
}

// install adds (exe != "") or removes TRACE's hooks in Claude Code's user settings. A file that does not parse is
// never overwritten; an existing file is copied to settings.json.trace-backup first.
func install(path, exe, matcher string, w io.Writer) error {
	settings := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON, left untouched: %v", path, err)
		}
	case os.IsNotExist(err):
		if exe == "" {
			fmt.Fprintln(w, "no Claude Code settings file; nothing to remove")
			return nil
		}
	default:
		return err
	}
	changed := setHooks(settings, exe, matcher)
	if len(changed) == 0 {
		fmt.Fprintf(w, "hooks already up to date in %s\n", path)
		return nil
	}
	if b != nil {
		if err := os.WriteFile(path+".trace-backup", b, 0o600); err != nil {
			return err
		}
	}
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", append(out, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	verb := "added"
	if exe == "" {
		verb = "removed"
	}
	fmt.Fprintf(w, "hooks %s in %s: %s\n", verb, path, strings.Join(changed, ", "))
	if b != nil {
		fmt.Fprintf(w, "previous file kept as %s.trace-backup\n", path)
	}
	fmt.Fprintln(w, "restart open Claude Code sessions: they read hooks when they start")
	return nil
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
	if runtime.GOOS == "windows" {
		exe = filepath.ToSlash(exe)
	}
	return exe, nil
}
