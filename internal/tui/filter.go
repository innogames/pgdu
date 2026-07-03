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

// substringMatch returns true when target contains query as a contiguous
// (case-insensitive) substring. Empty queries match everything.
func substringMatch(query, target string) bool {
	if query == "" {
		return true
	}
	return containsFold(target, query)
}

// containsFold reports whether s contains substr case-insensitively, without the
// per-call allocation of strings.Contains(strings.ToLower(s), …). The filter runs
// it across every row on every keystroke and the rows (flattened queries) are
// long, so allocating a lowercased copy of each target per frame dominates the
// render. When substr is ASCII (the common case for a typed filter) both sides are
// folded inline: multibyte UTF-8 bytes in s are >= 0x80 and pass through
// unchanged, and no ASCII needle byte is >= 0x80, so they never spuriously match —
// the result equals the lowercased-Contains it replaces. A non-ASCII needle falls
// back to that allocating path so unicode case-folding stays correct.
func containsFold(s, substr string) bool {
	if !isASCII(substr) {
		return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
	}
	n, m := len(s), len(substr)
	if m > n {
		return false
	}
	for i := 0; i+m <= n; i++ {
		j := 0
		for ; j < m; j++ {
			if lowerASCII(s[i+j]) != lowerASCII(substr[j]) {
				break
			}
		}
		if j == m {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// matchFilter applies the active filter to one item's name using the rule that
// suits the level. levelStatements rows carry the whole normalized query as
// their name, where fuzzy subsequence matching degenerates (a scattered
// b…a…t…t…l…e matches almost any long statement), so it uses substring
// matching instead; every other level keeps fuzzy matching, which is forgiving
// of typos in short identifiers like game_player_inventory_log.
func (s *screen) matchFilter(name string) bool {
	if s.level == levelStatements {
		return substringMatch(s.filter, name)
	}
	return fuzzyMatch(s.filter, name)
}

// visKey identifies a cached filtered index slice: it is stale once the filter
// text, the item-list revision, or the item count changes. Comparable by ==.
type visKey struct {
	filter string
	rev    uint64
	n      int
}

// visibleIndexes returns indexes into s.items that pass the active filter,
// preserving the underlying order. When the filter is empty this is the
// identity mapping. Callers treat s.cursor and s.offset as indexes into the
// returned slice; the renderer and the navigation handlers reach into
// s.items via this indirection so the source list never has to be mutated
// by filtering. The result is cached (see visCache) so repeated calls within a
// render — and across the many render frames of a scroll — don't re-match every
// row. Callers must treat the returned slice as read-only.
func (s *screen) visibleIndexes() []int {
	key := visKey{s.filter, s.itemsRev, len(s.items)}
	if s.visCacheOK && s.visCacheKey == key {
		return s.visCache
	}
	out := s.computeVisibleIndexes()
	s.visCache = out
	s.visCacheKey = key
	s.visCacheOK = true
	return out
}

func (s *screen) computeVisibleIndexes() []int {
	if s.filter == "" {
		out := make([]int, len(s.items))
		for i := range s.items {
			out[i] = i
		}
		return out
	}
	var out []int
	for i, it := range s.items {
		if s.matchFilter(it.name) {
			out = append(out, i)
		}
	}
	return out
}

// visibleLen counts items that pass the current filter. It reuses the
// visibleIndexes cache so cursor-bound/label/status callers don't trigger a
// separate full re-match.
func (s *screen) visibleLen() int {
	if s.filter == "" {
		return len(s.items)
	}
	return len(s.visibleIndexes())
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

// resetCursor jumps the selection back to the top of the list. cursor and
// offset move together so the viewport can't be left scrolled past the
// selection.
func (s *screen) resetCursor() {
	s.cursor = 0
	s.offset = 0
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
	end := min(offset+height, length)
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
