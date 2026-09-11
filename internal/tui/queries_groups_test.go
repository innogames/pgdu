package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// groupRows is a small window: three statements on public.t (two selects, one
// update), one on s.u, and a BEGIN with no table.
func groupRows() []pg.QueryStat {
	return []pg.QueryStat{
		{QueryID: 1, Query: "select * from t where id = $1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100, WALBytes: 10},
		{QueryID: 2, Query: "select x from t where y = $1", Calls: 50, Rows: 500, TotalExecTime: 250, SharedBlksHit: 50, SharedBlksRead: 50},
		{QueryID: 3, Query: "update t set x = $1 where id = $2", Calls: 10, Rows: 10, TotalExecTime: 50, WALBytes: 4096},
		{QueryID: 4, Query: "select * from s.u", Calls: 1, Rows: 1, TotalExecTime: 100},
		{QueryID: 5, Query: "BEGIN", Calls: 1000, TotalExecTime: 100},
	}
}

// groupCellByID resolves a grouped row's cell by stable column id.
func groupCellByID(descs []stmtGroupColDesc, cells []pg.DiagCell, id stmtColID) pg.DiagCell {
	i := indexOfCol(descs, id)
	if i < 0 || i >= len(cells) {
		return pg.DiagCell{}
	}
	return cells[i]
}

// groupedStatementsScreen is a loaded top-queries table over groupRows with
// the model's stack holding it, ready for key-driven tests.
func groupedStatementsScreen() (*Model, *screen) {
	rows := groupRows()
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test", loaded: true,
		stat: stmtState{rows: rows, baselineAt: time.Now().Add(-time.Minute), sampledAt: time.Now(), trackPlanning: true}}
	m := newTestModel(s)
	m.width, m.height = 220, 40
	m.rebuildStatementItems(s)
	return m, s
}

// The by-table roll-up folds statements by their qualified main table, pools
// the counters (mean_ms = Σtime÷Σcalls, weighted hit%) and buckets statements
// without a table; the Σ footer reconciles with the whole window.
func TestGroupQueryStatsByTable(t *testing.T) {
	rows := groupRows()
	groups := groupQueryStats(rows, stmtViewByTable)
	byKey := map[string]stmtGroup{}
	for _, g := range groups {
		byKey[g.key] = g
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (t, s.u, no table)", len(groups))
	}
	g := byKey["t"]
	if g.n != 3 || g.sum.Calls != 160 || g.sum.TotalExecTime != 800 || g.sum.WALBytes != 4106 {
		t.Errorf("group t = n%d calls=%d ms=%v wal=%d", g.n, g.sum.Calls, g.sum.TotalExecTime, g.sum.WALBytes)
	}
	if g.display != "t" {
		t.Errorf("display %q", g.display)
	}
	if u := byKey["s.u"]; u.n != 1 || u.display != "s.u" {
		t.Errorf("qualified table kept as key and display: %+v", u)
	}
	if nt := byKey[stmtNoTable]; nt.n != 1 || nt.sum.Calls != 1000 {
		t.Errorf("no-table bucket: %+v", nt)
	}

	m := newTestModel()
	items, descs, total := m.buildStatementGroupItems(stmtViewByTable, rows, windowExecMs(rows), true)
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	for _, it := range items {
		if !it.hasChildren || it.stmtGroupKey == "" {
			t.Errorf("group rows must be drillable and carry their key: %+v", it.name)
		}
		cells := it.data.([]pg.DiagCell)
		if it.stmtGroupKey == "t" {
			if got := groupCellByID(descs, cells, colMeanMs).Num; got != 5 {
				t.Errorf("pooled mean_ms = %v, want 800/160 = 5", got)
			}
			if got := groupCellByID(descs, cells, colPctTime).Num; got != 80 {
				t.Errorf("time%% = %v, want 800/1000 = 80", got)
			}
			if got := groupCellByID(descs, cells, colQueries).Num; got != 3 {
				t.Errorf("queries = %v, want 3", got)
			}
			if got := groupCellByID(descs, cells, colGroup).Display; got != "t" {
				t.Errorf("key column = %q", got)
			}
		}
	}
	if descs[len(descs)-1].name != "table" {
		t.Errorf("key column header = %q, want table", descs[len(descs)-1].name)
	}
	if total == nil {
		t.Fatal("footer missing")
	}
	sum := sumQueryStats(rows)
	if got := groupCellByID(descs, total, colTotalMs).Num; got != sum.TotalExecTime {
		t.Errorf("Σ total_ms = %v, want %v", got, sum.TotalExecTime)
	}
	if got := groupCellByID(descs, total, colQueries).Num; got != 5 {
		t.Errorf("Σ queries = %v, want 5", got)
	}
	if got := groupCellByID(descs, total, colGroup).Display; got != "← Sum" {
		t.Errorf("footer label = %q", got)
	}
	if _, _, empty := m.buildStatementGroupItems(stmtViewByTable, nil, 0, true); empty != nil {
		t.Error("empty window has no footer")
	}
}

