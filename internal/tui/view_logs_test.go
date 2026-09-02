package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

// sampleLogReport is a small parsed log with every category the groups pane
// sections on, built through the real parser so the test exercises the same
// path as a loaded file.
func sampleLogReport(t *testing.T) *pg.LogReport {
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
	r, err := pg.LoadLog(t.Context(), src, "", time.UTC, 0, pg.AggOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// memLogSource is an in-memory pg.LogSource for tests.
type memLogSource struct{ data []byte }

func (s *memLogSource) Info() pg.LogSourceInfo {
	return pg.LogSourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Size: int64(len(s.data))}
}

func (s *memLogSource) ReadTail(_ context.Context, n int64) ([]byte, pg.LogWindow, error) {
	return s.data, pg.LogWindow{Requested: n, FileSize: int64(len(s.data)), Bytes: int64(len(s.data))}, nil
}

func (s *memLogSource) ReadFrom(context.Context, int64) ([]byte, error) { return nil, pg.ErrNotIncremental }
func (s *memLogSource) Cursor(context.Context) *pg.LogCursor              { return nil }

func newLogTestModel(t *testing.T) (*Model, *screen) {
	t.Helper()
	// Wide enough that the rows under test (title + stats suffix, DETAIL tail)
	// are not clipped by truncateToWidth.
	m := &Model{width: 320, height: 40}
	// Built by hand rather than via logScreen, which needs a live client.
	s := &screen{
		level: levelLogs, title: "log", tool: toolLogs,
		logSrc: &memLogSource{}, logWindow: logDefaultWindow, loaded: true,
		sort: sortByCount, sortDesc: true, // logShowSpam left false: the tests below start from the filtered view
	}
	s.logReport = sampleLogReport(t)
	m.stack = []*screen{{level: levelTools}, s}
	m.rebuildLogItems(s)
	return m, s
}

func TestLogGroupItemsSectionsAndSpam(t *testing.T) {
	m, s := newLogTestModel(t)

	// Spam hidden: only errors + warnings + temp files remain, each under its
	// section header, errors first.
	var sections []string
	var titles []string
	for _, it := range s.items {
		switch v := it.data.(type) {
		case logSection:
			sections = append(sections, v.title)
		case *pg.LogGroup:
			titles = append(titles, v.Title)
			if v.Category.IsSpam() {
				t.Errorf("spam group %q shown with spam hidden", v.Title)
			}
		}
	}
	if strings.Join(sections, ",") != "errors,warnings,temp files" {
		t.Errorf("sections = %v", sections)
	}
	if len(titles) != 4 || titles[0] != `duplicate key value violates unique constraint "channel_name_plugin_idx"` {
		t.Errorf("titles = %v", titles)
	}
	// The cursor never rests on a header row.
	s.resetCursor()
	m.skipLogHeader(s, 1)
	if _, hdr := s.items[s.visibleIndexes()[s.cursor]].data.(logSection); hdr {
		t.Error("cursor rests on a section header")
	}

	// Spam shown: slow queries and checkpoints appear, and their sections
	// trail the signal ones.
	s.logShowSpam = true
	m.rebuildLogItems(s)
	sections = sections[:0]
	for _, it := range s.items {
		if v, ok := it.data.(logSection); ok {
			sections = append(sections, v.title)
		}
	}
	if strings.Join(sections, ",") != "errors,warnings,temp files,slow queries,checkpoints" {
		t.Errorf("sections with spam = %v", sections)
	}

	// Flat mode: no headers at all.
	s.logGroupBy = logGroupByNone
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
	if strings.Contains(out, "DELETE FROM event_log") {
		t.Error("slow query shown with spam hidden")
	}

	hdr := stripANSI(m.renderLogHeader(s))
	for _, want := range []string{
		"postgresql-17-main.log", "whole file", "prefix %t [%p-%l] %q%u@%h", "detected",
		"8 entries", "2 errors", "1 fatal", "1 warnings", "2 slow", "1 ckpt",
		"spam hidden (3)", "by category", "⟳ off",
		"errors", "all", "per cell",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("header missing %q:\n%s", want, hdr)
		}
	}

	// Timeline pane: generic table with the default columns, newest first.
	s.logView = logViewTimeline
	m.rebuildLogItems(s)
	if s.diagCols == nil || len(s.items) != 5 {
		t.Fatalf("timeline: cols=%v items=%d", s.diagCols, len(s.items))
	}
	if s.diagCols[s.diagSortCol].Name != "time" || !s.sortDesc {
		t.Errorf("timeline default sort = %s desc=%v", s.diagCols[s.diagSortCol].Name, s.sortDesc)
	}
	first := s.items[0].data.([]pg.DiagCell)
	if first[0].Display != "07:00:00" {
		t.Errorf("newest entry first: got %q", first[0].Display)
	}
	if e := s.logEntryOf(s.items[0]); e == nil || e.Severity != pg.SevWarning {
		t.Errorf("logEntryOf on a timeline row = %+v", e)
	}
	table := stripANSI(m.renderDiagResult(s, 20))
	if !strings.Contains(table, "WARNING") || !strings.Contains(table, "pgbouncer") {
		t.Errorf("timeline table:\n%s", table)
	}
}

