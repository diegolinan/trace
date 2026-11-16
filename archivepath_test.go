package main

import (
	"path/filepath"
	"testing"
)

// The raw copy keeps the transcript's place, a subagent's beside its parent's, and the prompt log at
// .claude/history.jsonl.gz, where restore looks for it (SPEC R1).
func TestArchivePath(t *testing.T) {
	raw := filepath.Join("A", "raw")
	projects := filepath.Join("U", ".claude", "projects")
	for in, want := range map[string]string{
		filepath.Join(projects, "C--work", "s1.jsonl"):                                             filepath.Join(raw, "C--work", "s1.jsonl.gz"),
		filepath.Join(projects, "C--work", "s1", "subagents", "agent-x.jsonl"):                     filepath.Join(raw, "C--work", "s1", "subagents", "agent-x.jsonl.gz"),
		filepath.Join(projects, "C--work", "s1", "subagents", "workflows", "wf1", "agent-y.jsonl"): filepath.Join(raw, "C--work", "s1", "subagents", "workflows", "wf1", "agent-y.jsonl.gz"),
		filepath.Join("U", ".claude", "history.jsonl"):                                             filepath.Join(raw, ".claude", "history.jsonl.gz"),
	} {
		got, err := archivePath(raw, in)
		if err != nil || got != want {
			t.Errorf("archivePath(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
}
