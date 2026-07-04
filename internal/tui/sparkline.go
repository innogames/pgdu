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
