package tui

import (
	"strings"
	"time"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// Column ids for the size-history table. One registry serves every scope: a
// roll-up row is a pg.GrowthRow too, so unlike the top-queries roll-ups there
// is no second registry, no second colTable and no second prefs key.
type growthColID string

const (
	growthColName     growthColID = "name"
	growthColSize     growthColID = "size"
	growthColBase     growthColID = "base"
	growthColDelta    growthColID = "delta"
	growthColDeltaPct growthColID = "delta_pct"
	growthColPerDay   growthColID = "per_day"
	growthColTag      growthColID = "tag"
	growthColRels     growthColID = "relations"

	growthColDeltaHeap  growthColID = "delta_heap"
	growthColDeltaIdx   growthColID = "delta_idx"
	growthColDeltaToast growthColID = "delta_toast"
	growthColRows       growthColID = "rows"
	growthColRowsDay    growthColID = "rows_day"
	growthColKind       growthColID = "relkind"
	growthColBound      growthColID = "bound"
)

// growthCtx is what a cell needs beyond its own row: the scope being rendered
// (which gates the scope-specific columns) and the window length (which turns
// a delta into a rate). A zero window means no window is applied, and the rate
// columns say "—" rather than claiming no growth.
type (
	growthCtx struct {
		view   growthView
		window time.Duration
	}
	growthColDesc = colDesc[growthColID, pg.GrowthRow, growthCtx]
)

var growthSpec = colSpec[growthColID, pg.GrowthRow, growthCtx]{
	registry:    growthColumnRegistry,
	prefsKey:    colPrefsGrowth,
	defaultSort: growthColPerDay,
	title:       "choose which columns the growth table shows",
}

// growthBytes renders an absolute byte figure (a size, not a change).
func growthBytes(b int64) pg.DiagCell { return diagNum(humanize.Bytes(b), float64(b)) }

// growthDelta renders a byte change. The text comes from the same
// diagCellText the renderer uses, so the cell, the width pass and the headline
// bar all spell the sign identically however the sort moves.
func growthDelta(b int64) pg.DiagCell { return diagNum(signedBytes(b), float64(b)) }

// growthRate renders a per-day figure, or "—" with no numeric when there is no
// window to divide by.
func growthRate(v float64, window time.Duration, bytes bool) pg.DiagCell {
	if window <= 0 {
		return pg.DiagCell{Display: "—"}
	}
	if bytes {
		return diagNum(signedBytes(int64(v)), v)
	}
	return diagNum(signedCount(int64(v)), v)
}

// growthPct renders the size change as a percentage. An undefined ratio — a
// zero baseline, i.e. a relation that did not exist — reads "new" and carries
// no numeric, so it sinks under the generic table's unknown-sorts-last rule
// instead of a +Inf that would sort first and mean nothing.
func growthPct(r pg.GrowthRow) pg.DiagCell {
	pct, ok := r.DeltaPct()
	if !ok {
		return pg.DiagCell{Display: "new"}
	}
	return diagNum(signedPct(pct), pct)
}

// growthRows renders a live-row count, or "—" when the window straddles a
// statistics reset and the counters cannot be differenced. Sizes are measured
// and stay trustworthy; rows are counted and do not.
func growthRows(n int64, known bool) pg.DiagCell {
	if !known {
		return pg.DiagCell{Display: "—"}
	}
	return diagNum(formatRows(n), float64(n))
}

// growthColumnRegistry is the single source of truth for the growth table's
// columns in display order: identity, then the window's size figures, then the
// scope-specific annotations, then the opt-in breakdowns.
func growthColumnRegistry() []growthColDesc {
	return []growthColDesc{
		{id: growthColName, name: "name", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "relation, schema, database or partition-tree root, depending on the scope (always shown)",
			cell: func(r pg.GrowthRow, c growthCtx) pg.DiagCell {
				return pg.DiagCell{Display: growthName(r, c.view)}
			}},

		// The window's headline figures.
		{id: growthColSize, name: "size", kind: pg.DiagBytes, defaultOn: true,
			desc: "total size at the window's end",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthBytes(r.EndBytes) }},
		{id: growthColBase, name: "base", kind: pg.DiagBytes, defaultOn: true,
			desc: "total size at the window's start",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthBytes(r.BaseBytes) }},
		{id: growthColDelta, name: "Δ", kind: pg.DiagDeltaBytes, defaultOn: true,
			desc: "size change across the window; negative for a relation that shrank",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthDelta(r.DeltaBytes) }},
		{id: growthColDeltaPct, name: "Δ%", kind: pg.DiagDeltaPct, defaultOn: true,
			desc: "size change as a share of the baseline; undefined (\"new\") against a zero baseline",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthPct(r) }},
		{id: growthColPerDay, name: "per_day", kind: pg.DiagDeltaBytes, defaultOn: true,
			desc: "bytes per day: Δ normalised by the window's length, so windows of different lengths compare directly",
			cell: func(r pg.GrowthRow, c growthCtx) pg.DiagCell {
				return growthRate(r.BytesPerDay, c.window, true)
			}},

		// Scope-specific annotations.
		{id: growthColTag, name: "tag", kind: pg.DiagGrowthTag, defaultOn: true,
			desc:      "new (absent from the baseline) · gone (dropped) · rewritten (relfilenode changed: VACUUM FULL / CLUSTER / REINDEX)",
			available: func(c growthCtx) bool { return c.view == growthByTable },
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell {
				d := r.Tag.String()
				if r.FloorCapped && d != "" {
					// The absent side was captured with a size floor, so the
					// relation may simply have been under it.
					d += "?"
				}
				return pg.DiagCell{Display: d}
			}},
		{id: growthColRels, name: "relations", kind: pg.DiagInt, defaultOn: true,
			desc:      "how many relations this row folds in",
			available: func(c growthCtx) bool { return c.view.grouped() },
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell {
				return diagNum(formatRows(int64(r.Parts)), float64(r.Parts))
			}},

		// Opt-in breakdowns.
		{id: growthColDeltaHeap, name: "Δheap", kind: pg.DiagDeltaBytes, defaultOn: true,
			desc: "heap (main fork) share of the size change",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthDelta(r.DeltaHeap) }},
		{id: growthColDeltaIdx, name: "Δidx", kind: pg.DiagDeltaBytes, defaultOn: true,
			desc: "index share of the size change — index growth outpacing heap growth is the usual bloat tell",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthDelta(r.DeltaIdx) }},
		{id: growthColDeltaToast, name: "Δtoast", kind: pg.DiagDeltaBytes, defaultOn: true,
			desc: "TOAST share of the size change",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthDelta(r.DeltaToast) }},
		{id: growthColRows, name: "rows", kind: pg.DiagCount, defaultOn: true,
			desc: "live rows at the window's end (n_live_tup); \"—\" when the window straddles a statistics reset",
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell { return growthRows(r.EndRows, r.RowsKnown) }},
		{id: growthColRowsDay, name: "rows_day", kind: pg.DiagDeltaCount, defaultOn: true,
			desc: "live rows added per day; bytes-per-row against per_day tells row growth from bloat",
			cell: func(r pg.GrowthRow, c growthCtx) pg.DiagCell {
				if !r.RowsKnown {
					return pg.DiagCell{Display: "—"}
				}
				return growthRate(r.RowsPerDay, c.window, false)
			}},
		{id: growthColKind, name: "k", kind: pg.DiagText,
			desc:      "relkind: r table · p partitioned parent · m materialized view",
			available: func(c growthCtx) bool { return c.view == growthByTable },
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell {
				return pg.DiagCell{Display: r.RelKind}
			}},
		{id: growthColBound, name: "bound", kind: pg.DiagText,
			desc: "partition bound — for a RANGE-partitioned table this is a real time axis, so bytes per partition is a growth curve from one sample",
			available: func(c growthCtx) bool {
				return c.view == growthByTable || c.view == growthByPartTree
			},
			cell: func(r pg.GrowthRow, _ growthCtx) pg.DiagCell {
				if r.PartBound == "" {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: strings.Join(strings.Fields(r.PartBound), " ")}
			}},
	}
}

