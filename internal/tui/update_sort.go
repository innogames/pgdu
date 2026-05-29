package tui

import (
	"sort"

	"pgdu/internal/pg"
)

// applySort orders s.items by s.sort/s.sortDesc, using Name as a stable
// tiebreaker so reversing direction doesn't shuffle equal rows arbitrarily.
func (m *Model) applySort(s *screen) {
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

// itemRows extracts the row-count estimate from a table item. Second return is
// false for any item whose payload isn't a pg.Table, or when EstRows is
// negative (planner stats unavailable).
func itemRows(it item) (int64, bool) {
	t, ok := it.data.(pg.Table)
	if !ok {
		return 0, false
	}
	if t.EstRows < 0 {
		return 0, false
	}
	return t.EstRows, true
}

// validSorts declares which sort modes are meaningful at each level. Keys
// outside the returned set are silently ignored in handleKey, so adding a new
// level here is the single source of truth for "which sort keys do what".
// validSorts is also the cycle order for the `s` key — the first entry is
// the default sort for a freshly opened screen.
func validSorts(l level) []sortMode {
	switch l {
	case levelTools:
		return []sortMode{sortByName}
	case levelTables:
		return []sortMode{sortBySize, sortByRows, sortByName}
	case levelBufferTables:
		return []sortMode{sortBySize, sortByTotal, sortByCached, sortByHitRatio, sortByName}
	default:
		return []sortMode{sortBySize, sortByName}
	}
}

// cycleSort advances s.sort to the next entry in validSorts(s.level) and
// resets the direction to that column's natural default. Single-entry sort
// lists (e.g. levelTools) become a no-op.
func (m *Model) cycleSort(s *screen) {
	opts := validSorts(s.level)
	if len(opts) < 2 {
		return
	}
	idx := 0
	for i, sm := range opts {
		if sm == s.sort {
			idx = i
			break
		}
	}
	next := opts[(idx+1)%len(opts)]
	s.sort = next
	s.sortDesc = next.defaultDesc()
	m.applySort(s)
}
