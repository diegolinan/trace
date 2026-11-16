package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Behaviour tests for ingest.go, clean.go, store.go, checks.go and config.go (quality gates, group A).

// gaStore is a fresh store in a temporary folder, closed when the test ends.
func gaStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The store has one connection: a read that starts while an import's transaction is open waits for it, and then
// sees what the import stored, never the store as it was before.
func TestGaReadWaitsForOpenImport(t *testing.T) {
	db := gaStore(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into session(id, host, role) values ('s1', 'claude-code', 'r')`); err != nil {
		t.Fatal(err)
	}
	read := make(chan int, 1)
	go func() {
		n := -1
		db.QueryRow("select count(*) from session").Scan(&n)
		read <- n
	}()
	select {
	case n := <-read:
		tx.Rollback()
		t.Fatalf("a read ran beside the open import and saw %d sessions", n)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := <-read; n != 1 {
		t.Fatalf("the read saw %d sessions after the import, want 1", n)
	}
}

// A store from before redaction gets every old text redacted when opened, not only the first one of each table
// (SPEC P1).
func TestGaOldRowsRedactedEveryOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	tok := "gh" + "p_" + strings.Repeat("Q7", 18)
	for _, q := range []string{
		`insert into card(sha, first_seen, text) values ('a', 'now', 'one ` + tok + `')`,
		`insert into card(sha, first_seen, text) values ('b', 'now', 'two ` + tok + `')`,
		`insert into card(sha, first_seen, text) values ('c', 'now', 'three ` + tok + `')`,
		`pragma user_version = 1`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	if db, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := count(t, db, "select count(*) from card where text like ?", "%"+tok+"%"); n != 0 {
		t.Fatalf("%d of 3 old cards still hold the secret", n)
	}
}

// Configured anchors are tagged in the order of their names, whatever order config.json gives them in.
func TestGaExtraAnchorsInNameOrder(t *testing.T) {
	smKeepCfg(t)
	cfg.Anchors = map[string]string{"zeta": `\bZ\d+\b`, "alpha": `\bA\d+\b`, "mid": `\bM\d+\b`}
	compileConfig()
	got := anchors("Z1 M2 A3", "reply", "")
	want := []anchorHit{{"alpha", "A3"}, {"mid", "M2"}, {"zeta", "Z1"}}
	if len(got) != len(want) {
		t.Fatalf("tags %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tags %v, want %v", got, want)
		}
	}
}
