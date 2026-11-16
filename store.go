package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// Layer 0 only: what happened, verbatim. Derived records (decisions, rejections, vectors) arrive with the code
// that uses them.
const schema = `
create table if not exists source_file (
  id integer primary key,
  host text not null,
  path text not null unique,
  size integer,
  mtime text,
  read_to_byte integer not null default 0,
  archived_as text,
  last_error text
);
create table if not exists ingest_run (
  id integer primary key,
  started_at text not null,
  ended_at text,
  trigger text,
  files_seen integer,
  events_added integer,
  error text
);
create table if not exists session (
  id text primary key,
  host text not null,
  role text not null,
  parent_id text,
  agent_type text,
  cwd text,
  branch_first text,
  branch_last text,
  version_first text,
  model text,
  title text,
  started_at text,
  ended_at text,
  active_secs integer,
  compactions integer not null default 0,
  excluded text
);
-- Every name a session was given, keyed by its id: sessions get renamed, so the id is who and a name is a label.
create table if not exists session_name (
  session_id text not null,
  name text not null,
  first_seen text,
  primary key (session_id, name)
);
create table if not exists loaded_text (
  sha text primary key,
  first_seen text,
  text text
);
create table if not exists event (
  id integer primary key,
  uuid text unique,
  session_id text not null references session(id),
  seq integer not null,
  ts text not null,
  speaker text not null,
  kind text not null,
  text text,
  tool_name text,
  tool_input text,
  tool_use_id text,
  is_error integer,
  exit_code integer,
  branch text,
  compaction_no integer,
  loaded_sha text,
  file_id integer references source_file(id),
  byte_offset integer,
  redacted text
);
create index if not exists event_session on event(session_id, seq);
create index if not exists event_ts on event(ts);
create index if not exists event_tooluse on event(tool_use_id);
create table if not exists unmapped (
  signature text primary key,
  count integer not null,
  last_seen text
);
create table if not exists anchor (
  event_id integer not null references event(id),
  type text not null,
  value text not null,
  verified integer
);
create index if not exists anchor_value on anchor(type, value);
-- Re-injection (reinject.go): each card text once, and each decision to inject or to restart the count.
create table if not exists card (
  sha text primary key,
  first_seen text,
  text text
);
create table if not exists injection (
  id integer primary key,
  session_id text not null,
  ts text not null,
  ctx_tokens integer not null,
  reason text not null,
  card_sha text,
  chars integer
);
create index if not exists injection_session on injection(session_id, id);
-- The instructions a Codex rollout last recorded, and how far it has been read (codexSources): rollouts only grow.
create table if not exists codex_instructions (
  rollout text primary key,
  offset integer not null,
  text text not null
);
-- The instruction files a Claude Code transcript last recorded as loaded, as JSON, and how far it has been read
-- (claudeSources): transcripts only grow.
create table if not exists claude_instructions (
  transcript text primary key,
  offset integer not null,
  files text not null
);
-- Every run of a configured check (checks.go): passes too, so the failure rate has its denominator.
create table if not exists check_run (
  id integer primary key,
  ts text not null,
  session_id text,
  name text not null,
  tool text,
  target text,
  ok integer not null,
  output text
);
create virtual table if not exists event_fts using fts5(text, content='event', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
create virtual table if not exists event_tri using fts5(text, content='event', content_rowid='id', tokenize='trigram');
create trigger if not exists event_ai after insert on event begin
  insert into event_fts(rowid, text) values (new.id, new.text);
  insert into event_tri(rowid, text) values (new.id, new.text);
end;
-- A changed or deleted text must leave both indexes too, or old words stay searchable (cass #537); forget needs it.
create trigger if not exists event_ad after delete on event begin
  insert into event_fts(event_fts, rowid, text) values ('delete', old.id, old.text);
  insert into event_tri(event_tri, rowid, text) values ('delete', old.id, old.text);
end;
create trigger if not exists event_au after update of text on event begin
  insert into event_fts(event_fts, rowid, text) values ('delete', old.id, old.text);
  insert into event_tri(event_tri, rowid, text) values ('delete', old.id, old.text);
  insert into event_fts(rowid, text) values (new.id, new.text);
  insert into event_tri(rowid, text) values (new.id, new.text);
end;
`

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// busy_timeout first: pragmas run in order, and journal_mode needs the lock that parallel hooks hold.
	db, err := driver.Open("file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)&_pragma=journal_size_limit(4194304)",
		func(c *sqlite3.Conn) error { return fts5.Register(c) })
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer connection (mnemos; ncruces #404 on Windows pools)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate brings a store of any age to the current schema: the tables, the columns added after the first import,
// and the one-time mends.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := addColumns(db); err != nil {
		return err
	}
	if err := mendZstKeys(db); err != nil {
		return err
	}
	return mendRedaction(db)
}

