package tui

import (
	"testing"

	"pgdu/internal/pg"
)

func TestStmtColumnRegistry(t *testing.T) {
	seen := map[stmtColID]bool{}
	for _, d := range stmtColumnRegistry() {
		if seen[d.id] {
			t.Errorf("duplicate column id %q", d.id)
		}
		seen[d.id] = true
		if d.name == "" || d.desc == "" {
			t.Errorf("column %q: name and desc must be set", d.id)
		}
		if d.cell == nil {
			t.Errorf("column %q: nil cell builder", d.id)
		}
		if d.mandatory && !d.defaultOn {
			t.Errorf("column %q: mandatory columns must also be default-on", d.id)
		}
	}
	if !seen[colQuery] {
		t.Error("registry must contain the mandatory query column")
	}
}

func colIDSet(descs []stmtColDesc) map[stmtColID]bool {
	out := make(map[stmtColID]bool, len(descs))
	for _, d := range descs {
		out[d.id] = true
	}
	return out
}

// TestVisibleStmtColsPlanningGate pins the track_planning availability gate:
// with planning off the plan columns disappear entirely (not just hide), with
// it on only the default-on one appears.
func TestVisibleStmtColsPlanningGate(t *testing.T) {
	m := &Model{}

	off := colIDSet(stmtSpec.visibleCols(&m.stmtTable, stmtCtx{trackPlanning: false}))
	for _, id := range []stmtColID{colPlanMs, colMeanPlanMs, colPlans} {
		if off[id] {
			t.Errorf("track_planning off: column %q must be unavailable", id)
		}
	}

	on := colIDSet(stmtSpec.visibleCols(&m.stmtTable, stmtCtx{trackPlanning: true}))
	if !on[colMeanPlanMs] {
		t.Error("track_planning on: default-on mean_plan_ms should appear")
	}
	if on[colPlanMs] || on[colPlans] {
		t.Error("track_planning on: opt-in plan_ms/plans stay hidden by default")
	}
}

func TestVisibleStmtColsUserToggles(t *testing.T) {
	m := &Model{stmtTable: colTable[stmtColID]{visible: map[stmtColID]bool{
		colHit:     false, // hide a default-on column
		colDirtied: true,  // enable an opt-in column
		colQuery:   false, // mandatory — the toggle must be ignored
	}}}
	ids := colIDSet(stmtSpec.visibleCols(&m.stmtTable, stmtCtx{}))
	if ids[colHit] {
		t.Error("hidden default-on column is still visible")
	}
	if !ids[colDirtied] {
		t.Error("user-enabled opt-in column is missing")
	}
	if !ids[colQuery] {
		t.Error("mandatory query column must survive an off toggle")
	}
	if !ids[colTotalMs] {
		t.Error("untouched default-on column is missing")
	}
}

// TestCellsForStaysParallel pins the invariant the registry design exists for:
// cells, diag columns and descriptors are projected from the same slice, so
// they stay index-parallel for any visibility/availability combination.
func TestCellsForStaysParallel(t *testing.T) {
	m := &Model{}
	q := pg.QueryStat{Query: "select 1", Calls: 3, Rows: 6, TotalExecTime: 12,
		SharedBlksHit: 9, SharedBlksRead: 3}
	for _, tp := range []bool{false, true} {
		ctx := stmtCtx{windowMs: 100, trackPlanning: tp}
		descs := stmtSpec.visibleCols(&m.stmtTable, ctx)
		if len(descs) == 0 {
			t.Fatalf("trackPlanning=%v: no visible columns", tp)
		}
		if cells := cellsFor(descs, q, ctx); len(cells) != len(descs) {
			t.Fatalf("trackPlanning=%v: %d cells for %d columns", tp, len(cells), len(descs))
		}
		if cols := diagColumnsFrom(descs); len(cols) != len(descs) {
			t.Fatalf("trackPlanning=%v: %d diag columns for %d descs", tp, len(cols), len(descs))
		}
	}

	descs := stmtSpec.visibleCols(&m.stmtTable, stmtCtx{})
	cells := cellsFor(descs, q, stmtCtx{})
	qi := indexOfCol(descs, colQuery)
	if qi < 0 || cells[qi].Display != "select 1" {
		t.Errorf("query cell at index %d = %+v, want display %q", qi, cells, "select 1")
	}
}

