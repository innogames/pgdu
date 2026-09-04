package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// sampleLogReport is a small parsed log with every category the groups pane
// sections on, built through the real parser so the test exercises the same
// path as a loaded file.
func sampleLogReport(t *testing.T) *pglog.Report {
	t.Helper()
	text := strings.Join([]string{
		`2026-09-02 00:15:25 UTC [872628-1] herocity0@2a00:1f78:fffd:4301::1221 ERROR:  duplicate key value violates unique constraint "channel_name_plugin_idx"`,
		`2026-09-02 00:15:25 UTC [872628-2] herocity0@2a00:1f78:fffd:4301::1221 DETAIL:  Key (name, plugin)=(player-to-player-1-2, chat) already exists.`,
		`2026-09-02 00:15:25 UTC [872628-3] herocity0@2a00:1f78:fffd:4301::1221 STATEMENT:  INSERT INTO channel(plugin, name, ephemeral) VALUES ($1, $2, $3) RETURNING id`,
		`2026-09-02 00:58:21 UTC [900454-1] herocity0@2a00:1f78:fffd:4301::1212 ERROR:  duplicate key value violates unique constraint "channel_name_plugin_idx"`,
		`2026-09-02 01:16:09 UTC [872039-3] herocity0@2a00:1f78:fffd:4301::1220 LOG:  duration: 248.569 ms  execute <unnamed>: DELETE FROM event_log WHERE id IN (1, 2)`,
		`2026-09-02 02:16:09 UTC [872039-4] herocity0@2a00:1f78:fffd:4301::1220 LOG:  duration: 8337.081 ms  execute <unnamed>: DELETE FROM event_log WHERE id IN (3)`,
		`2026-09-02 03:17:05 UTC [3256329-604] LOG:  checkpoint complete: wrote 1271754 buffers (16.6%); 0 WAL file(s) added, 0 removed, 505 recycled; write=959.123 s, sync=0.080 s, total=959.437 s`,
		`2026-09-02 04:22:52 UTC [3256335-843] LOG:  temporary file: path "base/pgsql_tmp/pgsql_tmp3256335.420", size 16408284`,
		`2026-09-02 06:43:56 UTC [1165189-1] matze@[local] FATAL:  database "pgbouncer" does not exist`,
		`2026-09-02 07:00:00 UTC [1165190-1] matze@[local] WARNING:  there is no transaction in progress`,
	}, "\n") + "\n"
	src := &memLogSource{data: []byte(text)}
	r, err := pglog.Load(t.Context(), src, "", time.UTC, 0, pglog.AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// memLogSource is an in-memory pglog.Source for tests.
type memLogSource struct{ data []byte }

func (s *memLogSource) Info() pglog.SourceInfo {
	return pglog.SourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Size: int64(len(s.data))}
}

func (s *memLogSource) ReadTail(_ context.Context, n int64) ([]byte, pglog.Window, error) {
	return s.data, pglog.Window{Requested: n, FileSize: int64(len(s.data)), Bytes: int64(len(s.data))}, nil
}

func (s *memLogSource) ReadFrom(context.Context, int64) ([]byte, error) {
	return nil, pglog.ErrNotIncremental
}
func (s *memLogSource) Cursor(context.Context) *pglog.Cursor { return nil }

func newLogTestModel(t *testing.T) (*Model, *screen) {
	t.Helper()
	// Wide enough that the rows under test (title + stats suffix, DETAIL tail)
	// are not clipped by truncateToWidth.
	m := &Model{width: 320, height: 40}
	// Built by hand rather than via logScreen, which needs a live client.
	s := &screen{
		level: levelLogs, title: "log", tool: toolLogs,
		log: logState{src: &memLogSource{}, window: logDefaultWindow}, loaded: true,
		sort: sortByCount, sortDesc: true}
	s.log.report = sampleLogReport(t)
	m.stack = []*screen{{level: levelTools}, s}
	m.rebuildLogItems(s)
	return m, s
}

func TestLogGroupItemsSections(t *testing.T) {
	m, s := newLogTestModel(t)

	// Signal sections first (errors, warnings, temp files), chatter last
	// (slow queries, checkpoints); the biggest group leads its section.
	var sections []string
	var titles []string
	for _, it := range s.items {
		switch v := it.data.(type) {
		case logSection:
			sections = append(sections, v.title)
		case *pglog.Group:
			titles = append(titles, v.Title)
		}
	}
	if strings.Join(sections, ",") != "errors,warnings,temp files,slow queries,checkpoints" {
		t.Errorf("sections = %v", sections)
	}
	if len(titles) != 6 || titles[0] != `duplicate key value violates unique constraint "channel_name_plugin_idx"` {
		t.Errorf("titles = %v", titles)
	}
	// The cursor never rests on a header row.
	s.resetCursor()
	m.skipLogHeader(s, 1)
	if _, hdr := s.items[s.visibleIndexes()[s.cursor]].data.(logSection); hdr {
		t.Error("cursor rests on a section header")
	}

	// Flat mode: no headers at all.
	s.log.groupBy = logGroupByNone
	m.rebuildLogItems(s)
	for _, it := range s.items {
		if _, ok := it.data.(logSection); ok {
			t.Error("flat mode emitted a section header")
		}
	}
}

func TestRenderLogGroupsAndHeader(t *testing.T) {
	m, s := newLogTestModel(t)
	out := stripANSI(m.renderLogGroups(s, 20))
	for _, want := range []string{
		"errors", "2 group(s)", "3 entries",
		`duplicate key value violates unique constraint "channel_name_plugin_idx"`,
		`database "pgbouncer" does not exist`,
		"FATAL", "ERROR", "WARNING",
		"temporary file", "total 15.65 MB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("groups pane missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "DELETE FROM event_log") || !strings.Contains(out, "checkpoint complete") {
		t.Error("slow-query / checkpoint groups missing")
	}

	hdr := stripANSI(m.renderLogHeader(s))
	for _, want := range []string{
		"postgresql-17-main.log", "whole file", "prefix %t [%p-%l] %q%u@%h", "detected",
		"8 entries", "2 errors", "1 fatal", "1 warnings", "2 slow", "1 ckpt",
		"by category", "⟳ off",
		"errors", "all", "per cell",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header missing %q:\n%s", want, hdr)
		}
	}

	// Timeline pane: generic table with the default columns, newest first.
	s.log.view = logViewTimeline
	m.rebuildLogItems(s)
	if s.diagCols == nil || len(s.items) != 8 {
		t.Fatalf("timeline: cols=%v items=%d", s.diagCols, len(s.items))
	}
	if s.diagCols[s.diagSortCol].Name != "time" || !s.sortDesc {
		t.Errorf("timeline default sort = %s desc=%v", s.diagCols[s.diagSortCol].Name, s.sortDesc)
	}
	first := s.items[0].data.([]pg.DiagCell)
	if first[0].Display != "07:00:00" {
		t.Errorf("newest entry first: got %q", first[0].Display)
	}
	if e := s.logEntryOf(s.items[0]); e == nil || e.Severity != pglog.SevWarning {
		t.Errorf("logEntryOf on a timeline row = %+v", e)
	}
	table := stripANSI(m.renderDiagResult(s, 20))
	if !strings.Contains(table, "WARNING") || !strings.Contains(table, "pgbouncer") {
		t.Errorf("timeline table:\n%s", table)
	}

	// Slow pane: the same table restricted to duration: entries, slowest first,
	// with its own remembered sort so tabbing back to the timeline keeps time↓.
	s.log.view = s.log.view.next(false)
	if s.log.view != logViewSlow {
		t.Fatalf("tab after timeline = %v", s.log.view)
	}
	m.rebuildLogItems(s)
	if len(s.items) != 2 {
		t.Fatalf("slow pane rows = %d", len(s.items))
	}
	if s.diagCols[s.diagSortCol].Name != "dur" || !s.sortDesc {
		t.Errorf("slow default sort = %s desc=%v", s.diagCols[s.diagSortCol].Name, s.sortDesc)
	}
	var prev float64 = -1
	for i, it := range s.items {
		e := s.logEntryOf(it)
		if e == nil || e.Category != pglog.CatSlowQuery {
			t.Fatalf("slow row %d: %+v", i, e)
		}
		if prev >= 0 && e.DurationMs > prev {
			t.Errorf("slow rows not duration-desc: %v after %v", e.DurationMs, prev)
		}
		prev = e.DurationMs
	}
	s.log.view = s.log.view.next(false).next(false) // groups → timeline
	m.rebuildLogItems(s)
	if s.diagCols[s.diagSortCol].Name != "time" || len(s.items) != 8 {
		t.Errorf("timeline after slow: sort=%s items=%d", s.diagCols[s.diagSortCol].Name, len(s.items))
	}
}

