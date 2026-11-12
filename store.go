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
	db, err := driver.Open("file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_pragma=synchronous(normal)&_pragma=foreign_keys(on)&_pragma=journal_size_limit(4194304)",
		func(c *sqlite3.Conn) error { return fts5.Register(c) })
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer connection (mnemos; ncruces #404 on Windows pools)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Columns added after the first import; "duplicate column" means the database already has them.
	for _, alter := range []string{
		"alter table source_file add column tail_sha text",                            // hash of the bytes just before read_to_byte
		"alter table source_file add column bad_lines integer not null default 0",     // lines no JSON parse could recover
		"alter table source_file add column archive_bytes integer not null default 0", // raw copy length at the last commit
		"alter table source_file add column skipped text",                             // why a file is never read (a Codex thread outside scope)
	} {
		_, err := db.Exec(alter)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, err
		}
		if err == nil && strings.Contains(alter, "archive_bytes") {
			// Copies made before the column existed: their current size is the committed length.
			if err := backfillArchiveBytes(db); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	return db, nil
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
