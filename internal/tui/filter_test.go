package tui

import "testing"

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		query, target string
		want          bool
	}{
		{"", "anything", true},
		{"abc", "abc", true},
		{"ac", "abc", true},              // subsequence, gap allowed
		{"GPI", "game_player_inv", true}, // case-insensitive
		{"cba", "abc", false},            // order matters
		{"abcd", "abc", false},           // query longer than match
		{"xyz", "abc", false},
	}
	for _, c := range cases {
		if got := fuzzyMatch(c.query, c.target); got != c.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", c.query, c.target, got, c.want)
		}
	}
}

func TestSubstringMatch(t *testing.T) {
	cases := []struct {
		query, target string
		want          bool
	}{
		{"", "anything", true},
		{"battle", "select * from battle", true},
		{"battle", "BATTLE", true}, // case-insensitive
		// The subsequence that fooled fuzzyMatch: b…a…t…t…l…e scattered across a
		// long statement must NOT match as a substring.
		{"battle", "select b1_0.id, b1_0.created_at from player_state", false},
		{"ac", "abc", false}, // not contiguous
	}
	for _, c := range cases {
		if got := substringMatch(c.query, c.target); got != c.want {
			t.Errorf("substringMatch(%q, %q) = %v, want %v", c.query, c.target, got, c.want)
		}
	}
}

func TestMatchFilterPerLevel(t *testing.T) {
	// b…a…t…t…l…e is a subsequence of this query but not a substring.
	const q = "select b1_0.id, b1_0.created_at from player_state where a > $1"

	stmt := &screen{level: levelStatements, filter: "battle"}
	if stmt.matchFilter(q) {
		t.Error("levelStatements should use substring matching: 'battle' must not match a query that only contains it as a subsequence")
	}

	other := &screen{level: levelTables, filter: "battle"}
	if !other.matchFilter(q) {
		t.Error("non-statement levels should keep fuzzy matching: 'battle' is a subsequence of the query")
	}
}

func TestViewportRange(t *testing.T) {
	cases := []struct {
		name                           string
		cursor, offset, height, length int
		wantOffset, wantEnd            int
	}{
		{"cursor inside window", 2, 0, 10, 20, 0, 10},
		{"cursor below window scrolls down", 12, 0, 10, 20, 3, 13},
		{"cursor above window scrolls up", 1, 5, 10, 20, 1, 11},
		{"end clamped to length", 0, 0, 10, 4, 0, 4},
		{"negative offset clamped", 0, -3, 10, 20, 0, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			off, end := viewportRange(c.cursor, c.offset, c.height, c.length)
			if off != c.wantOffset || end != c.wantEnd {
				t.Errorf("viewportRange(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					c.cursor, c.offset, c.height, c.length, off, end, c.wantOffset, c.wantEnd)
			}
		})
	}
}

func TestMaxItemSize(t *testing.T) {
	items := []item{{size: 10}, {size: 50}, {size: 30}}
	if got := maxItemSize(items, []int{0, 1, 2}); got != 50 {
		t.Errorf("maxItemSize all = %d, want 50", got)
	}
	if got := maxItemSize(items, []int{0, 2}); got != 30 {
		t.Errorf("maxItemSize subset = %d, want 30", got)
	}
	if got := maxItemSize(items, nil); got != 0 {
		t.Errorf("maxItemSize empty = %d, want 0", got)
	}
}

func TestVisibleIndexesAndLen(t *testing.T) {
	s := &screen{items: []item{
		{name: "users"},
		{name: "orders"},
		{name: "order_items"},
	}}

	// No filter: identity mapping over every item.
	if got := s.visibleIndexes(); len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Errorf("visibleIndexes (no filter) = %v, want [0 1 2]", got)
	}
	if got := s.visibleLen(); got != 3 {
		t.Errorf("visibleLen (no filter) = %d, want 3", got)
	}

	// Fuzzy filter "ord" matches the two order tables.
	s.filter = "ord"
	idx := s.visibleIndexes()
	if len(idx) != 2 || idx[0] != 1 || idx[1] != 2 {
		t.Errorf("visibleIndexes(ord) = %v, want [1 2]", idx)
	}
	if got := s.visibleLen(); got != 2 {
		t.Errorf("visibleLen(ord) = %d, want 2", got)
	}
}
