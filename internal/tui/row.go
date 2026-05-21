package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// row is the shared visual primitive for every list level. It renders a
// fixed-width size bar with an optional bloat overlay, followed by the
// human-readable size, the row's name (highlighted when selected), and a
// trailing detail string.
type row struct {
	size        int64
	bloat       int64 // 0 when unknown or none
	hasBloat    bool  // true once bloat has been measured for this row
	hasChildren bool  // true when this row can be drilled into
	maxSize     int64 // largest sibling, used to scale the bar
	barW        int   // bar width in cells; chosen by the caller from terminal width
	name        string
	detail      string
	selected    bool

	// Optional heap/index/toast breakdown — when any are non-zero, the bar is
	// drawn as three coloured segments instead of one solid block. Used by the
	// tables level so the composition of each table is visible at a glance.
	heap, idx, toast int64
}

// barWidthMin is the fallback bar width when the terminal is too narrow for
// the dynamic calculation to give us anything sensible.
const barWidthMin = 20

func renderRow(r row) string {
	w := r.barW
	if w < barWidthMin {
		w = barWidthMin
	}
	var bar string
	if r.heap > 0 || r.idx > 0 || r.toast > 0 {
		bar = renderSegmentedBar(r.heap, r.idx, r.toast, r.maxSize, w)
	} else {
		bar = renderBar(r.size, r.bloat, r.maxSize, w)
	}
	sizeStr := humanize.Bytes(r.size)
	name := r.name
	if r.selected {
		name = styleSelected.Render(name)
	}
	detail := ""
	if r.detail != "" {
		detail = "  " + styleMuted.Render(r.detail)
	}
	cursor := "  "
	if r.selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	bloatStr := ""
	if r.hasBloat {
		pct := 0
		if r.size > 0 {
			pct = int(float64(r.bloat) * 100.0 / float64(r.size))
		}
		bloatStr = styleMuted.Render(padRight(fmt.Sprintf("(%d%% bloat)", pct), 12)) + "  "
	}
	childMark := "  "
	if r.hasChildren {
		childMark = styleMuted.Render("+ ")
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + bloatStr + childMark + name + detail
}

// renderBar paints a fixed-width bar where the live portion is colorBar and
// the bloated portion is colorBloat at the tail. If size==0 we just emit an
// empty bar so layout stays stable.
func renderBar(size, bloat, max int64, width int) string {
	if max <= 0 {
		max = 1
	}
	filled := int(float64(width) * float64(size) / float64(max))
	if filled > width {
		filled = width
	}
	var bloatChars int
	if bloat > 0 && size > 0 {
		bloatChars = int(float64(filled) * float64(bloat) / float64(size))
		if bloatChars > filled {
			bloatChars = filled
		}
	}
	live := filled - bloatChars
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(styleBar.Render(strings.Repeat("▇", live)))
	b.WriteString(styleBloat.Render(strings.Repeat("▇", bloatChars)))
	b.WriteString(styleMuted.Render(strings.Repeat("░", width-filled)))
	b.WriteString("]")
	return b.String()
}

// renderSegmentedBar paints a single bar split into three coloured segments
// (heap / index / toast). Each segment's width is proportional to its bytes
// over `max`; any width left over after the three segments — i.e. the
// difference between pg_total_relation_size and (heap+idx+toast), which is
// FSM/VM/toast-index overhead — is shown as muted "empty" cells.
func renderSegmentedBar(heap, idx, toast, max int64, width int) string {
	if max <= 0 {
		max = 1
	}
	h := int(float64(width) * float64(heap) / float64(max))
	i := int(float64(width) * float64(idx) / float64(max))
	t := int(float64(width) * float64(toast) / float64(max))
	if h < 0 {
		h = 0
	}
	if i < 0 {
		i = 0
	}
	if t < 0 {
		t = 0
	}
	if h+i+t > width {
		// Rounding can in principle push us over by 1–2 cells; trim toast last
		// since it's typically the largest segment and absorbs the loss best.
		over := h + i + t - width
		t -= over
		if t < 0 {
			t = 0
		}
	}
	used := h + i + t
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(styleHeapSeg.Render(strings.Repeat("▇", h)))
	b.WriteString(styleIndexSeg.Render(strings.Repeat("▇", i)))
	b.WriteString(styleToastSeg.Render(strings.Repeat("▇", t)))
	b.WriteString(styleMuted.Render(strings.Repeat("░", width-used)))
	b.WriteString("]")
	return b.String()
}

// renderBufferBar paints a three-segment occupancy bar: this-db (colorBar),
// other-dbs (colorAccent), free (muted dots). Segments are sized
// proportionally and any width loss from truncation is absorbed by the free
// tail so the bar stays exactly `width` cells wide.
func renderBufferBar(thisDB, otherDB, total int64, width int) string {
	if total <= 0 {
		total = 1
	}
	thisChars := int(float64(width) * float64(thisDB) / float64(total))
	otherChars := int(float64(width) * float64(otherDB) / float64(total))
	if thisChars < 0 {
		thisChars = 0
	}
	if otherChars < 0 {
		otherChars = 0
	}
	if thisChars+otherChars > width {
		otherChars = width - thisChars
		if otherChars < 0 {
			otherChars = 0
			thisChars = width
		}
	}
	freeChars := width - thisChars - otherChars
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(styleBar.Render(strings.Repeat("▇", thisChars)))
	b.WriteString(styleBarAlt.Render(strings.Repeat("▇", otherChars)))
	b.WriteString(styleMuted.Render(strings.Repeat("░", freeChars)))
	b.WriteString("]")
	return b.String()
}

func padRight(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

// Column widths for the buffer-tables view; kept here so the header and rows
// stay aligned.
const (
	bufColBuffered = 11 // "1023.99 MB"
	bufColTotal    = 11
	bufColCached   = 8 // "100.0%"
	bufColHit      = 8
)

// renderBufferList draws the shared-buffer occupancy view as a column table:
// bar | buffered bytes | total table size | cached % | hit % | table name.
// The bar visualises BufferedBytes scaled to the largest sibling so the eye
// still gets a quick "which table dominates the cache" read.
func (m *Model) renderBufferList(s *screen, height int) string {
	var max int64
	for _, it := range s.items {
		if it.size > max {
			max = it.size
		}
	}

	// Header consumes one line; clamp the row budget to what's left.
	rowsH := height - 1
	if rowsH < 0 {
		rowsH = 0
	}
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if rowsH > 0 && s.cursor >= s.offset+rowsH {
		s.offset = s.cursor - rowsH + 1
	}
	end := s.offset + rowsH
	if end > len(s.items) {
		end = len(s.items)
	}

	barW := m.barWidth(s)

	var b strings.Builder
	b.WriteString(renderBufferHeader(s.sort, barW))
	b.WriteString("\n")
	for i := s.offset; i < end; i++ {
		it := s.items[i]
		st, _ := it.data.(pg.TableBufferStat)
		b.WriteString(renderBufferRow(it, st, max, barW, i == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderBufferHeader(sort sortMode, barW int) string {
	mark := func(label string, active bool, arrow string) string {
		if active {
			return label + arrow
		}
		return label
	}
	bufLabel := mark("buffered", sort == sortBySize, "↓")
	hitLabel := mark("hit", sort == sortByHitRatio, "↑")
	nameLabel := mark("table", sort == sortByName, "↑")
	// Pad: cursor (2) + bar slot (barW+2) + "  " then columns.
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(bufLabel, bufColBuffered) + "  " +
		padRight("total", bufColTotal) + "  " +
		padRight("cached", bufColCached) + "  " +
		padRight(hitLabel, bufColHit) + "  " +
		nameLabel
	return styleMuted.Render(line)
}

func renderBufferRow(it item, st pg.TableBufferStat, maxSize int64, barW int, selected bool) string {
	bar := renderBar(it.size, 0, maxSize, barW)
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	bufStr := humanize.Bytes(st.BufferedBytes)
	totStr := humanize.Bytes(st.TotalBytes)
	cachedStr := "—"
	if st.TotalBytes > 0 {
		cachedStr = fmt.Sprintf("%.1f%%", float64(st.BufferedBytes)/float64(st.TotalBytes)*100)
	}
	hitStr := "—"
	if hr := st.HitRatio(); hr >= 0 {
		hitStr = fmt.Sprintf("%.1f%%", hr*100)
	}
	return cursor + bar + "  " +
		padRight(bufStr, bufColBuffered) + "  " +
		padRight(totStr, bufColTotal) + "  " +
		padRight(cachedStr, bufColCached) + "  " +
		padRight(hitStr, bufColHit) + "  " +
		name
}
