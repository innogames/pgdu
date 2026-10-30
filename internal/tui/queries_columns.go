package tui

import (
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
	colMinMs       stmtColID = "min_ms"
	colMaxMs       stmtColID = "max_ms"
	colStddevMs    stmtColID = "stddev_ms"
	colCV          stmtColID = "cv"
	colPlanMs      stmtColID = "plan_ms"
	colMeanPlanMs  stmtColID = "mean_plan_ms"
	colPlans       stmtColID = "plans"
	colCalls       stmtColID = "calls"
	colRows        stmtColID = "rows"
	colRowsPerCall stmtColID = "rows/call"
	colCallsPerSec stmtColID = "calls/s"
	colRowsPerSec  stmtColID = "rows/s"
	colWALPerSec   stmtColID = "wal/s"
	colActive      stmtColID = "active"
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
	colLocalHit    stmtColID = "local_hit"
	colLocalRead   stmtColID = "local_read"
	colLocalDirty  stmtColID = "local_dirtied"
	colLocalWrit   stmtColID = "local_written"
	colWALRecs     stmtColID = "wal_recs"
	colWALFPI      stmtColID = "wal_fpi"
	colMainTable   stmtColID = "table"
	colType        stmtColID = "T"
	colQuery       stmtColID = "query"
)

// stmtCtx carries the per-build inputs a column's cell builder needs beyond the
// QueryStat row itself: the window's total exec time (the time% denominator),
// the window's wall-clock length in seconds (the rate columns' denominator) and
// whether planning-time collection is on (gates the plan columns).
//
// windowSec is zero while the window is still opening and on a cumulative
// anchor whose stats_reset the server would not tell us — the rate columns
// render "—" rather than dividing by a span they don't have.
type stmtCtx struct {
	windowMs      float64
	windowSec     float64
	trackPlanning bool
}

// perSec is the shared body of the rate columns: a counter over the window's
// wall-clock length, or an unset cell when there is no window to divide by.
func perSec(n float64, ctx stmtCtx, format func(float64) string) pg.DiagCell {
	if ctx.windowSec <= 0 {
		return pg.DiagCell{Display: "—"}
	}
	v := n / ctx.windowSec
	return diagNum(format(v), v)
}

// stmtColDesc is a top-queries column; stmtSpec binds the registry to its picker
// state (Model.stmtTable), prefs key and sort fallback.
type stmtColDesc = colDesc[stmtColID, pg.QueryStat, stmtCtx]

