package tui

import (
	"strconv"
	"strings"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// tblColID is the stable identity of a Table overview column. It keys the user's
// visibility set (the C picker) and tracks the active sort column across rebuilds.
type tblColID string

const (
	tblColTable     tblColID = "table"
	tblColSize      tblColID = "size"
	tblColHeap      tblColID = "heap"
	tblColIndexes   tblColID = "indexes"
	tblColToast     tblColID = "toast"
	tblColIdxRatio  tblColID = "idx_ratio"
	tblColRows      tblColID = "rows"
	tblColDead      tblColID = "dead"
	tblColDeadPct   tblColID = "dead_pct"
	tblColIns       tblColID = "ins"
	tblColUpd       tblColID = "upd"
	tblColDel       tblColID = "del"
	tblColHotUpd    tblColID = "hot_upd"
	tblColNonHotUpd tblColID = "non_hot_upd"
	tblColHotPct    tblColID = "hot_pct"
	tblColWrites    tblColID = "writes"
	tblColSeqScan   tblColID = "seq_scan"
	tblColIdxScan   tblColID = "idx_scan"
	tblColSeqPct    tblColID = "seq_pct"
	tblColSeqRead   tblColID = "seq_read"
	tblColIdxFetch  tblColID = "idx_fetch"
	tblColCache     tblColID = "cache"
	tblColIdxCache  tblColID = "idx_cache"
	tblColBuffered  tblColID = "buffered"
	tblColDirty     tblColID = "dirty"
	tblColHeapRead  tblColID = "heap_read"
	tblColHeapHit   tblColID = "heap_hit"
	tblColModSince  tblColID = "mod_since_analyze"
	tblColInsSince  tblColID = "ins_since_vacuum"
	tblColVacAge    tblColID = "vac_age"
	tblColAnaAge    tblColID = "ana_age"
	tblColVacuumN   tblColID = "vacuum_count"
	tblColAutovacN  tblColID = "autovacuum_count"
	tblColXidAge    tblColID = "xid_age"
	tblColFill      tblColID = "fillfactor"
	tblColAutovac   tblColID = "autovac"
)

// tblColDesc is a Table overview column. Its cells need no per-build context,
// so tblCtx is empty; tblSpec binds the registry to its picker state
// (Model.tblTable) with size as the sort fallback.
type (
	tblCtx     struct{}
	tblColDesc = colDesc[tblColID, pg.TableStat, tblCtx]
)

var tblSpec = colSpec[tblColID, pg.TableStat, tblCtx]{
	registry:    tableColumnRegistry,
	prefsKey:    colPrefsTableStats,
	defaultSort: tblColSize,
	title:       "choose which columns the table overview shows",
}

// Cell helpers shared by the registry entries.
func tblBytes(b int64) pg.DiagCell { return diagNum(humanize.Bytes(b), float64(b)) }
func tblCount(n int64) pg.DiagCell { return diagNum(formatRows(n), float64(n)) }

// tblPct renders a 0–100 percentage, or "—" when ok is false (ratio undefined,
// e.g. no scans / no updates yet) so an undefined ratio never reads as 0%.
func tblPct(pct float64, ok bool) pg.DiagCell {
	if !ok {
		return pg.DiagCell{Display: "—"}
	}
	return diagNum(fmt1(pct)+"%", pct)
}

// tblAge renders milliseconds-since as a human age, or "never" (no numeric, so
// it sorts last) when the action has never run.
func tblAge(ms *float64) pg.DiagCell {
	if ms == nil {
		return pg.DiagCell{Display: "never"}
	}
	return diagNum(fmtAge(*ms), *ms)
}

// tableColumnRegistry is the single source of truth for the Table overview
// table's columns in display order. The table name leads (left-aligned); the
// total-size column follows as the headline bar (rebuildTableStatItems points
// diagBarCol at it when visible); then activity / cache / maintenance / options.
func tableColumnRegistry() []tblColDesc {
	return []tblColDesc{
		{id: tblColTable, name: "table", kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "schema-local table name (always shown)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return pg.DiagCell{Display: r.Name} }},

		// Size — total relation size is the headline bar.
		{id: tblColSize, name: "size", kind: pg.DiagBytes, defaultOn: true,
			desc: "total relation size on disk (heap + indexes + TOAST); the headline bar",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblBytes(r.TotalBytes) }},
		{id: tblColHeap, name: "heap", kind: pg.DiagBytes,
			desc: "heap (main fork) size — pg_relation_size",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblBytes(r.HeapBytes) }},
		{id: tblColIndexes, name: "idx_size", kind: pg.DiagBytes,
			desc: "combined size of all indexes — pg_indexes_size",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblBytes(r.IndexesBytes) }},
		{id: tblColToast, name: "toast", kind: pg.DiagBytes,
			desc: "TOAST size (out-of-line large values, incl. its index + FSM/VM)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblBytes(r.ToastBytes) }},
		{id: tblColIdxRatio, name: "idx/heap", kind: pg.DiagFloat,
			desc: "index bytes / heap bytes — high values flag over-indexed tables",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell {
				v, ok := r.IdxHeapRatio()
				if !ok {
					return pg.DiagCell{Display: "—"}
				}
				return diagNum(fmt1(v)+"x", v)
			}},

		// Rows & churn — the "active table" signals.
		{id: tblColRows, name: "rows", kind: pg.DiagInt, defaultOn: true,
			desc: "estimated live rows (n_live_tup)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NLive) }},
		{id: tblColDead, name: "dead", kind: pg.DiagInt,
			desc: "dead (yet-to-be-vacuumed) rows (n_dead_tup)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NDead) }},
		{id: tblColDeadPct, name: "dead%", kind: pg.DiagPercentBad, defaultOn: true,
			desc: "dead rows as a share of live+dead — high = bloat / vacuum lag",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblPct(r.DeadPct(), r.NLive+r.NDead > 0) }},
		{id: tblColIns, name: "ins", kind: pg.DiagInt, defaultOn: true,
			desc: "rows inserted since the last stats reset (n_tup_ins)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NInsert) }},
		{id: tblColUpd, name: "upd", kind: pg.DiagInt, defaultOn: true,
			desc: "rows updated since the last stats reset (n_tup_upd)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NUpdate) }},
		{id: tblColDel, name: "del", kind: pg.DiagInt, defaultOn: true,
			desc: "rows deleted since the last stats reset (n_tup_del)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NDelete) }},
		{id: tblColHotUpd, name: "hot_upd", kind: pg.DiagInt,
			desc: "HOT (heap-only-tuple) updates — index-free, cheap updates (n_tup_hot_upd)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NHotUpdate) }},
		{id: tblColNonHotUpd, name: "non_hot_upd", kind: pg.DiagInt,
			desc: "non-HOT updates (upd − hot_upd) — touched every index; high = index write amplification",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NonHotUpdate()) }},
		{id: tblColHotPct, name: "hot%", kind: pg.DiagPercentGraded, defaultOn: true,
			desc: "HOT share of updates — low on a hot table = FILLFACTOR / over-indexing candidate",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { v, ok := r.HotPct(); return tblPct(v, ok) }},
		{id: tblColWrites, name: "writes", kind: pg.DiagInt,
			desc: "total row churn (ins + upd + del) — a quick activity sort",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.Writes()) }},

		// Scans — access pattern / missing-index signals.
		{id: tblColSeqScan, name: "seq", kind: pg.DiagInt, defaultOn: true,
			desc: "sequential scans started on the table (seq_scan)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.SeqScan) }},
		{id: tblColIdxScan, name: "idx_scan", kind: pg.DiagInt, defaultOn: true,
			desc: "index scans started on the table (idx_scan)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.IdxScan) }},
		{id: tblColSeqPct, name: "seq%", kind: pg.DiagPercentBad, defaultOn: true,
			desc: "sequential share of all scans — high on a large table = missing-index candidate",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { v, ok := r.SeqPct(); return tblPct(v, ok) }},
		{id: tblColSeqRead, name: "seq_read", kind: pg.DiagInt,
			desc: "live rows fetched by sequential scans (seq_tup_read)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.SeqTupRead) }},
		{id: tblColIdxFetch, name: "idx_fetch", kind: pg.DiagInt,
			desc: "live rows fetched by index scans (idx_tup_fetch)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.IdxTupFetch) }},

		// Cache — shared-buffer hit ratios (higher is better).
		{id: tblColCache, name: "cache", kind: pg.DiagPercentGraded, defaultOn: true,
			desc: "heap shared-buffer hit ratio (heap_blks_hit / (hit+read)) — low = cache-cold table",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { v, ok := r.HeapHitPct(); return tblPct(v, ok) }},
		{id: tblColIdxCache, name: "idx_cache", kind: pg.DiagPercentGraded,
			desc: "index shared-buffer hit ratio (idx_blks_hit / (hit+read))",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { v, ok := r.IdxHitPct(); return tblPct(v, ok) }},
		{id: tblColBuffered, name: "buffered", kind: pg.DiagBytes,
			desc: "bytes of this table (heap + TOAST + indexes) in shared buffers right now — point-in-time (pg_buffercache)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell {
				if !r.BufsKnown {
					return pg.DiagCell{Display: "—"}
				}
				return tblBytes(r.BufferedBytes)
			}},
		{id: tblColDirty, name: "dirty", kind: pg.DiagBytes, defaultOn: true,
			desc: "bytes currently dirty (modified, not yet flushed) in shared buffers — live buffer-pool write pressure (pg_buffercache)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell {
				if !r.BufsKnown {
					return pg.DiagCell{Display: "—"}
				}
				return tblBytes(r.DirtyBytes)
			}},
		{id: tblColHeapRead, name: "heap_read", kind: pg.DiagInt,
			desc: "heap blocks read from disk (heap_blks_read)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.HeapBlksRead) }},
		{id: tblColHeapHit, name: "heap_hit", kind: pg.DiagInt,
			desc: "heap blocks served from shared buffers (heap_blks_hit)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.HeapBlksHit) }},

		// Maintenance.
		{id: tblColModSince, name: "mod", kind: pg.DiagInt,
			desc: "rows changed since the last ANALYZE (n_mod_since_analyze) — planner-stat staleness",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NModSinceAnalyze) }},
		{id: tblColInsSince, name: "ins_vac", kind: pg.DiagInt,
			desc: "rows inserted since the last VACUUM (n_ins_since_vacuum)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.NInsSinceVacuum) }},
		{id: tblColVacAge, name: "vac_age", kind: pg.DiagDuration,
			desc: "time since the most recent (auto)vacuum — stale = vacuum lag",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblAge(r.VacAgeMs) }},
		{id: tblColAnaAge, name: "ana_age", kind: pg.DiagDuration,
			desc: "time since the most recent (auto)analyze",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblAge(r.AnaAgeMs) }},
		{id: tblColVacuumN, name: "vac#", kind: pg.DiagInt,
			desc: "manual + auto vacuum runs (vacuum_count + autovacuum_count)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.VacuumCount + r.AutovacuumCount) }},
		{id: tblColAutovacN, name: "autovac#", kind: pg.DiagInt,
			desc: "autovacuum runs on this table (autovacuum_count)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.AutovacuumCount) }},
		{id: tblColXidAge, name: "xid_age", kind: pg.DiagCostGraded,
			desc: "age(relfrozenxid) — transaction-ID wraparound risk (graded against the worst table in view)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell { return tblCount(r.FrozenXIDAge) }},

		// Storage options.
		{id: tblColFill, name: "fill", kind: pg.DiagInt,
			desc: "FILLFACTOR storage parameter (100 = packed; lower leaves room for HOT updates)",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell {
				ff := r.FillFactor()
				return diagNum(strconv.Itoa(ff), float64(ff))
			}},
		{id: tblColAutovac, name: "autovac", kind: pg.DiagText,
			desc: "per-table autovacuum_enabled override (on/off), or — when it inherits the cluster default",
			cell: func(r pg.TableStat, _ tblCtx) pg.DiagCell {
				v, ok := r.AutovacuumReloption()
				if !ok {
					return pg.DiagCell{Display: "—"}
				}
				return pg.DiagCell{Display: v}
			}},
	}
}

// buildTableStatItems converts TableStats into generic-table rows. Returns items
// and the projected column descriptors (parallel to each item's cells). Each
// item carries the relation OID in statQueryID so drill-in / describe can find
// the originating TableStat in s.tblRows regardless of the current sort order.
func (m *Model) buildTableStatItems(rows []pg.TableStat) ([]item, []tblColDesc) {
	descs := tblSpec.visibleCols(&m.tblTable, tblCtx{})
	items := make([]item, len(rows))
	for i, r := range rows {
		cells := cellsFor(descs, r, tblCtx{})
		// item.name is the space-joined cell display so the fuzzy filter can
		// match any column value.
		parts := make([]string, len(cells))
		for j, c := range cells {
			parts[j] = c.Display
		}
		items[i] = item{
			name:        strings.Join(parts, " "),
			data:        cells,
			hasChildren: true, // Enter → the table's disk parts
			statQueryID: int64(r.OID),
		}
	}
	return items, descs
}

// sumTableStats aggregates rows into a single TableStat for the pinned footer.
// Every additive counter (sizes, row counts, scan/block counters, vacuum runs)
// is summed, so running the column cell builders over the result yields true
// totals for the additive columns and correctly pooled ratios for the derived
// ones for free: dead% = Σdead ÷ Σ(live+dead), cache = Σhit ÷ Σ(hit+read),
// idx/heap = Σidx ÷ Σheap, and so on. The non-additive fields can't be summed:
// the two age columns get the mean over rows that have a value, and xid_age the
// max — the worst (closest-to-wraparound) relation in view, which is the number
// that matters — since adding transaction ages together is meaningless.
func sumTableStats(rows []pg.TableStat) pg.TableStat {
	var t pg.TableStat
	var vacSum, anaSum float64
	var vacN, anaN int
	for _, r := range rows {
		t.HeapBytes += r.HeapBytes
		t.IndexesBytes += r.IndexesBytes
		t.ToastBytes += r.ToastBytes
		t.TotalBytes += r.TotalBytes
		t.NLive += r.NLive
		t.NDead += r.NDead
		t.NInsert += r.NInsert
		t.NUpdate += r.NUpdate
		t.NDelete += r.NDelete
		t.NHotUpdate += r.NHotUpdate
		t.NModSinceAnalyze += r.NModSinceAnalyze
		t.NInsSinceVacuum += r.NInsSinceVacuum
		t.SeqScan += r.SeqScan
		t.IdxScan += r.IdxScan
		t.SeqTupRead += r.SeqTupRead
		t.IdxTupFetch += r.IdxTupFetch
		t.VacuumCount += r.VacuumCount
		t.AutovacuumCount += r.AutovacuumCount
		t.AnalyzeCount += r.AnalyzeCount
		t.AutoanalyzeCount += r.AutoanalyzeCount
		t.HeapBlksRead += r.HeapBlksRead
		t.HeapBlksHit += r.HeapBlksHit
		t.IdxBlksRead += r.IdxBlksRead
		t.IdxBlksHit += r.IdxBlksHit
		t.BufferedBytes += r.BufferedBytes
		t.DirtyBytes += r.DirtyBytes
		t.BufsKnown = t.BufsKnown || r.BufsKnown
		if r.FrozenXIDAge > t.FrozenXIDAge {
			t.FrozenXIDAge = r.FrozenXIDAge
		}
		if r.VacAgeMs != nil {
			vacSum += *r.VacAgeMs
			vacN++
		}
		if r.AnaAgeMs != nil {
			anaSum += *r.AnaAgeMs
			anaN++
		}
	}
	if vacN > 0 {
		v := vacSum / float64(vacN)
		t.VacAgeMs = &v
	}
	if anaN > 0 {
		a := anaSum / float64(anaN)
		t.AnaAgeMs = &a
	}
	return t
}

// tblFooterCells builds the pinned footer row for the Table overview, projecting
// the per-column aggregate (sumTableStats) through the same cell builders as the
// data rows so the cells stay parallel to descs by construction. Returns nil when
// there are no rows. The leading table column carries a "Σ N tables" label; the
// two storage-option columns (fillfactor, autovac) are blanked because their
// per-row default ("100" / "—") would otherwise read as a real aggregate.
func tblFooterCells(descs []tblColDesc, rows []pg.TableStat) []pg.DiagCell {
	if len(rows) == 0 {
		return nil
	}
	total := cellsFor(descs, sumTableStats(rows), tblCtx{})
	for i, d := range descs {
		switch d.id {
		case tblColTable:
			total[i] = pg.DiagCell{Display: "Σ " + formatRows(int64(len(rows))) + " tables"}
		case tblColFill, tblColAutovac:
			total[i] = pg.DiagCell{Display: ""}
		}
	}
	return total
}

// rebuildTableStatItems rebuilds the generic-table items for the table-overview
// screen from its cached tblRows, applying the current column selection. Call it
// after a fresh load or any column toggle so projection + sort stay in sync.
func (m *Model) rebuildTableStatItems(s *screen) {
	items, descs := m.buildTableStatItems(s.tbl.rows)
	s.tbl.cols = descs
	s.diagCols = diagColumnsFrom(descs)
	s.diagTotalRow = tblFooterCells(descs, s.tbl.rows)
	// No headline bar here: the table overview carries many numeric columns, so
	// -1 lets the diag renderer hand the bar's width back to the data columns
	// (view_diag.go's no-bar path). size still renders as a humanized number.
	s.diagBarCol = -1
	tblSpec.syncSort(&m.tblTable, s, descs)
	s.items = items
	s.diagMetricsDirty = true
	m.applySort(s)
}
