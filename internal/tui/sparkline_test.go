package tui

import "testing"

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
