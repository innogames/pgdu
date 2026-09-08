package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pgdu/internal/pg"
)

func TestLSNSpan(t *testing.T) {
	cases := []struct {
		start, end string
		want       int64
		ok         bool
	}{
		{"3CAA5/C710E000", "3CAA5/C810E000", 16 << 20, true}, // one segment
		{"0/FFFFFFF0", "1/10", 32, true},                     // crosses the hi boundary
		{"0/0", "0/0", 0, true},
		{"1/0", "0/0", 0, false}, // inverted
		{"", "0/10", 0, false},   // unresolved
		{"nope", "0/10", 0, false},
	}
	for _, c := range cases {
		got, ok := lsnSpan(c.start, c.end)
		if got != c.want || ok != c.ok {
			t.Errorf("lsnSpan(%q, %q) = %d,%v want %d,%v", c.start, c.end, got, ok, c.want, c.ok)
		}
	}
}

// The header's window line must follow the rmgr load that resolves it, not the
// (faster) summary load: on first load the summary lands before the window
// exists, and on refresh it would otherwise carry the previous window.
func TestWALSummaryWindowFollowsOverviewLoad(t *testing.T) {
	s := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", sort: sortBySize, sortDesc: true}
	m := newTestModel(s)
	m.width = 200

	m.onWALSummaryLoaded(walSummaryLoadedMsg{db: "app", summary: pg.WALSummary{InsertLSN: "3CAA5/C81E87B0"}})
	if got := ansi.Strip(m.renderWALSummary(s)); !strings.Contains(got, "window: not resolved") {
		t.Errorf("before the rmgr load the window must read unresolved, got %q", got)
	}

	m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", start: "3CAA5/B3BB0000", end: "3CAA5/B4BB0000",
		stats: []pg.WALRmgrStat{{Name: "Heap", Count: 1, RecordSize: 10, FPISize: 90, CombinedSize: 100}}})
	got := ansi.Strip(m.renderWALSummary(s))
	if !strings.Contains(got, "window: 3CAA5/B3BB0000 … 3CAA5/B4BB0000  ·  last 16.00 MB analysed") {
		t.Errorf("first window missing from header: %q", got)
	}

	// Refresh: the summary re-lands first (old window still on the screen),
	// then the rmgr load moves the window. The header must show the new one.
	m.onWALSummaryLoaded(walSummaryLoadedMsg{db: "app", summary: pg.WALSummary{InsertLSN: "3CAA5/D0000000"}})
	m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", start: "3CAA5/C710E000", end: "3CAA5/C810E000",
		stats: []pg.WALRmgrStat{{Name: "Heap", Count: 1, RecordSize: 10, FPISize: 90, CombinedSize: 100}}})
	got = ansi.Strip(m.renderWALSummary(s))
	if !strings.Contains(got, "window: 3CAA5/C710E000 … 3CAA5/C810E000") || strings.Contains(got, "B3BB0000") {
		t.Errorf("header kept the stale window after refresh: %q", got)
	}
	// The clamped resolver can hand back less than a full segment; the label
	// follows the LSNs, not the constant.
	m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", start: "3CAA5/C800E000", end: "3CAA5/C810E000",
		stats: []pg.WALRmgrStat{{Name: "Heap", Count: 1, CombinedSize: 1}}})
	if got := ansi.Strip(m.renderWALSummary(s)); !strings.Contains(got, "last 1.00 MB analysed") {
		t.Errorf("window size must be the LSN span: %q", got)
	}
}

func TestWALTotalsRows(t *testing.T) {
	rmgrs := []pg.WALRmgrStat{
		{Name: "Heap", Count: 3800, RecordSize: 789 << 10, FPISize: 6 << 20, CombinedSize: (789 << 10) + (6 << 20)},
		{Name: "Transaction", Count: 240, RecordSize: 8 << 10, CombinedSize: 8 << 10},
	}
	got := ansi.Strip(renderWALRmgrTotals(rmgrs, 10))
	for _, want := range []string{"6.78 MB", "797.00 KB", "6.00 MB", "4.0k", "Σ 2 resource managers"} {
		if !strings.Contains(got, want) {
			t.Errorf("rmgr Σ row lacks %q: %q", want, got)
		}
	}
	if renderWALRmgrTotals(nil, 10) != "" {
		t.Error("rmgr Σ row must be empty with no rows")
	}

	rels := []pg.WALRelStat{
		{RelName: "battle", DataBytes: 1 << 20, FPIBytes: 9 << 20, RecCount: 2800, BlockCount: 1600},
		{RelName: "worker", DataBytes: 1 << 20, FPIBytes: 1 << 20, RecCount: 200, BlockCount: 100},
	}
	got = ansi.Strip(renderWALRelTotals(rels, 10))
	for _, want := range []string{"12.00 MB", "10.00 MB (83%)", "3.0k", "1.7k", "Σ 2 relations"} {
		if !strings.Contains(got, want) {
			t.Errorf("relation Σ row lacks %q: %q", want, got)
		}
	}

	// The by-relation title names how much of the window the block-level
	// bytes cover, so the gap to the rmgr table doesn't read as lost WAL.
	s := &screen{level: levelWAL, db: "app", loaded: true,
		wal: walState{start: "3CAA5/C710E000", end: "3CAA5/C810E000", rels: rels}}
	title := ansi.Strip(renderWALRelationsTitle(s))
	if !strings.Contains(title, "12.00 MB total · 75.0% of the 16.00 MB window") {
		t.Errorf("relations title lacks window coverage: %q", title)
	}
	// Without rows the title is just the label — the note row says why.
	s.wal.rels = nil
	if title := ansi.Strip(renderWALRelationsTitle(s)); strings.Contains(title, "total") {
		t.Errorf("empty relation table must not print totals: %q", title)
	}
}