// addColumns adds the columns added after the first import; "duplicate column" means the store already has them.
func addColumns(db *sql.DB) error {
	for _, alter := range []string{
		"alter table source_file add column tail_sha text",                            // hash of the bytes just before read_to_byte
		"alter table source_file add column bad_lines integer not null default 0",     // lines no JSON parse could recover
		"alter table source_file add column archive_bytes integer not null default 0", // raw copy length at the last commit
		"alter table source_file add column skipped text",                             // why a file is never read (a Codex thread outside scope)
	} {
		_, err := db.Exec(alter)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
		if err == nil && strings.Contains(alter, "archive_bytes") {
			// Copies made before the column existed: their current size is the committed length.
			if err := backfillArchiveBytes(db); err != nil {
				return err
			}
		}
	}
	return nil
}

// mendRedaction runs once per store (user_version 1 to 2): text stored outside events before it went through redact
// is redacted now (SPEC P1). Each table is read whole first, then written, so no write runs under an open read.
func mendRedaction(db *sql.DB) error {
	var v int
	if err := db.QueryRow("pragma user_version").Scan(&v); err != nil || v >= 2 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, tc := range [][2]string{{"check_run", "target"}, {"check_run", "output"}, {"loaded_text", "text"},
		{"claude_instructions", "files"}, {"codex_instructions", "text"}, {"card", "text"}} {
		rows, err := tx.Query("select rowid, " + tc[1] + " from " + tc[0] + " where " + tc[1] + " is not null")
		if err != nil {
			return err
		}
		fixed := map[int64]string{}
		for rows.Next() {
			var id int64
			var s string
			rows.Scan(&id, &s)
			if r := redacted(s); r != s {
				fixed[id] = r
			}
		}
		rows.Close()
		for id, s := range fixed {
			if _, err := tx.Exec("update "+tc[0]+" set "+tc[1]+" = ? where rowid = ?", s, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec("pragma user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}

// mendZstKeys runs once per store (user_version 0 to 1). Codex rows without an ordinal used to be keyed by the
// compressed file's name too, so one row could be stored twice. Each copy whose plain-keyed twin exists is removed
// with its tags; the rest take the plain-name key (SPEC I3).
func mendZstKeys(db *sql.DB) error {
	var v int
	if err := db.QueryRow("pragma user_version").Scan(&v); err != nil || v >= 1 {
		return err
	}
	const zst = "uuid like 'codex:%:@%.jsonl.zst:%'"
	const twin = "exists (select 1 from event p where p.uuid = replace(event.uuid, '.jsonl.zst:', '.jsonl:'))"
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"delete from anchor where event_id in (select id from event where " + zst + " and " + twin + ")",
		"delete from event where " + zst + " and " + twin,
		"update event set uuid = replace(uuid, '.jsonl.zst:', '.jsonl:') where " + zst,
		"pragma user_version = 1",
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func backfillArchiveBytes(db *sql.DB) error {
	rows, err := db.Query("select id, archived_as from source_file where archived_as is not null")
	if err != nil {
		return err
	}
	sizes := map[int64]int64{}
	for rows.Next() {
		var id int64
		var p string
		rows.Scan(&id, &p)
		if st, err := os.Stat(p); err == nil {
			sizes[id] = st.Size()
		}
	}
	rows.Close()
	for id, n := range sizes {
		if _, err := db.Exec("update source_file set archive_bytes = ? where id = ?", n, id); err != nil {
			return err
		}
	}
	return nil
}

// openReadOnly is for search and stats: no schema, no migration, never a writer (claude-mem #3233, engram #1967).
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return driver.Open("file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(3000)",
		func(c *sqlite3.Conn) error { return fts5.Register(c) })
}