// growthName is the key column's text. At leaf scope a relation is
// schema-qualified unless it is in public, matching how the disk browser names
// relations; the folded scopes already are their own name.
func growthName(r pg.GrowthRow, v growthView) string {
	switch v {
	case growthBySchema:
		if r.Schema == "" {
			return "(no schema)"
		}
		return r.Schema
	case growthByDatabase:
		return r.Database
	}
	return r.Qualified()
}

// buildGrowthItems projects rows through the visible columns. item.name is the
// space-joined cell display so the / filter matches any column value, and
// growthKey carries the row's aggregation key for narrowing and the parts jump.
func (m *Model) buildGrowthItems(rows []pg.GrowthRow, ctx growthCtx) ([]item, []growthColDesc) {
	descs := growthSpec.visibleCols(&m.growthTable, ctx)
	items := make([]item, len(rows))
	for i, r := range rows {
		cells := cellsFor(descs, r, ctx)
		parts := make([]string, len(cells))
		for j, c := range cells {
			parts[j] = c.Display
		}
		items[i] = item{
			name:      strings.Join(parts, " "),
			data:      cells,
			growthKey: pg.ScopeKey(r, ctx.view.scope()),
			// Exactly what drillIn will do on this row: a grouped row narrows,
			// a live relation opens its disk parts, a dropped one has nothing
			// left to open.
			hasChildren: ctx.view.grouped() || r.Tag != pg.GrowthGone,
		}
	}
	return items, descs
}

