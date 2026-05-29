package tui

import "strings"

// fuzzyMatch returns true when every rune in query appears in target in order
// (case-insensitive). Empty queries match everything. This is the same
// matching rule fzf uses for its default mode — cheap, predictable, and
// forgiving of typos in long table names like `game_player_inventory_log`.
func fuzzyMatch(query, target string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	t := strings.ToLower(target)
	qi := 0
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}

// visibleIndexes returns indexes into s.items that pass the active filter,
// preserving the underlying order. When the filter is empty this is the
// identity mapping. Callers treat s.cursor and s.offset as indexes into the
// returned slice; the renderer and the navigation handlers reach into
// s.items via this indirection so the source list never has to be mutated
// by filtering.
func (s *screen) visibleIndexes() []int {
	if s.filter == "" {
		out := make([]int, len(s.items))
		for i := range s.items {
			out[i] = i
		}
		return out
	}
	var out []int
	for i, it := range s.items {
		if fuzzyMatch(s.filter, it.name) {
			out = append(out, i)
		}
	}
	return out
}

// visibleLen counts items that pass the current filter without building the
// index slice. Cheaper than visibleIndexes when callers only need the count
// (cursor bounds, position label, filter status).
func (s *screen) visibleLen() int {
	if s.filter == "" {
		return len(s.items)
	}
	n := 0
	for _, it := range s.items {
		if fuzzyMatch(s.filter, it.name) {
			n++
		}
	}
	return n
}

// clampCursor pins s.cursor inside the current visible range. Used after
// anything that can change the visible set (filter edits, sort, reloads)
// so the cursor never points past the end of the list.
func (s *screen) clampCursor() {
	n := s.visibleLen()
	if s.cursor >= n {
		s.cursor = n - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

// viewportRange adjusts offset so cursor stays inside [offset, offset+height)
// and returns the new offset plus the half-open end. Callers use the offset
// to scroll the list and the end (clamped to the underlying length elsewhere)
// to bound their render loop. height should be > 0.
func viewportRange(cursor, offset, height, length int) (int, int) {
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+height {
		offset = cursor - height + 1
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + height
	if end > length {
		end = length
	}
	return offset, end
}

// maxItemSize returns the largest .size among the items pointed to by vis.
// Used to scale per-row bars against the dominant sibling row. Returns 0 when
// vis is empty or every item is non-positive.
func maxItemSize(items []item, vis []int) int64 {
	var m int64
	for _, idx := range vis {
		if items[idx].size > m {
			m = items[idx].size
		}
	}
	return m
}
