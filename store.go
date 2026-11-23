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
-- The user's tasks (SPEC T): asked_event and closed_event are the typed messages that asked for and closed each one.
-- No foreign key: an event can be removed (mendZstKeys) and its task must stay.
create table if not exists task (
  id integer primary key,
  text text not null,
  due text,
  asked_event integer,
  created_at text not null,
  closed_at text,
  closed_how text,
  closed_event integer,
  announced_at text
);
-- The sessions the prompt hook named each fired, untold task in, so it names it once per session (SPEC A7).
create table if not exists task_named (
  task_id integer not null,
  session_id text not null,
  primary key (task_id, session_id)
);
-- A task's conditions of ready (SPEC T5, T6): another task (after_id) or words (needs), numbered per task; met_event is the
-- typed message the user met it with (task met), none when its task was closed as done.
create table if not exists task_condition (
  task_id integer not null,
  n integer not null,
  after_id integer,
  needs text,
  met_at text,
  met_session text,
  met_event integer,
  primary key (task_id, n)
);
-- A rule or decision of the user, recorded by the agent (SPEC D): the message that set it, the quote shown from it,
-- and, once out of force, when and what replaced it (none: dropped).
create table if not exists rule (
  id integer primary key,
  event_id integer not null,
  quote text not null,
  decision integer not null default 0,
  session_id text,
  added_at text not null,
  out_at text,
  out_session text,
  replaced_by integer
);
-- A lesson an agent learned by testing (SPEC L): its text and the evidence quoted from the tool result (event_id) that
-- showed it, both redacted and clipped; a machine lesson or one of the folder cwd and role; once out of force, as rule.
create table if not exists lesson (
  id integer primary key,
  text text not null,
  evidence text not null,
  event_id integer not null,
  machine integer not null default 0,
  cwd text not null,
  role text,
  session_id text not null,
  about text,
  added_at text not null,
  out_at text,
  out_session text,
  replaced_by integer
);
-- Each hook call that wrote to the agent: how many characters, for the context TRACE takes (SPEC K2).
create table if not exists hook_out (
  ts text not null,
  session_id text not null,
  event text not null,
  chars integer not null
);
-- The newest typed message the net at a prompt last asked about, per session (SPEC D6).
create table if not exists rule_ask (
  session_id text primary key,
  at text not null
);
-- A sprint (SPEC T18): a set of stories the user gave one date, its goal in the user's words, and the task that is its
-- review, due at its end. At most one is open.
create table if not exists sprint (
  id integer primary key,
  goal text not null,
  review_task integer,
  opened_at text not null,
  opened_session text,
  closed_at text,
  closed_session text
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
	if err := mendRedaction(db); err != nil {
		return err
	}
	return mendWorkerPrompts(db)
}

