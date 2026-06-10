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

	// rows is the estimated row count; only rendered when hasRows is true.
	rows    int64
	hasRows bool

	// pages is the heap page count; only rendered when hasPages is true.
	pages    int64
	hasPages bool
}

// barWidthMin is the fallback bar width when the terminal is too narrow for
// the dynamic calculation to give us anything sensible. barWidthMax caps the
// bar on very wide terminals so the bar doesn't turn into ASCII art at the
// expense of the numeric columns.
const (
	barWidthMin = 20
	barWidthMax = 80
)

func renderRow(r row) string {
	w := max(r.barW, barWidthMin)
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
	rowsStr := ""
	if r.hasRows {
		rowsStr = styleMuted.Render(padRight("~"+formatRows(r.rows), rowsColW)) + "  "
	}
	pagesStr := ""
	if r.hasPages {
		pagesStr = styleMuted.Render(padRight(formatRows(r.pages)+"p", pagesColW)) + "  "
	}
	childMark := "  "
	if r.hasChildren {
		childMark = styleMuted.Render("+ ")
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + rowsStr + pagesStr + bloatStr + childMark + name + detail
}

// rowsColW is the padded width of the ~rows column on the tables level.
// formatRows produces at most "999.9M"/"999.9G" (6 chars) + the "~" prefix,
// so 7 fits every realistic value with one space of slack.
const rowsColW = 7

// pagesColW matches rowsColW: formatRows output (max 6 chars) + a "p" suffix.
const pagesColW = 7

// barSegment is one coloured run inside a bar. cells must be >= 0; paintBar
// will clip if the segments together exceed the bar width.
type barSegment struct {
	cells int
	style lipgloss.Style
}

// paintBar emits "[<segments…><muted padding>]" totalling exactly width cells
// (excluding the brackets). Segments are rendered in order; any width left
// over after them is filled with muted dots so the layout stays stable for
// rows whose data doesn't reach the bar's max.
func paintBar(width int, segs ...barSegment) string {
	var b strings.Builder
	b.WriteString("[")
	used := 0
	for _, s := range segs {
		c := max(s.cells, 0)
		if used+c > width {
			c = width - used
		}
		b.WriteString(s.style.Render(strings.Repeat("▇", c)))
		used += c
	}
	if used < width {
		b.WriteString(styleMuted.Render(strings.Repeat("░", width-used)))
	}
	b.WriteString("]")
	return b.String()
}

// renderBar paints a fixed-width bar where the live portion is colorBar and
// the bloated portion is colorBloat at the tail. If size==0 we just emit an
// empty bar so layout stays stable.
func renderBar(size, bloat, max int64, width int) string {
	if max <= 0 {
		max = 1
	}
	filled := min(int(float64(width)*float64(size)/float64(max)), width)
	var bloatChars int
	if bloat > 0 && size > 0 {
		bloatChars = min(int(float64(filled)*float64(bloat)/float64(size)), filled)
	}
	live := filled - bloatChars
	return paintBar(width,
		barSegment{cells: live, style: styleBar},
		barSegment{cells: bloatChars, style: styleBloat},
	)
}

// renderSolidBar paints a single-segment bar in the caller's chosen style.
// Used by the buffer-tables row renderer so each row can carry the palette
// colour of its matching slice in the summary bar above.
func renderSolidBar(size, max int64, width int, style lipgloss.Style) string {
	if max <= 0 {
		max = 1
	}
	filled := min(int(float64(width)*float64(size)/float64(max)), width)
	return paintBar(width, barSegment{cells: filled, style: style})
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
	h := max0(int(float64(width) * float64(heap) / float64(max)))
	i := max0(int(float64(width) * float64(idx) / float64(max)))
	t := max0(int(float64(width) * float64(toast) / float64(max)))
	if h+i+t > width {
		// Rounding can in principle push us over by 1–2 cells; trim toast last
		// since it's typically the largest segment and absorbs the loss best.
		t = max0(width - h - i)
	}
	return paintBar(width,
		barSegment{cells: h, style: styleHeapSeg},
		barSegment{cells: i, style: styleIndexSeg},
		barSegment{cells: t, style: styleToastSeg},
	)
}

// renderHeapPageBar paints one heap page as live | dead | free, scaled to a
// fixed BLCKSZ so the bar reads as "how packed is this page?" rather than
// "how packed compared to the biggest sibling?". The dead segment uses
// styleBloat for semantic parity with the parts view's bloat overlay.
func renderHeapPageBar(live, dead int64, width int) string {
	const blockSize int64 = 8192
	bytesToCells := func(b int64) int {
		if b <= 0 {
			return 0
		}
		c := int(float64(width) * float64(b) / float64(blockSize))
		if c < 0 {
			return 0
		}
		if c > width {
			return width
		}
		return c
	}
	l := bytesToCells(live)
	d := bytesToCells(dead)
	if l+d > width {
		// Rounding can push us one cell over; trim the dead segment last
		// since live is the dominant visual.
		d = max0(width - l)
	}
	return paintBar(width,
		barSegment{cells: l, style: styleHeapSeg},
		barSegment{cells: d, style: styleBloat},
	)
}

// bufferSlice is one named contribution to the this-db portion of the
// shared_buffers bar — typically the top-N tables by buffered bytes. OID
// lets the row renderer match each list row back to its slice colour.
type bufferSlice struct {
	oid   uint32
	name  string
	bytes int64
	style lipgloss.Style
}

// renderBufferBar paints a multi-segment occupancy bar: per-table slices
// (palette colours) at the head, "this-db remainder" (colorBar) for the
// rest of the current database, "other-dbs" (colorAccent), then free
// (muted dots). When slices is empty/nil the result is the original
// three-segment bar. Segments are sized proportionally and any width loss
// from rounding is absorbed by the free tail so the bar stays exactly
// `width` cells wide.
func renderBufferBar(slices []bufferSlice, thisDBRemainder, otherDB, total int64, width int) string {
	if total <= 0 {
		total = 1
	}
	bytesToCells := func(b int64) int {
		return max0(int(float64(width) * float64(b) / float64(total)))
	}
	segs := make([]barSegment, 0, len(slices)+2)
	used := 0
	for i, sl := range slices {
		c := bytesToCells(sl.bytes)
		if used+c > width {
			c = width - used
		}
		segs = append(segs, barSegment{cells: c, style: bufferSliceStyle(i)})
		used += c
	}
	rem := bytesToCells(thisDBRemainder)
	if used+rem > width {
		rem = width - used
	}
	segs = append(segs, barSegment{cells: rem, style: styleBar})
	used += rem
	other := bytesToCells(otherDB)
	if used+other > width {
		other = width - used
	}
	segs = append(segs, barSegment{cells: other, style: styleBarAlt})
	return paintBar(width, segs...)
}

// renderServerMemBar paints the host-RAM occupancy: shared_buffers used,
// shared_buffers free (empty PG pages), other-used (kernel/apps),
// reclaimable cache, and then the truly-free tail. Segments are sized
// proportionally over `total`; rounding loss falls into the free tail so
// the bar stays exactly `width` cells wide.
func renderServerMemBar(sbUsed, sbFree, otherUsed, cache, total int64, width int) string {
	if total <= 0 {
		total = 1
	}
	bytesToCells := func(b int64) int {
		return max0(int(float64(width) * float64(b) / float64(total)))
	}
	used := 0
	clamp := func(c int) int {
		if used+c > width {
			c = width - used
		}
		if c < 0 {
			c = 0
		}
		used += c
		return c
	}
	a := clamp(bytesToCells(sbUsed))
	b := clamp(bytesToCells(sbFree))
	c := clamp(bytesToCells(otherUsed))
	d := clamp(bytesToCells(cache))
	return paintBar(width,
		barSegment{cells: a, style: styleBar},
		barSegment{cells: b, style: styleSBFree},
		barSegment{cells: c, style: styleOtherUsed},
		barSegment{cells: d, style: styleCache},
	)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func padRight(s string, n int) string {
	w := displayWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// displayWidth is lipgloss.Width with a fast path for pure printable-ASCII
// strings, whose display width is simply their byte length. This avoids
// lipgloss's grapheme-cluster segmentation, which dominated CPU while scrolling
// the (almost always ASCII) top-queries table. Any byte outside 0x20–0x7e —
// including the 0x1b that begins an ANSI escape, and any UTF-8 lead byte — falls
// back to the correct (slower) lipgloss path.
func displayWidth(s string) int {
	if w, ok := asciiWidth(s); ok {
		return w
	}
	return lipgloss.Width(s)
}

// asciiWidth returns (len(s), true) when s is entirely printable ASCII, else
// (0, false). Restricting to 0x20–0x7e keeps control bytes and ANSI escapes
// (0x1b) on the slow path, where their zero/variable display width is handled
// correctly.
func asciiWidth(s string) (int, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return 0, false
		}
	}
	return len(s), true
}

// Column widths for the buffer-tables view; kept here so the header and rows
// stay aligned.
const (
	bufColBuffered = 11 // "1023.99 MB"
	bufColTotal    = 11
	bufColCached   = 8 // "100.0%"
	bufColHit      = 8
	bufColDirty    = 10 // dirty bytes — usually small or "0 B"
)

// renderBufferList draws the shared-buffer occupancy view as a column table:
// bar | buffered bytes | total table size | cached % | hit % | table name.
// The bar visualises BufferedBytes scaled to the largest sibling so the eye
// still gets a quick "which table dominates the cache" read. rankByOID
// maps each row to its rank among all buffered tables (0 = biggest); the
// row picks its bar colour from bufferSlicePalette by that rank, cycling
// on overflow, so the brightest hues match the top slices on the summary
// bar above.
func (m *Model) renderBufferList(s *screen, height int, rankByOID map[uint32]int) string {
	vis := s.visibleIndexes()
	max := maxItemSize(s.items, vis)

	// Header consumes one line; clamp the row budget to what's left.
	rowsH := height - 1
	if rowsH < 0 {
		rowsH = 0
	}
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	barW := m.barWidth(s)

	// Grade the dirty column relative to the biggest dirty footprint visible, so
	// the heaviest write-pressure tables read red at a glance (costStyleRelative).
	var maxDirty int64
	for _, vi := range vis {
		if st, ok := s.items[vi].data.(pg.TableBufferStat); ok && st.DirtyBytes > maxDirty {
			maxDirty = st.DirtyBytes
		}
	}

	var b strings.Builder
	b.WriteString(renderBufferHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		st, _ := it.data.(pg.TableBufferStat)
		barStyle := styleBar
		if idx, ok := rankByOID[st.OID]; ok && idx < len(bufferSlicePalette) {
			barStyle = bufferSliceStyle(idx)
		}
		b.WriteString(renderBufferRow(it, st, max, maxDirty, barW, vi == s.cursor, barStyle))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderBufferHeader(sort sortMode, sortDesc bool, barW int) string {
	arrow := "↑"
	if sortDesc {
		arrow = "↓"
	}
	mark := func(label string, active bool) string {
		if active {
			return label + arrow
		}
		return label
	}
	bufLabel := mark("buffered", sort == sortBySize)
	totalLabel := mark("total", sort == sortByTotal)
	cachedLabel := mark("cached", sort == sortByCached)
	hitLabel := mark("hit", sort == sortByHitRatio)
	nameLabel := mark("table", sort == sortByName)
	dirtyLabel := mark("dirty", sort == sortByDirty)
	// Pad: cursor (2) + bar slot (barW+2) + "  " then columns.
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(bufLabel, bufColBuffered) + "  " +
		padRight(totalLabel, bufColTotal) + "  " +
		padRight(cachedLabel, bufColCached) + "  " +
		padRight(hitLabel, bufColHit) + "  " +
		padRight(dirtyLabel, bufColDirty) + "  " +
		nameLabel
	return styleMuted.Render(line)
}

func renderTablesHeader(s *screen, barW int) string {
	arrow := "↑"
	if s.sortDesc {
		arrow = "↓"
	}
	mark := func(label string, active bool) string {
		if active {
			return label + arrow
		}
		return label
	}

	anyBloat := false
	for _, it := range s.items {
		if it.hasBloat {
			anyBloat = true
			break
		}
	}

	// Indent: cursor (2) + bar slot (barW+2) + "  " gap — mirrors renderRow's
	// prefix so each label sits directly above its value column.
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(mark("size", s.sort == sortBySize), 10) + "  " +
		padRight(mark("~rows", s.sort == sortByRows), rowsColW) + "  "

	if s.tool == toolPageInspect {
		line += padRight("pages", pagesColW) + "  "
	} else if anyBloat {
		line += padRight("bloat", 12) + "  "
	}
	// 2-cell placeholder for the childMark ("+ " / "  ") before the name.
	line += "  " + mark("table", s.sort == sortByName)

	return styleMuted.Render(line)
}

func renderBufferRow(it item, st pg.TableBufferStat, maxSize, maxDirty int64, barW int, selected bool, barStyle lipgloss.Style) string {
	bar := renderSolidBar(it.size, maxSize, barW, barStyle)
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
		pct := float64(st.BufferedBytes) / float64(st.TotalBytes) * 100
		cachedStr = percentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct))
	}
	hitStr := "—"
	if hr := st.HitRatio(); hr >= 0 {
		pct := hr * 100
		hitStr = percentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct))
	}
	dirtyStr := styleMuted.Render("—")
	if st.DirtyBytes > 0 {
		dirtyStr = costStyleRelative(float64(st.DirtyBytes), float64(maxDirty)).Render(humanize.Bytes(st.DirtyBytes))
	}
	return cursor + bar + "  " +
		padRight(bufStr, bufColBuffered) + "  " +
		padRight(totStr, bufColTotal) + "  " +
		padRight(cachedStr, bufColCached) + "  " +
		padRight(hitStr, bufColHit) + "  " +
		padRight(dirtyStr, bufColDirty) + "  " +
		name
}
