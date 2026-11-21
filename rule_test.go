package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ruleRoot is a session s1 where the user typed a rule, then a go, then a decision.
func ruleRoot(t *testing.T) string {
	return tkRoot(t, mutLines(
		mutUser("u1", 0, "mirá, de acá en más trabajamos TDD y SDD, siempre"),
		mutUser("u2", 1, "dale"),
		mutUser("u3", 2, "TRACE no depende de git, sacalo"),
	))
}

// SPEC D1: the record is tied to the newest message of the user in the calling session that holds the quote, matched
// without accents or case, and keeps the user's own words as typed.
func TestRuleAddTiesToTheQuotedMessage(t *testing.T) {
	root := ruleRoot(t)
	out := tkTrace(t, root, "rule", "add", "-quote", "de aca en mas trabajamos TDD")
	if !strings.Contains(out, `rule 1 recorded: "de acá en más trabajamos TDD"`) {
		t.Errorf("add printed %q", out)
	}
	db := tkStore(t, root)
	if got := mutStr(t, db, `select substr(e.text, 1, 5) || ':' || r.quote || ':' || r.decision || ':' || r.session_id from rule r join event e on e.id = r.event_id`); got != "mirá,:de acá en más trabajamos TDD:0:s1" {
		t.Errorf("stored %q", got)
	}
	if out := tkTrace(t, root, "rule", "add", "-decision", "-quote", "no depende de git"); !strings.Contains(out, "rule 2 recorded") {
		t.Errorf("decision: %q", out)
	}
	if got := mutStr(t, db, `select decision from rule where id = 2`); got != "1" {
		t.Errorf("decision stored as %q", got)
	}
}

// SPEC D1: no quote, a short one, one no message holds, or a message already recorded in force: nothing is stored, and
// a quote that matches nothing shows the start of the user's last messages.
func TestRuleAddRefuses(t *testing.T) {
	root := ruleRoot(t)
	tkTrace(t, root, "rule", "add", "-quote", "trabajamos TDD y SDD")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"rule", "add"}, "-quote"},
		{[]string{"rule", "add", "-quote", "TDD SDD"}, "at least 3 words"},
		{[]string{"rule", "add", "-quote", "nada que ver con esto"}, "TRACE no depende de git"},
		{[]string{"rule", "add", "-quote", "TDD y SDD, siempre"}, "already recorded"},
		{[]string{"rule", "add", "-quote", "no depende de git", "-replaces", "9"}, "not in force"},
	} {
		out, errOut, code := rdRun(t, root, c.args...)
		if code == 0 || !strings.Contains(out+errOut, c.want) {
			t.Errorf("%v: exit %d, %q %q", c.args, code, out, errOut)
		}
	}
	if got := mutStr(t, tkStore(t, root), `select count(*) from rule`); got != "1" {
		t.Errorf("%s records", got)
	}
}

// SPEC D2, D3: -replaces and drop put a record out of force, deleting nothing; list shows those in force, newest
// first, and -all also those out of force with what replaced them or that they were dropped.
func TestRuleReplaceDropList(t *testing.T) {
	root := ruleRoot(t)
	tkTrace(t, root, "rule", "add", "-quote", "trabajamos TDD y SDD")
	tkTrace(t, root, "rule", "add", "-decision", "-quote", "no depende de git")
	if out := tkTrace(t, root, "rule", "add", "-quote", "de acá en más", "-replaces", "1"); !strings.Contains(out, "replaces 1") {
		t.Errorf("replace printed %q", out)
	}
	if out := tkTrace(t, root, "rule", "drop", "2"); !strings.Contains(out, "rule 2: dropped") {
		t.Errorf("drop printed %q", out)
	}
	list := tkTrace(t, root, "rule", "list")
	if !strings.Contains(list, "#3") || strings.Contains(list, "#1 ") || strings.Contains(list, "#2 ") {
		t.Errorf("list: %q", list)
	}
	all := tkTrace(t, root, "rule", "list", "-all")
	if !strings.Contains(all, "replaced by #3") || !strings.Contains(all, "dropped") || !strings.Contains(all, "decision") {
		t.Errorf("list -all: %q", all)
	}
	if got := mutStr(t, tkStore(t, root), `select count(*) from rule`); got != "3" {
		t.Errorf("%s records", got)
	}
	if _, _, code := rdRun(t, root, "rule", "drop", "2"); code == 0 {
		t.Error("dropped twice")
	}
}