// walKinds flattens a list into a readable signature: rmgr/relation names and
// the section kinds, in order.
func walKinds(items []item) []string {
	var out []string
	for _, it := range items {
		switch v := it.data.(type) {
		case pg.WALRmgrStat:
			out = append(out, "rmgr:"+v.Name)
		case pg.WALRelStat:
			out = append(out, "rel:"+it.name)
		case walSectionRow:
			out = append(out, []string{"blank", "Σrmgr", "title", "header", "loading", "error", "note", "Σrel"}[v.kind])
		}
	}
	return out
}

// The overview is one list with two tables: the rmgr rows with their Σ, then
// the by-relation title, header, rows and Σ — each table sorted on its own,
// and the relation half a single note line while its scan is pending / failed.
func TestBuildWALItemsLayout(t *testing.T) {
	rmgrs := []pg.WALRmgrStat{
		{Name: "Heap", Count: 10, CombinedSize: 100},
		{Name: "Btree", Count: 20, CombinedSize: 300},
	}
	rels := []pg.WALRelStat{
		{RelName: "public.small", DataBytes: 5, RecCount: 1, RelFileNode: 1},
		{RelName: "public.big", DataBytes: 50, RecCount: 4, RelFileNode: 2},
		{RelFileNode: 4711, DBName: "other", DataBytes: 1, RecCount: 1},
	}
	s := &screen{level: levelWAL, db: "app", loaded: true, sort: sortBySize, sortDesc: true,
		wal: walState{rmgrs: rmgrs, rels: rels}}
	want := "rmgr:Btree,rmgr:Heap,Σrmgr,blank,title,note,header,rel:public.big,rel:public.small,rel:relfilenode 4711,Σrel"
	if got := strings.Join(walKinds(buildWALItems(s)), ","); got != want {
		t.Errorf("layout\n got %s\nwant %s", got, want)
	}
	// Reversing the direction reorders both tables, nothing else.
	s.sortDesc = false
	want = "rmgr:Heap,rmgr:Btree,Σrmgr,blank,title,note,header,rel:relfilenode 4711,rel:public.small,rel:public.big,Σrel"
	if got := strings.Join(walKinds(buildWALItems(s)), ","); got != want {
		t.Errorf("ascending layout\n got %s\nwant %s", got, want)
	}

	s.wal.rels, s.wal.relsLoading = nil, true
	if got := strings.Join(walKinds(buildWALItems(s)), ","); got != "rmgr:Heap,rmgr:Btree,Σrmgr,blank,title,loading" {
		t.Errorf("loading layout: %s", got)
	}
	s.wal.relsLoading, s.wal.relsErr = false, errors.New("boom")
	if got := strings.Join(walKinds(buildWALItems(s)), ","); got != "rmgr:Heap,rmgr:Btree,Σrmgr,blank,title,error" {
		t.Errorf("error layout: %s", got)
	}
	s.wal.relsErr = nil
	if got := strings.Join(walKinds(buildWALItems(s)), ","); got != "rmgr:Heap,rmgr:Btree,Σrmgr,blank,title,note" {
		t.Errorf("empty layout: %s", got)
	}
}

