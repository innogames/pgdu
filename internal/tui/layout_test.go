package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

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
	for l := levelTools; l <= levelLast; l++ {
		for _, tl := range []tool{toolDisk, toolPageInspect} {
			for _, am := range ams {
				s := &screen{level: l, tool: tl}
				s.pages.index.AccessMethod = am
				r := barReserve(s)
				if r < 0 || r > 150 {
					t.Errorf("barReserve(level=%d tool=%d am=%s) = %d, want 0..150", l, tl, am, r)
				}
			}
		}
	}
}

// TestClipCells pins the clip semantics: fits → unchanged, overflow → at most
// width cells ending in the ellipsis, and huge inputs never trip the O(n²)
// trim loop this replaced (a 60 MB detoasted value used to pin a core; see
// the time guard below).
func TestClipCells(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"empty", "", 10, ""},
		{"zero width", "abc", 0, ""},
		{"ascii fits", "hello", 10, "hello"},
		{"ascii exact", "hello", 5, "hello"},
		{"ascii overflow", "hello world", 6, "hello…"},
		{"width one", "hello", 1, "…"},
		{"utf8 fits", "héllo", 10, "héllo"},
		{"utf8 overflow", "héllo wörld", 6, "héllo…"},
		{"wide cjk overflow", "日本語テキスト", 5, "日本…"},
	}
	for _, c := range cases {
		if got := clipCells(c.in, c.width); got != c.want {
			t.Errorf("%s: clipCells(%q, %d) = %q, want %q", c.name, c.in, c.width, got, c.want)
		}
	}
}

// TestClipCellsHuge feeds a multi-megabyte single-line value through every
// clip entry point at terminal width. The old trim-from-the-end loops were
// O(n²) here and effectively never returned; the byte cap makes each call
// O(width), so even a generous wall-clock bound holds with huge margin.
func TestClipCellsHuge(t *testing.T) {
	huge := strings.Repeat(`{"key":"value with ünïcode → tail"}`, 2_000_000) // ~70 MB
	hugeNL := strings.Repeat("line one\nline two\t", 3_000_000)              // ~54 MB, control bytes

	start := time.Now()
	for _, w := range []int{20, 80, 300} {
		for _, s := range []string{huge, hugeNL} {
			got := clipCells(s, w)
			if lw := lipgloss.Width(got); lw > w {
				t.Errorf("clipCells(huge, %d) rendered %d cells", w, lw)
			}
			if !strings.HasSuffix(got, "…") {
				t.Errorf("clipCells(huge, %d) lost the ellipsis", w)
			}
		}
		got := truncateValue(&huge, w)
		if lw := lipgloss.Width(got); lw > w {
			t.Errorf("truncateValue(huge, %d) rendered %d cells", w, lw)
		}
		if got := truncateValue(&hugeNL, w); strings.ContainsAny(got, "\n\t") {
			t.Errorf("truncateValue(hugeNL, %d) kept control bytes: %q", w, got)
		}
	}
	// ~1s of headroom for slow CI; the O(n²) loop needed hours.
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("clipping huge values took %v — width work is scaling with input size again", el)
	}
}

// TestTruncateValue pins the row-detail value renderer: NULL styling aside,
// values clip to width and multi-line content folds to one line.
func TestTruncateValue(t *testing.T) {
	if got := truncateValue(nil, 10); got != styleMuted.Render("NULL") {
		t.Errorf("truncateValue(nil) = %q", got)
	}
	v := "short"
	if got := truncateValue(&v, 10); got != "short" {
		t.Errorf("truncateValue(short) = %q", got)
	}
	nl := "first line\nsecond\tline"
	if got := truncateValue(&nl, 40); got != "first line second line" {
		t.Errorf("truncateValue(multiline) = %q", got)
	}
}
