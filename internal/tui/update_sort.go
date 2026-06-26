package tui

import (
	"sort"

	"pgdu/internal/pg"
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

	less := s.sort.less
	sort.SliceStable(s.items, func(i, j int) bool {
		if less(s.items[i], s.items[j]) {
			return !s.sortDesc
		}
		if less(s.items[j], s.items[i]) {
			return s.sortDesc
		}
		return s.items[i].name < s.items[j].name
	})
	s.clampCursor()
}

// syncStmtSort re-resolves the top-queries sort column index (s.diagSortCol) from
// the identity m.stmtSortColID after a rebuild, since hiding/showing columns
// shifts every index. When the sorted column is no longer visible it falls back
// to total_ms (the default), or the first visible column if that's hidden too.
func (m *Model) syncStmtSort(s *screen, descs []stmtColDesc) {
	idx := indexOfStmtCol(descs, m.stmtSortColID)
	if idx < 0 {
		idx = indexOfStmtCol(descs, colTotalMs)
		s.sortDesc = true
		if idx < 0 {
			idx = 0
		}
		if idx < len(descs) {
			m.stmtSortColID = descs[idx].id
		}
	}
	s.diagSortCol = idx
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

// itemHeapBytes / itemIndexBytes extract the heap and combined-index byte
// footprints carried on tables-level items. Gated on the pg.Table payload so a
// row from another level (where these fields are zero) sorts to the bottom
// rather than tying at zero with a genuinely empty heap/index.
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

// validSorts declares which sort modes are meaningful at each level. Keys
// outside the returned set are silently ignored in handleKey, so adding a new
// level here is the single source of truth for "which sort keys do what".
// validSorts is also the cycle order for the ←/→ keys — the first entry is
// the default sort for a freshly opened screen.
func validSorts(l level) []sortMode {
	switch l {
	case levelTools, levelDiagnostics:
		return []sortMode{sortByName}
	case levelTables:
		return []sortMode{sortBySize, sortByHeap, sortByIndex, sortByRows, sortByName}
	case levelParts:
		return []sortMode{sortBySize, sortByBloat, sortByType, sortByName}
	case levelColumns:
		return []sortMode{sortBySize, sortByAvgWidth, sortByColType, sortByName}
	case levelSchemas:
		return []sortMode{sortBySize, sortByTables, sortByName}
	case levelBufferTables:
		return []sortMode{sortBySize, sortByTotal, sortByCached, sortByHitRatio, sortByDirty, sortByName}
	case levelShmem:
		return []sortMode{sortBySize, sortByGroup, sortByName}
	case levelHeapPages:
		return []sortMode{sortByBlkno, sortBySize, sortByLiveLP, sortByRedirectLP, sortByDeadLP, sortByDeadRatio, sortByFreeSpace}
	case levelHeapTuples:
		return []sortMode{sortByLP, sortBySize}
	case levelTupleRow:
		return []sortMode{sortByName}
	case levelRelations:
		return []sortMode{sortBySize, sortByRows, sortByType, sortByName}
	case levelIndexPages:
		// Level leads so a freshly opened B-tree page view (the dominant index
		// AM) defaults to root-first and ←/→ cycles from there. GiST/BRIN/GIN
		// override the default to sortByBlkno at screen construction (level is
		// inert for them), but share this cycle list.
		return []sortMode{sortByLevel, sortByType, sortBySize, sortByDeadRatio, sortByFreeSpace, sortByBlkno}
	case levelIndexTuples:
		return []sortMode{sortByLP, sortBySize}
	case levelWAL:
		return []sortMode{sortBySize, sortByCount, sortByFPI, sortByName}
	case levelWALRecords:
		return []sortMode{sortBySize, sortByFPI, sortByName}
	case levelWALBlocks:
		return []sortMode{sortBySize, sortByName}
	case levelWALRelations:
		return []sortMode{sortBySize, sortByFPI, sortByCount, sortByName}
	case levelWALRelBlocks:
		return []sortMode{sortBySize, sortByName}
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
		case pg.DiagInt, pg.DiagFloat, pg.DiagPercent, pg.DiagBytes, pg.DiagPercentGraded, pg.DiagCostGraded, pg.DiagDuration:
			s.sortDesc = true
		default:
			s.sortDesc = false
		}
		// On the top-queries table, remember the chosen column by stable id so a
		// later column hide/show re-pins the sort to the same column (see
		// syncStmtSort). Same logic for the Activity table's actCols.
		if s.stmtCols != nil && s.diagSortCol < len(s.stmtCols) {
			m.stmtSortColID = s.stmtCols[s.diagSortCol].id
		}
		if s.actCols != nil && s.diagSortCol < len(s.actCols) {
			m.actSortColID = s.actCols[s.diagSortCol].id
		}
		if s.tblCols != nil && s.diagSortCol < len(s.tblCols) {
			m.tblSortColID = s.tblCols[s.diagSortCol].id
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
