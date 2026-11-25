package main

import (
	"regexp"
	"strings"
	"testing"
)

// Every rule, lesson and task command TRACE tells an agent to run, with a home that is not the default one, is
// read by trace's own parser as that subcommand in that home (SPEC E6, "The commands TRACE tells an agent").
func TestToldCommandsTakeAnotherHome(t *testing.T) {
	smKeepCfg(t) // parseArgs loads the other home's config into the global one
	def, other := t.TempDir(), t.TempDir()
	t.Setenv("TRACE_HOME", def)
	old := activeHome
	t.Cleanup(func() { activeHome = old })
	activeHome = other

	var b strings.Builder
	writeRuleInstruction(&b, "Ana")
	writeLessonInstruction(&b, "Ana")
	b.WriteString(ruleNetText([]ruleTyped{{"2026-11-25T10:00:00Z", "de acá en más, x"}}))
	if err := taskHelp(&b, "Ana"); err != nil {
		t.Fatal(err)
	}
	text := b.String()
	activeHome = old

	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(text, -1) {
		line := m[1]
		if strings.HasPrefix(line, "& ") || !strings.Contains(line, "-home") {
			continue // the PowerShell twin of a line already checked, or not a command
		}
		words := shellWords(line)
		if len(words) < 3 {
			continue
		}
		cmd, args := words[1], words[2:]
		o, err := parseArgs(cmd, args)
		if err != nil {
			t.Errorf("%s: refused: %v", line, err)
			continue
		}
		if !sameHome(o.home, other) {
			t.Errorf("%s: home %q, want %q", line, o.home, other)
		}
		if len(o.args) == 0 || strings.HasPrefix(o.args[0], "-") {
			t.Errorf("%s: subcommand read as %q", line, o.args)
			continue
		}
		seen[cmd+" "+o.args[0]] = true
	}
	activeHome = old
	for _, want := range []string{"rule add", "rule drop", "lesson add", "lesson drop", "task add", "task done", "task drop"} {
		if !seen[want] {
			t.Errorf("no %q command with -home was told", want)
		}
	}
}

// shellWords splits a told command line on spaces outside double quotes and drops the quotes.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			in, has = !in, true
		case r == ' ' && !in:
			if has {
				out = append(out, cur.String())
			}
			cur.Reset()
			has = false
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}
