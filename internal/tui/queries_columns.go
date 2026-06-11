package tui

import (
	"slices"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// stmtColID is the stable identity of a top-queries column. It keys the user's
// visibility set (the C picker) and tracks the active sort column across
// rebuilds, so it must never change even if the header label or position does.
type stmtColID string

const (
	colTotalMs     stmtColID = "total_ms"
	colPctTime     stmtColID = "time%"
	colMeanMs      stmtColID = "mean_ms"
	colPlanMs      stmtColID = "plan_ms"
	colMeanPlanMs  stmtColID = "mean_plan_ms"
	colPlans       stmtColID = "plans"
	colCalls       stmtColID = "calls"
	colRows        stmtColID = "rows"
	colRowsPerCall stmtColID = "rows/call"
	colHit         stmtColID = "hit"
	colMiss        stmtColID = "miss"
	colHitPct      stmtColID = "hit%"
	colBlkPerRow   stmtColID = "blk/row"
	colIOms        stmtColID = "io_ms"
	colWAL         stmtColID = "wal"
	colDirtied     stmtColID = "dirtied"
	colWritten     stmtColID = "written"
	colTempRead    stmtColID = "temp_read"
	colTempWritten stmtColID = "temp_written"
	colWALRecs     stmtColID = "wal_recs"
	colWALFPI      stmtColID = "wal_fpi"
	colTable       stmtColID = "table"
	colType        stmtColID = "T"
	colQuery       stmtColID = "query"
)

// stmtCtx carries the per-build inputs a column's cell builder needs beyond the
// QueryStat row itself: the window's total exec time (the time% denominator) and
// whether planning-time collection is on (gates the plan columns).
type stmtCtx struct {
	windowMs      float64
	trackPlanning bool
}

// stmtColDesc describes one top-queries column: its stable id, header label,
// render kind, whether it's shown by default, whether it can be toggled off, a
// one-line description for the C picker, an optional availability gate, and the
// cell builder. Adding a new metric column is a single entry in
// stmtColumnRegistry — statementColumns/cellsFor/the C picker all derive from it.
type stmtColDesc struct {
	id        stmtColID
	name      string
	kind      pg.DiagColumnKind
	defaultOn bool               // shown unless the user hides it
	mandatory bool               // can't be hidden (the query text — the table's reason to exist)
	desc      string             // one-line explanation shown in the C picker
	available func(stmtCtx) bool // nil = always available; gates plan columns on track_planning
	cell      func(pg.QueryStat, stmtCtx) pg.DiagCell
}

// stmtColumnRegistry is the single source of truth for the top-queries table's
// columns, in display order: numeric columns first, the wide text columns
// (table, T, query) last so the no-bar last-column grow in renderDiagResult
// lands on the query text. The default-on set reproduces the historical fixed
// schema exactly; the rest are opt-in via the C picker.
func stmtColumnRegistry() []stmtColDesc {
	planningOnly := func(ctx stmtCtx) bool { return ctx.trackPlanning }
	return []stmtColDesc{
		{id: colTotalMs, name: "total_ms", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "total execution time in the window",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(fmtMs(q.TotalExecTime), q.TotalExecTime) }},
		{id: colPctTime, name: "time%", kind: pg.DiagPercent, defaultOn: true,
			desc: "share of the window's total execution time",
			cell: func(q pg.QueryStat, ctx stmtCtx) pg.DiagCell {
				pct := 0.0
				if ctx.windowMs > 0 {
					pct = q.TotalExecTime / ctx.windowMs * 100
				}
				return diagNum(fmt1(pct), pct)
			}},
		{id: colMeanMs, name: "mean_ms", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "average execution time per call",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(fmtMs(q.MeanTime()), q.MeanTime()) }},
		{id: colPlanMs, name: "plan_ms", kind: pg.DiagFloat, available: planningOnly,
			desc: "total planning time (needs track_planning)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(fmtMs(q.TotalPlanTime), q.TotalPlanTime) }},
		{id: colMeanPlanMs, name: "mean_plan_ms", kind: pg.DiagCostGraded, defaultOn: true, available: planningOnly,
			desc: "average planning time per plan (needs track_planning)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.Plans <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				v := q.TotalPlanTime / float64(q.Plans)
				return diagNum(fmtMs(v), v)
			}},
		{id: colPlans, name: "plans", kind: pg.DiagInt, available: planningOnly,
			desc: "number of times the statement was planned",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(q.Plans), float64(q.Plans)) }},
		{id: colCalls, name: "calls", kind: pg.DiagInt, defaultOn: true,
			desc: "times the query executed in the window",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(q.Calls), float64(q.Calls)) }},
		{id: colRows, name: "rows", kind: pg.DiagInt, defaultOn: true,
			desc: "rows returned / affected across those calls",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(q.Rows), float64(q.Rows)) }},
		{id: colRowsPerCall, name: "rows/call", kind: pg.DiagFloat,
			desc: "average rows returned / affected per call (rows ÷ calls)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.Calls <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				v := q.RowsPerCall()
				return diagNum(fmtFloat(v), v)
			}},
		{id: colHit, name: "hit", kind: pg.DiagInt, defaultOn: true,
			desc: "shared blocks served from cache",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.SharedBlksHit), float64(q.SharedBlksHit))
			}},
		{id: colMiss, name: "miss", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "shared blocks read from disk/OS",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.SharedBlksRead), float64(q.SharedBlksRead))
			}},
		{id: colHitPct, name: "hit%", kind: pg.DiagPercentGraded, defaultOn: true,
			desc: "cache hit ratio: hit ÷ (hit+miss)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if hr, ok := q.HitRatio(); ok {
					return diagNum(fmt1(hr), hr)
				}
				return pg.DiagCell{Display: "—"}
			}},
		{id: colBlkPerRow, name: "blk/row", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "shared blocks (hit+read) per row — work per result row",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if bpr, ok := q.BlocksPerRow(); ok {
					return diagNum(fmtFloat(bpr), bpr)
				}
				return pg.DiagCell{Display: "—"}
			}},
		{id: colIOms, name: "io_ms", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "block read+write I/O time (needs track_io_timing)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(fmtMs(q.IOTime()), q.IOTime()) }},
		// wal grades on raw bytes (Num) but shows a humanized Display — DiagCostGraded,
		// not DiagBytes, so the renderer leaves Display alone (see diagNum below).
		{id: colWAL, name: "wal", kind: pg.DiagCostGraded, defaultOn: true,
			desc: "WAL bytes generated by the query",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(humanize.Bytes(q.WALBytes), float64(q.WALBytes))
			}},
		{id: colDirtied, name: "dirtied", kind: pg.DiagCostGraded,
			desc: "shared blocks dirtied (modified)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.SharedBlksDirtied), float64(q.SharedBlksDirtied))
			}},
		{id: colWritten, name: "written", kind: pg.DiagCostGraded,
			desc: "shared blocks written back to disk",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.SharedBlksWritten), float64(q.SharedBlksWritten))
			}},
		{id: colTempRead, name: "temp_read", kind: pg.DiagCostGraded,
			desc: "temp blocks read — spills past work_mem",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.TempBlksRead), float64(q.TempBlksRead))
			}},
		{id: colTempWritten, name: "temp_written", kind: pg.DiagCostGraded,
			desc: "temp blocks written — spills past work_mem",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.TempBlksWritten), float64(q.TempBlksWritten))
			}},
		{id: colWALRecs, name: "wal_recs", kind: pg.DiagInt,
			desc: "WAL records generated",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.WALRecords), float64(q.WALRecords))
			}},
		{id: colWALFPI, name: "wal_fpi", kind: pg.DiagCostGraded,
			desc: "WAL full-page images written",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(q.WALFPI), float64(q.WALFPI)) }},
		{id: colTable, name: "table", kind: pg.DiagText, defaultOn: true,
			desc: "main table parsed from the statement (d describes it)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: pg.MainTable(q.Query)} }},
		{id: colType, name: "T", kind: pg.DiagCmdType, defaultOn: true,
			desc: "command type: S/SL/L/I/U/D/M/T",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: pg.QueryKind(q.Query)} }},
		{id: colQuery, name: "query", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "the normalized statement text",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: flattenQuery(q.Query)} }},
	}
}