var stmtSpec = colSpec[stmtColID, pg.QueryStat, stmtCtx]{
	registry:    stmtColumnRegistry,
	prefsKey:    colPrefsQueries,
	defaultSort: colTotalMs,
	title:       "choose which columns the top-queries table shows — opt-in metrics are off by default",
	unavailNote: "track_planning off",
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
		// The extrema/stddev are cumulative since the last stats reset — they can't
		// be windowed (you can't subtract two extrema), so the diff zeroes them and
		// these cells show "—" on windowed rows. They carry real values on the
		// cumulative "since last reset" anchor and on queries new since the baseline.
		{id: colMinMs, name: "min_ms", kind: pg.DiagDuration,
			desc: "fastest single call (cumulative since stats reset — on windowed rows)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.MinExecTime <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return diagNum(fmtMs(q.MinExecTime), q.MinExecTime)
			}},
		{id: colMaxMs, name: "max_ms", kind: pg.DiagDuration,
			desc: "slowest single call (cumulative since stats reset) — spots outlier spikes a mean hides",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.MaxExecTime <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return diagNum(fmtMs(q.MaxExecTime), q.MaxExecTime)
			}},
		{id: colStddevMs, name: "stddev_ms", kind: pg.DiagDuration,
			desc: "standard deviation of execution time (cumulative since stats reset)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.StddevExecTime <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				return diagNum(fmtMs(q.StddevExecTime), q.StddevExecTime)
			}},
		{id: colCV, name: "cv", kind: pg.DiagCostGraded,
			desc: "coefficient of variation (stddev ÷ mean, cumulative) — high = erratic runtime, plan flips or lock waits",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				if q.StddevExecTime <= 0 || q.MeanExecTime <= 0 {
					return pg.DiagCell{Display: "—"}
				}
				v := q.StddevExecTime / q.MeanExecTime
				return diagNum(fmtFloat(v), v)
			}},
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
		// The window's wall-clock length is known exactly, so the rate columns
		// are pure arithmetic over counters already in hand. "active" is the
		// useful one: exec time per wall-second is the average number of
		// backends this one statement keeps busy, which is what decides whether
		// a query is worth tuning at all.
		{id: colCallsPerSec, name: "calls/s", kind: pg.DiagFloat,
			desc: "calls per second over the window",
			cell: func(q pg.QueryStat, ctx stmtCtx) pg.DiagCell {
				return perSec(float64(q.Calls), ctx, fmtFloat)
			}},
		{id: colRowsPerSec, name: "rows/s", kind: pg.DiagFloat,
			desc: "rows returned / affected per second over the window",
			cell: func(q pg.QueryStat, ctx stmtCtx) pg.DiagCell {
				return perSec(float64(q.Rows), ctx, fmtFloat)
			}},
		{id: colWALPerSec, name: "wal/s", kind: pg.DiagCostGraded,
			desc: "WAL bytes generated per second over the window",
			cell: func(q pg.QueryStat, ctx stmtCtx) pg.DiagCell {
				return perSec(float64(q.WALBytes), ctx, func(v float64) string {
					return humanize.Bytes(int64(v)) + "/s"
				})
			}},
		{id: colActive, name: "active", kind: pg.DiagCostGraded,
			desc: "average backends busy with this statement (exec time ÷ wall time)",
			cell: func(q pg.QueryStat, ctx stmtCtx) pg.DiagCell {
				return perSec(q.TotalExecTime/1000, ctx, fmtFloat)
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
					return diagNum(fmt1(bpr), bpr)
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
		// Local blocks are temp-table traffic. They already feed io_ms and the
		// detail view; without columns here a temp-table-heavy statement looks
		// like it touches nothing.
		{id: colLocalHit, name: "local_hit", kind: pg.DiagInt,
			desc: "local (temp-table) blocks served from the backend's own buffers",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.LocalBlksHit), float64(q.LocalBlksHit))
			}},
		{id: colLocalRead, name: "local_read", kind: pg.DiagCostGraded,
			desc: "local (temp-table) blocks read from disk/OS",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.LocalBlksRead), float64(q.LocalBlksRead))
			}},
		{id: colLocalDirty, name: "local_dirtied", kind: pg.DiagCostGraded,
			desc: "local (temp-table) blocks dirtied",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.LocalBlksDirtied), float64(q.LocalBlksDirtied))
			}},
		{id: colLocalWrit, name: "local_written", kind: pg.DiagCostGraded,
			desc: "local (temp-table) blocks written back to disk",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.LocalBlksWritten), float64(q.LocalBlksWritten))
			}},
		{id: colWALRecs, name: "wal_recs", kind: pg.DiagInt,
			desc: "WAL records generated",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell {
				return diagNum(formatRows(q.WALRecords), float64(q.WALRecords))
			}},
		{id: colWALFPI, name: "wal_fpi", kind: pg.DiagCostGraded,
			desc: "WAL full-page images written",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(q.WALFPI), float64(q.WALFPI)) }},
		{id: colMainTable, name: "table", kind: pg.DiagText, defaultOn: true,
			desc: "main table parsed from the statement (d describes it)",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: mainTableDisplay(q.Query)} }},
		{id: colType, name: "T", kind: pg.DiagCmdType, defaultOn: true,
			desc: "command type: S/SL/L/I/U/D/M/T",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: pg.QueryKind(q.Query)} }},
		{id: colQuery, name: "query", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "the normalized statement text",
			cell: func(q pg.QueryStat, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: flattenQuery(q.Query)} }},
	}
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
		case colMainTable, colType:
			total[i].Display = ""
		}
	}
}