func TestLogGroupAndEntryScreens(t *testing.T) {
	m, s := newLogTestModel(t)
	var dup *pg.LogGroup
	for _, it := range s.items {
		if g, ok := it.data.(*pg.LogGroup); ok && strings.HasPrefix(g.Title, "duplicate key") {
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
	for _, want := range []string{"severity", "ERROR", "pid", "872628", "MESSAGE", "duplicate key", "DETAIL", "Key (name, plugin)", "STATEMENT", "INSERT INTO channel"} {
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
	vis := m.visibleLogCols()
	if len(vis) == 0 || vis[0].id != logColTime {
		t.Errorf("default visible = %v", vis)
	}
	// Hiding the sort column falls back to time desc.
	m.logSortColID = logColPID
	m.ensureLogColsInit()
	m.logColsVisible[logColPID] = false
	s := &screen{}
	m.syncLogSort(s, m.visibleLogCols())
	if m.logSortColID != logColTime || !s.sortDesc {
		t.Errorf("sort fallback = %q desc=%v", m.logSortColID, s.sortDesc)
	}
}

func TestLogFileItems(t *testing.T) {
	items := logFileItems([]pg.LogCandidate{
		{Info: pg.LogSourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Size: 1 << 20, Current: true}, Reason: "/var/log/postgresql"},
		{Info: pg.LogSourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.2.gz", Size: 4096, Rotated: true}, Reason: "/var/log/postgresql"},
	})
	if len(items) != 2 || !items[0].hasChildren {
		t.Fatalf("items = %+v", items)
	}
	if !strings.Contains(items[0].detail, "current") || !strings.Contains(items[0].detail, "1.00 MB") {
		t.Errorf("detail = %q", items[0].detail)
	}
	if !strings.Contains(items[1].detail, "gz") || strings.Contains(items[1].detail, "current") {
		t.Errorf("gz detail = %q", items[1].detail)
	}
	m := &Model{width: 120}
	s := &screen{level: levelLogFiles, loaded: true, items: items}
	out := stripANSI(m.renderLogFiles(s, 5))
	if !strings.Contains(out, "postgresql-17-main.log.2.gz") || !strings.Contains(out, "via /var/log/postgresql") {
		t.Errorf("picker:\n%s", out)
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
	var target *pg.LogEntry
	for i := range s.logReport.Entries {
		if s.logReport.Entries[i].Category == pg.CatSlowQuery {
			target = &s.logReport.Entries[i]
		}
	}
	gs := m.logGroupScreen(s, &s.logReport.Groups[0])
	es := m.logEntryScreen(gs, target)
	m.stack = append(m.stack, gs, es)
	s.filter = "pgbouncer"

	m.jumpToLogEntry(s, target)
	if m.top() != s || s.logView != logViewTimeline {
		t.Fatalf("top=%v view=%v", m.top().level, s.logView)
	}
	if !s.logShowSpam || s.filter != "" {
		t.Errorf("filters not lifted: spam=%v filter=%q", s.logShowSpam, s.filter)
	}
	vis := s.visibleIndexes()
	if got := s.logEntryOf(s.items[vis[s.cursor]]); got == nil || got.Off != target.Off {
		t.Errorf("cursor not on the target entry: %+v", got)
	}
}

func TestLogHostnameColumn(t *testing.T) {
	m, s := newLogTestModel(t)
	s.logView = logViewTimeline
	m.rebuildLogItems(s)
	if idx := indexOfLogCol(s.logCols, logColHostname); idx >= 0 {
		t.Fatal("hostname column must be opt-in")
	}
	m.ensureLogColsInit()
	m.logColsVisible[logColHostname] = true
	s.logHosts = map[string]string{"2a00:1f78:fffd:4301::1221": "app-01.example"}
	m.rebuildLogItems(s)
	col := indexOfLogCol(s.logCols, logColHostname)
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
