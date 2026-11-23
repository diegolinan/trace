package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ruleTypedKinds are the kinds of event that carry words the user typed: prompts, steering, the words of a denied tool
// call and an answer to a question (SPEC D1).
const ruleTypedKinds = `('prompt', 'steering', 'refusal', 'answer')`

// ruleAddCall matches a tool call that runs trace rule add, for callerSession's fallback.
var ruleAddCall = regexp.MustCompile(`(?i)trace(?:\.exe)?\\?["']?\s+rule\s+add\b`)

// recordCmds are the subcommands of rule, the user's rules and decisions (SPEC D1 to D3), and of lesson, what agents
// learned by testing (L1 to L3), with how each command is used.
var recordCmds = map[string]struct {
	subs  map[string]func(*sql.DB, opts, io.Writer) error
	usage string
}{
	"rule": {map[string]func(*sql.DB, opts, io.Writer) error{"add": ruleAdd, "drop": ruleDrop, "list": ruleList},
		`rule needs add -quote "<the user's words>" [-decision] [-replaces <id>], drop <id> or list [-all]`},
	"lesson": {map[string]func(*sql.DB, opts, io.Writer) error{"add": lessonAdd, "drop": lessonDrop, "list": lessonList},
		`lesson needs add -text "<the lesson>" -evidence "<words of a tool result>" [-machine] [-about "<regexp>"] [-replaces <id>], drop <id> or list [-all]`},
}

// cmdRecords runs the subcommand of rule or lesson (o.cmd) named first in o.args. It reads what is new first, as task
// does, so the message or tool result quoted is in the store.
func cmdRecords(o opts) error {
	run := recordCmds[o.cmd].subs[first(o.args)]
	if run == nil {
		return usageError{errors.New(recordCmds[o.cmd].usage)}
	}
	if err := importNow(o.dbPath, o.importFn(o.cmd)); err != nil {
		fmt.Fprintf(os.Stderr, "trace: import before %s failed, using what is stored: %v\n", o.cmd, err)
	}
	db, err := openDB(o.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return run(db, o, os.Stdout)
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// ruleAdd ties a record to the newest message of the user in the calling session that holds the quote (SPEC D1).
func ruleAdd(db *sql.DB, o opts, w io.Writer) error {
	quote := ruleWords(o.quote)
	if len(quote) < 3 {
		return usageError{errors.New(`rule add needs -quote "<the user's words>", at least 3 words of one message; nothing stored`)}
	}
	session := callerSession(db)
	ev, said, err := ruleMessage(db, session, quote)
	if err != nil {
		return err
	}
	if _, err := regexp.Compile(o.about); err != nil {
		return usageError{fmt.Errorf("-about %q is not a regexp (%v); nothing stored", o.about, err)}
	}
	if o.replaces != "" && !inForce(db, "rule", o.replaces) {
		return fmt.Errorf("rule %s is not in force; nothing stored", o.replaces)
	}
	if id, err := overlapping(db, ev, quote, o.replaces); err != nil || id != 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("those words are already recorded in force as #%d (trace rule list); nothing stored", id)
	}
	now := clockNow().UTC().Format(time.RFC3339)
	res, err := db.Exec(`insert into rule(event_id, quote, decision, session_id, added_at, about)
		values (?, ?, ?, nullif(?, ''), ?, coalesce(nullif(?, ''), (select about from rule where id = ?)))`, ev, said, o.decision, session, now, o.about, o.replaces)
	return recorded(db, "rule", res, err, said, session, now, o.replaces, w)
}

// recorded says what an insert into table (rule or lesson) recorded and puts the one it replaces out of force (SPEC
// D1, L1).
func recorded(db *sql.DB, table string, res sql.Result, err error, said, session, now, replaces string, w io.Writer) error {
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	fmt.Fprintf(w, "%s %d recorded: %q\n", table, id, said)
	if replaces != "" {
		db.Exec(`update `+table+` set out_at = ?, out_session = nullif(?, ''), replaced_by = ? where id = ?`, now, session, id, replaces)
		fmt.Fprintf(w, "replaces %s\n", replaces)
	}
	return nil
}

// ruleMessage is the newest message of the user in the session whose words hold quote, as its id and the quoted span
// in the user's own spelling; when none does, an error with the start of the user's last 3 messages.
func ruleMessage(db *sql.DB, session string, quote []string) (int64, string, error) {
	id, said, last, err := quotedEvent(db, `speaker = 'user' and kind in `+ruleTypedKinds, session, quote)
	if err != nil || said != "" {
		return id, said, err
	}
	return 0, "", fmt.Errorf("no message of the user in this session holds those words; nothing stored. The last ones:\n%s", strings.Join(last, "\n"))
}

// quotedEvent is the newest event of the session matching where whose words hold quote, as its id and the quoted span
// in the event's own spelling; when none does, said is "" and last has the start of the newest 3.
func quotedEvent(db *sql.DB, where, session string, quote []string) (id int64, said string, last []string, err error) {
	rows, err := db.Query(`select id, text from event where session_id = ? and `+where+` order by ts desc, id desc limit 500`, session)
	if err != nil {
		return 0, "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		rows.Scan(&id, &text)
		if said := quoteIn(text, quote); said != "" {
			return id, said, nil, nil
		}
		if len(last) < 3 {
			last = append(last, "- "+clip(strings.Join(strings.Fields(text), " "), 80))
		}
	}
	return 0, "", last, rows.Err()
}

// quoteIn is the run of text's words that matches quote, folded, in text's own spelling, or "".
func quoteIn(text string, quote []string) string {
	if i := quoteAt(text, quote); i >= 0 {
		return strings.Join(strings.Fields(text)[i:i+len(quote)], " ")
	}
	return ""
}

// quoteAt is the index of the first of text's words where quote matches, folded, or -1.
func quoteAt(text string, quote []string) int {
	words := strings.Fields(text)
	for i := 0; i+len(quote) <= len(words); i++ {
		if ruleMatch(words[i:i+len(quote)], quote) {
			return i
		}
	}
	return -1
}

// overlapping is the id of a record in force of message ev, other than replaces, whose words overlap quote's words
// in the message, or 0: one message can hold two rules, but not the same words twice (SPEC D1a).
func overlapping(db *sql.DB, ev int64, quote []string, replaces string) (int64, error) {
	var text string
	if err := db.QueryRow(`select text from event where id = ?`, ev).Scan(&text); err != nil {
		return 0, err
	}
	at := quoteAt(text, quote)
	rows, err := db.Query(`select id, quote from rule where event_id = ? and out_at is null and id != ?`, ev, replaces)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var said string
		if err := rows.Scan(&id, &said); err != nil {
			return 0, err
		}
		w := ruleWords(said)
		if s := quoteAt(text, w); s < at+len(quote) && at < s+len(w) {
			return id, nil
		}
	}
	return 0, rows.Err()
}

