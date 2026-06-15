package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
)

// keyMsg builds a tea.KeyMsg for a literal key string: " " becomes the space
// key (which the Refresh binding matches), anything else a rune key press.
func keyMsg(s string) tea.KeyMsg {
	if s == " " {
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// renderModel builds a Model with a given screen on top and renders it. The
// client is never used by the render path, so a non-connecting one is fine.
func renderModel(top *screen) string {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	m.width, m.height = 200, 40
	m.stack = append(m.stack, top)
	return m.View()
}

// cellByID resolves a footer/row cell by its stable column id, independent of
// where the column lands in the current projection.
func cellByID(descs []stmtColDesc, cells []pg.DiagCell, id stmtColID) pg.DiagCell {
	i := indexOfStmtCol(descs, id)
	if i < 0 || i >= len(cells) {
		return pg.DiagCell{}
	}
	return cells[i]
}

func TestRenderStatementsTable(t *testing.T) {
	rows := []pg.QueryStat{
		{QueryID: 1, Query: "select * from t where id = $1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100, WALBytes: 4096},
		{QueryID: 2, Query: "update t set x = $1 where id = $2", Calls: 10, Rows: 10, TotalExecTime: 50, SharedBlksHit: 5, SharedBlksRead: 5},
	}
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	items, descs, windowMs, total := m.buildStatementItems(rows, true)
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, statRows: rows, statWindowExecMs: windowMs, items: items,
		diagCols: diagColumnsFrom(descs), stmtCols: descs, diagBarCol: -1, diagTotalRow: total,
		diagSortCol: 0, sortDesc: true,
		statBaselineAt: time.Now().Add(-90 * time.Second), statSampledAt: time.Now(),
		statTrackPlanning: true,
	}
	out := renderModel(s)
	for _, want := range []string{"total_ms", "hit%", "plan_ms", "miss", "blk/row", "query", "window", "since", "← Sum"} {
		if !strings.Contains(out, want) {
			t.Errorf("statements table missing %q in output", want)
		}
	}
}

// The pinned footer totals the whole table: additive columns are summed and the
// derived columns are pooled (mean = Σtime÷Σcalls, time% = 100, hit% weighted).
func TestStatementsTotalRow(t *testing.T) {
	rows := []pg.QueryStat{
		{QueryID: 1, Query: "select 1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100, WALBytes: 4096},
		{QueryID: 2, Query: "select 2", Calls: 10, Rows: 10, TotalExecTime: 50, SharedBlksHit: 5, SharedBlksRead: 5},
	}
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	_, descs, _, total := m.buildStatementItems(rows, true)
	if total == nil {
		t.Fatal("expected a total row for a non-empty table")
	}
	if got := cellByID(descs, total, colCalls).Num; got != 110 {
		t.Errorf("total calls = %v, want 110", got)
	}
	if got := cellByID(descs, total, colRows).Num; got != 110 {
		t.Errorf("total rows = %v, want 110", got)
	}
	if got := cellByID(descs, total, colTotalMs).Num; got != 550 {
		t.Errorf("total total_ms = %v, want 550", got)
	}
	if got := cellByID(descs, total, colPctTime).Num; got != 100 {
		t.Errorf("total time%% = %v, want 100 (whole window)", got)
	}
	// Pooled mean: 550ms ÷ 110 calls = 5, not the (5+5)/2 average of per-row means.
	if got := cellByID(descs, total, colMeanMs).Num; got != 5 {
		t.Errorf("pooled mean_ms = %v, want 5", got)
	}
	if got := cellByID(descs, total, colQuery).Display; got != "← Sum" {
		t.Errorf("query column should be labelled, got %q", got)
	}
	// An empty table yields no footer.
	if _, _, _, empty := m.buildStatementItems(nil, true); empty != nil {
		t.Error("empty table should have no total row")
	}
}

// With track_planning off the plan_ms column is dropped from the table and a
// note pointing at the setting is shown above it.
func TestRenderStatementsTrackPlanningOff(t *testing.T) {
	rows := []pg.QueryStat{
		{QueryID: 1, Query: "select * from t where id = $1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100},
	}
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	items, descs, windowMs, total := m.buildStatementItems(rows, false)
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, statRows: rows, statWindowExecMs: windowMs, items: items,
		diagCols: diagColumnsFrom(descs), stmtCols: descs, diagBarCol: -1, diagTotalRow: total,
		diagSortCol: 0, sortDesc: true,
		statBaselineAt: time.Now().Add(-30 * time.Second), statSampledAt: time.Now(),
		statTrackPlanning: false,
	}
	out := renderModel(s)
	if strings.Contains(out, "plan_ms") {
		t.Error("plan_ms column should be hidden when track_planning is off")
	}
	if !strings.Contains(out, "track_planning off") {
		t.Error("expected a note pointing at the track_planning setting")
	}
	// Other columns must survive the drop.
	for _, want := range []string{"total_ms", "mean_ms", "hit%", "blk/row", "query"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q after dropping plan_ms", want)
		}
	}
}

// When pg_stat_statements isn't installed the table is replaced by a blocking
// install prompt — no table, instructions on how to install instead.
func TestStatementsMissingExtension(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	m.width, m.height = 120, 30
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		items: []item{{name: "x"}}, diagCols: m.statementColumns(true),
	}
	m.stack = append(m.stack, s)
	m.onStatementsLoaded(statementsLoadedMsg{
		db:  "test",
		err: &pg.MissingExtensionError{Extension: "pg_stat_statements", DB: "test", Installable: true},
	})
	if s.extPrompt == nil || !s.extPrompt.blocking {
		t.Fatal("expected a blocking install prompt when pg_stat_statements is missing")
	}
	if len(s.items) != 0 || s.diagCols != nil {
		t.Error("table should be cleared when the extension is missing")
	}
	out := m.View()
	for _, want := range []string{"Extension required", "pg_stat_statements", "CREATE EXTENSION"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing-extension view missing %q", want)
		}
	}
	if strings.Contains(out, "total_ms") {
		t.Error("no table should render when the extension is missing")
	}
}

