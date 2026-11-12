package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHomeAndConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRACE_HOME", dir)
	if traceHome() != dir {
		t.Fatalf("TRACE_HOME ignored: %s", traceHome())
	}
	if c := loadConfig(dir); !reflect.DeepEqual(c, config{}) {
		t.Fatalf("no file should give defaults, got %+v", c)
	}
	if dataDir(dir, config{}) != dir {
		t.Fatal("no data setting should keep the data in the home")
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"data":"D:/d","repo":"D:/r","scope":"X"}`), 0o644)
	if c := loadConfig(dir); c.Repo != "D:/r" || c.Scope != "X" || dataDir(dir, c) != "D:/d" {
		t.Fatalf("config not read: %+v", c)
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{bad`), 0o644)
	if c := loadConfig(dir); !reflect.DeepEqual(c, config{}) {
		t.Fatalf("bad JSON should give defaults, got %+v", c)
	}
}

func TestMarkText(t *testing.T) {
	for text, want := range map[string]marks{
		"a, dale":                       {approval: true},
		"Sí, metele!!!":                 {approval: true},
		"pará. no te adelantes":         {rejection: true},
		"no entiendo, explicame":        {},
		"no se a que te referis":        {},
		"te dije que era en la mac":     {correction: true},
		"De acá en más guardalo en raw": {rule: true},
		"No cambié nada":                {},
		"dale <pasted_content id=\"x\">no hagas nada</pasted_content id=\"x\">": {approval: true},
	} {
		if got := markText(text); got != want {
			t.Errorf("%q: got %+v, want %+v", text, got, want)
		}
	}
}

// Scores the rules against Codex's blind labels of 200 real messages (kept in the data folder, never in git: they hold the user's words). Skipped when absent.
// Prompt-log rows are left out: history.jsonl cannot tell the user's typing from another agent's.
func TestMarksAgainstLabels(t *testing.T) {
	dir := filepath.Join(dataDir(traceHome(), loadConfig(traceHome())), "label") + "/"
	sf, err := os.Open(dir + "sample.jsonl")
	if err != nil {
		t.Skip("no labelled sample on this machine")
	}
	defer sf.Close()
	type msg struct {
		ID   int64  `json:"id"`
		Host string `json:"host"`
		Text string `json:"text"`
	}
	text := map[int64]string{}
	sc := bufio.NewScanner(sf)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m msg
		json.Unmarshal(sc.Bytes(), &m)
		if m.Host == "claude-code" {
			text[m.ID] = m.Text
		}
	}
	raw, err := os.ReadFile(dir + "labels.json")
	if err != nil {
		t.Skip("no labels")
	}
	var lab struct {
		Items []struct {
			ID                              int64 `json:"id"`
			Correction, Rejection, Approval bool
			Standing                        bool `json:"standing_rule"`
		} `json:"items"`
	}
	json.Unmarshal(raw, &lab)
	type score struct{ tp, fp, fn int }
	s := map[string]*score{"correction": {}, "rejection": {}, "approval": {}, "rule": {}}
	add := func(k string, got, want bool) {
		switch {
		case got && want:
			s[k].tp++
		case got:
			s[k].fp++
		case want:
			s[k].fn++
		}
	}
	n := 0
	for _, l := range lab.Items {
		tx, ok := text[l.ID]
		if !ok {
			continue
		}
		n++
		m := markText(tx)
		add("correction", m.correction, l.Correction)
		add("rejection", m.rejection, l.Rejection)
		add("approval", m.approval, l.Approval)
		add("rule", m.rule, l.Standing)
	}
	// Pinned on 2026-11-11, on the same 183 the rules were tuned on, so these are optimistic. A change may only do better.
	pin := map[string]score{"approval": {33, 3, 9}, "rejection": {8, 0, 10}, "correction": {13, 5, 34}, "rule": {2, 1, 3}}
	for k, v := range s {
		t.Logf("%-10s found %2d right, %2d wrong, missed %2d (of %d messages)", k, v.tp, v.fp, v.fn, n)
		if p := pin[k]; v.tp < p.tp || v.fp > p.fp {
			t.Errorf("%s got worse: %+v, pinned %+v", k, *v, p)
		}
	}
}
