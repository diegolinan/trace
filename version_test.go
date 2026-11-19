package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// SPEC E3: trace version answers with no home and with a broken config.json, under all three spellings.
func TestVersionCommand(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "trace"), 0o755)
	os.WriteFile(filepath.Join(root, "trace", "config.json"), []byte("{not json"), 0o644)
	for _, arg := range []string{"version", "-version", "--version"} {
		out, errOut, code := runTrace(t, root, "", nil, arg)
		if code != 0 || !strings.HasPrefix(out, "trace ") {
			t.Errorf("%s: exit %d, out %q\n%s", arg, code, out, errOut)
		}
	}
}

// SPEC E3: the stamped tag wins, then the module version, then dev with the commit.
func TestVersionString(t *testing.T) {
	commit := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}}}
	for _, c := range []struct {
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"v0.3.36", commit, "trace v0.3.36"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "v0.3.35"}}, "trace v0.3.35"},
		{"", commit, "trace dev (0123456789ab)"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "trace dev"},
		{"", nil, "trace dev"},
		{"", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.time", Value: "2026-11-19T10:00:00Z"}}}, "trace dev"},
		{"", &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789ab"}}}, "trace dev (0123456789ab)"},
	} {
		if got := versionString(c.stamped, c.info); got != c.want {
			t.Errorf("versionString(%q, ...) = %q, want %q", c.stamped, got, c.want)
		}
	}
}