// A read-only SELECT in the detail view offers the EXPLAIN ANALYZE affordance
// once a sample call is available; a non-read-only statement must not.
func TestRenderStatementDetailAnalyzeAffordance(t *testing.T) {
	sel := pg.QueryStat{QueryID: 1, Query: "select * from t where id = $1", Calls: 1}
	s := &screen{
		level: levelStatementDetail, title: "query", tool: toolQueries, db: "test",
		loaded: true, statDetail: &sel, statWindowExecMs: 100,
		statSampleCall: "select * from t where id = 1::integer",
	}
	if out := renderModel(s); !strings.Contains(out, "ANALYZE") {
		t.Error("read-only SELECT detail should offer EXPLAIN ANALYZE")
	}

	upd := pg.QueryStat{QueryID: 2, Query: "update t set x = $1 where id = $2", Calls: 1}
	s.statDetail = &upd
	s.statSampleCall = "update t set x = 1 where id = 2"
	if out := renderModel(s); strings.Contains(out, "ANALYZE") {
		t.Error("UPDATE detail must not offer EXPLAIN ANALYZE (it would execute)")
	}
}

func TestRenderStatementDetailAndInfo(t *testing.T) {
	q := pg.QueryStat{
		QueryID: 7, Query: "select * from t where id = $1", Calls: 50, Rows: 50,
		TotalExecTime: 250, TotalPlanTime: 12, Plans: 50,
		SharedBlksHit: 400, SharedBlksRead: 100, WALBytes: 8192,
	}
	s := &screen{
		level: levelStatementDetail, title: "query", tool: toolQueries, db: "test",
		loaded: true, statDetail: &q, statWindowExecMs: 1000,
		statSampleCall: "select * from t where id = 1::integer",
		statExplain:    "Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)\n  Filter: (id = $1)",
	}
	out := renderModel(s)
	for _, want := range []string{"query 7", "window metrics", "plan time", "blocks/row", "sample call", "explain (generic plan)", "Seq Scan"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q", want)
		}
	}

	// The ? info overlay must render without panicking and cover the columns.
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	m.width, m.height = 200, 50
	m.stack = append(m.stack, s)
	m.showInfo = true
	if m.View() == "" {
		t.Fatal("info overlay rendered empty")
	}
	// View clips the (scrollable) overlay to the viewport at the current scroll
	// offset, so assert content coverage against the full unscrolled body.
	info := m.renderInfoOverlay(s, 200)
	for _, want := range []string{"Top queries reference", "the window", "columns", "plan_ms", "miss", "blk/row", "cost colours", "GENERIC_PLAN"} {
		if !strings.Contains(info, want) {
			t.Errorf("info overlay missing %q", want)
		}
	}
}

