package tui

import (
	"math"
	"strings"
	"testing"
)

func TestSparkline(t *testing.T) {
	tests := []struct {
		name     string
		vals     []float64
		width    int
		scaleMax float64
		want     string
	}{
		{"empty pads", nil, 4, 0, "    "},
		{"zero width", []float64{1}, 0, 0, ""},
		{"all zero no panic", []float64{0, 0, 0}, 3, 0, "▁▁▁"},
		{"ramp orders glyphs", []float64{1, 2, 3, 4, 5, 6, 7, 8}, 8, 0, "▁▂▃▄▅▆▇█"},
		{"short history left-pads", []float64{8, 8}, 4, 0, "  ██"},
		{"long history keeps most recent", []float64{8, 8, 8, 1, 2}, 2, 0, "▄█"},
		{"shared scale", []float64{1, 1}, 2, 8, "▁▁"},
	}
	for _, tt := range tests {
		if got := sparkline(tt.vals, tt.width, tt.scaleMax); got != tt.want {
			t.Errorf("%s: sparkline(%v, %d, %v) = %q, want %q", tt.name, tt.vals, tt.width, tt.scaleMax, got, tt.want)
		}
	}
}

func TestTrendline(t *testing.T) {
	nan := math.NaN()
	tests := []struct {
		name string
		vals []float64
		want string
	}{
		// Min-max scaling: the series' own minimum sits on the bottom glyph
		// and its maximum on the top, whatever the absolute figures are.
		{"ramp spans the ramp", []float64{1, 2, 3, 4, 5, 6, 7, 8}, "▁▂▃▄▅▆▇█"},
		{"offset ramp scales the same", []float64{1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000}, "▁▂▃▄▅▆▇█"},
		// The case the column exists for: a climb that is small against the
		// table's size but real, which a zero-based scale would draw flat.
		// 28.1 → 29.5 GB is 5% — well clear of trendFlatSpan.
		{"growth small against the total is visible", []float64{28.1, 28.5, 29.0, 29.5}, "▁▃▆█"},
		// A level series has no range; it draws level rather than as noise.
		{"flat draws level, and quietly", []float64{100, 100, 100}, "▁▁▁"},
		// Below trendFlatSpan the range is jitter, and magnifying it into a
		// staircase would report growth that did not happen.
		{"jitter under the epsilon is flat", []float64{1000, 1001, 1000, 1002}, "▁▁▁▁"},
		{"a tenth of a percent is flat", []float64{28.10, 28.11, 28.12, 28.13}, "▁▁▁▁"},
		{"one percent is not", []float64{100, 101, 102, 103}, "▁▃▆█"},
		// A gap BETWEEN two measurements is drawn through: captures are sparse
		// and clustered, so dotting every gap spends the column on when pgdu
		// ran rather than on what the table did.
		{"gap between measurements is drawn through", []float64{1, nan, 8}, "▁▅█"},
		{"long gap interpolates evenly", []float64{1, nan, nan, nan, 9}, "▁▃▅▇█"},
		// Before the first measurement there is nothing to draw a line from.
		{"leading unknown stays a gap", []float64{nan, nan, 1, 4, 8}, "··▁▄█"},
		{"all unknown", []float64{nan, nan}, "··"},
		{"empty", nil, ""},
		{"single value is level", []float64{5}, "▁"},
		// A relation created mid-history rises from a real zero.
		{"created mid-history", []float64{0, 0, 4, 8}, "▁▁▅█"},
		{"shrink descends", []float64{8, 7, 6, 5, 4, 3, 2, 1}, "█▇▆▅▄▃▂▁"},
	}
	for _, tt := range tests {
		if got := trendline(tt.vals); got != tt.want {
			t.Errorf("%s: trendline(%v) = %q, want %q", tt.name, tt.vals, got, tt.want)
		}
	}
}

// Interpolation is a drawing decision and must not leak into the numbers: the
// range a trace is scaled into comes from measurements only, so a drawn-through
// cell can never redefine the top or bottom of the column.
func TestTrendlineScalesOnMeasurementsOnly(t *testing.T) {
	nan := math.NaN()
	// The measured values are the extremes, so the ends must be ▁ and █ and
	// the drawn-through middle must sit strictly between them.
	got := []rune(trendline([]float64{0, nan, nan, nan, 100}))
	if got[0] != '▁' || got[len(got)-1] != '█' {
		t.Errorf("measured ends = %q, want ▁ … █", string(got))
	}
	for _, r := range got[1 : len(got)-1] {
		if r == '▁' || r == '█' {
			t.Errorf("drawn-through cell reached an extreme: %q", string(got))
		}
	}
}

// The trace's width is load-bearing, not cosmetic: the generic table sizes the
// column from the widest cell, and a cell that is narrower shifts the columns
// after it. A space would be worse still — truncateDiagCell folds whitespace
// runs on its non-ASCII path, which every trace takes.
func TestTrendlineFixedWidthAndSpaceFree(t *testing.T) {
	nan := math.NaN()
	cases := [][]float64{
		{1, 2, 3, 4, 5},
		{nan, nan, 3, 4, 5},
		{1, nan, nan, nan, 5},
		{nan, nan, nan, nan, nan},
		{7, 7, 7, 7, 7},
	}
	for _, vals := range cases {
		got := trendline(vals)
		if n := len([]rune(got)); n != len(vals) {
			t.Errorf("trendline(%v) is %d runes wide, want %d", vals, n, len(vals))
		}
		if strings.ContainsAny(got, " \t\n") {
			t.Errorf("trendline(%v) = %q contains whitespace", vals, got)
		}
	}
}
