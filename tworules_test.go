package main

import (
	"strings"
	"testing"
)

// SPEC D1a: two quotes from one message whose spans do not overlap are both stored; an overlapping one is refused
// naming the record it overlaps; -replaces frees only its own record's span; a drop frees its span.
func TestTwoRulesInOneMessage(t *testing.T) {
	root := tkRoot(t, mutLines(
		mutUser("u1", 0, "usá el logo, sacá el neon de todos lados. No crees imagenes vos solo, después las hacemos en foto."),
	))
	add := func(args ...string) (string, int) {
		out, errOut, code := rdRun(t, root, append([]string{"rule", "add"}, args...)...)
		return out + errOut, code
	}
	if out, code := add("-quote", "usá el logo, sacá el neon de todos lados"); code != 0 {
		t.Fatalf("first rule: %d %s", code, out)
	}
	if out, code := add("-quote", "No crees imagenes vos solo"); code != 0 {
		t.Fatalf("second rule of the same message refused: %s", out)
	}
	for _, c := range []struct {
		args []string
		id   string
	}{
		{[]string{"-quote", "el neon de todos"}, "#1"},                       // inside the first
		{[]string{"-quote", "usá el logo, sacá el neon de todos lados"}, "#1"}, // the same again
		{[]string{"-quote", "de todos lados. No crees"}, "#"},                // across both
		{[]string{"-quote", "vos solo, después las", "-replaces", "1"}, "#2"}, // replacing #1 over #2's words
	} {
		if out, code := add(c.args...); code == 0 || !strings.Contains(out, "already recorded in force as "+c.id) {
			t.Errorf("%q: exit %d, %s", c.args, code, out)
		}
	}
	if out, code := add("-quote", "el logo, sacá el neon", "-replaces", "1"); code != 0 {
		t.Errorf("rewording #1 within its own words: %s", out)
	}
	tkTrace(t, root, "rule", "drop", "2")
	if out, code := add("-quote", "No crees imagenes vos solo"); code != 0 {
		t.Errorf("a dropped record's words stay taken: %s", out)
	}
	// The words between #3 and #4 touch both and overlap neither.
	if out, code := add("-quote", "de todos lados."); code != 0 {
		t.Errorf("words right next to two records refused: %s", out)
	}
	if got := mutStr(t, tkStore(t, root), `select group_concat(id) from (select id from rule where out_at is null order by id)`); got != "3,4,5" {
		t.Errorf("in force: %s, want 3,4,5", got)
	}
	if !strings.Contains(ruleInstruction, "A message with two rules gets two records, each with its own quote.") {
		t.Error("the agent is not told that a message can hold two rules (D9)")
	}
}