// The detail view names the parameter source: real captured values when
// pg_qualstats fed a real example, a "synthesized — install pg_qualstats" note
// when it's absent, and the explain header flips real/generic to match.
func TestRenderStatementDetailSourceHint(t *testing.T) {
	sel := pg.QueryStat{QueryID: 1, Query: "select * from t where id = $1", Calls: 1}
	base := func() *screen {
		return &screen{
			level: levelStatementDetail, title: "query", tool: toolQueries, db: "test",
			loaded: true, statDetail: &sel, statWindowExecMs: 100,
			statSampleCall: "select * from t where id = 42",
		}
	}

	// Real values from pg_qualstats: "real values" hint, "(real plan)" header,
	// and the captured-values affordance offered.
	s := base()
	s.statSampleReal = true
	s.statQualstats = true
	out := renderModel(s)
	for _, want := range []string{"real values · pg_qualstats", "explain (real plan)", "to browse the real values"} {
		if !strings.Contains(out, want) {
			t.Errorf("real-source detail missing %q", want)
		}
	}
	if strings.Contains(out, "generic plan") {
		t.Error("real-source detail must not show the generic-plan header")
	}

	// No pg_qualstats: synthesized, with the install hint and generic plan.
	s = base()
	s.statSampleReal = false
	s.statQualstats = false
	out = renderModel(s)
	if !strings.Contains(out, "synthesized — install pg_qualstats") {
		t.Error("missing-qualstats detail should suggest installing pg_qualstats")
	}
	if !strings.Contains(out, "explain (generic plan)") {
		t.Error("missing-qualstats detail should use the generic-plan header")
	}
	if strings.Contains(out, "to browse the real values") {
		t.Error("captured-values affordance must not show without pg_qualstats")
	}

	// pg_qualstats absent but preloaded: an install offer is surfaced, so both
	// the sample-call hint and the non-blocking ext hint point at the i key.
	s = base()
	s.statSampleReal = false
	s.statQualstats = false
	s.extPrompt = &extPrompt{name: extQualstats, db: "test", installable: true, reason: extPromptReasonQualstats}
	out = renderModel(s)
	if !strings.Contains(out, "press i to install pg_qualstats for real values") {
		t.Error("preloaded-but-absent detail should offer the i-key install in the sample hint")
	}
	if !strings.Contains(out, "to install pg_qualstats") {
		t.Error("preloaded-but-absent detail should render the install ext hint")
	}
}

// The captured-values level lists real constants with their frequency and
// offers EXPLAIN ANALYZE on the highlighted one.
func TestRenderStatementSamples(t *testing.T) {
	sel := pg.QueryStat{QueryID: 9, Query: "select * from t where id = $1", Calls: 1}
	samples := []pg.QualSample{
		{Relation: "t", Column: "id", Operator: "=", ConstValue: "42::integer", Occurrences: 1500},
		{Relation: "t", Column: "id", Operator: "=", ConstValue: "7::integer", Occurrences: 30},
	}
	s := &screen{
		level: levelStatementSamples, title: "values", tool: toolQueries, db: "test",
		loaded: true, statDetail: &sel, statQualstats: true, statSampleReal: true,
		statSampleCall: "select * from t where id = 42::integer",
		items:          sampleItems(samples),
	}
	out := renderModel(s)
	for _, want := range []string{"captured values · query 9", "t.id = 42::integer", "t.id = 7::integer", "EXPLAIN (ANALYZE)"} {
		if !strings.Contains(out, want) {
			t.Errorf("samples view missing %q", want)
		}
	}
}

