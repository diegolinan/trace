package main

import (
	"reflect"
	"testing"
)

// Taking this memory's hooks out of an event's groups leaves everything else as it was: a group that is not an
// object, a group with none of ours, and the other hooks (and the matcher) of a group that held ours too.
func TestWithoutMine(t *testing.T) {
	mineHook := map[string]any{"command": "trace", "args": []any{"hook", "-home", "H"}}
	other := map[string]any{"command": "lint"}
	mine := func(h any) bool { return reflect.DeepEqual(h, mineHook) }
	groups := []any{
		"not an object",
		map[string]any{"hooks": []any{other}},
		map[string]any{"matcher": "Bash", "hooks": []any{mineHook, other}},
		map[string]any{"hooks": []any{mineHook}},
	}
	got := withoutMine(groups, mine)
	want := []any{
		"not an object",
		map[string]any{"hooks": []any{other}},
		map[string]any{"matcher": "Bash", "hooks": []any{other}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutMine:\n got %v\nwant %v", got, want)
	}
	if inner := groups[2].(map[string]any)["hooks"].([]any); len(inner) != 2 {
		t.Fatalf("the settings read in were changed in place: %v", inner)
	}
	if withoutMine(nil, mine) != nil {
		t.Fatal("no groups must give none")
	}
}