// The relation scan is chained off the overview load (it needs the resolved
// window), fills the second table only for the window the screen still shows,
// and never disturbs the settled rmgr half when it fails.
func TestWALRelationsFollowOverview(t *testing.T) {
	s := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", sort: sortBySize, sortDesc: true}
	m := newTestModel(s)
	cmd := m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", start: "0/1000", end: "0/2000",
		stats: []pg.WALRmgrStat{{Name: "Heap", Count: 1, CombinedSize: 1}}})
	if cmd == nil {
		t.Fatal("overview load must chain the relation scan")
	}
	if !s.wal.relsLoading || !s.loaded {
		t.Errorf("relsLoading=%v loaded=%v after the overview landed", s.wal.relsLoading, s.loaded)
	}
	// A late answer for a previous window is dropped.
	m.onWALRelationsLoaded(walRelationsLoadedMsg{db: "app", start: "0/0", end: "0/1000", rels: []pg.WALRelStat{{RelName: "stale"}}})
	if !s.wal.relsLoading || len(s.wal.rels) != 0 {
		t.Errorf("stale relation result was accepted: %+v", s.wal.rels)
	}
	m.onWALRelationsLoaded(walRelationsLoadedMsg{db: "app", start: "0/1000", end: "0/2000", rels: []pg.WALRelStat{{RelName: "t", DataBytes: 1, RecCount: 1}}})
	if s.wal.relsLoading || len(s.wal.rels) != 1 {
		t.Errorf("relation result not applied: loading=%v rels=%+v", s.wal.relsLoading, s.wal.rels)
	}
	if got := strings.Join(walKinds(s.items), ","); got != "rmgr:Heap,Σrmgr,blank,title,header,rel:t,Σrel" {
		t.Errorf("items after both loads: %s", got)
	}
	// A failed scan is the relation table's problem alone.
	m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", start: "0/2000", end: "0/3000",
		stats: []pg.WALRmgrStat{{Name: "Heap", Count: 1, CombinedSize: 1}}})
	m.onWALRelationsLoaded(walRelationsLoadedMsg{db: "app", start: "0/2000", end: "0/3000", err: errors.New("permission denied")})
	if s.err != nil || !s.loaded || s.wal.relsErr == nil {
		t.Errorf("relation failure leaked into the screen: err=%v loaded=%v relsErr=%v", s.err, s.loaded, s.wal.relsErr)
	}
	// An overview error leaves nothing to chain.
	if cmd := m.onWALOverviewLoaded(walOverviewLoadedMsg{db: "app", err: errors.New("no pg_walinspect")}); cmd != nil {
		t.Error("a failed overview must not chain the relation scan")
	}
}

// The cursor moves across both tables without ever resting on a section row,
// Enter opens records for a rmgr and block refs for a relation, and a filter
// keeps the section rows so each table keeps its own header.
func TestWALOverviewNavigation(t *testing.T) {
	s := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", loaded: true, sort: sortBySize, sortDesc: true,
		wal: walState{start: "0/1000", end: "0/2000",
			rmgrs: []pg.WALRmgrStat{{Name: "Heap", Count: 1, CombinedSize: 2}, {Name: "Btree", Count: 1, CombinedSize: 1}},
			rels:  []pg.WALRelStat{{RelName: "public.a", DataBytes: 2, RecCount: 1}, {RelName: "public.b", DataBytes: 1, RecCount: 1}}}}
	m := newTestModel(s)
	m.width, m.height = 200, 50
	m.applySort(s)
	press := func(k tea.KeyType) { m.Update(tea.KeyMsg{Type: k}) }
	at := func() string { return walKinds([]item{s.items[s.visibleIndexes()[s.cursor]]})[0] }

	press(tea.KeyDown) // Btree
	press(tea.KeyDown) // over Σ, blank, title, header → first relation
	if got := at(); got != "rel:public.a" {
		t.Errorf("down from the last rmgr landed on %s", got)
	}
	press(tea.KeyUp)
	if got := at(); got != "rmgr:Btree" {
		t.Errorf("up from the first relation landed on %s", got)
	}
	press(tea.KeyEnd)
	if got := at(); got != "rel:public.b" {
		t.Errorf("End landed on %s", got)
	}
	press(tea.KeyDown) // nothing selectable below: stay
	if got := at(); got != "rel:public.b" {
		t.Errorf("down past the end landed on %s", got)
	}
	press(tea.KeyHome)
	if got := at(); got != "rmgr:Heap" {
		t.Errorf("Home landed on %s", got)
	}

	// Enter dispatches by table.
	m.drillIn()
	if top := m.top(); top.level != levelWALRecords || top.wal.rmgr != "Heap" {
		t.Errorf("Enter on a rmgr pushed %v (rmgr %q)", top.level, top.wal.rmgr)
	}
	m.stack = m.stack[:len(m.stack)-1]
	press(tea.KeyEnd)
	m.drillIn()
	if top := m.top(); top.level != levelWALRelBlocks || top.wal.relLabel != "public.b" {
		t.Errorf("Enter on a relation pushed %v (%q)", top.level, top.wal.relLabel)
	}
	m.stack = m.stack[:len(m.stack)-1]

	// Filtering keeps the frame rows and hides the non-matching data rows.
	s.filter = "public"
	s.itemsRev++
	if got := strings.Join(walKinds(indexItems(s)), ","); got != "Σrmgr,blank,title,header,rel:public.a,rel:public.b,Σrel" {
		t.Errorf("filtered view: %s", got)
	}
	s.filter = ""
	s.itemsRev++
	if out := ansi.Strip(m.View()); !strings.Contains(out, "Σ 2 resource managers") || !strings.Contains(out, "Σ 2 relations") ||
		!strings.Contains(out, "by relation") || strings.Contains(out, "w by relation") {
		t.Errorf("rendered overview lacks the two tables or still advertises w:\n%s", out)
	}
	// A list with no selectable row at all (the window came back empty) must
	// not spin the skip forever.
	s.wal.rmgrs, s.wal.rels = nil, nil
	m.applySort(s)
	s.skipInertRow(1)
}

func indexItems(s *screen) []item {
	vis := s.visibleIndexes()
	out := make([]item, 0, len(vis))
	for _, i := range vis {
		out = append(out, s.items[i])
	}
	return out
}