// mendWorkerPrompts runs once per store (user_version 2 to 3): what a program wrote in a session it started was
// stored as the user's before SPEC I9. MCP-started sessions of that time are not known as such and stay as they are.
func mendWorkerPrompts(db *sql.DB) error {
	var v int
	if err := db.QueryRow("pragma user_version").Scan(&v); err != nil || v >= 3 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update event set speaker = 'program' where speaker = 'user'
		and session_id in (select id from session where host = 'claude-code' and role = 'worker')`); err != nil {
		return err
	}
	if _, err := tx.Exec("pragma user_version = 3"); err != nil {
		return err
	}
	return tx.Commit()
}

// markNotTypedByUser stores as the program's what the user's sessions hold from the periods config.json says
// someone else typed in the user's terminal (SPEC I10). It runs on every import, so a period added later applies.
func markNotTypedByUser(db *sql.DB) error {
	for _, p := range cfg.NotTypedByUser {
		if _, err := db.Exec(`update event set speaker = 'program' where speaker = 'user' and (session_id = ?
			or substr(ts, 1, 10) between ? and ? and session_id in (select id from session where role <> 'subagent'))`,
			p.Session, p.From, p.To); err != nil {
			return err
		}
	}
	return nil
}

// addColumns adds the columns added after the first import; "duplicate column" means the store already has them.
// A column's fill runs once, when the column is added: the rows from before it get their value then.
func addColumns(db *sql.DB) error {
	for _, c := range []struct {
		alter string
		fill  func(*sql.DB) error
	}{
		{"alter table source_file add column tail_sha text", nil},                        // hash of the bytes just before read_to_byte
		{"alter table source_file add column bad_lines integer not null default 0", nil}, // lines no JSON parse could recover
		// Raw copy length at the last commit; for copies made before the column, their current size.
		{"alter table source_file add column archive_bytes integer not null default 0", backfillArchiveBytes},
		{"alter table source_file add column skipped text", nil}, // why a file is never read (a Codex thread outside scope)
		// The alarm's columns (SPEC A), all at once, so the store's shape is settled before the parts that use them.
		{"alter table task add column added_session text", nil}, // the session that ran task add (A5)
		{"alter table task add column fire_state text", nil},    // announced_at's claim: delivering, then delivered or failed (A2)
		{"alter table task add column fire_session text", nil},  // the session the fire targeted (A2, A5)
		{"alter table task add column fire_reason text", nil},   // why a fire failed (A2, A7)
		// When the user was told (A8); tasks T4 announced before the alarm existed count as told (A2).
		{"alter table task add column told_at text", fillWith("update task set told_at = announced_at where announced_at is not null")},
		{"alter table task add column holds integer not null default 0", nil},               // turns the stop hook held for this task (A8)
		{"alter table task add column marker text", nil},                                    // trace-alarm:<id>:<nonce> of the fire's message (A4, A9)
		{"alter table task add column zone text", nil},                                      // the UTC offset the time was said in (A10)
		{"alter table task add column self integer not null default 0", nil},                // added with -self: no message asked for it (T1)
		{"alter table task_condition add column done_when text", nil},                       // a criterion of done, in words (T8)
		{"alter table task add column level text not null default 'task'", nil},             // epic, story, task or subtask (T10)
		{"alter table task add column parent_id integer", nil},                              // the open task one level up (T11)
		{"alter table task add column moved_at text", nil},                                  // the last move or level change (T11)
		{"alter table task add column moved_session text", nil},                             // the session that ran it (T11)
		{"alter table task add column kind text", nil},                                      // a story's kind; none is story (T14)
		{"alter table task add column points integer", nil},                                 // a story's points, 1 to 13 (T15)
		{"alter table task add column started_at text", nil},                                // in progress since (T16)
		{"alter table task add column started_session text", nil},                           // the session that started it (T16)
		{"alter table task add column stopped_at text", nil},                                // back to to do, the last time (T16)
		{"alter table task add column stopped_session text", nil},                           // the session that stopped it (T16)
		{"alter table task add column rank integer", fillWith("update task set rank = id")}, // the order among siblings (T17); before it, oldest first
		{"alter table task add column sprint_id integer", nil},
		{"alter table rule add column about text", nil}, // the files or commands a record is about, a regexp (D11)                              // the sprint a story is in (T18)
	} {
		_, err := db.Exec(c.alter)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
		if err == nil && c.fill != nil {
			if err := c.fill(db); err != nil {
				return err
			}
		}
	}
	return nil
}

// fillWith is a migration fill that runs one statement.
func fillWith(statement string) func(*sql.DB) error {
	return func(db *sql.DB) error {
		_, err := db.Exec(statement)
		return err
	}
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
		if err := redactColumn(tx, tc[0], tc[1]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("pragma user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}

// redactColumn redacts every text in one column of one table: all of it read first, then the changed ones written.
func redactColumn(tx *sql.Tx, table, column string) error {
	rows, err := tx.Query("select rowid, " + column + " from " + table + " where " + column + " is not null")
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
		if _, err := tx.Exec("update "+table+" set "+column+" = ? where rowid = ?", s, id); err != nil {
			return err
		}
	}
	return nil
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

// openBrief opens an existing store for one short write, waiting at most a second for its lock and migrating nothing:
// the stop hook must never hold up the host's turn (SPEC A8).
func openBrief(path string) (*sql.DB, error) {
	return driver.Open("file:" + filepath.ToSlash(path) + "?mode=rw&_pragma=busy_timeout(1000)")
}
