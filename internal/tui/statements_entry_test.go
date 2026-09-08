package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
)

// newTestModel builds a Model with the default key map and no live connection,
// enough to drive the pure Update paths (message handlers, key dispatch).
func newTestModel(stack ...*screen) *Model {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "", "")
	m.target = "db:5432"
	m.hostLabel = "db:5432"
	if len(stack) > 0 {
		m.stack = append(m.stack[:1], stack...)
	}
	return m
}

// Picking a database in the Top-queries tool pushes the (unloaded) table and,
// on top of it, the snapshot browser in entry mode; other tools keep the
// single schemas screen.
func TestDatabaseChildScreens(t *testing.T) {
	got := databaseChildScreens(toolQueries, "app")
	if len(got) != 2 || got[0].level != levelStatements || got[1].level != levelSnapshots {
		t.Fatalf("toolQueries screens = %v, want [statements, snapshots]", got)
	}
	if !got[1].stat.entry || !got[1].loading || got[1].db != "app" || got[0].db != "app" {
		t.Errorf("entry browser not armed: entry=%v loading=%v dbs=%q/%q", got[1].stat.entry, got[1].loading, got[0].db, got[1].db)
	}
	if got[0].loaded || got[0].stat.baseline != nil {
		t.Error("the statements table must not load before the base is picked")
	}
	if other := databaseChildScreens(toolDisk, "app"); len(other) != 1 || other[0].level != levelSchemas {
		t.Errorf("toolDisk screens = %v, want [schemas]", other)
	}
}

// The entry picker lists session start first (preselected, barred with the live
// count), then the snapshots, then since-last-reset — and no "now" row, since
// the end of an entry window is always live. The L browser keeps "now" on top.
func TestSnapshotsListedEntryPicker(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	metas := []pg.SnapshotMeta{
		{Path: "/snaps/b.json.gz", Target: "db:5432", Database: "app", CapturedAt: t1.Add(time.Hour), QueryCount: 30},
		{Path: "/snaps/a.json.gz", Target: "db:5432", Database: "app", CapturedAt: t1, QueryCount: 20},
	}
	msg := snapshotsListedMsg{metas: metas, liveCount: 42}

	st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	browser := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loading: true, stat: stmtState{entry: true}}
	m := newTestModel(st, browser)
	m.onSnapshotsListed(msg)

	paths := func(s *screen) []string {
		out := make([]string, len(s.items))
		for i, it := range s.items {
			out[i] = it.snapPath
		}
		return out
	}
	want := []string{snapSession, "/snaps/b.json.gz", "/snaps/a.json.gz", snapReset}
	if got := paths(browser); !slices.Equal(got, want) {
		t.Errorf("entry rows = %q, want %q", got, want)
	}
	if browser.cursor != 0 || browser.items[0].size != 42 {
		t.Errorf("session start should be preselected and barred with the live count: cursor=%d size=%d", browser.cursor, browser.items[0].size)
	}
	if !browser.loaded || browser.loading {
		t.Error("browser should be marked loaded")
	}

	// The L browser over a running table: "now" leads, session start follows.
	st.stat.sessionStart = t1
	st.stat.sessionBaseline = map[int64]pg.QueryStat{1: {}}
	lb := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loading: true}
	m = newTestModel(st, lb)
	m.onSnapshotsListed(msg)
	want = []string{snapNow, snapSession, "/snaps/b.json.gz", "/snaps/a.json.gz", snapReset}
	if got := paths(lb); !slices.Equal(got, want) {
		t.Errorf("L rows = %q, want %q", got, want)
	}
	if lb.items[0].size != 42 || lb.items[1].size != 1 {
		t.Errorf("now/session sizes = %d/%d, want 42/1", lb.items[0].size, lb.items[1].size)
	}
}

// Esc on the entry picker leaves the tool: the table beneath has never loaded.
// Esc on the L browser over a loaded table just returns to the table.
func TestEntryPickerBackLeavesTool(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}

	st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	browser := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loaded: true, stat: stmtState{entry: true}}
	m := newTestModel(st, browser)
	m.handleKey(esc)
	if len(m.stack) != 1 || m.top().level != levelTools {
		t.Errorf("after Esc on the entry picker the stack should be back at the tool menu, got %d screens (top %v)", len(m.stack), m.top().level)
	}

	loaded := &screen{level: levelStatements, tool: toolQueries, db: "app", loaded: true}
	lb := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loaded: true}
	m = newTestModel(loaded, lb)
	m.handleKey(esc)
	if len(m.stack) != 2 || m.top() != loaded {
		t.Errorf("Esc on the L browser should return to the table, got %d screens (top %v)", len(m.stack), m.top().level)
	}
}

// Enter on the entry picker's rows configures the table's window and pops the
// browser: session start → a fresh live baseline; since last reset → the
// cumulative window. Both return the table's load command.
func TestEntryPickerEnter(t *testing.T) {
	for _, c := range []struct {
		path       string
		cumulative bool
	}{{snapSession, false}, {snapReset, true}} {
		st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
		browser := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loaded: true, stat: stmtState{entry: true}}
		m := newTestModel(st, browser)
		m.onSnapshotsListed(snapshotsListedMsg{liveCount: 3})
		for i, it := range browser.items {
			if it.snapPath == c.path {
				browser.cursor = i
			}
		}
		cmd := m.drillIn()
		if cmd == nil {
			t.Errorf("%s: expected the table's load command", c.path)
		}
		if m.top() != st {
			t.Errorf("%s: browser should pop back to the table, top is %v", c.path, m.top().level)
		}
		if st.stat.cumulative != c.cumulative {
			t.Errorf("%s: cumulative = %v, want %v", c.path, st.stat.cumulative, c.cumulative)
		}
		if c.cumulative && st.stat.baseline == nil {
			t.Errorf("%s: cumulative window needs an empty (non-nil) baseline", c.path)
		}
		if !c.cumulative && st.stat.baseline != nil {
			t.Errorf("%s: session start must leave the baseline to the first live sample", c.path)
		}
	}
}