func TestLogGroupAndEntryScreens(t *testing.T) {
	m, s := newLogTestModel(t)
	var dup *pglog.Group
	for _, it := range s.items {
		if g, ok := it.data.(*pglog.Group); ok && strings.HasPrefix(g.Title, "duplicate key") {
			dup = g
		}
	}
	if dup == nil {
		t.Fatal("duplicate-key group not found")
	}
	gs := m.logGroupScreen(s, dup)
	m.stack = append(m.stack, gs)
	m.rebuildLogChild(gs)
	gs.loaded = true
	if len(gs.items) != 2 {
		t.Fatalf("group rows = %d", len(gs.items))
	}
	// Newest first (sortByLast desc).
	if e := gs.logEntryOf(gs.items[0]); e == nil || e.Time.Format("15:04") != "00:58" {
		t.Errorf("first group row = %+v", e)
	}
	out := stripANSI(m.renderLogGroup(gs, 10))
	if !strings.Contains(out, "pid 872628") || !strings.Contains(out, "DETAIL: Key (name, plugin)") {
		t.Errorf("group rows:\n%s", out)
	}
	hdr := stripANSI(m.renderLogHeader(gs))
	if !strings.Contains(hdr, "2 entries") || !strings.Contains(hdr, "first 09-02 00:15:25") {
		t.Errorf("group header:\n%s", hdr)
	}

	es := m.logEntryScreen(gs, gs.logEntryOf(gs.items[1]))
	es.loaded = true
	body := stripANSI(m.renderLogEntry(es, 40))
	for _, want := range []string{"severity", "ERROR", "pid", "872628", "table", "channel  ·  d describes it", "MESSAGE", "duplicate key", "DETAIL", "Key (name, plugin)", "STATEMENT", "INSERT INTO channel"} {
		if !strings.Contains(body, want) {
			t.Errorf("entry body missing %q:\n%s", want, body)
		}
	}
}