func TestSampleAnalyzeQuery(t *testing.T) {
	one := pg.QualSample{Column: "id", Operator: "=", ConstValue: "42::integer"}
	// Single-parameter query: the captured constant is unambiguously $1.
	if got := sampleAnalyzeQuery("select * from t where id = $1", "example", one); got != "select * from t where id = 42::integer" {
		t.Errorf("single-param substitution wrong: %q", got)
	}
	// Multi-parameter query: can't map one constant to one of several $n — fall
	// back to the representative example query.
	if got := sampleAnalyzeQuery("select * from t where a = $1 and b = $2", "EXAMPLE", one); got != "EXAMPLE" {
		t.Errorf("multi-param should fall back to example, got %q", got)
	}
}

func TestUniqueParams(t *testing.T) {
	cases := map[string]int{
		"select 1":                                0,
		"select * from t where id = $1":           1,
		"select * from t where a=$1 and b=$2":     2,
		"values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)": 10,
		"where x = $1 or y = $1":                  1, // distinct, not occurrences
	}
	for q, want := range cases {
		if got := uniqueParams(q); got != want {
			t.Errorf("uniqueParams(%q) = %d, want %d", q, got, want)
		}
	}
}

// The projected columns and every row's cells (and the footer) must stay the
// same length and order regardless of which columns are visible — the generic
// renderer walks them strictly in parallel by index.
func TestStatementColumnProjectionParallel(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	rows := []pg.QueryStat{{QueryID: 1, Query: "select 1", Calls: 1, TotalExecTime: 1}}

	check := func(label string) {
		items, descs, _, total := m.buildStatementItems(rows, true)
		cols := diagColumnsFrom(descs)
		for _, it := range items {
			cells, _ := it.data.([]pg.DiagCell)
			if len(cells) != len(cols) {
				t.Errorf("%s: row cells=%d, cols=%d (must stay parallel)", label, len(cells), len(cols))
			}
		}
		if total != nil && len(total) != len(cols) {
			t.Errorf("%s: total cells=%d, cols=%d", label, len(total), len(cols))
		}
	}
	check("defaults")

	m.ensureStmtColsInit()
	for _, d := range stmtColumnRegistry() {
		m.stmtColsVisible[d.id] = true
	}
	check("all opt-in enabled")
	m.stmtColsVisible[colMiss] = false
	check("a default column hidden")

	// The query column is mandatory: it survives even when explicitly disabled.
	m.stmtColsVisible[colQuery] = false
	_, descs, _, _ := m.buildStatementItems(rows, true)
	if indexOfStmtCol(descs, colQuery) < 0 {
		t.Error("query column must always be present (mandatory)")
	}
}

// Planning columns are unavailable when track_planning is off, even if the user
// enabled them — they'd always read zero.
func TestStatementColumnsTrackPlanningGate(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	m.ensureStmtColsInit()
	m.stmtColsVisible[colPlanMs] = true
	m.stmtColsVisible[colMeanPlanMs] = true
	m.stmtColsVisible[colPlans] = true

	off := m.visibleStmtCols(stmtCtx{trackPlanning: false})
	for _, id := range []stmtColID{colPlanMs, colMeanPlanMs, colPlans} {
		if indexOfStmtCol(off, id) >= 0 {
			t.Errorf("%q must be hidden when track_planning is off even if enabled", id)
		}
	}
	on := m.visibleStmtCols(stmtCtx{trackPlanning: true})
	if indexOfStmtCol(on, colPlanMs) < 0 {
		t.Error("plan_ms should appear when track_planning is on and enabled")
	}
}