// The first live sample becomes the session anchor whatever base was picked at
// entry: a cumulative window keeps its empty baseline but still records where
// the session started, so the L browser can offer "session start" later.
func TestStatementsLoadedCapturesSessionAnchor(t *testing.T) {
	stats := []pg.QueryStat{{QueryID: 1, Query: "select 1", Calls: 5}, {QueryID: 2, Query: "select 2", Calls: 7}}

	cum := &screen{level: levelStatements, tool: toolQueries, db: "app", stat: stmtState{cumulative: true, baseline: map[int64]pg.QueryStat{}}}
	m := newTestModel(cum)
	m.onStatementsLoaded(statementsLoadedMsg{db: "app", stats: stats})
	if len(cum.stat.sessionBaseline) != 2 || cum.stat.sessionStart.IsZero() {
		t.Errorf("cumulative window did not capture the session anchor: %d entries, start %v", len(cum.stat.sessionBaseline), cum.stat.sessionStart)
	}
	if len(cum.stat.baseline) != 0 || len(cum.stat.rows) != 2 {
		t.Errorf("cumulative window should diff against an empty baseline: baseline=%d rows=%d", len(cum.stat.baseline), len(cum.stat.rows))
	}

	live := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	m = newTestModel(live)
	m.onStatementsLoaded(statementsLoadedMsg{db: "app", stats: stats})
	if len(live.stat.baseline) != 2 || len(live.stat.sessionBaseline) != 2 || !live.stat.baselineAt.Equal(live.stat.sessionStart) {
		t.Errorf("session window should take the first sample as both baseline and anchor: baseline=%d anchor=%d", len(live.stat.baseline), len(live.stat.sessionBaseline))
	}
}

// The entry picker renders its own header (base picker, window preview from
// the highlighted row) and tags the preselected session-start row as default.
func TestRenderEntryPicker(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	browser := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loading: true, stat: stmtState{entry: true}}
	m := newTestModel(st, browser)
	m.width, m.height = 200, 40
	m.onSnapshotsListed(snapshotsListedMsg{
		metas:     []pg.SnapshotMeta{{Path: "/snaps/a.json.gz", Target: "db:5432", Database: "app", CapturedAt: t1, QueryCount: 20}},
		liveCount: 42,
	})
	out := ansi.Strip(m.View())
	for _, want := range []string{"pick the window's base", "window: session start → now", "session start", "default", "since last reset", "a.json.gz", "new snapshots: press S"} {
		if !strings.Contains(out, want) {
			t.Errorf("entry picker missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "now · live") {
		t.Errorf("entry picker must not list the now row:\n%s", out)
	}
	// Moving onto the snapshot row previews that window instead.
	browser.cursor = 1
	if out := ansi.Strip(m.View()); !strings.Contains(out, "window: "+t1.Local().Format("2006-01-02 15:04:05")+" → now") {
		t.Errorf("preview should follow the cursor:\n%s", out)
	}
}

// A window whose baseline the entry picker installs before the first load
// (cumulative / snapshot) must open in the same default order as the live
// window: total_ms descending — even when an earlier visit left the shared
// sort column set, so the syncSort fallback that also flips the direction
// does not kick in.
func TestStatementsEntryWindowsDefaultSortDesc(t *testing.T) {
	stats := []pg.QueryStat{
		{QueryID: 1, Query: "select 1", Calls: 5, TotalExecTime: 1},
		{QueryID: 2, Query: "select 2", Calls: 7, TotalExecTime: 100},
		{QueryID: 3, Query: "select 3", Calls: 7, TotalExecTime: 10},
	}
	check := func(t *testing.T, m *Model, s *screen) {
		t.Helper()
		if !s.sortDesc || m.stmtTable.sortColID != colTotalMs {
			t.Fatalf("sort = %v desc=%v, want total_ms desc", m.stmtTable.sortColID, s.sortDesc)
		}
		if len(s.items) != 3 || s.items[0].name != "select 2" || s.items[2].name != "select 1" {
			t.Errorf("rows not in total_ms desc order: %v", s.items)
		}
	}

	cum := &screen{level: levelStatements, tool: toolQueries, db: "app", stat: stmtState{cumulative: true, baseline: map[int64]pg.QueryStat{}}}
	m := newTestModel(cum)
	m.stmtTable.sortColID = colTotalMs // left over from a previous visit
	m.onStatementsLoaded(statementsLoadedMsg{db: "app", stats: stats})
	check(t, m, cum)

	end := &pg.Snapshot{CapturedAt: time.Now(), Stats: stats}
	frozen := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	m = newTestModel(frozen)
	m.stmtTable.sortColID = colTotalMs
	m.onSnapshotFrozenLoaded(snapshotFrozenLoadedMsg{end: end, cumulative: true})
	check(t, m, frozen)
}
