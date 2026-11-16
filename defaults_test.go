package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Pins the card's limits: 7 and 8000 when the setting is absent, zero or negative; the setting otherwise.
func TestReinjectLimits(t *testing.T) {
	for _, c := range []struct {
		r      *reinjectCfg
		factor float64
		budget int
	}{
		{nil, 7, 8000},
		{&reinjectCfg{}, 7, 8000},
		{&reinjectCfg{Factor: -1, Budget: -5}, 7, 8000},
		{&reinjectCfg{Factor: 3.5, Budget: 1200}, 3.5, 1200},
	} {
		if f, b := c.r.limits(); f != c.factor || b != c.budget {
			t.Errorf("%+v: limits %v %v, want %v %v", c.r, f, b, c.factor, c.budget)
		}
	}
}

// Pins the homes: the variable when set, else the folder in the user's home.
func TestHomes(t *testing.T) {
	u, _ := os.UserHomeDir()
	for _, c := range []struct {
		env, sub string
		get      func() string
	}{{"TRACE_HOME", ".trace", traceHome}, {"CODEX_HOME", ".codex", codexHome}} {
		t.Setenv(c.env, "")
		if got := c.get(); got != filepath.Join(u, c.sub) {
			t.Errorf("%s unset: %s", c.env, got)
		}
		t.Setenv(c.env, filepath.Join(u, "elsewhere"))
		if got := c.get(); got != filepath.Join(u, "elsewhere") {
			t.Errorf("%s set: %s", c.env, got)
		}
	}
}
