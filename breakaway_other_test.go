//go:build !windows

package main

// jobForbidsBreakaway: only Windows has jobs; elsewhere the alarm always starts on its own.
func jobForbidsBreakaway() bool { return false }
