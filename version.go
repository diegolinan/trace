package main

import "runtime/debug"

// version is the release tag, stamped in by the release build (-ldflags "-X main.version=<tag>"); empty otherwise.
var version string

// versionString is what `trace version` prints (SPEC E3): the stamped tag, else the module version Go records, else
// dev with the commit Go records.
func versionString(stamped string, info *debug.BuildInfo) string {
	if stamped != "" {
		return "trace " + stamped
	}
	if info == nil {
		return "trace dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return "trace " + v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return "trace dev (" + s.Value[:12] + ")"
		}
	}
	return "trace dev"
}

// isVersionArg says whether the first argument asks for the version.
func isVersionArg(a string) bool { return a == "version" || a == "-version" || a == "--version" }

func buildInfo() *debug.BuildInfo {
	info, _ := debug.ReadBuildInfo()
	return info
}
