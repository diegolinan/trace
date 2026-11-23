package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// SPEC E6: a flag after the words is refused with exit 2, before any home is read or written; a word that only
// starts with "-" stays a word; a hook still exits 0 with nothing on stdout.
func TestFlagAfterWordsRefused(t *testing.T) {
	root := t.TempDir()
	def, other := filepath.Join(root, "trace"), filepath.Join(root, "other")
	for _, c := range []struct {
		flag string
		args []string
	}{
		{"-home", []string{"search", "hello", "-home", other}},
		{"-home", []string{"search", "hello", "--home", other}},
		{"-home", []string{"search", "hello", "-home=" + other}},
		{"-who", []string{"search", "hello", "-who=user"}},
		{"-all", []string{"search", "hello", "world", "-all"}},
		{"-n", []string{"show", "s#1", "-n", "3"}},
		{"-home", []string{"task", "done", "3", "-home", other}},
		{"-decision", []string{"rule", "add", "-quote", "a b c", "x", "-decision"}},
		{"-home", []string{"stats", "foo", "-home", other}},
	} {
		_, errOut, code := runTrace(t, root, "", nil, c.args...)
		if code != 2 {
			t.Errorf("%q: exit %d, want 2: %s", c.args, code, errOut)
		}
		if !strings.Contains(errOut, c.flag) || !strings.Contains(errOut, "before") {
			t.Errorf("%q: the message does not name %s and where it goes: %s", c.args, c.flag, errOut)
		}
		if exists(def) || exists(other) {
			t.Fatalf("%q: a home was created before the refusal", c.args)
		}
	}
	// Words that start with "-" but name no flag, and one quoted word with a space, stay words.
	if _, errOut, code := runTrace(t, root, "", nil, "search", "-projects=", "-history=", "-codex=",
		"hello", "-5%", "-5", "-foo", "-", "-who user", "home=x"); code == 2 {
		t.Errorf("words starting with - were refused: %s", errOut)
	}
	if out, _, code := runTrace(t, root, `{"hook_event_name":"Stop"}`, nil, "hook", "x", "-home", other); code != 0 || out != "" {
		t.Errorf("hook with a flag after a word: exit %d, stdout %q", code, out)
	}
}
