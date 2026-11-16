package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A store from before archive_bytes existed gets the column, filled with each raw copy's size on disk.
func TestArchiveBytesBackfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	copyFile := filepath.Join(dir, "s1.jsonl.gz")
	os.WriteFile(copyFile, make([]byte, 1234), 0o644)
	db.Exec(`insert into source_file(host, path, archived_as) values ('claude-code', 't1', ?), ('claude-code', 't2', ?)`, copyFile, filepath.Join(dir, "gone.gz"))
	if _, err := db.Exec("alter table source_file drop column archive_bytes"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := count(t, db, "select archive_bytes from source_file where path = 't1'"); n != 1234 {
		t.Errorf("backfilled %d bytes, want 1234", n)
	}
	if n := count(t, db, "select archive_bytes from source_file where path = 't2'"); n != 0 {
		t.Errorf("a missing copy got %d bytes, want 0", n)
	}
}

// One import at a time: while the lock is held, importOnce waits up to its limit, then skips without running; once
// free, it runs.
func TestImportOnceWaitsThenSkips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	held, err := tryLock(path + ".lock")
	if err != nil || held == nil {
		t.Fatalf("lock: %v", err)
	}
	ran := false
	run := func(*sql.DB) error { ran = true; return nil }
	start := time.Now()
	if err := importOnce(path, 700*time.Millisecond, run); err != nil || ran {
		t.Fatalf("with the lock held: err %v, ran %v", err, ran)
	}
	if d := time.Since(start); d < 700*time.Millisecond || d > 3*time.Second {
		t.Errorf("waited %v for a 700 ms limit", d)
	}
	held.Close()
	if err := importOnce(path, 0, run); err != nil || !ran {
		t.Fatalf("with the lock free: err %v, ran %v", err, ran)
	}
}
