package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

// The bar must occupy exactly its budget whatever the sign, or a row with a
// shrink would not line up with the row above it.
func TestRenderDeltaBarWidth(t *testing.T) {
	for _, width := range []int{1, 2, 6, 11, 12, 40} {
		for _, num := range []float64{-100, -1, 0, 1, 100} {
			got := stripANSI(renderDeltaBar(num, 100, width))
			if n := len([]rune(got)); n != width+colBrackets {
				t.Errorf("renderDeltaBar(%v, width %d) is %d runes, want %d: %q",
					num, width, n, width+colBrackets, got)
			}
		}
	}
}

// Direction lives in the geometry: a shrink runs left of the centre, growth
// runs right, so the two read apart without going to the digits.
func TestRenderDeltaBarSides(t *testing.T) {
	const width = 10
	// Halves have to be split by rune: the block and shade glyphs are three
	// bytes each, so byte slicing cuts one in half and compares garbage.
	halves := func(num float64) (string, string) {
		r := []rune(strings.Trim(stripANSI(renderDeltaBar(num, 100, width)), "[]"))
		return string(r[:width/2]), string(r[width/2:])
	}
	left, right := halves(-100)
	if strings.Contains(left, "░") || strings.Contains(right, "▇") {
		t.Errorf("a full shrink must fill the left half only: %q|%q", left, right)
	}
	left, right = halves(100)
	if strings.Contains(left, "▇") || strings.Contains(right, "░") {
		t.Errorf("full growth must fill the right half only: %q|%q", left, right)
	}
	left, right = halves(0)
	if strings.Contains(left+right, "▇") {
		t.Errorf("no change must draw an empty bar: %q|%q", left, right)
	}
}

// A change too small to earn a cell still gets one. Rounding it away would
// make the bar say the row did not move, which is the one thing it is for.
func TestRenderDeltaBarKeepsTinyChangesVisible(t *testing.T) {
	for _, num := range []float64{1, -1} {
		got := strings.Trim(stripANSI(renderDeltaBar(num, 1e9, 20)), "[]")
		if strings.Count(got, "▇") != 1 {
			t.Errorf("renderDeltaBar(%v) = %q, want exactly one filled cell", num, got)
		}
	}
}

// The regression this replaces: barMax tracked only positive values, so a
// window where everything shrank scaled every bar against zero and drew the
// whole column empty.
func TestDeltaBarScalesWhenEverythingShrank(t *testing.T) {
	gb := int64(1) << 30
	rows := []pg.GrowthRow{
		gRow("public", "equipment", 47*gb, 28*gb, pg.GrowthSteady),
		gRow("public", "battle", 83*gb, 81*gb, pg.GrowthSteady),
	}
	s := growthScreen(rows, 94*time.Hour)
	m := newTestModel(s)
	m.width = 140
	m.rebuildGrowthItems(s)
	s.sortDesc = false // ascending: the biggest shrink on top
	m.applySort(s)

	out := stripANSI(m.renderDiagResult(s, 4))
	if s.diagBarMax <= 0 {
		t.Fatalf("barMax = %v; an all-negative column must still scale", s.diagBarMax)
	}
	if !strings.Contains(out, "▇") {
		t.Errorf("every bar rendered empty:\n%s", out)
	}
	// The largest shrink reaches the far edge of its half.
	if !strings.Contains(out, "[▇") {
		t.Errorf("the largest shrink should reach the left edge:\n%s", out)
	}
}
