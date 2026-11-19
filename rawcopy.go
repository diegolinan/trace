package main

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
)

// keepLine is which lines of path go into its raw copy: with a scope, only the prompt log's lines in scope (SPEC P3);
// nil keeps every byte.
func (im *importer) keepLine(path string) func([]byte) bool {
	if filepath.Base(path) != "history.jsonl" || strings.TrimSpace(im.scope) == "" {
		return nil
	}
	return func(line []byte) bool {
		var h struct {
			Project string `json:"project"`
		}
		json.Unmarshal(line, &h)
		return inScope(h.Project, im.scope)
	}
}

// copyLines copies r to w; when keep is set, only the lines it accepts.
func copyLines(w io.Writer, r io.Reader, keep func([]byte) bool) error {
	if keep == nil {
		_, err := io.Copy(w, r)
		return err
	}
	b, err := io.ReadAll(r) // the prompt log's new span: a few MB at most
	if err != nil {
		return err
	}
	for _, line := range bytes.SplitAfter(b, []byte("\n")) {
		if keep(line) {
			if _, err := w.Write(line); err != nil {
				return err
			}
		}
	}
	return nil
}
