package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// boardCard is one task as the board page shows it (SPEC T24).
type boardCard struct {
	ID                               int64
	Level, Kind, Parent, Text, Asked string
	Due, Waits, Criteria, Closed     string
	Points                           int64
}

// boardColumn is one of the page's four columns.
type boardColumn struct {
	Key, Title string
	Cards      []boardCard
}

// taskBoard writes the board as one HTML page at path, prints where, and opens it (SPEC T24).
func taskBoard(db *sql.DB, path string, w io.Writer) error {
	cols, err := boardColumns(db)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := boardPage.Execute(f, map[string]any{"Columns": cols, "When": clockNow().Format("Monday 2006-01-02 15:04 (UTC-07:00)")}); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintln(w, "board written to", path)
	return openPage(path)
}

// boardColumns reads the open tasks and those closed in the last 14 days, in board order, into the four columns.
func boardColumns(db *sql.DB) ([]boardColumn, error) {
	since := clockNow().Add(-14 * 24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := db.Query(`select t.id, t.level, coalesce(t.kind, ''), coalesce(t.points, 0), coalesce(p.text, ''), t.text, coalesce(e.text, ''),
		t.self, coalesce(t.due, ''), coalesce(t.zone, ''), t.started_at is not null, coalesce(t.closed_how, '')
		from task t left join task p on p.id = t.parent_id left join event e on e.id = t.asked_event
		where t.closed_at is null or t.closed_at >= ? order by t.closed_at is not null, t.rank, t.id`, since)
	if err != nil {
		return nil, err
	}
	var cards []boardCard
	var started []bool
	for rows.Next() {
		var c boardCard
		var self, inProgress bool
		var zone string
		rows.Scan(&c.ID, &c.Level, &c.Kind, &c.Points, &c.Parent, &c.Text, &c.Asked, &self, &c.Due, &zone, &inProgress, &c.Closed)
		if self {
			c.Asked = "added by an agent on its own"
		}
		c.Due = dueText(c.Due, zone, "")
		cards, started = append(cards, c), append(started, inProgress)
	}
	rows.Close() // read whole before asking more: the store has one connection
	cols := []boardColumn{{Key: "progress", Title: "In progress"}, {Key: "waiting", Title: "Waiting"}, {Key: "todo", Title: "To do"}, {Key: "closed", Title: "Closed, last 14 days"}}
	for i, c := range cards {
		c.Waits, c.Criteria = waitingOn(db, c.ID, 0), criteriaText(db, strconv.FormatInt(c.ID, 10))
		cols[boardColumnOf(c, started[i])].Cards = append(cols[boardColumnOf(c, started[i])].Cards, c)
	}
	return cols, rows.Err()
}

// boardColumnOf is the index of the card's column: closed, in progress, waiting, else to do.
func boardColumnOf(c boardCard, inProgress bool) int {
	switch {
	case c.Closed != "":
		return 3
	case inProgress:
		return 0
	case c.Waits != "":
		return 1
	}
	return 2
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

// boardPage is the page; html/template escapes every text (SPEC T24).
var boardPage = template.Must(template.New("board").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>TRACE board</title>
<style>
:root { --bg: #f7f5f0; --card: #fff; --ink: #1d1d1b; --muted: #6b6a66; --line: #dedad2; --accent: #2a6f97; }
@media (prefers-color-scheme: dark) { :root { --bg: #161615; --card: #22221f; --ink: #ecebe6; --muted: #a3a29c; --line: #3a3934; --accent: #7fb7d9; } }
body { margin: 0; background: var(--bg); color: var(--ink); font: 14px/1.45 system-ui, sans-serif; }
header { padding: 16px; border-bottom: 1px solid var(--line); }
h1 { margin: 0; font-size: 18px; } header p { margin: 4px 0 0; color: var(--muted); }
main { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 16px; padding: 16px; }
section h2 { font-size: 13px; text-transform: uppercase; letter-spacing: .04em; color: var(--muted); margin: 0 0 8px; }
article { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; }
.meta { color: var(--muted); font-size: 12px; } .text { margin: 4px 0; } .tag { color: var(--accent); font-weight: 600; }
ul { margin: 4px 0 0 18px; padding: 0; color: var(--muted); font-size: 12px; }
</style></head><body>
<header><h1>TRACE board</h1><p>Snapshot of {{.When}}. Run <code>trace task board</code> again to refresh.</p></header>
<main>{{range .Columns}}
<section data-col="{{.Key}}"><h2>{{.Title}} ({{len .Cards}})</h2>{{range .Cards}}
<article>
<div class="meta"><span class="tag">#{{.ID}} {{.Level}}</span>{{if .Kind}} · {{.Kind}}{{end}}{{if .Points}} · {{.Points}} pts{{end}}{{if .Parent}} · under: {{.Parent}}{{end}}{{if .Closed}} · {{.Closed}}{{end}}</div>
<div class="text">{{.Text}}</div>
<ul>{{if .Asked}}<li>asked: {{.Asked}}</li>{{end}}{{if .Due}}<li>due {{.Due}}</li>{{end}}{{if .Waits}}<li>{{.Waits}}</li>{{end}}{{if .Criteria}}<li>done when: {{.Criteria}}</li>{{end}}</ul>
</article>{{end}}
</section>{{end}}
</main></body></html>
`))
