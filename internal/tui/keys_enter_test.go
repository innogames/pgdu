package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// enterLabel names Enter's destination per level and row, and turns the key off
// where drillIn would do nothing.
func TestEnterLabel(t *testing.T) {
	heap := pg.Part{Name: "t", Kind: pg.PartHeap}
	idx := pg.Part{Name: "t_pkey", Kind: pg.PartIndex}
	sel := pg.QueryStat{QueryID: 1, Query: "select 1"}
	upd := pg.QueryStat{QueryID: 2, Query: "update t set x = 1"}
	with := func(s *screen, its ...item) *screen { s.items = its; s.loaded = true; return s }
	cases := []struct {
		name  string
		s     *screen
		label string
		ok    bool
	}{
		{"tools", with(&screen{level: levelTools}), "open tool", true},
		{"databases disk", with(&screen{level: levelDatabases, tool: toolDisk}), "schemas", true},
		{"databases queries", with(&screen{level: levelDatabases, tool: toolQueries}), "top queries", true},
		{"databases diag picker", with(&screen{level: levelDatabases, tool: toolTools, diag: &pg.Diagnostic{}}), "run", true},
		{"schemas buffers", with(&screen{level: levelSchemas, tool: toolBuffers}), "buffers", true},
		{"tables disk", with(&screen{level: levelTables, tool: toolDisk}), "parts", true},
		{"tables pages", with(&screen{level: levelTables, tool: toolPageInspect}), "heap pages", true},
		{"parts heap row", with(&screen{level: levelParts}, partToItem(heap)), "columns", true},
		{"parts index row", with(&screen{level: levelParts}, partToItem(idx)), "", false},
		{"table overview", with(&screen{level: levelTableStats}), "disk parts", true},
		{"heap tuple normal", with(&screen{level: levelHeapTuples},
			item{data: pg.HeapTuple{LPFlags: pg.LPNormal, Ctid: new("(0,1)")}}), "byte layout", true},
		{"heap tuple redirect", with(&screen{level: levelHeapTuples},
			item{data: pg.HeapTuple{LPFlags: pg.LPRedirect}}), "follow hop", true},
		{"heap tuple dead", with(&screen{level: levelHeapTuples},
			item{data: pg.HeapTuple{LPFlags: pg.LPDead}}), "", false},
		{"relation index", with(&screen{level: levelRelations},
			item{data: pg.Relation{Kind: pg.RelBTreeIndex}}), "index pages", true},
		{"index tuples gin", with(&screen{level: levelIndexTuples, pages: pageState{index: pg.Relation{AccessMethod: "gin"}}}), "", false},
		{"index tuples downlink", with(&screen{level: levelIndexTuples, pages: pageState{index: pg.Relation{AccessMethod: "btree"}, indexPageType: "i"}},
			item{data: pg.IndexTuple{ItemOffset: 2}}), "child page", true},
		{"index tuples posting", with(&screen{level: levelIndexTuples, pages: pageState{index: pg.Relation{AccessMethod: "btree"}}},
			item{data: pg.IndexTuple{ItemOffset: 2, Posting: []pg.IndexTuple{{}}}}), "unfold", true},
		{"wal rmgr", with(&screen{level: levelWAL}, walRmgrToItem(pg.WALRmgrStat{Name: "Heap", Count: 1})), "records", true},
		{"wal section", with(&screen{level: levelWAL}, walSectionItem(walRowRelTitle)), "", false},
		{"statements", with(&screen{level: levelStatements}), "query detail", true},
		// ANALYZE runs the sample call, so Enter needs a complete one to run.
		{"statement detail select", with(&screen{level: levelStatementDetail, stat: stmtState{detail: &sel, sampleCall: "select 1"}}), "EXPLAIN ANALYZE", true},
		{"statement detail select without call", with(&screen{level: levelStatementDetail, stat: stmtState{detail: &sel}}), "", false},
		{"statement detail update", with(&screen{level: levelStatementDetail, stat: stmtState{detail: &upd, sampleCall: "update t set x = 1 where id = 2"}}), "", false},
		{"statement samples single param", with(&screen{level: levelStatementSamples, stat: stmtState{detail: &pg.QueryStat{Query: "select * from t where id = $1"}}}), "EXPLAIN ANALYZE", true},
		{"statement samples multi param no call", with(&screen{level: levelStatementSamples, stat: stmtState{detail: &pg.QueryStat{Query: "select * from t where a = $1 and b = $2"}}}), "", false},
		{"activity", with(&screen{level: levelActivity}), "query detail", true},
		{"overview reset row", overviewActionScreen(3), "reset stats", true},
		{"overview lock-tree recommendation", overviewActionScreen(0), "lock tree", true},
		{"overview diagnostic recommendation", overviewActionScreen(1), "Database stats", true},
		{"overview recommendation without target", overviewActionScreen(2), "", false},
		{"diag result with fix", with(&screen{level: levelDiagnosticResult, diag: &pg.Diagnostic{Fix: func(func(string) (string, bool)) (string, bool) { return "x", true }}}), "fix", true},
		{"diag result no fix", with(&screen{level: levelDiagnosticResult, diag: &pg.Diagnostic{}}), "", false},
		{"logs groups", with(&screen{level: levelLogs}, item{data: &pglog.Group{}}), "entries", true},
		{"logs section", with(&screen{level: levelLogs}, item{data: logSection{}}), "fold", true},
		{"logs section folded", with(&screen{level: levelLogs}, item{data: logSection{collapsed: true}}), "unfold", true},
		{"pgbouncer log row", with(&screen{level: levelPgBouncer}, item{data: pgbLogRow{}}), "log analyzer", true},
		{"columns leaf", with(&screen{level: levelColumns}), "", false},
		{"lock tree leaf", with(&screen{level: levelLockTree}), "", false},
		{"progress leaf", with(&screen{level: levelProgress}), "", false},
		{"pgbouncer show leaf", with(&screen{level: levelPgBouncerShow}), "", false},
	}
	for _, tc := range cases {
		label, ok := enterLabel(tc.s)
		if ok != tc.ok || (ok && label != tc.label) {
			t.Errorf("%s: enterLabel = (%q, %v), want (%q, %v)", tc.name, label, ok, tc.label, tc.ok)
		}
	}
}