// When the sorted column is no longer visible, syncStmtSort re-pins the sort to
// total_ms (the default) rather than dangling at a stale index.
func TestSyncStmtSortFallback(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	s := &screen{level: levelStatements}
	m.stmtSortColID = colPlans // opt-in + planning-gated, so absent from the defaults
	descs := m.visibleStmtCols(stmtCtx{trackPlanning: true})
	m.syncStmtSort(s, descs)
	if m.stmtSortColID != colTotalMs {
		t.Errorf("hidden sort column should fall back to total_ms, got %q", m.stmtSortColID)
	}
	if s.diagSortCol < 0 || s.diagSortCol >= len(descs) || descs[s.diagSortCol].id != colTotalMs {
		t.Errorf("diagSortCol should point at total_ms")
	}
	if !s.sortDesc {
		t.Error("fallback should default to descending")
	}
}

// Opening the C overlay and toggling a column rebuilds the table from the cached
// window and reflects in the rendered output, without a DB round-trip.
func TestColumnConfigToggleRebuilds(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	m.width, m.height = 200, 40
	rows := []pg.QueryStat{{QueryID: 1, Query: "select 1", Calls: 1, TotalExecTime: 1, TempBlksRead: 7}}
	items, descs, windowMs, total := m.buildStatementItems(rows, true)
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, statRows: rows, statWindowExecMs: windowMs, items: items,
		diagCols: diagColumnsFrom(descs), stmtCols: descs, diagBarCol: -1, diagTotalRow: total,
		diagSortCol: 0, sortDesc: true,
		statBaselineAt: time.Now().Add(-time.Minute), statSampledAt: time.Now(),
		statTrackPlanning: true,
	}
	m.stack = append(m.stack, s)

	// temp_read is opt-in (off by default), so it isn't in the table yet.
	if strings.Contains(m.View(), "temp_read") {
		t.Fatal("temp_read should be hidden by default")
	}
	// Open the picker, move to temp_read, toggle it on.
	m.ensureStmtColsInit()
	m.showColumnConfig = true
	m.colCfgCursor = indexInRegistry(colTempRead)
	if cmd := m.handleColumnConfigKey(s, keyMsg(" ")); cmd != nil {
		t.Fatal("toggling a column should not issue a command (no DB round-trip)")
	}
	if !m.showColumnConfig {
		t.Fatal("space should toggle the column, not close the overlay")
	}
	m.showColumnConfig = false // close the overlay to render the table
	if !strings.Contains(m.View(), "temp_read") {
		t.Error("temp_read should appear in the table after enabling it")
	}
}

func indexInRegistry(id stmtColID) int {
	return indexOfStmtCol(stmtColumnRegistry(), id)
}

func TestSampleLabel(t *testing.T) {
	if got := sampleLabel(pg.QualSample{Relation: "t", Column: "id", Operator: "=", ConstValue: "42"}); got != "t.id = 42" {
		t.Errorf("full label wrong: %q", got)
	}
	if got := sampleLabel(pg.QualSample{Column: "id", ConstValue: "42"}); got != "id = 42" {
		t.Errorf("no-relation label should default operator to =: %q", got)
	}
	if got := sampleLabel(pg.QualSample{ConstValue: "42"}); got != "42" {
		t.Errorf("bare-value label wrong: %q", got)
	}
}

// An empty window (no activity since baseline) must render the header without
// tripping the generic table's no-rows path.
func TestRenderStatementsEmptyWindow(t *testing.T) {
	mb := NewModel(pg.New(cli.Config{}), 2*time.Second, "", nil, "")
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, diagCols: mb.statementColumns(true), diagBarCol: -1, diagSortCol: 0,
		statBaselineAt: time.Now(), statSampledAt: time.Now(),
	}
	out := renderModel(s)
	if !strings.Contains(out, "queries") {
		t.Error("empty-window render lost the header")
	}
}