func TestLabelStmtFooter(t *testing.T) {
	m := &Model{}
	descs := stmtSpec.visibleCols(&m.stmtTable, stmtCtx{})
	total := make([]pg.DiagCell, len(descs))
	labelStmtFooter(descs, total)

	if qi := indexOfCol(descs, colQuery); total[qi].Display != "← Sum" {
		t.Errorf("footer query cell = %q, want ← Sum", total[qi].Display)
	}
	for _, id := range []stmtColID{colMainTable, colType} {
		if i := indexOfCol(descs, id); i >= 0 && total[i].Display != "" {
			t.Errorf("footer %q cell = %q, want blank", id, total[i].Display)
		}
	}
}

// The rate columns divide by the window's wall-clock length, which is not
// known while the window is still opening or on a cumulative anchor whose
// stats_reset the server withheld. They must say so rather than divide by zero
// or quietly report the raw counter.
func TestStmtRateColumnsNeedAWindow(t *testing.T) {
	q := pg.QueryStat{Calls: 120, Rows: 600, WALBytes: 2048, TotalExecTime: 30_000}
	byID := map[stmtColID]stmtColDesc{}
	for _, d := range stmtColumnRegistry() {
		byID[d.id] = d
	}

	for _, id := range []stmtColID{colCallsPerSec, colRowsPerSec, colWALPerSec, colActive} {
		if got := byID[id].cell(q, stmtCtx{}); got.Display != "—" || got.HasNum {
			t.Errorf("%s with no window: got %+v, want an unset — cell", id, got)
		}
	}

	// A 60 s window: 120 calls → 2/s, 600 rows → 10/s, 30 s of exec time over
	// 60 s of wall time → 0.5 backends busy on average.
	ctx := stmtCtx{windowSec: 60}
	for _, c := range []struct {
		id   stmtColID
		want float64
	}{
		{colCallsPerSec, 2},
		{colRowsPerSec, 10},
		{colActive, 0.5},
	} {
		got := byID[c.id].cell(q, ctx)
		if !got.HasNum || got.Num != c.want {
			t.Errorf("%s over a 60s window: got %+v, want Num=%v", c.id, got, c.want)
		}
	}
}

// A counter this server does not collect must read "—" and carry no sort
// value, so it sinks below a server that collects it and reports zero. The two
// are different answers and the table must not conflate them.
func TestStmtOptionalColumnsDistinguishAbsentFromZero(t *testing.T) {
	byID := map[stmtColID]stmtColDesc{}
	for _, d := range stmtColumnRegistry() {
		byID[d.id] = d
	}
	i64 := func(v int64) *int64 { return &v }
	f64 := func(v float64) *float64 { return &v }

	// An older server: none of these counters exist. jit_total_time is a sum
	// of COALESCEd parts so it arrives as a present zero even then — only
	// jit_functions says whether JIT is accounted for at all.
	absent := pg.QueryStat{Calls: 10, TotalExecTime: 100, JITTotalTime: f64(0)}
	for _, id := range []stmtColID{colJITms, colJITPct, colParallelPct, colGenericPct, colWALBufFull} {
		if got := byID[id].cell(absent, stmtCtx{}); got.Display != "—" || got.HasNum {
			t.Errorf("%s on a server without the counter: got %+v, want an unset — cell", id, got)
		}
	}

	// A server that collects them and measured none.
	zero := pg.QueryStat{
		Calls: 10, TotalExecTime: 100,
		JITFunctions: i64(0), JITTotalTime: f64(0), WALBuffersFull: i64(0),
	}
	if got := byID[colWALBufFull].cell(zero, stmtCtx{}); !got.HasNum || got.Num != 0 {
		t.Errorf("wal_buf_full measured-zero: got %+v, want a present 0", got)
	}
	if got := byID[colJITms].cell(zero, stmtCtx{}); !got.HasNum {
		t.Errorf("jit_ms measured-zero: got %+v, want a present value", got)
	}

	// parallel% is a ratio: it needs both ends, and a zero denominator is not
	// 0 % but no answer.
	noDen := pg.QueryStat{ParallelWorkersLaunched: i64(0), ParallelWorkersToLaunch: i64(0)}
	if got := byID[colParallelPct].cell(noDen, stmtCtx{}); got.HasNum {
		t.Errorf("parallel pct with nothing planned: got %+v, want no value", got)
	}
	half := pg.QueryStat{ParallelWorkersLaunched: i64(2), ParallelWorkersToLaunch: i64(4)}
	if got := byID[colParallelPct].cell(half, stmtCtx{}); !got.HasNum || got.Num != 50 {
		t.Errorf("parallel pct, 2 of 4 launched: got %+v, want 50", got)
	}
}
