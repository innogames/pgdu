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
	m := NewModel(pg.New(cli.Config{}))
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
	m := NewModel(pg.New(cli.Config{}))
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
	m := NewModel(pg.New(cli.Config{}))
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
