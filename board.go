package main

import (
	"database/sql"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// boardCriterion is one criterion of done and whether it is met (SPEC T8, T24).
type boardCriterion struct {
	Text string
	Met  bool
}

// boardLink names another task on the page: its id and words.
type boardLink struct {
	ID   int64
	Text template.HTML
}

// boardCard is one task as the board page shows it (SPEC T24): short on its card, the rest in its detail.
type boardCard struct {
	ID                                             int64
	Level, Kind, State, Closed, Asked, Due, Sprint string
	Points                                         int64
	Text                                           template.HTML
	Epic                                           *boardLink
	Path, Children                                 []boardLink
	Waits                                          []string
	Criteria                                       []boardCriterion
	Met                                            int
	NoCriteria, Overdue                            bool
	Flags, Find                                    string
	parent                                         int64
	words                                          string
}

// boardColumn is one state's column: its key, its title and its cards.
type boardColumn struct {
	Key, Title string
	Cards      []*boardCard
}

// boardData is everything the page shows.
type boardData struct {
	When, Start, SprintGoal, SprintDue string
	Columns                            []boardColumn
	Epics, Details                     []*boardCard
	Counts                             [][3]string
}

// boardStates are the columns in order, by the key a card's State holds.
var boardStates = [][2]string{{"todo", "To do"}, {"waiting", "Waiting"}, {"progress", "In progress"}, {"closed", "Closed, last 14 days"}}

// taskBoard writes the board as one HTML page at path, prints where, and opens it (SPEC T24).
func taskBoard(db *sql.DB, path string, w io.Writer) error {
	d, err := boardRead(db)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := boardPage.Execute(f, d); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintln(w, "board written to", path)
	return openPage(path)
}

// boardRead reads the open tasks and those closed in the last 14 days, newest closed first, and lays them out.
func boardRead(db *sql.DB) (boardData, error) {
	now := clockNow()
	since := now.Add(-14 * 24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := db.Query(`select t.id, t.level, coalesce(t.kind, ''), case when t.level = 'epic' then coalesce((select sum(c.points) from task c
		where c.parent_id = t.id and coalesce(c.closed_how, '') != 'drop'), 0) else coalesce(t.points, 0) end, coalesce(t.parent_id, 0), t.text,
		coalesce(e.text, ''), t.self, coalesce(t.due, ''), coalesce(t.zone, ''), t.started_at is not null, coalesce(t.closed_how, ''),
		t.told_at is null, coalesce(t.sprint_id = (select id from sprint where closed_at is null), 0)
		from task t left join event e on e.id = t.asked_event where t.closed_at is null or t.closed_at >= ?
		order by t.closed_at is not null, t.closed_at desc, t.rank, t.id`, since)
	if err != nil {
		return boardData{}, err
	}
	var all []*boardCard
	for rows.Next() {
		all = append(all, boardScan(rows, now))
	}
	rows.Close() // read whole before asking more: the store has one connection
	if err := rows.Err(); err != nil {
		return boardData{}, err
	}
	d := boardData{When: showAt(now, time.Local)}
	var due, zone string
	db.QueryRow(`select s.goal, coalesce(r.due, ''), coalesce(r.zone, '') from sprint s left join task r on r.id = s.review_task
		where s.closed_at is null`).Scan(&d.SprintGoal, &due, &zone)
	d.SprintDue = dueText(due, zone, "")
	for _, c := range all {
		c.Waits, c.Criteria = unmetConditions(db, c.ID, boardCondition), boardCriteria(db, c.ID)
		if c.Sprint != "" {
			c.Sprint = d.SprintGoal
		}
	}
	boardLayout(&d, all)
	return d, nil
}

// boardScan is one row as a card: its words marked, its due time shown, and whether it is overdue and not told.
func boardScan(rows *sql.Rows, now time.Time) *boardCard {
	c := &boardCard{}
	var due, zone, how string
	var self, started, untold, inSprint bool
	rows.Scan(&c.ID, &c.Level, &c.Kind, &c.Points, &c.parent, &c.words, &c.Asked, &self, &due, &zone, &started, &how, &untold, &inSprint)
	c.Text, c.Due, c.Asked = boardMarks(c.words), dueText(due, zone, ""), cleanAsked(c.Asked)
	if self {
		c.Asked = "added by an agent on its own"
	}
	if inSprint {
		c.Sprint = "yes"
	}
	c.Closed = map[string]string{"done": "done", "drop": "dropped"}[how]
	at, err := time.Parse(time.RFC3339, due)
	c.Overdue = err == nil && c.Closed == "" && untold && at.Before(now)
	c.State = boardStateOf(c.Closed != "", started)
	return c
}

// boardStateOf is a card's column before its conditions are read: closed, in progress, or to do.
func boardStateOf(closed, started bool) string {
	switch {
	case closed:
		return "closed"
	case started:
		return "progress"
	}
	return "todo"
}

// boardLayout links the cards, marks them, puts every non-epic in its column, and counts the markers.
func boardLayout(d *boardData, all []*boardCard) {
	boardLinks(all)
	counts := map[string]int{}
	d.Columns = make([]boardColumn, len(boardStates))
	for i, s := range boardStates {
		d.Columns[i] = boardColumn{Key: s[0], Title: s[1]}
	}
	for _, c := range all {
		d.Details = append(d.Details, c)
		if c.Level == "epic" {
			if c.Closed == "" {
				d.Epics = append(d.Epics, c)
			}
			continue
		}
		boardMark(c)
		for _, f := range strings.Fields(c.Flags) {
			counts[f]++
		}
		d.Columns[boardColumnIndex(c.State)].Cards = append(d.Columns[boardColumnIndex(c.State)].Cards, c)
	}
	d.Start = "todo"
	if counts["progress"] > 0 {
		d.Start = "progress"
	}
	for _, f := range [][2]string{{"progress", "In progress"}, {"waiting", "Waiting"}, {"nocrit", "No criteria"}, {"overdue", "Overdue, not told"}} {
		d.Counts = append(d.Counts, [3]string{f[0], f[1], strconv.Itoa(counts[f[0]])})
	}
}

// boardLinks gives each card its children, the path down from its top task, and its epic.
func boardLinks(all []*boardCard) {
	byID := map[int64]*boardCard{}
	for _, c := range all {
		byID[c.ID] = c
	}
	for _, c := range all {
		if p := byID[c.parent]; p != nil {
			p.Children = append(p.Children, boardLink{c.ID, c.Text})
		}
		for p := byID[c.parent]; p != nil; p = byID[p.parent] {
			c.Path = append([]boardLink{{p.ID, p.Text}}, c.Path...)
			if p.Level == "epic" {
				c.Epic = &boardLink{p.ID, p.Text}
			}
		}
	}
}

// boardMark sets a card's state from what it waits on, its markers, its met criteria and the words a search reads.
func boardMark(c *boardCard) {
	if c.State == "todo" && len(c.Waits) > 0 {
		c.State = "waiting"
	}
	c.NoCriteria = c.Level == "story" && len(c.Criteria) == 0 && c.Closed == ""
	flags := []string{c.State, c.Closed}
	if c.NoCriteria {
		flags = append(flags, "nocrit")
	}
	if c.Overdue {
		flags = append(flags, "overdue")
	}
	c.Flags = strings.Join(strings.Fields(strings.Join(flags, " ")), " ")
	find := []string{"#" + strconv.FormatInt(c.ID, 10), c.words, c.Asked, c.Kind}
	for _, k := range c.Criteria {
		find = append(find, k.Text)
		if k.Met {
			c.Met++
		}
	}
	c.Find = strings.ToLower(strings.Join(append(find, c.Waits...), " "))
}

// boardColumnIndex is the index in boardStates of a state.
func boardColumnIndex(state string) int {
	for i, s := range boardStates {
		if s[0] == state {
			return i
		}
	}
	return 0
}

// boardCondition is one unmet condition on the board: the words it needs, or the task it comes after and its state.
func boardCondition(n, after int64, needs, how string, waits bool) string {
	if after == 0 {
		return strings.Join(strings.Fields(needs), " ")
	}
	return conditionText(n, after, needs, how, waits)
}

// boardCriteria is every criterion of done of a task, in order, met or not.
func boardCriteria(db *sql.DB, id int64) []boardCriterion {
	rows, err := db.Query(`select done_when, met_at is not null from task_condition where task_id = ? and done_when is not null order by n`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []boardCriterion
	for rows.Next() {
		var c boardCriterion
		rows.Scan(&c.Text, &c.Met)
		out = append(out, c)
	}
	return out
}

var (
	boardPastedRe = regexp.MustCompile(`(?s)<pasted_content[^>]*>.*?</pasted_content[^>]*>`)
	boldRe        = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	codeRe        = regexp.MustCompile("`([^`\n]+)`")
)

// cleanAsked is the asking words with pasted content named, not shown, on one line, clipped.
func cleanAsked(s string) string {
	return clip(strings.Join(strings.Fields(boardPastedRe.ReplaceAllString(s, " [pasted text] ")), " "), 600)
}

// boardMarks is a text escaped, with only **bold** and `code` rendered on the escaped text (SPEC T24).
func boardMarks(s string) template.HTML {
	e := template.HTMLEscapeString(s)
	e = boldRe.ReplaceAllString(e, "<strong>$1</strong>")
	return template.HTML(codeRe.ReplaceAllString(e, "<code>$1</code>"))
}

// openPage opens a file in the system's browser; a variable so tests do not open one.
var openPage = func(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	}
	return exec.Command("xdg-open", path).Start()
}

//go:embed fonts/*.woff2
var boardFonts embed.FS

// boardFontFaces is the @font-face rules with the three fonts inside the page, so it needs no network (SPEC T24).
func boardFontFaces() template.CSS {
	var b strings.Builder
	for _, f := range [][3]string{{"Die Grotesk", "500", "die-grotesk-medium"}, {"Die Grotesk Reading", "400", "die-grotesk-reading-regular"}, {"Space Mono", "400", "space-mono-regular"}} {
		data, _ := boardFonts.ReadFile("fonts/" + f[2] + ".woff2")
		fmt.Fprintf(&b, "@font-face { font-family: %q; font-weight: %s; font-display: swap; src: url(data:font/woff2;base64,%s) format(\"woff2\"); }\n",
			f[0], f[1], base64.StdEncoding.EncodeToString(data))
	}
	return template.CSS(b.String())
}

// boardPage is the page, after PAGE's guide for Diego's site; html/template escapes every text (SPEC T24).
var boardPage = template.Must(template.New("board").Funcs(template.FuncMap{"fonts": boardFontFaces, "more": func(n int) int { return n - 1 }}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>TRACE Tasks</title>
<style>
{{fonts}}
:root { --paper: #FBF8F5; --ink: #161515; --pink: #F17192; --cyan: #00C7D1; --yellow: #FDEA55;
  --bg: var(--paper); --fg: var(--ink); --muted: #5c5856; --line: #D3CDC8; --rule: #161515; --card: #FFFDFB; --focus: #000;
  --display: "Die Grotesk", "Arial Narrow", Arial, sans-serif; --read: "Die Grotesk Reading", "Helvetica Neue", Arial, sans-serif;
  --mono: "Space Mono", "SFMono-Regular", Consolas, monospace; color-scheme: light dark; }
:root[data-theme=light] { color-scheme: light; }
:root[data-theme=dark] { color-scheme: dark; --bg: #161515; --fg: #FBF8F5; --muted: #b9b3ae; --line: rgba(251,248,245,.2);
  --rule: #FBF8F5; --card: #1e1c1b; --focus: var(--yellow); }
@media (prefers-color-scheme: dark) { :root:not([data-theme=light]) { --bg: #161515; --fg: #FBF8F5; --muted: #b9b3ae;
  --line: rgba(251,248,245,.2); --rule: #FBF8F5; --card: #1e1c1b; --focus: var(--yellow); } }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 400 15px/1.45 var(--read); }
.top { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 20px; padding: 24px 32px 12px; }
h1 { font: 500 32px/1.1 var(--display); margin: 0; }
h1 span { color: var(--muted); }
.mono, .k, .when { font: 12px/1.5 var(--mono); }
.when { color: var(--muted); }
.note { flex-basis: 100%; color: var(--muted); font-size: 13px; margin: 0; overflow-wrap: anywhere; }
.sprint { margin: 0 32px 12px; padding: 10px 0; border-top: 1px solid var(--rule); border-bottom: 1px solid var(--rule);
  display: flex; flex-wrap: wrap; gap: 4px 16px; align-items: baseline; }
.sprint .goal { font-size: 15px; }
.bar { display: flex; flex-wrap: wrap; gap: 8px 12px; align-items: center; padding: 0 32px 12px; }
input[type=search], select { font: 15px var(--read); color: var(--fg); background: var(--card); border: 1px solid var(--rule);
  border-radius: 2px; padding: 8px 10px; min-height: 40px; }
input[type=search] { flex: 1 1 260px; max-width: 420px; min-width: 0; }
select { flex: 0 1 320px; min-width: 0; max-width: 100%; }
.counts { display: flex; flex-wrap: wrap; gap: 8px; padding: 0 32px 16px; align-items: center; }
.head { border-bottom: 1px solid var(--rule); background: var(--bg); }
@media (min-width: 761px) { body { height: 100vh; display: flex; flex-direction: column; overflow: hidden; }
  .head { flex: none; } .board { flex: 1 1 auto; overflow: auto; min-height: 0; } }
button { font: inherit; color: inherit; cursor: pointer; }
.counts button, .clear, .tabs button { background: none; border: 1px solid var(--line); border-radius: 2px; padding: 6px 10px;
  min-height: 36px; font: 13px var(--read); }
.counts button b, .tabs button b { font: 12px var(--mono); margin-left: 6px; }
.counts button[aria-pressed=true], .tabs button[aria-selected=true] { border-color: var(--rule); box-shadow: inset 0 -3px 0 var(--fg); }
.counts .results { font: 12px var(--mono); color: var(--muted); margin-left: auto; }
.clear[hidden] { display: none; }
.tabs { display: none; }
.board { padding: 0 32px 40px; }
.grid { display: grid; grid-template-columns: repeat(4, minmax(280px, 1fr)); gap: 20px; min-width: 1180px; }
.col h2 { position: sticky; top: 0; z-index: 1; background: var(--bg); font: 500 15px/1.2 var(--display); margin: 0 0 12px; padding: 20px 0 8px; border-bottom: 1px solid var(--rule);
  display: flex; justify-content: space-between; }
.col h2 b { font: 12px var(--mono); }
.col[data-col=todo] h2 { border-bottom: 3px solid var(--pink); }
.col[data-col=progress] h2 { border-bottom: 3px solid var(--cyan); }
.col[data-col=waiting] h2 { border-bottom: 3px solid var(--yellow); }
.cards { display: flex; flex-direction: column; gap: 12px; }
.empty { color: var(--muted); font-size: 13px; margin: 0; }
button.card { all: unset; box-sizing: border-box; display: block; cursor: pointer; background: var(--card); border: 1px solid var(--line);
  border-radius: 2px; padding: 14px 16px; }
button.card:hover { border-color: var(--rule); }
button:focus-visible, select:focus-visible, input:focus-visible { outline: 3px solid var(--focus); outline-offset: 3px; }
.card > span { display: block; }
.ctx { font-size: 12px; color: var(--muted); margin-bottom: 4px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.k { display: flex !important; gap: 8px; color: var(--muted); }
.k .pts { margin-left: auto; }
.t { font-size: 16px; line-height: 1.4; margin-top: 6px; overflow-wrap: anywhere;
  display: -webkit-box !important; -webkit-line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
.wait { margin-top: 10px; font-size: 13px; border-left: 3px solid var(--yellow); padding-left: 8px; }
.foot { margin-top: 10px; display: flex !important; flex-wrap: wrap; gap: 6px 10px; font: 12px var(--mono); color: var(--muted); }
.tag { color: var(--ink); padding: 0 6px; border: 1px solid var(--rule); border-radius: 2px; }
.tag.overdue { background: var(--pink); border-color: var(--pink); }
.tag.progress { background: var(--cyan); border-color: var(--cyan); }
.tag:not(.overdue):not(.progress) { color: var(--fg); }
.theme { margin-left: auto; background: none; border: 1px solid var(--line); border-radius: 2px; padding: 6px 10px; min-height: 36px; font: 12px var(--mono); }
.card[hidden] { display: none; }
code { font-family: var(--mono); font-size: .9em; background: rgba(127,127,127,.14); padding: 0 3px; }
dialog { margin: 0 0 0 auto; width: min(640px, 100vw); height: 100vh; max-height: 100vh; max-width: 100vw; border: 0;
  border-left: 1px solid var(--rule); background: var(--bg); color: var(--fg); padding: 0; }
dialog::backdrop { background: rgba(22,21,21,.35); }
.dhead { position: sticky; top: 0; background: var(--bg); display: flex; justify-content: space-between; align-items: center;
  padding: 16px 28px; border-bottom: 1px solid var(--line); }
.dbody { padding: 8px 28px 40px; }
.close { background: none; border: 1px solid var(--rule); border-radius: 2px; padding: 8px 14px; min-height: 40px; font: 13px var(--read); }
.crumbs { font-size: 13px; color: var(--muted); display: flex; flex-wrap: wrap; gap: 4px; }
.crumbs button, .links button { background: none; border: 0; padding: 0; text-decoration: underline; text-underline-offset: 3px; color: inherit; text-align: left; }
dialog h3 { font: 500 24px/1.25 var(--display); margin: 10px 0 12px; overflow-wrap: anywhere; }
dialog h4 { font: 500 15px var(--display); margin: 28px 0 8px; padding-top: 12px; border-top: 1px solid var(--line); }
dialog p, dialog li { font-size: 16px; line-height: 1.55; margin: 0; overflow-wrap: anywhere; }
dialog ul { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
.crit { display: grid; grid-template-columns: 70px 1fr; gap: 10px; }
.crit b { font: 12px/1.9 var(--mono); }
.crit.met b::before { content: "\2713 "; } .crit:not(.met) b::before { content: "\25CB "; }
blockquote { margin: 0; border-left: 3px solid var(--rule); padding: 2px 0 2px 14px; font-size: 16px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.facts { display: flex; flex-wrap: wrap; gap: 4px 14px; }
@media (max-width: 760px) {
  .top, .bar, .counts { padding-left: 16px; padding-right: 16px; } .sprint { margin: 0 16px 12px; } h1 { font-size: 28px; }
  input[type=search], select { flex: 1 1 100%; max-width: none; }
  .tabs { display: flex; overflow-x: auto; gap: 6px; padding: 12px 16px; position: sticky; top: 0; z-index: 2; background: var(--bg); }
  .col h2 { position: static; }
  .board { padding: 12px 16px 32px; } .grid { display: block; min-width: 0; }
  body:not(.filtering) .col:not(.shown) { display: none; } .col { margin-bottom: 24px; }
  dialog { width: 100vw; border-left: 0; } .dhead, .dbody { padding-left: 16px; padding-right: 16px; } }
@media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
</style></head><body>
<div class="head"><header class="top"><h1>TRACE <span>/</span> Tasks</h1><span class="when">Snapshot of {{.When}}</span><button class="theme" id="theme" aria-label="Color scheme">Auto</button>
<p class="note">A view to read. Changes are made from TRACE; run <code>trace task board</code> again to refresh.</p></header>
{{if .SprintGoal}}<div class="sprint"><span class="mono">Sprint</span><span class="goal">{{.SprintGoal}}</span>{{if .SprintDue}}<span class="mono">review {{.SprintDue}}</span>{{end}}</div>{{end}}
<div class="bar"><input type="search" id="q" placeholder="Search words, asks, criteria" aria-label="Search">
<select id="epic" aria-label="Epic"><option value="">All epics</option>{{range .Epics}}<option value="{{.ID}}">{{.Text}}</option>{{end}}<option value="none">No epic</option></select></div>
<div class="counts" role="group" aria-label="Show only">{{range .Counts}}<button data-filter="{{index . 0}}" data-n="{{index . 2}}" aria-pressed="false">{{index . 1}}<b>{{index . 2}}</b></button>{{end}}
<button class="clear" id="clear" hidden>Clear</button><span class="results" id="results" aria-live="polite"></span></div></div>
<nav class="tabs" role="tablist" aria-label="State">{{range .Columns}}<button role="tab" data-tab="{{.Key}}" aria-selected="{{if eq .Key $.Start}}true{{else}}false{{end}}">{{.Title}}<b>{{len .Cards}}</b></button>{{end}}</nav>
<main class="board"><div class="grid">
{{range .Columns}}<section class="col{{if eq .Key $.Start}} shown{{end}}" data-col="{{.Key}}" data-count="{{len .Cards}}"><h2>{{.Title}}<b>{{len .Cards}}</b></h2><div class="cards">
{{range .Cards}}<button class="card" data-open="d{{.ID}}" data-epic="{{if .Epic}}{{.Epic.ID}}{{else}}none{{end}}" data-flags="{{.Flags}}" data-find="{{.Find}}" aria-haspopup="dialog">
{{if .Epic}}<span class="ctx">{{.Epic.Text}}</span>{{end}}
<span class="k"><span>#{{.ID}}</span><span>{{if .Kind}}{{.Kind}}{{else}}{{.Level}}{{end}}</span>{{if .Points}}<span class="pts">{{.Points}} pts</span>{{end}}</span>
<span class="t">{{.Text}}</span>
{{if and .Waits (not .Closed)}}<span class="wait">Waits: {{index .Waits 0}}{{if gt (len .Waits) 1}} · +{{more (len .Waits)}} more{{end}}</span>{{end}}
<span class="foot">{{if eq .State "progress"}}<span class="tag progress">In progress</span>{{end}}{{if .Overdue}}<span class="tag overdue">&#9888; Overdue, not told</span>{{end}}{{if .Due}}<span>{{.Due}}</span>{{end}}{{if .Criteria}}<span>{{.Met}}/{{len .Criteria}} criteria</span>{{end}}{{if .NoCriteria}}<span class="tag">No criteria</span>{{end}}{{if eq .Closed "done"}}<span class="tag">&#10003; Done</span>{{end}}{{if eq .Closed "dropped"}}<span class="tag">Dropped</span>{{end}}</span>
</button>
{{else}}<p class="empty">Nothing here.</p>{{end}}</div></section>
{{end}}</div></main>
{{range .Details}}<dialog id="d{{.ID}}" data-detail="{{.ID}}" aria-labelledby="h{{.ID}}">
<div class="dhead"><span class="mono">#{{.ID}} · {{.Level}}{{if .Kind}} · {{.Kind}}{{end}}</span><form method="dialog"><button class="close">Close</button></form></div>
<div class="dbody">
{{if .Path}}<nav class="crumbs" aria-label="Path">{{range .Path}}<button data-open="d{{.ID}}">#{{.ID}} {{.Text}}</button><span>&rsaquo;</span>{{end}}<span>#{{.ID}}</span></nav>{{end}}
<h3 id="h{{.ID}}">{{.Text}}</h3>
<div class="facts mono"><span>{{if .Closed}}{{.Closed}}{{else if eq .State "progress"}}in progress{{else if eq .State "waiting"}}waiting{{else}}to do{{end}}</span>{{if .Points}}<span>{{.Points}} pts</span>{{end}}{{if .Due}}<span>due {{.Due}}</span>{{end}}{{if .Overdue}}<span>&#9888; overdue, not told</span>{{end}}</div>
{{if .Waits}}<h4>What Holds It</h4><ul>{{range .Waits}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{if ne .Level "epic"}}<h4>Criteria Of Done</h4>{{if .Criteria}}<p class="mono">{{.Met}} of {{len .Criteria}} met</p><ul>{{range .Criteria}}<li class="crit{{if .Met}} met{{end}}"><b>{{if .Met}}Met{{else}}Open{{end}}</b><span>{{.Text}}</span></li>{{end}}</ul>{{else}}<p>This {{.Level}} has no criteria of done yet.</p>{{end}}{{end}}
<h4>Asked</h4>{{if .Asked}}<blockquote>{{.Asked}}</blockquote>{{else}}<p>The message that asked for it is not known.</p>{{end}}
{{if or .Children .Sprint}}<h4>Related</h4><ul class="links">{{if .Sprint}}<li>Sprint: {{.Sprint}}</li>{{end}}{{range .Children}}<li><button data-open="d{{.ID}}">#{{.ID}} {{.Text}}</button></li>{{end}}</ul>{{end}}
</div></dialog>
{{end}}
<script>
(function () {
  var root = document.documentElement, pick = document.getElementById("theme"), modes = ["auto", "light", "dark"], names = { auto: "Auto", light: "Light", dark: "Dark" };
  function setTheme(m) { if (m === "auto") root.removeAttribute("data-theme"); else root.setAttribute("data-theme", m);
    pick.textContent = names[m]; pick.dataset.mode = m; try { localStorage.setItem("trace-theme", m); } catch (e) {} }
  var saved = "auto"; try { saved = localStorage.getItem("trace-theme") || "auto"; } catch (e) {}
  setTheme(modes.indexOf(saved) >= 0 ? saved : "auto");
  pick.addEventListener("click", function () { setTheme(modes[(modes.indexOf(pick.dataset.mode) + 1) % 3]); });
  var norm = function (s) { return s.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase(); };
  var cards = [].slice.call(document.querySelectorAll("button.card")), q = document.getElementById("q"), epic = document.getElementById("epic");
  var clear = document.getElementById("clear"), results = document.getElementById("results"), on = {};
  cards.forEach(function (c) { c._find = norm(c.dataset.find); });
  function apply() {
    var words = norm(q.value.trim()), active = Object.keys(on), shown = 0;
    cards.forEach(function (c) {
      var flags = c.dataset.flags.split(" "), ok = (!words || c._find.indexOf(words) >= 0) && (!epic.value || c.dataset.epic === epic.value) &&
        (!active.length || active.some(function (f) { return flags.indexOf(f) >= 0; }));
      c.hidden = !ok; if (ok) shown++;
    });
    var filtering = !!(words || epic.value || active.length);
    document.body.classList.toggle("filtering", filtering); clear.hidden = !filtering;
    results.textContent = filtering ? shown + (shown === 1 ? " result" : " results") : "";
  }
  q.addEventListener("input", apply); epic.addEventListener("change", apply);
  document.querySelectorAll("[data-filter]").forEach(function (b) { b.addEventListener("click", function () {
    var f = b.dataset.filter; if (on[f]) delete on[f]; else on[f] = 1; b.setAttribute("aria-pressed", on[f] ? "true" : "false"); apply(); }); });
  clear.addEventListener("click", function () { q.value = ""; epic.value = ""; on = {};
    document.querySelectorAll("[data-filter]").forEach(function (b) { b.setAttribute("aria-pressed", "false"); }); apply(); });
  document.querySelectorAll("[data-tab]").forEach(function (t) { t.addEventListener("click", function () {
    document.querySelectorAll("[data-tab]").forEach(function (x) { x.setAttribute("aria-selected", x === t ? "true" : "false"); });
    document.querySelectorAll(".col").forEach(function (c) { c.classList.toggle("shown", c.dataset.col === t.dataset.tab); }); }); });
  var back = [], first = location.hash && document.getElementById(location.hash.slice(1));
  if (first && first.showModal) first.showModal();
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-open]"); if (!b) return;
    var d = document.getElementById(b.dataset.open); if (!d) return;
    var open = document.querySelector("dialog[open]"); if (open) open.close();
    back.push(b.closest("dialog") ? back[back.length - 1] : b); d.showModal();
  });
  document.querySelectorAll("dialog").forEach(function (d) { d.addEventListener("close", function () {
    if (!document.querySelector("dialog[open]")) { var b = back.pop(); back = []; if (b) b.focus(); } }); });
})();
</script>
</body></html>
`))
