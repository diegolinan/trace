package main

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC P3: with a scope, the prompt log's raw copy keeps only the lines in scope; without one, every line.
func TestScopedPromptLogCopy(t *testing.T) {
	for _, scope := range []string{"acme", ""} {
		root := t.TempDir()
		claude := filepath.Join(root, "claude")
		os.MkdirAll(claude, 0o755)
		log := `{"display":"in scope words","timestamp":1763000000000,"project":"C:\\work\\acme","sessionId":"a1"}` + "\n" +
			`{"display":"other project words","timestamp":1763000001000,"project":"C:\\private\\bank","sessionId":"b1"}` + "\n"
		os.WriteFile(filepath.Join(claude, "history.jsonl"), []byte(log), 0o644)
		if scope != "" {
			os.MkdirAll(filepath.Join(root, "trace"), 0o755)
			os.WriteFile(filepath.Join(root, "trace", "config.json"), []byte(`{"scope":"`+scope+`"}`), 0o644)
		}
		if _, errOut, code := runTrace(t, root, "", nil, "import", "-codex="); code != 0 {
			t.Fatalf("scope %q: import exit %d\n%s", scope, code, errOut)
		}
		copied := gunzipAll(t, filepath.Join(root, "trace", "raw", ".claude", "history.jsonl.gz"))
		if !strings.Contains(copied, "in scope words") {
			t.Errorf("scope %q: the in-scope line is missing from the copy:\n%s", scope, copied)
		}
		if got := strings.Contains(copied, "other project words"); got != (scope == "") {
			t.Errorf("scope %q: other project's line copied = %v\n%s", scope, got, copied)
		}
	}
}

func gunzipAll(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	return string(b)
}

// copyLines keeps exactly the lines keep accepts, the last one too when it has no newline, and returns a read or
// write failure instead of a short copy.
func TestCopyLines(t *testing.T) {
	var out strings.Builder
	keep := func(l []byte) bool { return strings.HasPrefix(string(l), "y") }
	if err := copyLines(&out, strings.NewReader("y1\nn2\ny3\nn4\ny5"), keep); err != nil || out.String() != "y1\ny3\ny5" {
		t.Errorf("got %q, %v", out.String(), err)
	}
	if err := copyLines(failWriter{}, strings.NewReader("y1\n"), keep); err == nil {
		t.Error("a failed write was not returned")
	}
	if err := copyLines(&out, failReader{}, keep); err == nil {
		t.Error("a failed read was not returned")
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
