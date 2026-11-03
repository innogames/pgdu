package tui

import (
	"math"
	"strings"
)

// sparkChars is the 8-step block ramp a sparkline cell scales into.
var sparkChars = [...]rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline renders vals as one glyph per value across exactly width cells,
// scaled 0..max(vals, scaleMax). Only the most recent width values are shown;
// shorter histories are left-padded with spaces so the trace fills in from
// the right as data accumulates. Pass scaleMax > 0 to share a scale across
// rows (e.g. class shares all scaled to the same max); 0 self-scales.
func sparkline(vals []float64, width int, scaleMax float64) string {
	if width <= 0 {
		return ""
	}
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	mx := scaleMax
	for _, v := range vals {
		if v > mx {
			mx = v
		}
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(vals)))
	for _, v := range vals {
		if mx <= 0 || v <= 0 {
			b.WriteRune(sparkChars[0])
			continue
		}
		// Ceil so any nonzero value lands at least one step up only when it
		// crosses a bucket boundary: v==mx maps to the top glyph, v==mx/8 to
		// the bottom one.
		idx := int(math.Ceil(v/mx*float64(len(sparkChars)))) - 1
		if idx >= len(sparkChars) {
			idx = len(sparkChars) - 1
		}
		if idx < 0 {
			idx = 0
		}
		b.WriteRune(sparkChars[idx])
	}
	return b.String()
}

const (
	// trendGap marks a cell whose value is unknown. It is deliberately NOT a
	// space: truncateDiagCell takes its slow path for any non-ASCII cell — and
	// a trace always is one — where it folds whitespace runs to a single
	// space, which would shift every glyph after a gap leftwards.
	trendGap = '·'
	// trendFlatSpan is the relative span below which a series counts as level.
	// Min-max scaling has no absolute meaning, so a range this narrow is
	// autovacuum jitter being magnified into a staircase; drawing it flat says
	// "nothing happened" instead of inventing a shape.
	trendFlatSpan = 0.01
)

// interpolateTrend fills the unknown cells that lie BETWEEN two measurements
// with a straight line between them, and leaves the ones outside that range
// alone.
//
// This is a drawing decision, not a claim about the data: the series keeps its
// NaNs, the long-run rate is still computed from measured endpoints only, and
// the min/max the trace is scaled into still comes from measured values. What
// it buys is the shape. Captures are sparse and clustered in practice — six
// over four days — so an honest dot-per-gap trace spends most of its width
// saying "pgdu was not run", which is a fact about pgdu rather than about the
// table, and buries the climb the column exists to show.
//
// A straight line is the right guess here: cells are spaced by elapsed time
// under both layouts, so linear in cells is linear in time, and a table's size
// between two measurements really does move more or less steadily. Cells
// before the first measurement stay unknown, because there is nothing on the
// left to draw a line from.
func interpolateTrend(vals []float64) []float64 {
	out := append([]float64(nil), vals...)
	prev := -1
	for i := range out {
		if math.IsNaN(out[i]) {
			continue
		}
		if prev >= 0 && i-prev > 1 {
			step := (out[i] - out[prev]) / float64(i-prev)
			base := out[prev]
			for j := prev + 1; j < i; j++ {
				out[j] = base + step*float64(j-prev)
			}
		}
		prev = i
	}
	return out
}

// trendline renders vals as one glyph per value, scaled between the series'
// OWN min and max rather than from zero.
//
// That is the difference from sparkline, and it is the point: a table that
// grew from 28.10 GB to 28.13 GB is a flat line on a zero-based scale, and the
// shape of how it got there — steadily, in one step, or after a rewrite — is
// the only thing this column has to say. Magnitude is left to the byte columns
// beside it; what a trace carries is form.
//
// Unknown cells between two measurements are drawn through (see
// interpolateTrend); ones before the first measurement draw trendGap. The
// result is always exactly len(vals) runes wide and contains no spaces, so the
// table's width pass and its cell truncation cannot move it.
func trendline(vals []float64) string {
	if len(vals) == 0 {
		return ""
	}
	// Scale on what was measured. Interpolated cells lie between their
	// neighbours by construction, so they could not widen the range anyway —
	// taking it from the measurements is what keeps that true by design
	// rather than by accident.
	mn, mx := math.Inf(1), math.Inf(-1)
	known := 0
	for _, v := range vals {
		if math.IsNaN(v) {
			continue
		}
		known++
		mn = math.Min(mn, v)
		mx = math.Max(mx, v)
	}
	if known == 0 {
		return strings.Repeat(string(trendGap), len(vals))
	}
	vals = interpolateTrend(vals)

	// A level series has no range to scale into, so the glyph it draws is a
	// free choice — and it must be the quietest one. Most relations in a
	// database do not change, so this is the common case, and drawing it at
	// the top of the cell paints a wall of █ down the column for exactly the
	// rows with nothing to say. A line along the floor lets the eye skip them
	// and leaves the loud glyphs to the rows that earned them.
	span := mx - mn
	flat := span <= 0 || (mx > 0 && span/math.Abs(mx) < trendFlatSpan)

	var b strings.Builder
	b.Grow(len(vals) * 3)
	top := len(sparkChars) - 1
	for _, v := range vals {
		switch {
		case math.IsNaN(v):
			b.WriteRune(trendGap)
		case flat:
			b.WriteRune(sparkChars[0])
		default:
			// Floor, not ceil: the minimum must land on the bottom glyph so a
			// trace reads from ▁ to █ across its own range. (sparkline ceils
			// because its floor is a real zero, not the series minimum.)
			idx := max(min(int((v-mn)/span*float64(top+1)), top), 0)
			b.WriteRune(sparkChars[idx])
		}
	}
	return b.String()
}