// ruleRow stores a record of event ev as the agent would; decision marks a decision, out its time out of force.
func ruleRow(t *testing.T, db *sql.DB, ev int64, quote string, decision bool, added, out string) {
	t.Helper()
	rzExec(t, db, `insert into rule(event_id, quote, decision, session_id, added_at, out_at) values (?, ?, ?, 'old', ?, nullif(?, ''))`, ev, quote, decision, added, out)
}

// ruleEvent stores a typed message of the user in session sid and returns its id.
func ruleEvent(t *testing.T, db *sql.DB, sid string, seq int, ts, text string) int64 {
	t.Helper()
	rzEvent(t, db, sid, seq, ts, "user", "prompt", text)
	var id int64
	db.QueryRow(`select id from event where session_id = ? and seq = ?`, sid, seq).Scan(&id)
	return id
}

// SPEC D4: once a store has records, the block shows them first, whatever host recorded them, from the folder, one inside
// it or one above it, rules newest first, decisions only from the last 14 days, none out of force or no longer the
// user's; the word-pattern section is gone. It is shown even when there is no earlier session to resume.
func TestRuleBlockShowsRecords(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	base := t.TempDir()
	f := filepath.Join(base, "site")
	old := clockNow
	clockNow = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { clockNow = old })
	rzSession(t, db, "old", projectRole(base), base, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	rzExec(t, db, `update session set host = 'codex' where id = 'old'`)
	rzSession(t, db, "sib", projectRole(base+"-x"), filepath.Join(base+"-x"), "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	a := ruleEvent(t, db, "old", 1, "2026-11-10T08:01:00Z", "de acá en más, TDD y SDD siempre")
	b := ruleEvent(t, db, "old", 2, "2026-11-10T08:02:00Z", "parches no, lo hablamos mil veces")
	c := ruleEvent(t, db, "old", 3, "2026-11-01T08:03:00Z", "usamos Jev para juzgar, decidido")
	d := ruleEvent(t, db, "old", 4, "2026-11-20T08:04:00Z", "TRACE no depende de git, sacalo")
	e := ruleEvent(t, db, "old", 5, "2026-11-10T08:05:00Z", "nunca publiques sin avisar")
	g := ruleEvent(t, db, "sib", 1, "2026-11-10T08:06:00Z", "regla de otra carpeta hermana")
	ruleRow(t, db, a, "TDD y SDD siempre", false, "2026-11-10T08:01:00Z", "")
	ruleRow(t, db, b, "parches no, lo hablamos", false, "2026-11-10T08:02:00Z", "")
	ruleRow(t, db, c, "usamos Jev para juzgar", true, "2026-11-01T08:03:00Z", "")
	ruleRow(t, db, d, "TRACE no depende de git", true, "2026-11-20T08:04:00Z", "")
	ruleRow(t, db, e, "nunca publiques sin avisar", false, "2026-11-10T08:05:00Z", "2026-11-11T08:00:00Z")
	ruleRow(t, db, g, "regla de otra carpeta hermana", false, "2026-11-10T08:06:00Z", "")
	rzExec(t, db, `update event set speaker = 'program' where id = ?`, a)
	got := rzBlock(t, db, f) // no earlier session in site: only the records
	head := "set as rules and decisions"
	if !strings.Contains(got, head) || strings.Contains(got, "refused or set as a rule") {
		t.Fatalf("block:\n%s", got)
	}
	for _, want := range []string{"#2", "parches no, lo hablamos", "#4", "TRACE no depende de git", "rule add -quote"} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"TDD y SDD", "Jev", "publiques", "hermana"} {
		if strings.Contains(got, not) {
			t.Errorf("block shows %q:\n%s", not, got)
		}
	}
}

// SPEC D4: in the card the recorded rules come before the instruction files, so the budget cuts the files first.
func TestCardRulesBeforeFiles(t *testing.T) {
	text, _ := card([]source{{label: "CLAUDE.md", text: strings.Repeat("file line\n", 5)}}, "### RULES\n- #1 kept\n", 1000)
	if i, j := strings.Index(text, "RULES"), strings.Index(text, "file line"); i < 0 || j < 0 || i > j {
		t.Errorf("rules at %d, files at %d:\n%s", i, j, text)
	}
}