func TestLogColumnRegistry(t *testing.T) {
	m := &Model{}
	reg := logColumnRegistry()
	seen := map[logColID]bool{}
	for _, d := range reg {
		if seen[d.id] {
			t.Errorf("duplicate column id %q", d.id)
		}
		seen[d.id] = true
		if d.name == "" || d.desc == "" || d.cell == nil {
			t.Errorf("column %q incomplete", d.id)
		}
	}
	if reg[len(reg)-1].id != logColMessage || !reg[len(reg)-1].mandatory {
		t.Error("message must be the mandatory last column (it takes the last-column grow)")
	}
	vis := logSpec(logViewTimeline).visibleCols(m.logTableFor(logViewTimeline), logCtx{})
	if len(vis) == 0 || vis[0].id != logColTime {
		t.Errorf("default visible = %v", vis)
	}
	// Hiding the sort column falls back to time desc.
	m.logTable.sortColID = logColPID
	m.logColsVisibleFor(logViewTimeline)[logColPID] = false
	s := &screen{}
	m.syncLogSort(s, logSpec(logViewTimeline).visibleCols(m.logTableFor(logViewTimeline), logCtx{}))
	if m.logTable.sortColID != logColTime || !s.sortDesc {
		t.Errorf("sort fallback = %q desc=%v", m.logTable.sortColID, s.sortDesc)
	}
}

func TestLogStatsColumnRegistry(t *testing.T) {
	reg := logStatsColumnRegistry()
	seen := map[logColID]bool{}
	mandatory := 0
	for _, d := range reg {
		if seen[d.id] {
			t.Errorf("duplicate column id %q", d.id)
		}
		seen[d.id] = true
		if d.name == "" || d.desc == "" || d.cell == nil {
			t.Errorf("column %q incomplete", d.id)
		}
		if d.mandatory {
			mandatory++
		}
	}
	if mandatory != 1 || reg[0].id != logColTime {
		t.Errorf("time must be the only mandatory column: %d mandatory, first %q", mandatory, reg[0].id)
	}
	// The stats pane's visibility map and sort memory are separate from the
	// timeline's, so hiding a stats column leaves the timeline untouched.
	m := &Model{}
	m.logColsVisibleFor(logViewStats)[logColWait] = false
	if len(logSpec(logViewStats).visibleCols(m.logTableFor(logViewStats), logCtx{})) != len(reg)-1 {
		t.Errorf("hiding wait: %d visible, want %d", len(logSpec(logViewStats).visibleCols(m.logTableFor(logViewStats), logCtx{})), len(reg)-1)
	}
	if _, leaked := m.logColsVisibleFor(logViewTimeline)[logColWait]; leaked {
		t.Error("stats visibility leaked into the timeline map")
	}
	if m.logSortCol(logViewStats) == m.logSortCol(logViewTimeline) {
		t.Error("stats pane must remember its own sort column")
	}
}

