package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// The scroll benchmarks render whole frames of the two scrolling detail bodies
// with the statement shape that made them crawl: an ORM's
// `hostname = any(ARRAY[…])` with a few thousand elements. One iteration is
// one key press, through Update and back out as a frame.

const benchArrayElems = 5000

// benchWideQuery is the normalized pg_stat_statements text ($n placeholders).
func benchWideQuery() string {
	var b strings.Builder
	b.WriteString("with valid_sgs as MATERIALIZED ( select sg.server_id, sg.hostname from server as sg " +
		"join server_string_attribute sg_state on ( sg_state.attribute_id = $1 and sg_state.value != $2 " +
		"and sg.server_id = sg_state.server_id ) where sg.servertype_id = $3 and sg.hostname = any(ARRAY[")
	for i := range benchArrayElems {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "$%d", i+4)
	}
	b.WriteString("]) ) select s.server_id, s.hostname from server s join valid_sgs v on v.server_id = s.server_id " +
		"join server_inet_attribute i on i.server_id = s.server_id " +
		"join server_relation_attribute r on r.server_id = s.server_id where s.servertype_id = $3 order by s.hostname")
	return b.String()
}

func benchHostname(i int) string {
	return fmt.Sprintf("'puppetserver-%d-outgoing.zone%d.sg'", i, i%40)
}

// benchWideCall is the same statement with every $n filled in.
func benchWideCall() string {
	q := benchWideQuery()
	repl := make([]string, 0, 2*(benchArrayElems+3))
	// Longest ordinals first so $12 is never read as $1 followed by a 2.
	for i := benchArrayElems + 3; i >= 1; i-- {
		repl = append(repl, fmt.Sprintf("$%d", i), benchHostname(i))
	}
	return strings.NewReplacer(repl...).Replace(q)
}

func benchStatementDetailModel() (*Model, *screen) {
	q := pg.QueryStat{
		QueryID: 1635833922934957777, Query: benchWideQuery(), Calls: 251, Rows: 148_500_000,
		TotalExecTime: 870_200, TotalPlanTime: 16_721, Plans: 251, SharedBlksHit: 149_100_000,
	}
	s := &screen{
		level: levelStatementDetail, title: "query", tool: toolQueries, db: "serveradmin", loaded: true,
		stat: stmtState{
			detail: &q, windowExecMs: 10_600_000, trackPlanning: true,
			sampleCall: benchWideCall(), sampleSource: sampleLog, sampleResolved: true,
			explain: "Sort  (cost=10.00..11.00 rows=1 width=40)\n  ->  Nested Loop  (cost=1.00..9.00 rows=1 width=40)",
		},
	}
	m := NewModel(pg.New(cli.Config{}), cli.Config{}, nil)
	m.width, m.height = 230, 60
	m.hostLabel = "db:5432"
	m.stack = append(m.stack, s)
	return m, s
}

func benchLogEntryModel(tb testing.TB) (*Model, *screen) {
	tb.Helper()
	var params strings.Builder
	for i := 1; i <= benchArrayElems+3; i++ {
		if i > 1 {
			params.WriteString(", ")
		}
		fmt.Fprintf(&params, "$%d = %s", i, benchHostname(i))
	}
	text := "2026-11-17 07:52:27 UTC [872039-3] serveradmin@host LOG:  duration: 3466.900 ms  execute <unnamed>: " +
		benchWideQuery() + "\n" +
		"2026-11-17 07:52:27 UTC [872039-4] serveradmin@host DETAIL:  Parameters: " + params.String() + "\n"
	r, err := pglog.Load(tb.Context(), &memLogSource{data: []byte(text)}, "", time.UTC, 0, pglog.AggOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	if len(r.Entries) != 1 {
		tb.Fatalf("entries = %d", len(r.Entries))
	}
	m := NewModel(pg.New(cli.Config{}), cli.Config{}, nil)
	m.width, m.height = 230, 60
	m.hostLabel = "db:5432"
	parent := &screen{level: levelLogs, tool: toolLogs, loaded: true, log: logState{report: r, src: &memLogSource{}}}
	es := m.logEntryScreen(parent, &r.Entries[0])
	es.loaded = true
	m.stack = append(m.stack, parent, es)
	return m, es
}

// benchScroll presses ↓ (then ↑, back and forth over the body) once per
// iteration and redraws, so every frame shows a different slice.
func benchScroll(b *testing.B, m *Model, s *screen) {
	b.Helper()
	if m.render() == "" {
		b.Fatal("empty frame")
	}
	down, up := tea.KeyPressMsg{Code: tea.KeyDown}, tea.KeyPressMsg{Code: tea.KeyUp}
	key := down
	b.ReportAllocs()
	for b.Loop() {
		switch {
		case s.offset >= 400:
			key = up
		case s.offset <= 0:
			key = down
		}
		m.Update(key)
		_ = m.render()
	}
	if s.offset == 0 {
		b.Fatal("the body never scrolled")
	}
}

// benchOpen is the cost the scroll path no longer pays per key: laying the
// body out from scratch, as on the first frame or after a resize.
func benchOpen(b *testing.B, m *Model) {
	b.Helper()
	b.ReportAllocs()
	for b.Loop() {
		m.width ^= 1 // 230 ↔ 231: a new width every frame defeats any layout memo
		_ = m.render()
	}
}

func BenchmarkScrollStatementDetail(b *testing.B) {
	m, s := benchStatementDetailModel()
	benchScroll(b, m, s)
}

func BenchmarkScrollLogEntry(b *testing.B) {
	m, s := benchLogEntryModel(b)
	benchScroll(b, m, s)
}

func BenchmarkOpenStatementDetail(b *testing.B) {
	m, _ := benchStatementDetailModel()
	benchOpen(b, m)
}

func BenchmarkOpenLogEntry(b *testing.B) {
	m, _ := benchLogEntryModel(b)
	benchOpen(b, m)
}

// lineBuf must page exactly like the string it replaces, or a body assembled
// from memoized lines would scroll to a different last screen.
func TestLineBufMatchesBodyLines(t *testing.T) {
	var l lineBuf
	var ref strings.Builder
	write := func(text string) { l.WriteString(text); ref.WriteString(text) }
	lines := func(ls ...string) {
		l.appendLines(ls)
		for _, ln := range ls {
			ref.WriteString(ln + "\n")
		}
	}
	write("\n  title\n\n")
	write("  header\n")
	lines("    a", "    b", "")
	lines()
	write("\n  next\n")
	write("    tail\n\n\n")
	got, want := l.body(), bodyLines(ref.String())
	if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
		t.Errorf("lines differ:\n got %q\nwant %q", got, want)
	}
	var empty lineBuf
	off := 3
	if out := scrollLines(empty.body(), &off, 4); out != "\n\n\n\n" || off != 0 {
		t.Errorf("empty body: %q off=%d", out, off)
	}
}

