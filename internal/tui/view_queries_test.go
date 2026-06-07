package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
)

// renderModel builds a Model with a given screen on top and renders it. The
// client is never used by the render path, so a non-connecting one is fine.
func renderModel(top *screen) string {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second)
	m.width, m.height = 200, 40
	m.stack = append(m.stack, top)
	return m.View()
}

func TestRenderStatementsTable(t *testing.T) {
	rows := []pg.QueryStat{
		{QueryID: 1, Query: "select * from t where id = $1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100, WALBytes: 4096},
		{QueryID: 2, Query: "update t set x = $1 where id = $2", Calls: 10, Rows: 10, TotalExecTime: 50, SharedBlksHit: 5, SharedBlksRead: 5},
	}
	items, windowMs := buildStatementItems(rows, true)
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, statRows: rows, statWindowExecMs: windowMs, items: items,
		diagCols: statementColumns(true), diagBarCol: -1, diagSortCol: colStmtTotalMs, sortDesc: true,
		statBaselineAt: time.Now().Add(-90 * time.Second), statSampledAt: time.Now(),
		statTrackPlanning: true,
	}
	out := renderModel(s)
	for _, want := range []string{"total_ms", "hit%", "plan_ms", "miss", "query", "window", "since"} {
		if !strings.Contains(out, want) {
			t.Errorf("statements table missing %q in output", want)
		}
	}
}

// With track_planning off the plan_ms column is dropped from the table and a
// note pointing at the setting is shown above it.
func TestRenderStatementsTrackPlanningOff(t *testing.T) {
	rows := []pg.QueryStat{
		{QueryID: 1, Query: "select * from t where id = $1", Calls: 100, Rows: 100, TotalExecTime: 500, SharedBlksHit: 900, SharedBlksRead: 100},
	}
	items, windowMs := buildStatementItems(rows, false)
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, statRows: rows, statWindowExecMs: windowMs, items: items,
		diagCols: statementColumns(false), diagBarCol: -1, diagSortCol: colStmtTotalMs, sortDesc: true,
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
	for _, want := range []string{"total_ms", "mean_ms", "hit%", "query"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q after dropping plan_ms", want)
		}
	}
}

// When pg_stat_statements isn't installed the table is replaced by a blocking
// install prompt — no table, instructions on how to install instead.
func TestStatementsMissingExtension(t *testing.T) {
	m := NewModel(pg.New(cli.Config{}), 2*time.Second)
	m.width, m.height = 120, 30
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		items: []item{{name: "x"}}, diagCols: statementColumns(true),
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
	for _, want := range []string{"query 7", "window metrics", "plan time", "sample call", "explain (generic plan)", "Seq Scan"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q", want)
		}
	}

	// The ? info overlay must render without panicking and cover the columns.
	m := NewModel(pg.New(cli.Config{}), 2*time.Second)
	m.width, m.height = 200, 50
	m.stack = append(m.stack, s)
	m.showInfo = true
	info := m.View()
	for _, want := range []string{"Top queries reference", "the window", "columns", "plan_ms", "miss", "GENERIC_PLAN"} {
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
	s := &screen{
		level: levelStatements, title: "queries", tool: toolQueries, db: "test",
		loaded: true, diagCols: statementColumns(true), diagBarCol: -1, diagSortCol: colStmtTotalMs,
		statBaselineAt: time.Now(), statSampledAt: time.Now(),
	}
	out := renderModel(s)
	if !strings.Contains(out, "queries") {
		t.Error("empty-window render lost the header")
	}
}
