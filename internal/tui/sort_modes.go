package tui

type sortMode int

const (
	sortBySize sortMode = iota
	sortByName
	sortByHitRatio
	sortByCached
	sortByTotal
	sortByRows
	sortByBlkno
	sortByDeadRatio
	sortByFreeSpace
	sortByLP
	sortByLevel
	sortByCount      // WAL: record count per resource manager
	sortByFPI        // WAL: full-page-image bytes
	sortByDirty      // buffer-tables: dirty (modified-in-memory) bytes
	sortByType       // index pages: page type (leaf/intr/root/del)
	sortByBloat      // parts: wasted-space fraction (bloat %)
	sortByHeap       // tables: heap (main fork) bytes
	sortByIndex      // tables: combined index bytes
	sortByColType    // columns: data type (text)
	sortByAvgWidth   // columns: pg_stats avg_width (bytes per non-null value)
	sortByTables     // schemas: table count
	sortByLiveLP     // heap pages: live line-pointer count
	sortByRedirectLP // heap pages: REDIRECT (HOT-hop) line-pointer count
	sortByDeadLP     // heap pages: DEAD line-pointer count
	sortByGroup      // shmem: subsystem category (buffer pool/WAL/locks/…)
)

// defaultDesc is the natural direction for each sort column: bigger-first for
// numeric "more is more" columns, alphabetical for name, ascending for hit
// ratio so the worst-cached tables bubble to the top.
func (sm sortMode) defaultDesc() bool {
	switch sm {
	case sortBySize, sortByRows, sortByCached, sortByTotal, sortByDeadRatio, sortByFreeSpace, sortByCount, sortByFPI, sortByDirty, sortByBloat, sortByHeap, sortByIndex, sortByAvgWidth, sortByTables, sortByLiveLP, sortByRedirectLP, sortByDeadLP, sortByLevel:
		// sortByLevel is descending so the B-tree page view opens root-first
		// (highest btpo_level at the top), reading the tree top-down.
		return true
	case sortByName, sortByHitRatio, sortByBlkno, sortByLP, sortByType, sortByColType, sortByGroup:
		return false
	}
	return false
}

// name is the short column label used in the status row and column headers.
func (sm sortMode) name() string {
	switch sm {
	case sortBySize:
		return "size"
	case sortByRows:
		return "rows"
	case sortByHitRatio:
		return "hit"
	case sortByCached:
		return "cached"
	case sortByTotal:
		return "total"
	case sortByBlkno:
		return "blkno"
	case sortByDeadRatio:
		return "dead%"
	case sortByFreeSpace:
		return "free"
	case sortByLP:
		return "lp"
	case sortByLevel:
		return "level"
	case sortByCount:
		return "count"
	case sortByFPI:
		return "fpi"
	case sortByDirty:
		return "dirty"
	case sortByType:
		return "type"
	case sortByBloat:
		return "bloat"
	case sortByHeap:
		return "heap"
	case sortByIndex:
		return "idx"
	case sortByColType:
		return "type"
	case sortByAvgWidth:
		return "avg"
	case sortByTables:
		return "tables"
	case sortByLiveLP:
		return "live"
	case sortByRedirectLP:
		return "R"
	case sortByDeadLP:
		return "dead"
	case sortByGroup:
		return "group"
	default:
		return "name"
	}
}

// label is name plus an arrow indicating the current sort direction.
func (sm sortMode) label(desc bool) string {
	arrow := "↑"
	if desc {
		arrow = "↓"
	}
	return sm.name() + arrow
}

// less returns true when item a should come before item b *ignoring* the
// direction flag — applySort applies direction by flipping the result.
// Items missing the comparator's payload (no rows estimate, no hit ratio)
// sort below items that have one, so "unknown" stays a distinct bucket from
// "small".
func (sm sortMode) less(a, b item) bool {
	switch sm {
	case sortBySize:
		return a.size < b.size
	case sortByName:
		return false
	case sortByRows:
		return lessByExtractor(a, b, itemRows)
	case sortByHitRatio:
		return lessByExtractor(a, b, itemHitRatio)
	case sortByCached:
		return lessByExtractor(a, b, itemCachedRatio)
	case sortByTotal:
		return lessByExtractor(a, b, itemTotalBytes)
	case sortByBlkno:
		return lessByExtractor(a, b, itemBlkno)
	case sortByDeadRatio:
		return lessByExtractor(a, b, itemDeadRatio)
	case sortByFreeSpace:
		return lessByExtractor(a, b, itemFreeSpace)
	case sortByLP:
		return lessByExtractor(a, b, itemLP)
	case sortByLevel:
		return lessByExtractor(a, b, itemTreeLevel)
	case sortByCount:
		return lessByExtractor(a, b, itemWALCount)
	case sortByFPI:
		return lessByExtractor(a, b, itemWALFPI)
	case sortByDirty:
		return lessByExtractor(a, b, itemDirtyBytes)
	case sortByType:
		return lessByExtractor(a, b, itemPageType)
	case sortByBloat:
		return lessByExtractor(a, b, itemBloatRatio)
	case sortByHeap:
		return lessByExtractor(a, b, itemHeapBytes)
	case sortByIndex:
		return lessByExtractor(a, b, itemIndexBytes)
	case sortByColType:
		return lessByStringExtractor(a, b, itemColType)
	case sortByAvgWidth:
		return lessByExtractor(a, b, itemColAvgWidth)
	case sortByTables:
		return lessByExtractor(a, b, itemSchemaTables)
	case sortByLiveLP:
		return lessByExtractor(a, b, itemLiveLP)
	case sortByRedirectLP:
		return lessByExtractor(a, b, itemRedirectLP)
	case sortByDeadLP:
		return lessByExtractor(a, b, itemDeadLP)
	case sortByGroup:
		return lessByExtractor(a, b, itemShmemGroup)
	}
	return false
}

// lessByExtractor compares two items via an extractor function, applying the
// "unknown sorts below known" rule: items where ok=false always sort after
// items where ok=true; two unknowns are considered equal (returns false).
// Used by sortMode.less to collapse the repeated 6-line extractor pattern.
func lessByExtractor[T int64 | float64](a, b item, extract func(item) (T, bool)) bool {
	ai, oka := extract(a)
	bi, okb := extract(b)
	if oka != okb {
		return okb // the item with a value sorts before the one without
	}
	if !oka {
		return false // both unknown: treat as equal
	}
	return ai < bi
}

// lessByStringExtractor is the string-keyed counterpart of lessByExtractor:
// same "unknown sorts below known" rule, lexicographic comparison of the key.
func lessByStringExtractor(a, b item, extract func(item) (string, bool)) bool {
	as, oka := extract(a)
	bs, okb := extract(b)
	if oka != okb {
		return okb // the item with a value sorts before the one without
	}
	if !oka {
		return false // both unknown: treat as equal
	}
	return as < bs
}