// SPEC D5: a change of the card names each record that left it.
func TestCardChangeNamesRecordsOutOfForce(t *testing.T) {
	old := "### rules\n- 2026-11-10 08:01 #1 rule: \"a b c\"\n- 2026-11-10 08:02 #2 rule: \"d e f\"\n"
	cur := "### rules\n- 2026-11-10 08:02 #2 rule: \"d e f\"\n- 2026-11-21 09:00 #3 rule: \"g h i\"\n"
	got := cardChange(old, cur)
	if !strings.Contains(got, "#3") || !strings.Contains(got, "out of force: #1") || strings.Contains(got, "#2") {
		t.Errorf("change:\n%s", got)
	}
}

// ruleNetStore is a store with session s1 where the user typed n messages, and its path.
func ruleNetStore(t *testing.T, n int) (string, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "m.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	rzExec(t, db, `insert into session(id, host, role) values ('s1', 'claude-code', 'r')`)
	for i := 1; i <= n; i++ {
		rzEvent(t, db, "s1", i, fmt.Sprintf("2026-11-21T10:%02d:00Z", i), "user", "prompt", fmt.Sprintf("mensaje numero %d", i))
	}
	return dbPath, db
}

// SPEC D6: after 10 typed messages with no record, the next prompt gives the agent the question and those messages,
// once; 9 messages, a subagent, or a record made after them give nothing.
func TestRuleNetAtPrompt(t *testing.T) {
	smKeepCfg(t)
	cfg.Checks, cfg.Reinject = nil, nil
	in := hookInput{Event: "UserPromptSubmit", SessionID: "s1"}
	if dbPath, _ := ruleNetStore(t, 9); smHook(t, in, dbPath, false) != "" {
		t.Error("asked after 9 messages")
	}
	dbPath, db := ruleNetStore(t, 10)
	if got := smHook(t, in, dbPath, false); !strings.Contains(got, "not recorded") || !strings.Contains(got, "mensaje numero 1") || !strings.Contains(got, "mensaje numero 10") {
		t.Errorf("net: %q", got)
	}
	if got := smHook(t, in, dbPath, false); got != "" {
		t.Errorf("asked twice: %q", got)
	}
	sub := in
	sub.AgentID = "a1"
	dbPath2, db2 := ruleNetStore(t, 12)
	if got := smHook(t, sub, dbPath2, false); got != "" {
		t.Errorf("a subagent got the net: %q", got)
	}
	rzExec(t, db2, `insert into rule(event_id, quote, session_id, added_at) values (1, 'x y z', 's1', '2026-11-21T10:30:00Z')`)
	if got := smHook(t, in, dbPath2, false); got != "" {
		t.Errorf("asked after a record: %q", got)
	}
	_ = db
}

// SPEC D7: after a compaction the block carries the question with the user's messages since the last record, even
// with the card off.
func TestRuleNetAtCompaction(t *testing.T) {
	smKeepCfg(t)
	cfg.Reinject = nil
	dbPath, _ := ruleNetStore(t, 3)
	var w strings.Builder
	in := hookInput{Event: "SessionStart", Source: "compact", SessionID: "s1"}
	if err := compactCard(in, dbPath, "r", "claude-code", &w); err != nil {
		t.Fatal(err)
	}
	if got := w.String(); !strings.Contains(got, "not recorded") || !strings.Contains(got, "mensaje numero 3") {
		t.Errorf("compaction net: %q", got)
	}
}

// SPEC D9: a subagent is told not to record the user's rules.
func TestSubagentToldNotToRecordRules(t *testing.T) {
	var w strings.Builder
	if err := contextHook(hookInput{Event: "SubagentStart"}, "r", "claude-code", "", false, &w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), "record rules") {
		t.Errorf("subagent line: %q", w.String())
	}
}

// SPEC D1: a quote of exactly 3 words that ends the message is found; a quote that matches nothing lists the newest 3
// messages of the user and not an older one; a second drop says the rule is not in force.
func TestRuleAddEdges(t *testing.T) {
	root := tkRoot(t, mutLines(
		mutUser("u0", 0, "el mensaje mas viejo de todos"),
		mutUser("u1", 1, "mirá, de acá en más trabajamos TDD y SDD, siempre"),
		mutUser("u2", 2, "dale"),
		mutUser("u3", 3, "TRACE no depende de git, sacalo"),
	))
	if _, _, code := rdRun(t, root, "rule", "add", "-quote", "git, sacalo"); code == 0 {
		t.Error("2 words accepted")
	}
	if out := tkTrace(t, root, "rule", "add", "-quote", "de git, sacalo"); !strings.Contains(out, `rule 1 recorded: "de git, sacalo"`) {
		t.Errorf("3 words at the end: %q", out)
	}
	_, errOut, _ := rdRun(t, root, "rule", "add", "-quote", "nada que ver aca")
	if !strings.Contains(errOut, "- TRACE no depende") || !strings.Contains(errOut, "- dale") || !strings.Contains(errOut, "- mirá") || strings.Contains(errOut, "mas viejo") {
		t.Errorf("last messages: %q", errOut)
	}
	tkTrace(t, root, "rule", "drop", "1")
	if _, errOut, _ := rdRun(t, root, "rule", "drop", "1"); !strings.Contains(errOut, "rule 1 is not in force") {
		t.Errorf("second drop: %q", errOut)
	}
}