func ruleMatch(words, quote []string) bool {
	for j, q := range quote {
		if ruleFold(words[j]) != q {
			return false
		}
	}
	return true
}

// ruleWords are a quote's words, folded, with empty ones left out.
func ruleWords(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if w := ruleFold(f); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// ruleFold is a word lower-cased, without accents, trimmed of punctuation at its ends.
func ruleFold(w string) string {
	return strings.TrimFunc(fold.Replace(strings.ToLower(w)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// inForce says whether a rule or a lesson (table) with this id is in force.
func inForce(db *sql.DB, table, id string) bool {
	return db.QueryRow(`select 1 from `+table+` where id = ? and out_at is null`, id).Scan(new(int)) == nil
}

// ruleDrop puts a record out of force with nothing replacing it: the user lifted it (SPEC D2).
func ruleDrop(db *sql.DB, o opts, w io.Writer) error { return dropRecord(db, "rule", o, w) }

// dropRecord puts a rule or a lesson (table) out of force with nothing replacing it (SPEC D2, L2).
func dropRecord(db *sql.DB, table string, o opts, w io.Writer) error {
	if len(o.args) < 2 {
		return usageError{fmt.Errorf("%[1]s drop needs a %[1]s id", table)}
	}
	res, err := db.Exec(`update `+table+` set out_at = ?, out_session = nullif(?, '') where id = ? and out_at is null`,
		clockNow().UTC().Format(time.RFC3339), callerSession(db), o.args[1])
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s %s is not in force; nothing changed", table, o.args[1])
	}
	fmt.Fprintf(w, "%s %s: dropped\n", table, o.args[1])
	return nil
}

// ruleList prints the records in force, newest first; with -all also those out of force (SPEC D3). A record whose
// message is no longer the user's (I9, I10) is never listed.
func ruleList(db *sql.DB, o opts, w io.Writer) error {
	where := "r.out_at is null"
	if o.all {
		where = "1"
	}
	rows, err := db.Query(`select r.id, e.ts, r.decision, r.quote, coalesce(r.replaced_by, 0), coalesce(r.out_at, ''), coalesce(r.about, '')
		from rule r join event e on e.id = r.event_id where ` + ruleUsersSQL + ` and ` + where + ` order by r.out_at is not null, r.id desc`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, by int64
		var at, quote, out, about string
		var decision bool
		rows.Scan(&id, &at, &decision, &quote, &by, &out, &about)
		fmt.Fprintf(w, "#%d %s %s: %q%s%s\n", id, local(at), ruleKind(decision), quote, aboutNote(about), ruleOut(by, out))
	}
	return rows.Err()
}

// ruleUsersSQL keeps the records whose message is still the user's (SPEC D2).
const ruleUsersSQL = `e.speaker = 'user' and e.kind in ` + ruleTypedKinds

func ruleKind(decision bool) string {
	if decision {
		return "decision"
	}
	return "rule"
}

// ruleOut is how a record out of force left: replaced by another, or dropped, and when.
func ruleOut(by int64, out string) string {
	switch {
	case out == "":
		return ""
	case by != 0:
		return fmt.Sprintf(" (replaced by #%d, %s)", by, local(out))
	}
	return " (dropped " + local(out) + ")"
}

// ruleInstruction tells the agent when to record (SPEC D9).
const ruleInstruction = "When %[1]s gives you a rule or a decision, record it with %[2]s, then tell %[1]s in one line what you recorded. " +
	"A rule is how you must work from now on, beyond the task at hand; a decision is a choice about the work that later sessions must keep. " +
	"Do not record a hold for the moment, an approval or go for one step, a request for one task, a question, a complaint, an opinion, " +
	"or a restatement of one already recorded. When %[1]s reverses or replaces one, record the new one with -replaces <id>; when %[1]s lifts one, %[3]s. " +
	"A message with two rules gets two records, each with its own quote. " +
	"When it is about certain files or commands, add -about \"<regexp>\" (for example -about \"publish\\.sh|git push\"): it then comes back right before such a tool call.\n"

// ruleRecord is a record in force as the block shows it.
type ruleRecord struct {
	id                      int64
	added, said, quote, sid string
	seq                     int
	decision                bool
	cwd, role, about        string
}

// ruleSection writes the records in force for a session of role in cwd, with the instruction, when there are any. It
// says whether the store has any record at all (until it does, the caller shows the word-pattern section instead) and
// whether it wrote (SPEC D4).
func ruleSection(db *sql.DB, w io.Writer, user, role, cwd string) (recorded, wrote bool, err error) {
	return rulesIn(db, w, user, role, cwd, capsAt(fitNone))
}

// rulesIn is ruleSection with the caps of a step that fits the hook limit (SPEC B2).
func rulesIn(db *sql.DB, w io.Writer, user, role, cwd string, c caps) (recorded, wrote bool, err error) {
	var n int
	db.QueryRow(`select count(*) from rule`).Scan(&n)
	if n == 0 {
		return false, false, nil
	}
	recs, err := ruleRecords(db, role, cwd)
	if err != nil || len(recs) == 0 {
		return true, false, err
	}
	writeRules(db, w, user, recs, c)
	writeRuleInstruction(w, user)
	return true, true, nil
}

// writeRuleInstruction tells the agent when and how to record (SPEC D9).
func writeRuleInstruction(w io.Writer, user string) {
	fmt.Fprintf(w, ruleInstruction+"\n", user, traceCmd("rule", ` add -quote "<the words>" [-decision]`), traceCmd("rule", " drop <id>"))
}

// ruleRecords are the records in force whose message is still the user's, from sessions in scope (SPEC D4): with roles,
// of the same role; without, in cwd, inside it or above it; any host. Newest first.
func ruleRecords(db *sql.DB, role, cwd string) ([]ruleRecord, error) {
	rows, err := db.Query(`select r.id, r.added_at, e.ts, r.quote, r.decision, e.session_id, e.seq, coalesce(s.cwd, ''), coalesce(s.role, ''), coalesce(r.about, '')
		from rule r join event e on e.id = r.event_id left join session s on s.id = e.session_id
		where r.out_at is null and ` + ruleUsersSQL + ` order by r.added_at desc, r.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ruleRecord
	for rows.Next() {
		var r ruleRecord
		rows.Scan(&r.id, &r.added, &r.said, &r.quote, &r.decision, &r.sid, &r.seq, &r.cwd, &r.role, &r.about)
		if ruleInScope(r, role, cwd) {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func ruleInScope(r ruleRecord, role, cwd string) bool {
	if len(cfg.Roles) > 0 {
		return r.role == role
	}
	return cwd == "" || r.cwd == "" || inFolder(r.cwd, cwd) || inFolder(cwd, r.cwd)
}

// writeRules writes up to 8 rules, then up to 5 decisions of the last 14 days (fewer to fit the hook limit), and how
// many more are in force.
func writeRules(db *sql.DB, w io.Writer, user string, recs []ruleRecord, c caps) {
	since := clockNow().UTC().Add(-14 * 24 * time.Hour).Format(time.RFC3339)
	var rules, decisions []ruleRecord
	for _, r := range recs {
		switch {
		case !r.decision && len(rules) < c.rules:
			rules = append(rules, r)
		case r.decision && r.added >= since && len(decisions) < c.decisions:
			decisions = append(decisions, r)
		}
	}
	fmt.Fprintf(w, "### What %[1]s set as rules and decisions (recorded; in force until replaced; what %[1]s says now wins)\n", user)
	for _, r := range append(rules, decisions...) {
		if c.answering {
			fmt.Fprintln(w, ruleLine(db, r))
		} else {
			fmt.Fprintln(w, hitWords(ruleHit(r)))
		}
	}
	if more := len(recs) - len(rules) - len(decisions); more > 0 {
		fmt.Fprintf(w, "and %d more: %s\n", more, traceCmd("rule", " list"))
	}
	fmt.Fprintln(w)
}

// ruleLine is a record as the block shows it: id, when, kind, the quote, and the end of the reply it answered.
func ruleLine(db *sql.DB, r ruleRecord) string { return refusalLine(db, ruleHit(r)) }

func ruleHit(r ruleRecord) lastingHit {
	return lastingHit{sid: r.sid, seq: r.seq, ts: r.said, text: `"` + r.quote + `"`, label: fmt.Sprintf("#%d %s", r.id, ruleKind(r.decision))}
}

// ruleNetQuestion asks the agent about messages it may not have recorded from (SPEC D6, D7).
const ruleNetQuestion = "Did %s set a rule or decision in these messages that you have not recorded? Record it with %s, or go on.\n"

// ruleTyped is one message the user typed: when, and its words.
type ruleTyped struct{ ts, text string }

// ruleTypedSince are the user's typed messages in session after the last record made in it and after since, oldest
// first.
func ruleTypedSince(db *sql.DB, session, since string) []ruleTyped {
	rows, err := db.Query(`select ts, text from event where session_id = ?1 and speaker = 'user' and kind in `+ruleTypedKinds+`
		and ts > max(coalesce((select max(added_at) from rule where session_id = ?1), ''), ?2) order by ts, id`, session, since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ruleTyped
	for rows.Next() {
		var m ruleTyped
		rows.Scan(&m.ts, &m.text)
		out = append(out, m)
	}
	return out
}

// ruleNetText is the question with the messages, the newest 20 at most, verbatim and clipped.
func ruleNetText(msgs []ruleTyped) string {
	var b strings.Builder
	fmt.Fprintf(&b, ruleNetQuestion, userName(), traceCmd("rule", ` add -quote "<the words>"`))
	if len(msgs) > 20 {
		fmt.Fprintf(&b, "(the newest 20 of %d)\n", len(msgs))
		msgs = msgs[len(msgs)-20:]
	}
	for _, m := range msgs {
		fmt.Fprintf(&b, "- %s: %s\n", local(m.ts), clip(strings.Join(strings.Fields(pasted.ReplaceAllString(m.text, "[pasted text]")), " "), 300))
	}
	return b.String()
}

// promptNet is the net at a prompt (SPEC D6): once the user has typed 10 messages in this session since the last record
// made in it and since the last time it was asked, the question with those messages; never for a subagent.
func promptNet(in hookInput, store *lazyStore) string {
	if in.Event != "UserPromptSubmit" || in.AgentID != "" || in.SessionID == "" {
		return ""
	}
	if _, err := os.Stat(store.path); err != nil {
		return ""
	}
	d := store.open()
	if d == nil {
		return ""
	}
	var asked string
	d.QueryRow(`select at from rule_ask where session_id = ?`, in.SessionID).Scan(&asked)
	msgs := ruleTypedSince(d, in.SessionID, asked)
	if len(msgs) < 10 {
		return ""
	}
	// The newest message shown, not the clock: a message imported late is still counted next time.
	d.Exec(`insert into rule_ask(session_id, at) values (?1, ?2) on conflict(session_id) do update set at = ?2`, in.SessionID, msgs[len(msgs)-1].ts)
	return ruleNetText(msgs)
}

// compactNet is the net after a compaction (SPEC D7): the question with the messages since the last record made in
// this session, or "" when there are none.
func compactNet(db *sql.DB, session string) string {
	if db == nil || session == "" {
		return ""
	}
	if msgs := ruleTypedSince(db, session, ""); len(msgs) > 0 {
		return ruleNetText(msgs) + "\n"
	}
	return ""
}
