package tui

import "testing"

// TestBarWidth pins the terminal-width clamp: levelDescribe reserves nothing,
// so the bar gets the full width, bounded by [barWidthMin, barWidthMax].
func TestBarWidth(t *testing.T) {
	s := &screen{level: levelDescribe}
	cases := []struct{ width, want int }{
		{10, barWidthMin}, // too narrow: fall back to the minimum
		{barWidthMin, barWidthMin},
		{50, 50}, // in range: track the terminal
		{barWidthMax, barWidthMax},
		{500, barWidthMax}, // very wide: cap so columns keep their share
	}
	for _, c := range cases {
		m := &Model{width: c.width}
		if got := m.barWidth(s); got != c.want {
			t.Errorf("barWidth(width=%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

// TestBarReserveSane is a smoke check over every level: the reserve must be
// non-negative and leave room for a bar on a normal-width terminal. It exists
// to catch a levelless typo in the barReserve arithmetic, not to pin exact
// sums (those live next to their renderers).
func TestBarReserveSane(t *testing.T) {
	ams := []string{"btree", "gist", "brin", "gin"}
	// levelTableStats is the last enum value; extend here if a level is added after it.
	for l := levelTools; l <= levelTableStats; l++ {
		for _, tl := range []tool{toolDisk, toolPageInspect} {
			for _, am := range ams {
				s := &screen{level: l, tool: tl}
				s.index.AccessMethod = am
				r := barReserve(s)
				if r < 0 || r > 150 {
					t.Errorf("barReserve(level=%d tool=%d am=%s) = %d, want 0..150", l, tl, am, r)
				}
			}
		}
	}
}
