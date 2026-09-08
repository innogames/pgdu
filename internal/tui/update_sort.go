package tui

import (
	"sort"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// applySort orders s.items by s.sort/s.sortDesc, using Name as a stable
// tiebreaker so reversing direction doesn't shuffle equal rows arbitrarily.
// For levelDiagnosticResult screens (diagCols != nil) it uses the generic
// column comparator instead of the sortMode-based one.
func (m *Model) applySort(s *screen) {
	// Reordering changes which index each row sits at, so the filtered-index
	// cache must rebuild. applySort runs after every load/rebuild too, so this
	// one bump covers the common item-mutation paths.
	s.itemsRev++
	// The WAL block detail is a sectioned key/value dump in a fixed, meaningful
	// order (record → block → tuple → hex → page image); sorting would shuffle
	// hex rows into the header.
	if s.level == levelWALBlockDetail {
		s.clampCursor()
		return
	}
	// The log groups pane orders itself (sections, then s.sort within each).
	if s.level == levelLogs && s.diagCols == nil {
		if s.log.report != nil {
			s.items = m.buildLogGroupItems(s)
			s.itemsRev++
		}
		s.clampCursor()
		return
	}
	// The WAL overview stacks two tables (rmgrs, then relations) with inert
	// section rows between them; it rebuilds from the screen's stats and sorts
	// each table by s.sort on its own. A clamp can land on the trailing Σ (the
	// relation list shrank on refresh), so step back onto a real row.
	if s.level == levelWAL {
		s.items = buildWALItems(s)
		s.clampCursor()
		s.skipInertRow(-1)
		return
	}
	if s.diagCols != nil {
		// Generic diagnostic-table sort: compare by diagSortCol, numeric rows
		// before text rows (HasNum=false sinks below rows with a value), then
		// fall back to the first-column Display as a tiebreaker.
		col := s.diagSortCol
		sort.SliceStable(s.items, func(i, j int) bool {
			ri, _ := s.items[i].data.([]pg.DiagCell)
			rj, _ := s.items[j].data.([]pg.DiagCell)
			var ci, cj pg.DiagCell
			if col < len(ri) {
				ci = ri[col]
			}
			if col < len(rj) {
				cj = rj[col]
			}
			// Rows without a numeric value always sort last regardless of
			// direction so nulls/missing data never pollute the top.
			if ci.HasNum != cj.HasNum {
				return ci.HasNum // row with a value always comes first
			}
			var less bool
			if ci.HasNum {
				less = ci.Num < cj.Num
			} else {
				less = ci.Display < cj.Display
			}
			if s.sortDesc {
				return !less
			}
			return less
		})
		s.clampCursor()
		return
	}

	sortItems(s.items, s.sort, s.sortDesc)
	s.clampCursor()
}

// sortItems orders items by mode/desc with the name as a stable tiebreaker, so
// reversing the direction never shuffles equal rows. Shared by applySort and
// the sectioned lists that sort each section on its own.
func sortItems(items []item, mode sortMode, desc bool) {
	less := mode.less
	sort.SliceStable(items, func(i, j int) bool {
		if less(items[i], items[j]) {
			return !desc
		}
		if less(items[j], items[i]) {
			return desc
		}
		return items[i].name < items[j].name
	})
}

// itemHitRatio extracts the hit ratio from an item's payload when it carries
// buffer-cache stats. The second return is false when the item has no such
// payload, or when the table has no recorded I/O (HitRatio == -1).
func itemHitRatio(it item) (float64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok {
		return 0, false
	}
	r := st.HitRatio()
	if r < 0 {
		return 0, false
	}
	return r, true
}

// itemTotalBytes extracts the on-disk total size for a buffer-tables item
// (pg_total_relation_size, the "total" column). Returns (0, false) for
// rows without buffer-stat data or where the catalog reported a zero size,
// so those sort below tables we can measure.
func itemTotalBytes(it item) (int64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok || st.TotalBytes <= 0 {
		return 0, false
	}
	return st.TotalBytes, true
}

// itemCachedRatio extracts the fraction of a table currently in the shared
// buffer cache (BufferedBytes / TotalBytes) from an item's payload. Returns
// (0, false) for items without buffer-stat data or with no size information,
// so those rows sort below tables that do have a measurable ratio.
func itemCachedRatio(it item) (float64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok || st.TotalBytes <= 0 {
		return 0, false
	}
	return float64(st.BufferedBytes) / float64(st.TotalBytes), true
}

// itemDirtyBytes extracts the dirty (modified-in-memory, awaiting flush) byte
// footprint of a buffer-tables item. Returns (0, false) for rows without
// buffer-stat data so they sort below tables we can measure; a real zero
// (clean table) still sorts as the smallest measurable value.
func itemDirtyBytes(it item) (int64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok {
		return 0, false
	}
	return st.DirtyBytes, true
}

// itemDirtyFrac is sortByDirtyPct's extractor: the dirty share of a table's
// buffered bytes, undefined (unsortable) for tables with nothing buffered.
func itemDirtyFrac(it item) (float64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok || st.BufferedBytes <= 0 {
		return 0, false
	}
	return float64(st.DirtyBytes) / float64(st.BufferedBytes), true
}

// itemUsageAvg extracts a buffer-tables item's mean clock-sweep usagecount
// (the "temp" column, 0..5). Returns (0, false) when the table has no pages
// in shared_buffers — a temperature only exists for cached pages — so those
// rows sort below tables with a measurable one rather than tying at cold.
func itemUsageAvg(it item) (float64, bool) {
	st, ok := it.data.(pg.TableBufferStat)
	if !ok || st.BufferedBytes <= 0 {
		return 0, false
	}
	return st.UsageAvg, true
}

// itemTemp is sortByTemp's extractor across both consumers of the shared
// temperature column: buffer-tables rows (mean usagecount) and page-inspector
// rows (the page's exact usagecount, see itemPageTemp).
func itemTemp(it item) (float64, bool) {
	if _, ok := it.data.(pg.TableBufferStat); ok {
		return itemUsageAvg(it)
	}
	return itemPageTemp(it)
}

// itemRows extracts the row-count estimate from a table or relation item.
// Second return is false for items lacking row estimates and for negative
// EstRows (planner stats unavailable).
func itemRows(it item) (int64, bool) {
	switch t := it.data.(type) {
	case pg.Table:
		if t.EstRows < 0 {
			return 0, false
		}
		return t.EstRows, true
	case pg.Relation:
		if t.EstRows < 0 {
			return 0, false
		}
		return t.EstRows, true
	}
	return 0, false
}

// itemBloatRatio extracts a relation's wasted-space fraction (bloat/size) for
// the parts level. Returns (0, false) for rows whose bloat hasn't been measured
// yet (hasBloat == false) so they sort below measured rows; a measured-zero row
// returns (0, true) and sorts as the least-bloated of the known ones.
func itemBloatRatio(it item) (float64, bool) {
	if !it.hasBloat || it.size <= 0 {
		return 0, false
	}
	return float64(it.bloat) / float64(it.size), true
}

// itemHeapBytes / itemIndexBytes / itemToastBytes extract the heap,
// combined-index and toast byte footprints carried on tables-level items.
// Gated on the pg.Table payload so a row from another level (where these
// fields are zero) sorts to the bottom rather than tying at zero with a
// genuinely empty heap/index/toast.
func itemHeapBytes(it item) (int64, bool) {
	if _, ok := it.data.(pg.Table); !ok {
		return 0, false
	}
	return it.heap, true
}

func itemIndexBytes(it item) (int64, bool) {
	if _, ok := it.data.(pg.Table); !ok {
		return 0, false
	}
	return it.idx, true
}

func itemToastBytes(it item) (int64, bool) {
	if _, ok := it.data.(pg.Table); !ok {
		return 0, false
	}
	return it.toast, true
}

// itemColType / itemColAvgWidth extract the data type and pg_stats avg_width
// of a per-column-space row. Gated on the pg.Column payload so rows from other
// levels (where these are absent) sort to the bottom rather than tying.
func itemColType(it item) (string, bool) {
	c, ok := it.data.(pg.Column)
	if !ok {
		return "", false
	}
	return c.Type, true
}

func itemColAvgWidth(it item) (int64, bool) {
	c, ok := it.data.(pg.Column)
	if !ok {
		return 0, false
	}
	return int64(c.AvgWidth), true
}

// itemSchemaTables extracts a schema's table count for the schemas level.
// Gated on the pg.Schema payload so rows from other levels sort to the bottom.
func itemSchemaTables(it item) (int64, bool) {
	sc, ok := it.data.(pg.Schema)
	if !ok {
		return 0, false
	}
	return sc.TableCount, true
}

// itemLogTime is the sortByLast extractor: a group's last-seen time or an
// entry's timestamp, as unix nanoseconds.
func itemLogTime(it item) (int64, bool) {
	switch v := it.data.(type) {
	case *pglog.Group:
		return v.Last.UnixNano(), true
	case *pglog.Entry:
		return v.Time.UnixNano(), true
	}
	return 0, false
}

// validSorts declares which sort modes are meaningful at each level. Keys
// outside the returned set are silently ignored in handleKey, so adding a new
// level here is the single source of truth for "which sort keys do what".
// validSorts is also the cycle order for the ←/→ keys — the first entry is
// the default sort for a freshly opened screen.
func validSorts(l level) []sortMode {
	switch l {
	case levelTools, levelDiagnostics, levelPgBouncer:
		return []sortMode{sortByName}
	case levelTables:
		return []sortMode{sortBySize, sortByHeap, sortByIndex, sortByToast, sortByRows, sortByName}
	case levelParts:
		return []sortMode{sortBySize, sortByBloat, sortByType, sortByName}
	case levelColumns:
		return []sortMode{sortBySize, sortByAvgWidth, sortByColType, sortByName}
	case levelSchemas:
		return []sortMode{sortBySize, sortByTables, sortByName}
	case levelBufferTables:
		return []sortMode{sortBySize, sortByTotal, sortByCached, sortByHitRatio, sortByDirty, sortByDirtyPct, sortByTemp, sortByName}
	case levelShmem:
		return []sortMode{sortBySize, sortByGroup, sortByName}
	case levelHeapPages:
		return []sortMode{sortByBlkno, sortBySize, sortByLiveLP, sortByRedirectLP, sortByDeadLP, sortByDeadRatio, sortByFreeSpace, sortByTemp}
	case levelHeapTuples:
		return []sortMode{sortByLP, sortBySize}
	case levelTupleRow, levelWALBlockDetail:
		return []sortMode{sortByName}
	case levelRelations:
		return []sortMode{sortBySize, sortByRows, sortByType, sortByName}
	case levelIndexPages:
		// Level leads so a freshly opened B-tree page view (the dominant index
		// AM) defaults to root-first and ←/→ cycles from there. GiST/BRIN/GIN
		// override the default to sortByBlkno at screen construction (level is
		// inert for them), but share this cycle list.
		return []sortMode{sortByLevel, sortByType, sortBySize, sortByDeadRatio, sortByFreeSpace, sortByTemp, sortByBlkno}
	case levelIndexTuples:
		return []sortMode{sortByLP, sortBySize}
	case levelWAL:
		// One cycle for both tables: record (rmgr) and pages (relation) exist in
		// only one of them, and leave the other in name order.
		return []sortMode{sortBySize, sortByRecord, sortByFPI, sortByCount, sortByPages, sortByName}
	case levelWALRecords:
		return []sortMode{sortBySize, sortByFPI, sortByName}
	case levelWALBlocks, levelWALRelBlocks:
		return []sortMode{sortBySize, sortByData, sortByName}
	case levelLogs:
		return []sortMode{sortByCount, sortByLast, sortByName}
	case levelLogGroup:
		return []sortMode{sortByLast}
	case levelLogFiles, levelLogEntry:
		return []sortMode{sortByName}
	default:
		return []sortMode{sortBySize, sortByName}
	}
}

// cycleSort steps s.sort by dir (+1 = next column via →, -1 = prev via ←)
// through validSorts(s.level), wrapping at both ends, and resets the direction
// to that column's natural default. Single-entry sort lists (e.g. levelTools)
// become a no-op. For levelDiagnosticResult the generic column set is cycled
// instead of sortMode.
func (m *Model) cycleSort(s *screen, dir int) {
	if s.diagCols != nil {
		n := len(s.diagCols)
		if n < 2 {
			return
		}
		s.diagSortCol = ((s.diagSortCol+dir)%n + n) % n
		// Numeric columns default to descending (biggest first);
		// text columns default to ascending (alphabetical).
		switch s.diagCols[s.diagSortCol].Kind {
		case pg.DiagInt, pg.DiagFloat, pg.DiagPercent, pg.DiagBytes, pg.DiagPercentGraded, pg.DiagCostGraded, pg.DiagDuration, pg.DiagPercentBad, pg.DiagCount:
			s.sortDesc = true
		default:
			s.sortDesc = false
		}
		// On the top-queries table, remember the chosen column by stable id so a
		// later column hide/show re-pins the sort to the same column (see
		// syncStmtSort). Same logic for the Activity table's actCols.
		if s.stat.cols != nil && s.diagSortCol < len(s.stat.cols) {
			m.stmtTable.sortColID = s.stat.cols[s.diagSortCol].id
		}
		if s.act.cols != nil && s.diagSortCol < len(s.act.cols) {
			m.actTable.sortColID = s.act.cols[s.diagSortCol].id
		}
		if s.tbl.cols != nil && s.diagSortCol < len(s.tbl.cols) {
			m.tblTable.sortColID = s.tbl.cols[s.diagSortCol].id
		}
		if s.level == levelLogs && s.log.cols != nil && s.diagSortCol < len(s.log.cols) {
			*m.logSortCol(s.log.view) = s.log.cols[s.diagSortCol].id
		}
		// Diagnostic results track the sort column by name (no stable ids), so a
		// later column hide/show can re-pin it (see rebuildDiagItems).
		if s.diagVisKey() != "" && s.diagSortCol < len(s.diagCols) {
			s.diagSortName = s.diagCols[s.diagSortCol].Name
		}
		m.applySort(s)
		return
	}

	opts := validSorts(s.level)
	n := len(opts)
	if n < 2 {
		return
	}
	idx := 0
	for i, sm := range opts {
		if sm == s.sort {
			idx = i
			break
		}
	}
	next := opts[((idx+dir)%n+n)%n]
	s.sort = next
	s.sortDesc = next.defaultDesc()
	m.applySort(s)
}