// The by-type roll-up keys on the command-type tag and heads its key column T.
func TestGroupQueryStatsByType(t *testing.T) {
	rows := groupRows()
	groups := groupQueryStats(rows, stmtViewByType)
	got := map[string]int{}
	for _, g := range groups {
		got[g.key] = g.n
	}
	if got["S"] != 3 || got["U"] != 1 || got["T"] != 1 {
		t.Errorf("by type = %v", got)
	}
	m := newTestModel()
	_, descs, _ := m.buildStatementGroupItems(stmtViewByType, rows, windowExecMs(rows), true)
	if descs[len(descs)-1].name != "T" {
		t.Errorf("key column header = %q, want T", descs[len(descs)-1].name)
	}
}

// Every grouped row, the footer and the projected columns stay parallel for
// any visibility set; the key column is mandatory.
func TestGroupColumnProjectionParallel(t *testing.T) {
	m := newTestModel()
	rows := groupRows()
	check := func(label string, v stmtView) {
		items, descs, total := m.buildStatementGroupItems(v, rows, windowExecMs(rows), true)
		for _, it := range items {
			if cells := it.data.([]pg.DiagCell); len(cells) != len(descs) {
				t.Errorf("%s: row cells=%d, cols=%d", label, len(cells), len(descs))
			}
		}
		if len(total) != len(descs) {
			t.Errorf("%s: total cells=%d, cols=%d", label, len(total), len(descs))
		}
		if indexOfCol(descs, colGroup) < 0 {
			t.Errorf("%s: key column must always be present", label)
		}
	}
	check("defaults", stmtViewByTable)
	stmtGroupSpec(stmtViewByTable).ensureInit(&m.stmtGroupTable)
	for _, d := range stmtGroupColumnRegistry(stmtViewByTable) {
		m.stmtGroupTable.visible[d.id] = true
	}
	check("all on", stmtViewByType)
	m.stmtGroupTable.visible[colGroup] = false
	m.stmtGroupTable.visible[colQueries] = false
	check("key hidden", stmtViewByTable)
	// The roll-ups' picker is its own: the list's visibility is untouched.
	if m.stmtTable.visible != nil {
		t.Error("the list's picker state must not be touched by the roll-ups")
	}
}