// Scrolling the query detail must reuse the statement layout, and anything
// that replaces the text must not: the memo is keyed by what is shown.
func TestStatementDetailScrollReusesLayout(t *testing.T) {
	m, s := benchStatementDetailModel()
	first := m.render()
	q0, c0 := &s.stat.queryLayout.lines[0], &s.stat.callLayout.lines[0]
	for range 5 {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	scrolled := m.render()
	if s.offset != 5 || scrolled == first {
		t.Fatalf("the body did not scroll (offset %d)", s.offset)
	}
	if &s.stat.queryLayout.lines[0] != q0 || &s.stat.callLayout.lines[0] != c0 {
		t.Error("a scroll laid the statement out again")
	}

	// The call sits far below the first screen, so read the layout itself.
	s.stat.sampleCall = "select 'only-this-call'"
	m.render()
	if got := plainLines(s.stat.callLayout.lines); len(got) != 1 || got[0] != "    select 'only-this-call'" {
		t.Errorf("a replaced sample call kept the old layout: %d lines", len(got))
	}
	if &s.stat.queryLayout.lines[0] != q0 {
		t.Error("replacing the sample call laid the statement out again")
	}
	m.width = 100
	m.render()
	if s.stat.queryLayout.width != 96 {
		t.Errorf("resize kept the layout of width %d", s.stat.queryLayout.width)
	}
}

// The log entry body is kept across frames but must follow the three things
// it derives from: the record, the width, and the report — a live tail
// re-parses the newest record in place, so the same pointer can gain a DETAIL.
func TestLogEntryBodyMemo(t *testing.T) {
	m, s := benchLogEntryModel(t)
	first := m.render()
	l0 := &s.log.entryBody.lines[0]
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if scrolled := m.render(); s.offset != 2 || scrolled == first {
		t.Fatalf("the body did not scroll (offset %d)", s.offset)
	}
	if &s.log.entryBody.lines[0] != l0 {
		t.Error("a scroll laid the entry out again")
	}

	// A refresh that changed the file: new report, same entry address, more text.
	s.offset = 0
	e := s.log.entry
	e.Hint = []byte("grown-by-the-live-tail")
	m.render()
	if strings.Contains(strings.Join(s.log.entryBody.lines, "\n"), "grown-by-the-live-tail") {
		t.Fatal("test premise: the body is memoized on an unchanged report")
	}
	next := *s.log.report
	s.log.report = &next
	m.render()
	if !strings.Contains(strings.Join(s.log.entryBody.lines, "\n"), "grown-by-the-live-tail") {
		t.Error("a new report kept the old entry body")
	}

	lines := len(s.log.entryBody.lines)
	m.width = 90
	m.render()
	if got := len(s.log.entryBody.lines); got <= lines {
		t.Errorf("resize kept the old wrap: %d lines, was %d", got, lines)
	}

	other := *e
	other.Hint = []byte("another-record")
	s.log.entry = &other
	m.render()
	if !strings.Contains(strings.Join(s.log.entryBody.lines, "\n"), "another-record") {
		t.Error("a different entry kept the old body")
	}
}