// indexOfStmtCol returns the position of id within descs, or -1 when absent.
func indexOfStmtCol(descs []stmtColDesc, id stmtColID) int {
	return slices.IndexFunc(descs, func(d stmtColDesc) bool { return d.id == id })
}

// stmtColEnabled reports whether column id should be shown. With no explicit
// entry in the visibility set it falls back to def (the registry default), so a
// fresh Model with a nil set renders exactly the historical default columns.
func (m *Model) stmtColEnabled(id stmtColID, def bool) bool {
	if v, ok := m.stmtColsVisible[id]; ok {
		return v
	}
	return def
}

// ensureStmtColsInit lazily materializes the visibility set from the registry
// defaults, so the C picker shows concrete checkbox state and toggling one
// column doesn't implicitly pin the defaults of every other.
func (m *Model) ensureStmtColsInit() {
	if m.stmtColsVisible != nil {
		return
	}
	m.stmtColsVisible = make(map[stmtColID]bool)
	for _, d := range stmtColumnRegistry() {
		m.stmtColsVisible[d.id] = d.defaultOn || d.mandatory
	}
}

// visibleStmtCols projects the registry to the columns that are both available
// for ctx and enabled by the user, in registry order. Mandatory columns are
// always kept regardless of the visibility set.
func (m *Model) visibleStmtCols(ctx stmtCtx) []stmtColDesc {
	var out []stmtColDesc
	for _, d := range stmtColumnRegistry() {
		if d.available != nil && !d.available(ctx) {
			continue
		}
		if d.mandatory || m.stmtColEnabled(d.id, d.defaultOn) {
			out = append(out, d)
		}
	}
	return out
}

// diagColumnsFrom maps projected descriptors to the renderer's column schema.
func diagColumnsFrom(descs []stmtColDesc) []pg.DiagColumn {
	cols := make([]pg.DiagColumn, len(descs))
	for i, d := range descs {
		cols[i] = pg.DiagColumn{Name: d.name, Kind: d.kind}
	}
	return cols
}

// cellsFor builds one row's cells over the already-projected descriptors, so the
// cells stay parallel to diagColumnsFrom(descs) by construction — there is no
// index arithmetic to keep in sync.
func cellsFor(descs []stmtColDesc, q pg.QueryStat, ctx stmtCtx) []pg.DiagCell {
	cells := make([]pg.DiagCell, len(descs))
	for i, d := range descs {
		cells[i] = d.cell(q, ctx)
	}
	return cells
}

// labelStmtFooter turns a summed row into the pinned "← Sum" footer: the label
// in the query column and blanks in the table/T text columns (the empty
// aggregate query would otherwise make MainTable/QueryKind emit junk). Located
// by column id so it holds whichever columns are currently visible.
func labelStmtFooter(descs []stmtColDesc, total []pg.DiagCell) {
	for i, d := range descs {
		switch d.id {
		case colQuery:
			total[i].Display = "← Sum"
		case colTable, colType:
			total[i].Display = ""
		}
	}
}