// ruleBlockStore is a store with one ended session in base and n rules and m decisions recorded from it, all on
// 2026-11-10 except the decisions, made `days` before the pinned clock.
func ruleBlockStore(t *testing.T, base string, n, m, days int) *sql.DB {
	t.Helper()
	db := rzDB(t)
	old := clockNow
	clockNow = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { clockNow = old })
	rzSession(t, db, "old", projectRole(base), base, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	for i := 1; i <= n+m; i++ {
		ts := fmt.Sprintf("2026-11-10T08:%02d:00Z", i)
		ev := ruleEvent(t, db, "old", i, ts, fmt.Sprintf("regla numero %02d dicha aca", i))
		if i > n {
			ts = clockNow().Add(-time.Duration(days)*24*time.Hour + time.Duration(i)*time.Second).UTC().Format(time.RFC3339)
		}
		ruleRow(t, db, ev, fmt.Sprintf("regla numero %02d", i), i > n, ts, "")
	}
	return db
}

// SPEC D4: up to 8 rules and 5 decisions, the decisions no older than 14 days, then how many more are in force.
func TestRuleBlockLimits(t *testing.T) {
	rzNoRoles(t)
	base := t.TempDir()
	got := rzBlock(t, ruleBlockStore(t, base, 9, 6, 13), base)
	for _, want := range []string{"regla numero 02", "regla numero 09", "regla numero 11", "regla numero 15", "and 2 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"regla numero 01\"", "regla numero 10\""} {
		if strings.Contains(got, not) {
			t.Errorf("shows %q:\n%s", not, got)
		}
	}
	base2 := t.TempDir()
	if got, _, _ := strings.Cut(rzBlock(t, ruleBlockStore(t, base2, 8, 1, 15), base2), "## Where"); strings.Contains(got, "regla numero 09") || !strings.Contains(got, "regla numero 08") || !strings.Contains(got, "and 1 more") {
		t.Errorf("a decision 15 days old, or the count:\n%s", got)
	}
	base3 := t.TempDir()
	if got := rzBlock(t, ruleBlockStore(t, base3, 8, 0, 0), base3); strings.Contains(got, "more:") {
		t.Errorf("more with none left:\n%s", got)
	}
}