// sumGrowthRows aggregates rows for the pinned Σ footer. The additive figures
// are summed and the derived ones re-derived from those totals by
// pg.FinishRollUp, so the footer's rate and ratio pool correctly instead of
// averaging the rows' own.
func sumGrowthRows(rows []pg.GrowthRow, window time.Duration) pg.GrowthRow {
	if len(rows) == 0 {
		return pg.GrowthRow{}
	}
	folded := pg.RollUpGrowth(rows, pg.GrowthByDatabase)
	// Rows may span databases only in the database scope; fold whatever came
	// back into one.
	total := pg.GrowthRow{RowsKnown: true}
	for _, r := range folded {
		total.BaseBytes += r.BaseBytes
		total.EndBytes += r.EndBytes
		total.BaseHeap += r.BaseHeap
		total.EndHeap += r.EndHeap
		total.BaseIdx += r.BaseIdx
		total.EndIdx += r.EndIdx
		total.BaseToast += r.BaseToast
		total.EndToast += r.EndToast
		total.BaseRows += r.BaseRows
		total.EndRows += r.EndRows
		total.Parts += r.Parts
		if !r.RowsKnown {
			total.RowsKnown = false
		}
	}
	out := []pg.GrowthRow{total}
	pg.FinishRollUp(out, window)
	return out[0]
}

// growthFooterCells projects the aggregate through the same cell builders as
// the data rows, so the footer stays parallel to descs by construction. The
// name column carries the count; tag, relkind and bound are blanked because
// their per-row value would read as a real aggregate.
func growthFooterCells(descs []growthColDesc, rows []pg.GrowthRow, ctx growthCtx) []pg.DiagCell {
	if len(rows) == 0 {
		return nil
	}
	total := cellsFor(descs, sumGrowthRows(rows, ctx.window), ctx)
	for i, d := range descs {
		switch d.id {
		case growthColName:
			total[i] = pg.DiagCell{Display: "Σ " + formatRows(int64(len(rows))) + " " + ctx.view.plural()}
		case growthColTag, growthColKind, growthColBound:
			total[i] = pg.DiagCell{Display: ""}
		case growthColRels:
			total[i] = diagNum(formatRows(int64(len(rows))), float64(len(rows)))
		}
	}
	return total
}

// rebuildGrowthItems rebuilds the generic-table items from the cached diff,
// applying the current scope, narrowing and column selection. Every load,
// scope switch, narrowing and column toggle funnels through here so projection
// and sort stay in step.
func (m *Model) rebuildGrowthItems(s *screen) {
	ctx := growthCtx{view: s.growth.view, window: s.growth.duration()}
	// Filter first, then fold, so the Σ footer reconciles with what is on
	// screen at every scope.
	rows := s.growth.visibleRows()
	if ctx.view.grouped() {
		rows = pg.FinishRollUp(pg.RollUpGrowth(rows, ctx.view.scope()), ctx.window)
	}

	items, descs := m.buildGrowthItems(rows, ctx)
	s.growth.cols = descs
	s.diagCols = diagColumnsFrom(descs)
	s.diagTotalRow = growthFooterCells(descs, rows, ctx)
	growthSpec.syncSort(&m.growthTable, s, descs)
	s.items = items
	s.diagMetricsDirty = true
	m.syncGrowthBar(s)
	m.applySort(s)
}

// syncGrowthBar points the headline bar at the sort column when that column is
// numeric, and turns it off otherwise so the width goes back to the data
// columns. The table's question changes with the sort — "what grew most" vs
// "what is biggest" — so the bar follows it rather than being pinned.
//
// Moving the bar changes every column's width, so the memo must be dirtied.
func (m *Model) syncGrowthBar(s *screen) {
	want := -1
	if s.diagSortCol >= 0 && s.diagSortCol < len(s.diagCols) &&
		numericDiagKind(s.diagCols[s.diagSortCol].Kind) {
		want = s.diagSortCol
	}
	if s.diagBarCol != want {
		s.diagBarCol = want
		s.diagMetricsDirty = true
	}
}
