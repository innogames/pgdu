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

	off := colIDSet(m.visibleStmtCols(stmtCtx{trackPlanning: false}))
	for _, id := range []stmtColID{colPlanMs, colMeanPlanMs, colPlans} {
		if off[id] {
			t.Errorf("track_planning off: column %q must be unavailable", id)
		}
	}

	on := colIDSet(m.visibleStmtCols(stmtCtx{trackPlanning: true}))
	if !on[colMeanPlanMs] {
		t.Error("track_planning on: default-on mean_plan_ms should appear")
	}
	if on[colPlanMs] || on[colPlans] {
		t.Error("track_planning on: opt-in plan_ms/plans stay hidden by default")
	}
}

func TestVisibleStmtColsUserToggles(t *testing.T) {
	m := &Model{stmtColsVisible: map[stmtColID]bool{
		colHit:     false, // hide a default-on column
		colDirtied: true,  // enable an opt-in column
		colQuery:   false, // mandatory — the toggle must be ignored
	}}
	ids := colIDSet(m.visibleStmtCols(stmtCtx{}))
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
		descs := m.visibleStmtCols(ctx)
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

	descs := m.visibleStmtCols(stmtCtx{})
	cells := cellsFor(descs, q, stmtCtx{})
	qi := indexOfStmtCol(descs, colQuery)
	if qi < 0 || cells[qi].Display != "select 1" {
		t.Errorf("query cell at index %d = %+v, want display %q", qi, cells, "select 1")
	}
}

func TestLabelStmtFooter(t *testing.T) {
	m := &Model{}
	descs := m.visibleStmtCols(stmtCtx{})
	total := make([]pg.DiagCell, len(descs))
	labelStmtFooter(descs, total)

	if qi := indexOfStmtCol(descs, colQuery); total[qi].Display != "← Sum" {
		t.Errorf("footer query cell = %q, want ← Sum", total[qi].Display)
	}
	for _, id := range []stmtColID{colTable, colType} {
		if i := indexOfStmtCol(descs, id); i >= 0 && total[i].Display != "" {
			t.Errorf("footer %q cell = %q, want blank", id, total[i].Display)
		}
	}
}