// SPEC D4: records only from another folder: no section, no word-pattern section either, and the instruction still in
// the block; with roles, the role decides, not the folder.
func TestRuleBlockScope(t *testing.T) {
	rzNoRoles(t)
	base := t.TempDir()
	db := ruleBlockStore(t, filepath.Join(base, "a"), 1, 0, 0)
	f := filepath.Join(base, "b")
	rzSession(t, db, "here", projectRole(f), f, "2026-11-10T10:00:00Z", "2026-11-10T11:00:00Z", 60)
	rzEvent(t, db, "here", 1, "2026-11-10T10:30:00Z", "user", "prompt", "no hace falta usar archivos")
	got := rzBlock(t, db, f)
	if strings.Contains(got, "regla numero") || strings.Contains(got, "set as rules") || strings.Contains(got, "refused or set as a rule") || !strings.Contains(got, "rule add -quote") {
		t.Errorf("out of scope:\n%s", got)
	}
	cfg.Roles = []roleRule{{Name: "x"}}
	var out strings.Builder
	if err := resumeBlock(db, "new", "startup", projectRole(filepath.Join(base, "a")), "claude-code", f, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"regla numero 01"`) {
		t.Errorf("with roles, the same role in another folder:\n%s", out.String())
	}
}

// SPEC D6, D7: more than 20 messages are cut to the newest 20, said so; a compaction with no message since the last
// record adds nothing.
func TestRuleNetEdges(t *testing.T) {
	msgs := make([]ruleTyped, 21)
	for i := range msgs {
		msgs[i] = ruleTyped{ts: "2026-11-21T10:00:00Z", text: fmt.Sprintf("m%02d", i)}
	}
	if got := ruleNetText(msgs); !strings.Contains(got, "newest 20 of 21") || strings.Contains(got, "m00") || !strings.Contains(got, "m20") {
		t.Errorf("21: %q", got)
	}
	if got := ruleNetText(msgs[:20]); strings.Contains(got, "newest 20") {
		t.Errorf("20: %q", got)
	}
	_, db := ruleNetStore(t, 0)
	if got := compactNet(db, "s1"); got != "" {
		t.Errorf("no messages: %q", got)
	}
}

// SPEC D1: a quote that starts the message is found.
func TestRuleQuoteAtStart(t *testing.T) {
	root := ruleRoot(t)
	if out := tkTrace(t, root, "rule", "add", "-quote", "TRACE no depende"); !strings.Contains(out, `rule 1 recorded: "TRACE no depende"`) {
		t.Errorf("quote at the start: %q", out)
	}
}

// SPEC D4, D9: with no record in the store the block keeps the word-pattern section and tells the agent how to record,
// once; with records it shows them, not the word-pattern section, and the instruction once.
func TestRuleBlockInstructionOnce(t *testing.T) {
	rzNoRoles(t)
	db := rzDB(t)
	f := t.TempDir()
	rzSession(t, db, "old", projectRole(f), f, "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	rzRefusal(t, db, "old", 1, "2026-11-10T08:01:00Z", "no hace falta usar archivos")
	got := rzBlock(t, db, f)
	if strings.Count(got, "rule add -quote") != 1 || !strings.Contains(got, "refused or set as a rule") {
		t.Errorf("no records:\n%s", got)
	}
	ev := ruleEvent(t, db, "old", 2, "2026-11-10T08:02:00Z", "parches no, lo hablamos mil veces")
	ruleRow(t, db, ev, "parches no, lo hablamos", false, "2026-11-10T08:02:00Z", "")
	got = rzBlock(t, db, f)
	if strings.Count(got, "rule add -quote") != 1 || strings.Contains(got, "refused or set as a rule") || !strings.Contains(got, `"parches no, lo hablamos"`) {
		t.Errorf("records:\n%s", got)
	}
}

// SPEC D4: records from a folder inside and from a session with no folder are in scope; a decision exactly 14 days old
// is shown.
func TestRuleBlockInsideAndEdgeOfWindow(t *testing.T) {
	rzNoRoles(t)
	base := t.TempDir()
	db := ruleBlockStore(t, filepath.Join(base, "in"), 1, 0, 0)
	rzSession(t, db, "nofolder", "r0", "", "2026-11-10T08:00:00Z", "2026-11-10T09:00:00Z", 60)
	ev := ruleEvent(t, db, "nofolder", 1, "2026-11-10T08:00:00Z", "decision sin carpeta alguna aca")
	ruleRow(t, db, ev, "decision sin carpeta", true, clockNow().Add(-14*24*time.Hour).UTC().Format(time.RFC3339), "")
	got := rzBlock(t, db, base)
	if !strings.Contains(got, `"regla numero 01"`) || !strings.Contains(got, `"decision sin carpeta"`) {
		t.Errorf("inside, no folder, 14 days:\n%s", got)
	}
}

// SPEC D7: one message since the last record is enough after a compaction.
func TestRuleNetCompactionOneMessage(t *testing.T) {
	_, db := ruleNetStore(t, 1)
	if got := compactNet(db, "s1"); !strings.Contains(got, "mensaje numero 1") {
		t.Errorf("one message: %q", got)
	}
}

// SPEC D3, D4: a record shows when the user said it, not when the agent recorded it.
func TestRuleShowsWhenSaid(t *testing.T) {
	rzNoRoles(t)
	db := ruleBlockStore(t, t.TempDir(), 0, 0, 0)
	ev := ruleEvent(t, db, "old", 1, "2026-11-10T08:01:00Z", "de aca en mas TDD siempre")
	ruleRow(t, db, ev, "de aca en mas TDD", false, "2026-11-21T03:54:00Z", "")
	said, recorded := local("2026-11-10T08:01:00Z"), local("2026-11-21T03:54:00Z")
	got := rzBlock(t, db, "")
	if !strings.Contains(got, said+" #1 rule") || strings.Contains(got, recorded) {
		t.Errorf("block:\n%s", got)
	}
	var w strings.Builder
	if err := ruleList(db, opts{}, &w); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.String(), said) || strings.Contains(w.String(), recorded) {
		t.Errorf("list: %q", w.String())
	}
}