const samplePgBouncerStatsLog = `2026-09-04 00:39:17.276 UTC [3118582] LOG stats: 90 xacts/s, 1357 queries/s, 0 client parses/s, 0 server parses/s, 0 binds/s, in 399055 B/s, out 1253187 B/s, xact 17861 us, query 242 us, wait 0 us
2026-09-04 00:39:30.000 UTC [3118582] LOG C-0x55d1c0a2b3e0: shop/app@10.1.2.3:53412 closing because: client close request (age=12s)
2026-09-04 00:40:17.275 UTC [3118582] LOG stats: 81 xacts/s, 1265 queries/s, 0 client parses/s, 0 server parses/s, 0 binds/s, in 358384 B/s, out 1321391 B/s, xact 17590 us, query 256 us, wait 1 us
2026-09-04 00:41:17.274 UTC [3118582] LOG stats: 71 xacts/s, 1076 queries/s, 0 client parses/s, 0 server parses/s, 0 binds/s, in 311512 B/s, out 881291 B/s, xact 16904 us, query 229 us, wait 0 us
`

func TestLogStatsPane(t *testing.T) {
	m, s := newLogTestModel(t)
	// A Postgres log has no stats pane: tab cycles back to the groups.
	if got := logViewSlow.next(s.log.report.PoolerStats > 0); got != logViewGroups {
		t.Errorf("slow.next(false) on a Postgres log = %v, want groups", got)
	}

	r, err := pglog.Load(t.Context(), &memLogSource{data: []byte(samplePgBouncerStatsLog)}, "", time.UTC, 0, pglog.AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.PoolerStats != 3 {
		t.Fatalf("PoolerStats = %d, want 3", r.PoolerStats)
	}
	s.log.report = r
	if got := logViewSlow.next(r.PoolerStats > 0); got != logViewStats {
		t.Errorf("slow.next(false) on a pgbouncer log = %v, want stats", got)
	}
	if got := logViewStats.next(true); got != logViewGroups {
		t.Errorf("stats.next(false) = %v, want groups", got)
	}
	s.log.view = logViewStats
	m.rebuildLogItems(s)
	if len(s.items) != 3 {
		t.Fatalf("stats rows = %d, want 3 (the socket line must be filtered out)", len(s.items))
	}
	reg := logStatsColumnRegistry()
	if len(s.diagCols) != len(reg) {
		t.Fatalf("diagCols = %d, want %d", len(s.diagCols), len(reg))
	}
	for i, d := range reg {
		if s.diagCols[i].Name != d.name {
			t.Errorf("column %d = %q, want %q", i, s.diagCols[i].Name, d.name)
		}
	}
	if s.diagCols[s.diagSortCol].Name != "time" || !s.sortDesc {
		t.Errorf("default sort = %q desc=%v, want time desc", s.diagCols[s.diagSortCol].Name, s.sortDesc)
	}
	// Newest first: the 00:41 line leads.
	first := s.items[s.visibleIndexes()[0]].data.([]pg.DiagCell)
	if first[0].Display != "00:41:17" || first[2].Num != 1076 {
		t.Errorf("first row = %q queries=%v", first[0].Display, first[2].Num)
	}
	out := stripANSI(m.renderDiagResult(s, 20))
	for _, want := range []string{"queries/s", "xacts/s", "1357", "389.70 KB/s", "1.20 MB/s", "17.9ms", "242µs", "1µs"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats pane missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "closing because") {
		t.Errorf("socket line leaked into the stats pane:\n%s", out)
	}
	// The header swaps the severity sparklines for one per pooler metric,
	// with the window peak at the end; the severity rows return on other panes.
	hdr := stripANSI(m.renderLogHeader(s))
	for _, want := range []string{"xacts/s", "queries/s", "peak 1357", "peak 389.70 KB/s", "peak 17.9ms", "peak 256µs", "1µs", "per cell"} {
		if !strings.Contains(hdr, want) {
			t.Errorf("stats header missing %q:\n%s", want, hdr)
		}
	}
	if strings.Contains(hdr, "errors") && strings.Contains(hdr, "▁") && strings.Contains(hdr, "warnings▁") {
		t.Errorf("severity sparklines still shown on the stats pane:\n%s", hdr)
	}
	s.log.view = logViewTimeline
	m.rebuildLogItems(s)
	if hdr := stripANSI(m.renderLogHeader(s)); strings.Contains(hdr, "peak ") {
		t.Errorf("pooler sparklines leaked into the timeline header:\n%s", hdr)
	}
}

func TestLogFileItems(t *testing.T) {
	cands := []pg.LogCandidate{
		{Info: pglog.SourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Size: 1 << 20, Lines: 12345, Current: true, ModTime: time.Now()}, Reason: "/var/log/postgresql"},
		{Info: pglog.SourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.2.gz", Size: 4096, Lines: -1, Rotated: true}, Reason: "/var/log/postgresql"},
	}
	items := logFileItems(cands)
	cols := logFileColumns()
	if len(items) != 2 || !items[0].hasChildren || items[0].logIdx != 1 || items[1].logIdx != 2 {
		t.Fatalf("items = %+v", items)
	}
	cells := items[0].data.([]pg.DiagCell)
	if len(cells) != len(cols) {
		t.Fatalf("cells %d vs cols %d", len(cells), len(cols))
	}
	if cells[2].Display != "1.00 MB" || cells[5].Display != "~12k" || cells[7].Display != "●" {
		t.Errorf("row 0 cells = %+v", cells)
	}
	if c := items[1].data.([]pg.DiagCell); c[1].Display != "gz" || c[5].Display != "—" || c[7].Display != "" {
		t.Errorf("gz cells = %+v", c)
	}
	m := &Model{width: 200}
	s := &screen{level: levelLogFiles, loaded: true, items: items, log: logState{cands: cands}, diagCols: cols, diagBarCol: -1, diagSortCol: 3, sortDesc: true}
	m.applySort(s)
	out := stripANSI(m.renderDiagResult(s, 5))
	if !strings.Contains(out, "postgresql-17-main.log.2.gz") || !strings.Contains(out, "/var/log/postgresql") || !strings.Contains(out, "~lines") {
		t.Errorf("picker:\n%s", out)
	}
	if out2 := stripANSI(m.renderLogFiles(&screen{level: levelLogFiles}, 5)); !strings.Contains(out2, "no readable log found") {
		t.Errorf("empty state:\n%s", out2)
	}
}

func TestLogHelpers(t *testing.T) {
	if fmtCount(999) != "999" || fmtCount(1016) != "1.0k" || fmtCount(12345) != "12k" || fmtCount(2_500_000) != "2.5M" {
		t.Errorf("fmtCount: %s %s %s %s", fmtCount(999), fmtCount(1016), fmtCount(12345), fmtCount(2_500_000))
	}
	a := time.Date(2026, 9, 2, 0, 15, 0, 0, time.UTC)
	if got := fmtSpan(a, a.Add(7*time.Hour)); got != "00:15→07:15" {
		t.Errorf("fmtSpan same day = %q", got)
	}
	if got := fmtSpan(a, a.Add(30*time.Hour)); got != "09-02→09-03" {
		t.Errorf("fmtSpan multi-day = %q", got)
	}
	if got := dedent("\t\tSELECT 1\n\t\t\tFROM t"); got != "SELECT 1\n\tFROM t" {
		t.Errorf("dedent = %q", got)
	}
	if got := collapseWS("a  b\n\tc", 100); got != "a b c" {
		t.Errorf("collapseWS = %q", got)
	}
}

func TestJumpToLogEntry(t *testing.T) {
	m, s := newLogTestModel(t)
	var target *pglog.Entry
	for i := range s.log.report.Entries {
		if s.log.report.Entries[i].Category == pglog.CatSlowQuery {
			target = &s.log.report.Entries[i]
		}
	}
	gs := m.logGroupScreen(s, &s.log.report.Groups[0])
	es := m.logEntryScreen(gs, target)
	m.stack = append(m.stack, gs, es)
	s.filter = "pgbouncer"

	m.jumpToLogEntry(s, target)
	if m.top() != s || s.log.view != logViewTimeline {
		t.Fatalf("top=%v view=%v", m.top().level, s.log.view)
	}
	if s.filter != "" {
		t.Errorf("filter not lifted: %q", s.filter)
	}
	vis := s.visibleIndexes()
	if got := s.logEntryOf(s.items[vis[s.cursor]]); got == nil || got.Off != target.Off {
		t.Errorf("cursor not on the target entry: %+v", got)
	}
}

func TestLogHostnameColumn(t *testing.T) {
	m, s := newLogTestModel(t)
	s.log.view = logViewTimeline
	m.rebuildLogItems(s)
	if indexOfCol(s.log.cols, logColHostname) < 0 || indexOfCol(s.log.cols, logColHost) >= 0 {
		t.Fatal("hostname must be on and the raw host column off by default")
	}
	s.log.hosts = map[string]string{"2a00:1f78:fffd:4301::1221": "app-01.example"}
	m.rebuildLogItems(s)
	col := indexOfCol(s.log.cols, logColHostname)
	if col < 0 {
		t.Fatal("hostname column not projected")
	}
	var resolved, raw bool
	for _, it := range s.items {
		switch it.data.([]pg.DiagCell)[col].Display {
		case "app-01.example":
			resolved = true
		case "2a00:1f78:fffd:4301::1212":
			raw = true
		}
	}
	if !resolved || !raw {
		t.Errorf("resolved=%v raw-fallback=%v", resolved, raw)
	}
}

func TestLogDescribeTarget(t *testing.T) {
	m, s := newLogTestModel(t)
	s.db = "shop"
	var dup *pglog.Group
	for _, it := range s.items {
		if g, ok := it.data.(*pglog.Group); ok && strings.HasPrefix(g.Title, "duplicate key") {
			dup = g
		}
	}
	gs := m.logGroupScreen(s, dup)
	m.stack = append(m.stack, gs)
	m.rebuildLogChild(gs)
	// Only the older duplicate-key error carries STATEMENT: INSERT INTO channel(...) …
	gs.cursor = len(gs.items) - 1
	tgt, ok := describeTarget(gs)
	if !ok || !tgt.byName || tgt.tableName != "channel" || tgt.db != "shop" {
		t.Errorf("group row target = %+v ok=%v", tgt, ok)
	}
	// On the overview a group row falls back to its newest sample with a statement.
	for vi, idx := range s.visibleIndexes() {
		if g, ok := s.items[idx].data.(*pglog.Group); ok && g == dup {
			s.cursor = vi
		}
	}
	tgt, ok = describeTarget(s)
	if !ok || !tgt.byName || tgt.tableName != "channel" || tgt.db != "shop" {
		t.Errorf("group row target = %+v ok=%v", tgt, ok)
	}
	// A slow-query entry resolves through its SQL.
	for i := range s.log.report.Entries {
		if e := &s.log.report.Entries[i]; e.Category == pglog.CatSlowQuery {
			es := m.logEntryScreen(gs, e)
			if tgt, ok := describeTarget(es); !ok || tgt.tableName != "event_log" {
				t.Errorf("slow entry target = %+v ok=%v", tgt, ok)
			}
			break
		}
	}
	// A checkpoint line has no statement to describe.
	for i := range s.log.report.Entries {
		if e := &s.log.report.Entries[i]; e.Category == pglog.CatCheckpoint {
			if _, ok := describeTarget(m.logEntryScreen(gs, e)); ok {
				t.Error("checkpoint entry should not be describable")
			}
		}
	}
}

func TestLogDurationColumnCheckpoint(t *testing.T) {
	m, s := newLogTestModel(t)
	s.log.view = logViewTimeline
	m.rebuildLogItems(s)
	col := indexOfCol(s.log.cols, logColDuration)
	for _, it := range s.items {
		e := s.logEntryOf(it)
		if e == nil || e.Category != pglog.CatCheckpoint {
			continue
		}
		c := it.data.([]pg.DiagCell)[col]
		if !c.HasNum || c.Num != 959437 || c.Display != "16.0m" {
			t.Errorf("checkpoint dur cell = %+v", c)
		}
	}
}
