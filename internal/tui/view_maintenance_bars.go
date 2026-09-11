package tui

import (
	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
)

// Bar helpers of the system overview. Every ratio on the screen is drawn with
// the one bar primitive (paintBar) at one width, so the bars read as a column
// the eye can run down; these helpers only decide the segments and the row
// shape around them.

const (
	// maintBarW is the bar width of the full-width sections and of the
	// stacked (narrow) layout; the paned layout narrows it via maintBarWidth.
	maintBarW = 20
	// barFigureW and barPctW are the columns for the "used / max" figure and
	// the percentage that follow a bar, so consecutive bar rows line up.
	barFigureW = 18
	barPctW    = 8
)

// maintBarWidth fits the shared bar width to a pane: the label column, the
// bar and a short figure must fit, but never below 8 cells.
func maintBarWidth(paneW int) int {
	return min(maintBarW, max(paneW-overviewLabelW-14, 8))
}

// gaugeBar paints one fill of ratio (clamped to [0,1]) in st. A non-zero
// ratio paints at least one cell, so a small but real value — 45 MB of swap
// in use — is not an empty bar.
func gaugeBar(ratio float64, st lipgloss.Style, width int) string {
	ratio = min(max(ratio, 0), 1)
	filled := int(float64(width) * ratio)
	if filled == 0 && ratio > 0 {
		filled = 1
	}
	return paintBar(width, barSegment{cells: filled, style: st})
}

// barPart is one named share of a composition bar.
type barPart struct {
	n     int64
	style lipgloss.Style
}

// shareBar paints parts proportionally over total; whatever they leave of
// total is the muted tail. Cells are dealt by proportionalCells, so every
// non-zero part (and the tail) keeps at least one cell, and parts that make
// up the whole total fill the bar exactly.
func shareBar(total int64, width int, parts ...barPart) string {
	if total <= 0 {
		return paintBar(width)
	}
	counts := make([]int, 0, len(parts)+1)
	var sum int64
	for _, p := range parts {
		n := max(p.n, 0)
		counts = append(counts, int(n))
		sum += n
	}
	counts = append(counts, int(max(total-sum, 0)))
	cells := proportionalCells(counts, width)
	segs := make([]barSegment, len(parts))
	for i, p := range parts {
		segs[i] = barSegment{cells: cells[i], style: p.style}
	}
	return paintBar(width, segs...)
}

// barCells lays out what follows a bar: the figure and the percentage in
// their columns. A figure wider than its column pushes the percentage right
// rather than gluing the two together.
func barCells(bar, figure, pct string) string {
	return barCellsW(bar, figure, pct, barFigureW)
}

// barCellsW is barCells with the figure column widened to figW, for a run of
// rows whose figures are longer than the default column.
func barCellsW(bar, figure, pct string, figW int) string {
	s := bar
	if figure != "" {
		s += "  " + padCol(figure, figW)
	}
	if pct != "" {
		if figure == "" {
			s += "  "
		}
		s += padCol(pct, barPctW)
	}
	return s
}

// padCol pads s to w cells, keeping at least a two-cell gap after it: a value
// that (nearly) fills its column overflows instead of touching the next one.
func padCol(s string, w int) string {
	if displayWidth(s) > w-2 {
		return s + "  "
	}
	return padRight(s, w)
}

// barRow is the overview row shape under every gauge: label, bar, figure,
// percentage, then a note that carries its own leading separator (as
// maintView.note does).
func barRow(label, bar, figure, pct, note string) string {
	return maintRow(label, barCells(bar, figure, pct)+note)
}

// barRowW is barRow with the figure column widened to figW; figureColW sizes
// it for a run of rows so their percentages line up.
func barRowW(label, bar, figure, pct, note string, figW int) string {
	return maintRow(label, barCellsW(bar, figure, pct, figW)+note)
}

// figureColW is the figure column for a run of bar rows: the default, or two
// cells more than the widest figure when one overflows it.
func figureColW(figures ...string) int {
	w := barFigureW
	for _, f := range figures {
		w = max(w, displayWidth(f)+2)
	}
	return w
}

// fill is the colour of a gauge's fill: the advice colour when a warning or
// critical finding fired for key — so the bar can never contradict the
// recommendation — and the plain bar colour otherwise. An informational note
// leaves the bar plain: it explains a healthy state rather than grading it.
func (v maintView) fill(key string) lipgloss.Style {
	if a := v.advice.Find(key); a != nil && a.Level > pg.AdviceInfo {
		return adviceStyle(a.Level)
	}
	return styleBar
}

// header renders a section title. When advice for any of keys fired at
// warning or critical level the title carries the recommendation panel's
// glyph for the worst of them, so a reader scrolling the page finds the
// sections with findings without reading every row. keys are the advice keys
// the section itself annotates, so the mark always points at a visible row.
func (v maintView) header(name string, keys ...string) string {
	h := "  " + styleHeader.Render(" "+name+" ")
	worst := pg.AdviceInfo
	for _, k := range keys {
		if a := v.advice.Find(k); a != nil && a.Level > worst {
			worst = a.Level
		}
	}
	if worst > pg.AdviceInfo {
		h += " " + adviceStyle(worst).Render(adviceGlyph(worst))
	}
	return h
}

// adviceGlyph is the one-character severity mark: ! critical, ~ warning,
// · informational.
func adviceGlyph(l pg.AdviceLevel) string {
	switch l {
	case pg.AdviceCrit:
		return "!"
	case pg.AdviceWarn:
		return "~"
	default:
		return "·"
	}
}

// dbName is the database the overview describes: the screen's, or the one
// the snapshot was taken in when pgdu was started without -d and the screen
// has none.
func (v maintView) dbName() string {
	if v.db != "" {
		return v.db
	}
	if v.info != nil {
		return v.info.Database
	}
	return ""
}

// inDB is " in <db>" for a known database and "" otherwise, so a sentence
// never ends in a dangling "in".
func inDB(db string) string {
	if db == "" {
		return ""
	}
	return " in " + db
}

// serverMemParts splits host RAM the way `free -w` does once shared_buffers
// is taken out: "other" is memory neither reclaimable nor PostgreSQL's
// (total − available − shared_buffers), "cache" the reclaimable part
// (available − free, what free calls buff/cache). Both clamp at zero against
// rounding races between the /proc reads.
func serverMemParts(total, available, free, sb int64) (otherUsed, cache int64) {
	return max(total-available-sb, 0), max(available-free, 0)
}