// Tab cycles list → by table → by type → list in place, rebuilding the table
// each time; the bar rides the sort column on the roll-ups and is off on the list.
func TestStatementsTabCyclesViews(t *testing.T) {
	m, s := groupedStatementsScreen()
	m.keys.applyContext(s)
	if !m.keys.StmtView.Enabled() {
		t.Fatal("tab must be enabled on the top-queries table")
	}
	if s.diagBarCol != -1 {
		t.Errorf("list bar = %d, want none", s.diagBarCol)
	}
	tab := tea.KeyMsg{Type: tea.KeyTab}

	m.Update(tab)
	if s.stat.view != stmtViewByTable || len(s.items) != 3 {
		t.Fatalf("after tab: view=%v items=%d", s.stat.view, len(s.items))
	}
	if s.diagCols[len(s.diagCols)-1].Name != "table" {
		t.Errorf("last column = %q, want table", s.diagCols[len(s.diagCols)-1].Name)
	}
	if s.stat.cols != nil || s.stat.groupCols == nil {
		t.Error("exactly the grouped descs must be set on a roll-up")
	}
	// Default sort total_ms↓ → the bar sits on total_ms.
	if s.diagBarCol != s.diagSortCol || s.stat.groupCols[s.diagSortCol].id != colTotalMs {
		t.Errorf("bar=%d sort=%d id=%v", s.diagBarCol, s.diagSortCol, s.stat.groupCols[s.diagSortCol].id)
	}
	if !s.sortDesc {
		t.Error("roll-up opens sorted descending")
	}
	if !strings.Contains(m.View(), "3 tables of 5 queries") {
		t.Error("header should count the tables of the window")
	}

	// → moves the sort (and the bar) to the next column; a text column has no bar.
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if s.diagBarCol != s.diagSortCol {
		t.Errorf("bar must follow the sort: bar=%d sort=%d", s.diagBarCol, s.diagSortCol)
	}
	if m.stmtGroupTable.sortColID != s.stat.groupCols[s.diagSortCol].id {
		t.Error("cycleSort must record the roll-up's sort column on its own picker state")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m.Update(tea.KeyMsg{Type: tea.KeyLeft}) // wraps onto the key column (text)
	if s.stat.groupCols[s.diagSortCol].id != colGroup || s.diagBarCol != -1 {
		t.Errorf("text sort column has no bar: id=%v bar=%d", s.stat.groupCols[s.diagSortCol].id, s.diagBarCol)
	}

	m.Update(tab)
	if s.stat.view != stmtViewByType || s.diagCols[len(s.diagCols)-1].Name != "T" {
		t.Fatalf("second tab: view=%v", s.stat.view)
	}
	m.Update(tab)
	if s.stat.view != stmtViewQueries || len(s.items) != 5 || s.diagBarCol != -1 {
		t.Fatalf("third tab returns to the list: view=%v items=%d bar=%d", s.stat.view, len(s.items), s.diagBarCol)
	}
	if m.stmtTable.sortColID != colTotalMs {
		t.Error("the list's sort memory must survive a trip through the roll-ups")
	}
}

// Enter on a roll-up row narrows the list to that group in place; time% stays
// relative to the whole window while the footer sums the group; Esc widens
// once, then leaves the screen.
func TestStatementsGroupNarrowAndEsc(t *testing.T) {
	m, s := groupedStatementsScreen()
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	// Move the cursor onto the t row (sorted total_ms↓ it is first, but resolve by name).
	vis := s.visibleIndexes()
	for i, idx := range vis {
		if s.items[idx].stmtGroupKey == "t" {
			s.cursor = i
		}
	}
	label, ok := enterLabel(s)
	if !ok || label != "narrow to table" {
		t.Errorf("enter label = %q %v", label, ok)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.stat.view != stmtViewQueries || s.stat.group == nil || s.stat.group.key != "t" {
		t.Fatalf("enter should narrow the list: view=%v group=%+v", s.stat.view, s.stat.group)
	}
	if len(s.items) != 3 {
		t.Fatalf("narrowed items = %d, want 3", len(s.items))
	}
	if s.stat.windowExecMs != 1000 {
		t.Errorf("time%% denominator must stay the whole window: %v", s.stat.windowExecMs)
	}
	if got := cellByID(s.stat.cols, s.diagTotalRow, colTotalMs).Num; got != 800 {
		t.Errorf("narrowed footer sums the group: %v", got)
	}
	if got := cellByID(s.stat.cols, s.diagTotalRow, colPctTime).Num; got != 80 {
		t.Errorf("narrowed footer time%% = %v, want 80 of the window", got)
	}
	if out := m.View(); !strings.Contains(out, "table t · 3 of 5 queries · esc widens") {
		t.Error("header should name the narrowing")
	}
	if label, _ := enterLabel(s); label != "query detail" {
		t.Errorf("narrowed list drills into detail, got %q", label)
	}

	// A live refresh re-diffs and rebuilds through the same narrowing.
	s.stat.baseline = map[int64]pg.QueryStat{}
	m.Update(statementsLoadedMsg{db: "test", stats: groupRows(), trackPlanning: true})
	if s.stat.group == nil || len(s.items) != 3 || len(s.stat.rows) != 5 {
		t.Errorf("refresh must keep the narrowing: group=%v items=%d rows=%d", s.stat.group, len(s.items), len(s.stat.rows))
	}

	depth := len(m.stack)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.stat.group != nil || len(s.items) != 5 || len(m.stack) != depth {
		t.Fatalf("first esc widens in place: group=%v items=%d stack=%d", s.stat.group, len(s.items), len(m.stack))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.stack) != depth-1 {
		t.Error("second esc leaves the screen")
	}
}

// Esc on a roll-up returns to the list rather than leaving the screen.
func TestStatementsEscLeavesRollup(t *testing.T) {
	m, s := groupedStatementsScreen()
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	depth := len(m.stack)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if s.stat.view != stmtViewQueries || len(m.stack) != depth {
		t.Errorf("esc on a roll-up: view=%v stack=%d", s.stat.view, len(m.stack))
	}
}

// d / u on a by-table row resolve the group's qualified table; type rows and
// the no-table bucket have nothing to describe.
func TestStatementsGroupDescribeTarget(t *testing.T) {
	m, s := groupedStatementsScreen()
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	place := func(key string) {
		for i, idx := range s.visibleIndexes() {
			if s.items[idx].stmtGroupKey == key {
				s.cursor = i
			}
		}
	}
	place("s.u")
	tgt, ok := stmtDescribeTarget(s)
	if !ok || !tgt.byName || tgt.tableName != "s.u" || tgt.db != "test" {
		t.Errorf("describe target = %+v %v", tgt, ok)
	}
	place(stmtNoTable)
	if _, ok := stmtDescribeTarget(s); ok {
		t.Error("the no-table bucket has nothing to describe")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if _, ok := stmtDescribeTarget(s); ok {
		t.Error("type rows have nothing to describe")
	}
}

// The roll-ups' C picker opens its own state and toggles rebuild the grouped table.
func TestStatementsGroupColumnPicker(t *testing.T) {
	m, s := groupedStatementsScreen()
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(keyMsg("C"))
	if !m.stmtGroupTable.showCfg || m.stmtTable.showCfg {
		t.Fatal("C on a roll-up opens the roll-ups' picker")
	}
	if !strings.Contains(m.View(), "by table roll-up") {
		t.Error("picker title should name the roll-up")
	}
	before := len(s.diagCols)
	m.stmtGroupTable.cfgCursor = indexOfCol(stmtGroupColumnRegistry(stmtViewByTable), colMiss)
	m.Update(keyMsg(" "))
	if len(s.diagCols) != before+1 {
		t.Errorf("toggling miss on should add a column: %d → %d", before, len(s.diagCols))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.stmtGroupTable.showCfg {
		t.Error("esc closes the picker")
	}
}
