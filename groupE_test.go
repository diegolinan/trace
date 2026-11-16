package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A transcript rewritten while its first line is still being written keeps the old raw copy (SPEC I7).
func TestRewriteCaughtMidLineKeepsOldRawCopy(t *testing.T) {
	f := newMutFix(t)
	old := mutLines(mutUser("w1", 0, "uno"), mutUser("w2", 1, "dos"))
	f.write(t, "w1.jsonl", old)
	f.importAll(t)
	line := mutLine(mutUser("w3", 2, "otro"))
	f.write(t, "w1.jsonl", line[:20])
	f.importAll(t)
	f.write(t, "w1.jsonl", line)
	f.importAll(t)
	olds := f.replaced("w1.jsonl")
	if len(olds) != 1 {
		t.Fatalf("old raw copies = %v, want one", olds)
	}
	if got := mutGunzip(t, olds[0]); got != old {
		t.Errorf("old raw copy = %q, want %q", got, old)
	}
	if got := mutGunzip(t, f.raw("w1.jsonl")); got != line {
		t.Errorf("new raw copy = %q, want %q", got, line)
	}
}

// importFails runs one import that must fail, then checks it kept nothing of the session.
func (f *mutFix) importFails(t *testing.T, sid string) {
	t.Helper()
	if err := runImport(f.db, filepath.Join(f.dir, "projects"), f.history(), "", filepath.Join(f.dir, "raw"), "acme", "test"); err == nil {
		t.Fatal("import succeeded, want it to report the failed file")
	}
	if got := count(t, f.db, "select count(*) from event where session_id = ?", sid); got != 0 {
		t.Errorf("events kept from a failed import = %d, want 0", got)
	}
}

// A raw copy that cannot be written fails the import and keeps nothing; the next run reads the file again (SPEC I8).
func TestRawCopyFailureKeepsNothing(t *testing.T) {
	f := newMutFix(t)
	text := mutLine(mutUser("x1", 0, "hola"))
	f.write(t, "x1.jsonl", text)
	blocker := filepath.Join(f.dir, "raw", proj)
	os.MkdirAll(filepath.Dir(blocker), 0o755)
	os.WriteFile(blocker, nil, 0o644)
	f.importFails(t, "x1")
	os.Remove(blocker)
	f.importAll(t)
	if got := count(t, f.db, "select count(*) from event where session_id = 'x1'"); got != 1 {
		t.Errorf("events after the raw folder came back = %d, want 1", got)
	}
	if got := mutGunzip(t, f.raw("x1.jsonl")); got != text {
		t.Errorf("raw copy = %q, want %q", got, text)
	}
}

// failInserts makes every insert into table fail, as a full disk would, until the returned func is called.
func failInserts(t *testing.T, f *mutFix, table string) func() {
	t.Helper()
	if _, err := f.db.Exec("create trigger fail_" + table + " before insert on " + table + " begin select raise(abort, 'disk full'); end"); err != nil {
		t.Fatal(err)
	}
	return func() { f.db.Exec("drop trigger fail_" + table) }
}

// A session name that cannot be stored fails the import; the next run stores it (SPEC I8).
func TestNameStoreFailureKeepsNothing(t *testing.T) {
	f := newMutFix(t)
	f.write(t, "y1.jsonl", mutLines(mutUser("y1", 0, "hola"), mutObj{"type": "custom-title", "customTitle": "mi-nombre"}))
	heal := failInserts(t, f, "session_name")
	f.importFails(t, "y1")
	heal()
	f.importAll(t)
	if got := mutStr(t, f.db, "select group_concat(name) from session_name where session_id = 'y1'"); got != "mi-nombre" {
		t.Errorf("names = %s, want mi-nombre", got)
	}
}

// A rule-file text that cannot be stored fails the import; the next run stores it (SPEC I8).
func TestRuleTextStoreFailureKeepsNothing(t *testing.T) {
	f := newMutFix(t)
	loaded := mutObj{"type": "attachment", "uuid": "z2", "timestamp": mutAt(1), "attachment": mutObj{"type": "instructions",
		"files": []mutObj{{"path": "CLAUDE.md", "type": "Project", "content": "NONCE_RULE keep answers short"}}}}
	f.write(t, "z1.jsonl", mutLines(mutUser("z1", 0, "hola"), loaded))
	heal := failInserts(t, f, "loaded_text")
	f.importFails(t, "z1")
	heal()
	f.importAll(t)
	if got := mutStr(t, f.db, "select group_concat(text) from loaded_text"); !strings.Contains(got, "NONCE_RULE") {
		t.Errorf("rule texts = %s, want the one loaded", got)
	}
}
