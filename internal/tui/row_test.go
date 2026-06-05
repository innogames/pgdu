package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// barVisibleWidth strips styling and counts the visible cells, including the
// two bracket characters paintBar always emits.
func barVisibleWidth(s string) int { return lipgloss.Width(s) }

func TestPaintBarWidthIsStable(t *testing.T) {
	const w = 20
	cases := []struct {
		name string
		segs []barSegment
	}{
		{"empty (all padding)", nil},
		{"single full segment", []barSegment{{cells: w, style: styleBar}}},
		{"oversized segment clips to width", []barSegment{{cells: w + 10, style: styleBar}}},
		{"two segments", []barSegment{{cells: 5, style: styleBar}, {cells: 5, style: styleBloat}}},
		{"negative cells treated as zero", []barSegment{{cells: -3, style: styleBar}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Brackets add 2 cells on top of the bar interior.
			if got := barVisibleWidth(paintBar(w, c.segs...)); got != w+2 {
				t.Errorf("paintBar width = %d, want %d", got, w+2)
			}
		})
	}
}

func TestRenderBarWidthIsStable(t *testing.T) {
	const w = 30
	for _, tc := range []struct {
		name             string
		size, bloat, max int64
	}{
		{"zero size", 0, 0, 100},
		{"full", 100, 0, 100},
		{"with bloat", 100, 40, 100},
		{"max zero is safe", 10, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := barVisibleWidth(renderBar(tc.size, tc.bloat, tc.max, w)); got != w+2 {
				t.Errorf("renderBar width = %d, want %d", got, w+2)
			}
		})
	}
}

func TestRenderSegmentedAndHeapBarWidth(t *testing.T) {
	const w = 24
	if got := barVisibleWidth(renderSegmentedBar(30, 20, 10, 60, w)); got != w+2 {
		t.Errorf("renderSegmentedBar width = %d, want %d", got, w+2)
	}
	// Rounding must never push the combined segments past the bar width.
	if got := barVisibleWidth(renderSegmentedBar(1, 1, 1, 2, w)); got != w+2 {
		t.Errorf("renderSegmentedBar (rounding) width = %d, want %d", got, w+2)
	}
	if got := barVisibleWidth(renderHeapPageBar(8000, 192, w)); got != w+2 {
		t.Errorf("renderHeapPageBar width = %d, want %d", got, w+2)
	}
}

func TestPadRight(t *testing.T) {
	if got := padRight("abc", 6); got != "abc   " {
		t.Errorf("padRight(\"abc\", 6) = %q, want %q", got, "abc   ")
	}
	if got := padRight("abcdef", 3); got != "abcdef" {
		t.Errorf("padRight no-op = %q, want %q", got, "abcdef")
	}
}

func TestMax0(t *testing.T) {
	if max0(-5) != 0 {
		t.Error("max0(-5) should be 0")
	}
	if max0(7) != 7 {
		t.Error("max0(7) should be 7")
	}
}