// The footer takes its Enter hint from enterLabel and hides the key on leaves.
//
//go:fix inline
func TestApplyContextEnterFooter(t *testing.T) {
	parts := &screen{level: levelParts, tool: toolDisk, loaded: true,
		items: []item{partToItem(pg.Part{Name: "t", Kind: pg.PartHeap})}}
	m := newTestModel(parts)
	m.keys.applyContext(parts)
	if got := stripANSI(m.help.View(m.keys)); !strings.Contains(got, "↵ columns") {
		t.Errorf("parts footer = %q, want a ↵ columns hint", got)
	}
	leaf := &screen{level: levelColumns, tool: toolDisk, loaded: true}
	m.keys.applyContext(leaf)
	if got := stripANSI(m.help.View(m.keys)); strings.Contains(got, "↵") {
		t.Errorf("columns footer must not advertise Enter: %q", got)
	}
	// Keys that open another view read "→ <where>" in the full help; in-place
	// actions keep their verb.
	if d := m.keys.PageInspect.Help().Desc; d != "→ pages" {
		t.Errorf("PageInspect help = %q, want a → jump", d)
	}
	if d := m.keys.ReverseSort.Help().Desc; strings.HasPrefix(d, "→") {
		t.Errorf("ReverseSort is an in-place action, help = %q", d)
	}
}

// overviewActionScreen is a system overview with three recommendations behind
// its four reset rows, the cursor on action row cursor.
func overviewActionScreen(cursor int) *screen {
	s := &screen{level: levelMaintenance, tool: toolMaintenance, db: "postgres"}
	s.maintenance.advice = pg.AdviceSet{
		{Key: "lock_waits", Level: pg.AdviceCrit, Target: pg.AdviceTargetLockTree},
		{Key: "deadlocks", Level: pg.AdviceWarn, Target: pg.AdviceTargetDiagnostic, DiagKey: "database_stats"},
		{Key: "swap", Level: pg.AdviceWarn},
	}
	s.maintenance.cursor = cursor
	return s
}
