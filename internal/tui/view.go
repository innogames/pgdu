package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	s := m.top()

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	contentHeight := max(
		// header + blank + help
		m.height-4, 3)

	var rankByOID map[uint32]int
	if s.level == levelBufferTables && (s.bufferSummary != nil || s.bufferSummaryErr != nil) {
		var summary string
		summary, rankByOID = m.renderBufferSummary(s)
		b.WriteString(summary)
		b.WriteString("\n")
		contentHeight -= strings.Count(summary, "\n") + 1
	}

	// Non-blocking prompts (hints) render above the list and consume one
	// line of the content area. Blocking prompts take over the whole
	// content area in the switch below.
	if s.extPrompt != nil && !s.extPrompt.blocking {
		b.WriteString(m.renderExtHint(s))
		b.WriteString("\n")
		contentHeight--
	}

	if banner := m.renderReindexBanner(s); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
		contentHeight--
	}

	if line := m.renderFilterLine(s); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
		contentHeight--
	}

	// Reserve a line for the colour legend (rendered after the list, before
	// the help row) on levels whose bars carry more than one colour.
	legend := renderLegend(s)
	if legend != "" {
		contentHeight--
	}

	switch {
	case m.showInfo && s.level == levelBufferTables:
		b.WriteString(m.renderBufferInfo(contentHeight))
	case s.extPrompt != nil && s.extPrompt.blocking:
		b.WriteString(m.renderExtPrompt(s, contentHeight))
	case s.loading || !s.loaded:
		b.WriteString(fmt.Sprintf("  %s loading %s…\n", m.spinner.View(), s.title))
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case s.err != nil:
		b.WriteString(styleErr.Render("  error: "+s.err.Error()) + "\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case len(s.items) == 0:
		b.WriteString("  (no items)\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	default:
		switch s.level {
		case levelTools:
			b.WriteString(m.renderToolPicker(s, contentHeight))
		case levelBufferTables:
			b.WriteString(m.renderBufferList(s, contentHeight, rankByOID))
		default:
			b.WriteString(m.renderList(s, contentHeight))
		}
	}

	if legend != "" {
		b.WriteString(legend)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleHelp.Render(m.help.View(m.keys)))
	return b.String()
}

// renderLegend returns a one-line colour legend for the current level so
// the user can decode the bar colours without guessing. Returns "" on
// levels whose bars are monochrome (no legend needed).
func renderLegend(s *screen) string {
	swatch := func(style lipgloss.Style, label string) string {
		return style.Render("▇") + " " + styleMuted.Render(label)
	}
	sep := styleMuted.Render("  ·  ")
	switch s.level {
	case levelTables:
		return "  " + swatch(styleHeapSeg, "heap") + sep +
			swatch(styleIndexSeg, "index") + sep +
			swatch(styleToastSeg, "toast")
	case levelParts:
		return "  " + swatch(styleBar, "size") + sep +
			swatch(styleBloat, "bloat")
	}
	return ""
}

func (m *Model) renderHeader() string {
	s := m.top()
	mode := m.bloatBadge()
	left := styleHeader.Render(" pgdu ") + " " + styleMuted.Render(m.target) + " " + mode
	crumbs := m.breadcrumb()
	return left + "    " + crumbs + "\n" + styleMuted.Render(strings.Repeat("─", maxInt(m.width-1, 1))) + "\n" +
		"  " + m.renderStatus(s)
}

// renderStatus is the one-line status row under the header: sort mode,
// cursor position (e.g. "12/438"), current level, and a bloat-scan
// progress indicator on the parts level.
func (m *Model) renderStatus(s *screen) string {
	parts := []string{
		"sort: " + s.sort.label(s.sortDesc),
		positionLabel(s),
		"level: " + levelLabel(s.level),
	}
	if bs := bloatScanLabel(s); bs != "" {
		parts = append(parts, bs)
	}
	return strings.Join(parts, "  ·  ")
}

func (m *Model) bloatBadge() string {
	// Bloat is only meaningful on the disk tool; suppress the badge elsewhere
	// to keep the header clean.
	top := m.top()
	if top.level == levelTools || top.tool != toolDisk {
		return ""
	}
	if !m.fetchBloat {
		return styleMuted.Render("[bloat off]")
	}
	return styleBadge.Render("[bloat on]")
}

func (m *Model) breadcrumb() string {
	parts := []string{"server"}
	for _, sc := range m.stack {
		switch sc.level {
		case levelTools:
		case levelDatabases:
			parts = append(parts, sc.tool.Name())
		case levelSchemas:
			parts = append(parts, sc.db)
		case levelTables, levelBufferTables:
			parts = append(parts, sc.schema)
		case levelParts:
			parts = append(parts, sc.table.Name)
		case levelColumns:
			parts = append(parts, "heap")
		}
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		if i == len(parts)-1 {
			out[i] = styleCrumbActive.Render(p)
		} else {
			out[i] = styleBreadcrumb.Render(p)
		}
	}
	return strings.Join(out, styleBreadcrumb.Render(" ▸ "))
}

func (m *Model) renderToolPicker(s *screen, height int) string {
	vis := s.visibleIndexes()
	var b strings.Builder
	for vi, idx := range vis {
		it := s.items[idx]
		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = styleSelected.Render("▶ ")
			name = styleSelected.Render(name)
		}
		childMark := "  "
		if it.hasChildren {
			childMark = styleMuted.Render("+ ")
		}
		b.WriteString(cursor)
		b.WriteString(childMark)
		b.WriteString(padRight(name, 20))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render(it.detail))
		b.WriteString("\n")
	}
	for i := len(vis); i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderFilterLine draws the single-line filter affordance above the list.
// While focused it shows the live input with a trailing caret; once blurred
// but non-empty it shows the committed query plus a hint for how to clear
// or re-edit. Returns "" when there's nothing to draw (no filter, no focus).
func (m *Model) renderFilterLine(s *screen) string {
	if s.filter == "" && !s.filterFocused {
		return ""
	}
	matches := fmt.Sprintf("(%d/%d matches)", s.visibleLen(), len(s.items))
	if s.filterFocused {
		caret := styleSelected.Render("▏")
		return "  " + styleSelected.Render("/") + s.filter + caret + "  " + styleMuted.Render(matches)
	}
	hint := styleMuted.Render(matches+" — press ") +
		styleBadge.Render("/") + styleMuted.Render(" to edit, ") +
		styleBadge.Render("esc") + styleMuted.Render(" to clear")
	return "  " + styleMuted.Render("filter: ") + s.filter + "  " + hint
}

// summaryLabelWidth is the width of the label column ("server memory" /
// "shared_buffers") at the head of each summary row. Set to max(len) of
// the two labels so the bars' opening brackets line up.
const summaryLabelWidth = 14

// summaryBarMax caps the summary bar width on very wide terminals so a
// 4k-cell window doesn't stretch the bar into ASCII art at the expense of
// the stats line's readability.
const summaryBarMax = 200

// renderBufferInfo draws a static explainer for the server-memory and
// shared_buffers bars, sized to fill `height` lines so the help row stays
// pinned to the bottom. Shown when the user toggles `?` on
// levelBufferTables.
func (m *Model) renderBufferInfo(height int) string {
	sw := func(style lipgloss.Style) string { return style.Render("▇") }
	var b strings.Builder
	mu := styleMuted.Render
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Bar reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" server memory ") + "  " +
		mu("the whole host's RAM (MemTotal) — the superset") + "\n")
	b.WriteString("    " + sw(styleBar) + "  " + mu("sb used      pages of shared_buffers actively caching data") + "\n")
	b.WriteString("    " + sw(styleSBFree) + "  " + mu("sb free      empty pages reserved by Postgres but not yet used") + "\n")
	b.WriteString("    " + sw(styleOtherUsed) + "  " + mu("other        memory used outside shared_buffers (other procs, kernel)") + "\n")
	b.WriteString("    " + sw(styleCache) + "  " + mu("cache        reclaimable kernel buffers + page cache (≈ MemAvailable − MemFree)") + "\n")
	b.WriteString("    " + mu("░  free         truly unallocated memory (MemFree)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" shared_buffers ") + "  " +
		mu("the Postgres-only subset of server memory — a slice of the bar above") + "\n")
	b.WriteString("    " + sw(styleBar) + "  " + mu("this db      buffered pages belonging to the current database") + "\n")
	b.WriteString("    " + sw(styleBarAlt) + "  " + mu("other dbs    buffered pages from other databases (and shared catalogs)") + "\n")
	b.WriteString("    " + mu("░  free         empty pages within shared_buffers") + "\n\n")

	b.WriteString("  " + mu("The top 10 tables by BufferedBytes each get a distinct palette hue;") + "\n")
	b.WriteString("  " + mu("their row bar matches the slice on the shared_buffers bar above.") + "\n")
	b.WriteString("  " + mu("Tables ranked 11+ use the default bar colour.") + "\n")

	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// summaryBarWidth picks the bar width for the two stacked summary bars.
// Wider than the per-row bars by design — there's nothing to align against
// once the stats text moved onto its own line.
func (m *Model) summaryBarWidth() int {
	// "  " indent + label + "  [" prefix + "]" suffix + 2 cells slack.
	reserve := 2 + summaryLabelWidth + 3 + 1 + 2
	w := m.width - reserve
	if w < barWidthMin {
		return barWidthMin
	}
	if w > summaryBarMax {
		return summaryBarMax
	}
	return w
}

// renderBufferSummary draws the multi-line summary block: an optional
// server-memory bar with stats, then the shared_buffers bar with stats.
// The biggest tables are painted as slices on the shared_buffers bar from
// bufferSlicePalette; the returned rankByOID map ranks every buffered
// table so list rows below can pick the same palette colour by rank.
func (m *Model) renderBufferSummary(s *screen) (string, map[uint32]int) {
	if s.bufferSummaryErr != nil {
		return "  " + styleMuted.Render("shared buffers: ") +
			styleErr.Render(s.bufferSummaryErr.Error()), nil
	}
	sum := s.bufferSummary
	if sum == nil || sum.TotalBytes <= 0 {
		return "  " + styleMuted.Render("shared_buffers: unavailable"), nil
	}

	barW := m.summaryBarWidth()
	lines := make([]string, 0, 4)

	// Server-memory bar (suppressed when we have no host RAM info, or
	// when MemAvailable / MemFree are unavailable — without both we can't
	// split cache out from truly-free memory).
	if sum.ServerMemBytes > 0 && sum.ServerMemAvailableBytes > 0 && sum.ServerMemFreeBytes > 0 {
		sbUsed := sum.ThisDBBytes + sum.OtherDBBytes
		sbFree := sum.FreeBytes()
		sbTotal := sum.TotalBytes
		// "Other used" is non-shared_buffers memory that isn't reclaimable
		// (= total - available - SB). The cache (= reclaimable buffers +
		// page cache) is what `free -w` calls "buff/cache"; we approximate
		// it as MemAvailable - MemFree, which is close enough for a bar.
		// Clamp all derived values to >=0 in case of rounding races.
		otherUsed := sum.ServerMemBytes - sum.ServerMemAvailableBytes - sbTotal
		if otherUsed < 0 {
			otherUsed = 0
		}
		cache := sum.ServerMemAvailableBytes - sum.ServerMemFreeBytes
		if cache < 0 {
			cache = 0
		}
		bar := renderServerMemBar(sbUsed, sbFree, otherUsed, cache, sum.ServerMemBytes, barW)
		muted := styleMuted.Render
		sw := func(style lipgloss.Style) string { return style.Render("▇") + " " }
		stats := muted(fmt.Sprintf("shared buffer %s (", humanize.Bytes(sbTotal))) +
			sw(styleBar) + muted(fmt.Sprintf("used %s / ", humanize.Bytes(sbUsed))) +
			sw(styleSBFree) + muted(fmt.Sprintf("free %s)  ·  ", humanize.Bytes(sbFree))) +
			sw(styleOtherUsed) + muted(fmt.Sprintf("other %s  ·  ", humanize.Bytes(otherUsed))) +
			sw(styleCache) + muted(fmt.Sprintf("cache %s  ·  ", humanize.Bytes(cache))) +
			muted(fmt.Sprintf("░ free %s  ·  total %s",
				humanize.Bytes(sum.ServerMemFreeBytes),
				humanize.Bytes(sum.ServerMemBytes)))
		lines = append(lines, summaryRow("server memory", bar))
		lines = append(lines, summaryStats(stats))
	}

	// Shared-buffers bar. Slice count is dynamic — fit as many distinct
	// tables as we have palette colours for, dropping the trailing ones
	// whose proportion would round to a sub-cell slice (invisible).
	slices := topBufferSlices(s.items, sum.TotalBytes, barW)
	var sliceTotal int64
	for _, sl := range slices {
		sliceTotal += sl.bytes
	}
	remainder := sum.ThisDBBytes - sliceTotal
	if remainder < 0 {
		remainder = 0
	}
	sbBar := renderBufferBar(slices, remainder, sum.OtherDBBytes, sum.TotalBytes, barW)
	usedPct := float64(sum.ThisDBBytes+sum.OtherDBBytes) * 100 / float64(sum.TotalBytes)
	usedStr := percentStyle(usedPct).Render(fmt.Sprintf("%.1f%% used", usedPct))
	muted := styleMuted.Render
	sw := func(style lipgloss.Style) string { return style.Render("▇") + " " }
	sbStats := usedStr + muted("  ·  ") +
		sw(styleBar) + muted(fmt.Sprintf("this db %s  ·  ", humanize.Bytes(sum.ThisDBBytes))) +
		sw(styleBarAlt) + muted(fmt.Sprintf("other %s  ·  ", humanize.Bytes(sum.OtherDBBytes))) +
		muted(fmt.Sprintf("░ free %s  ·  total %s",
			humanize.Bytes(sum.FreeBytes()),
			humanize.Bytes(sum.TotalBytes)))
	lines = append(lines, summaryRow("shared_buffers", sbBar))
	lines = append(lines, summaryStats(sbStats))

	return strings.Join(lines, "\n"), rankBuffersByOID(s.items)
}

// summaryRow is one "  <label>  [bar]" line. label is padded so multiple
// rows' opening brackets line up.
func summaryRow(label, bar string) string {
	return "  " + styleMuted.Render(padRight(label, summaryLabelWidth)) + "  " + bar
}

// summaryStats is a stats line sitting under a summary bar. It's indented
// to align with the bar's content (after the opening bracket) so the eye
// can pair stats to the bar above.
func summaryStats(stats string) string {
	indent := strings.Repeat(" ", 2+summaryLabelWidth+3)
	return indent + stats
}

// topBufferSlices picks the biggest items in the screen by BufferedBytes
// and returns them as bufferSlice values coloured from bufferSlicePalette.
// The cap is the palette size, further trimmed to drop trailing entries
// whose proportion would round below 1 cell on the bar (invisible). The
// selection is by absolute size — independent of the current sort — so
// the bar always shows the biggest cache users.
func topBufferSlices(items []item, total int64, width int) []bufferSlice {
	type pair struct {
		oid   uint32
		name  string
		bytes int64
	}
	pairs := make([]pair, 0, len(items))
	for _, it := range items {
		st, ok := it.data.(pg.TableBufferStat)
		if !ok || st.BufferedBytes <= 0 {
			continue
		}
		pairs = append(pairs, pair{oid: st.OID, name: st.Schema + "." + st.Name, bytes: st.BufferedBytes})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].bytes > pairs[j].bytes })
	if cap := len(bufferSlicePalette); len(pairs) > cap {
		pairs = pairs[:cap]
	}
	if total > 0 && width > 0 {
		minBytes := int64(float64(total) / float64(width))
		for len(pairs) > 0 && pairs[len(pairs)-1].bytes < minBytes {
			pairs = pairs[:len(pairs)-1]
		}
	}
	out := make([]bufferSlice, len(pairs))
	for i, p := range pairs {
		out[i] = bufferSlice{oid: p.oid, name: p.name, bytes: p.bytes, style: bufferSliceStyle(i)}
	}
	return out
}

// rankBuffersByOID assigns every buffer-stat item a rank (0 = biggest
// BufferedBytes) so row bars in the list can pick a palette colour by
// rank. The same rank is used for the top-N slices on the summary bar,
// so a row's colour matches its slice exactly for tables in the top-N.
func rankBuffersByOID(items []item) map[uint32]int {
	type entry struct {
		oid   uint32
		bytes int64
	}
	es := make([]entry, 0, len(items))
	for _, it := range items {
		st, ok := it.data.(pg.TableBufferStat)
		if !ok || st.BufferedBytes <= 0 {
			continue
		}
		es = append(es, entry{oid: st.OID, bytes: st.BufferedBytes})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].bytes > es[j].bytes })
	out := make(map[uint32]int, len(es))
	for i, e := range es {
		out[e.oid] = i
	}
	return out
}

func (m *Model) renderList(s *screen, height int) string {
	vis := s.visibleIndexes()
	max := maxItemSize(s.items, vis)
	s.offset, _ = viewportRange(s.cursor, s.offset, height, len(vis))
	end := min(s.offset+height, len(vis))
	barW := m.barWidth(s)
	var b strings.Builder
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		b.WriteString(renderRow(row{
			size: it.size, bloat: it.bloat, hasBloat: it.hasBloat, hasChildren: it.hasChildren, maxSize: max,
			barW: barW,
			heap: it.heap, idx: it.idx, toast: it.toast,
			rows: it.rows, hasRows: it.hasRows,
			name: it.name, detail: it.detail, selected: vi == s.cursor,
		}))
		b.WriteString("\n")
	}
	// Pad to fixed height so help line stays put.
	for i := end - s.offset; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderReindexBanner renders the one-line status for the per-row REINDEX
// flow on the parts level: pending confirmation, in-flight progress, or the
// last failure. Returns "" when there's nothing to show.
func (m *Model) renderReindexBanner(s *screen) string {
	if s.level != levelParts {
		return ""
	}
	switch {
	case s.reindexing != "":
		return "  " + styleMuted.Render(m.spinner.View()+" REINDEX INDEX CONCURRENTLY "+s.reindexing+"…")
	case s.pendingReindex != "":
		return "  " + styleSelected.Render("confirm: ") +
			styleMuted.Render("REINDEX INDEX CONCURRENTLY "+s.pendingReindex+" — press ") +
			styleBadge.Render("y") +
			styleMuted.Render(" to run, ") +
			styleBadge.Render("n") +
			styleMuted.Render(" (or any other key) to cancel")
	case s.reindexErr != nil:
		return "  " + styleErr.Render("reindex failed: "+s.reindexErr.Error())
	}
	return ""
}

// renderExtHint renders a single muted line above the list, suggesting an
// optional extension. Pressing `i` triggers the install.
func (m *Model) renderExtHint(s *screen) string {
	p := s.extPrompt
	if p == nil {
		return ""
	}
	if s.installing {
		return "  " + styleMuted.Render(m.spinner.View()+" installing "+p.name+"…")
	}
	if p.err != nil {
		return "  " + styleErr.Render("install "+p.name+" failed: "+p.err.Error()) + "  " +
			styleMuted.Render("(press i to retry)")
	}
	if !p.installable {
		return "  " + styleMuted.Render("hint: "+p.reason+" — "+p.name+" not available on this server")
	}
	return "  " + styleMuted.Render("hint: "+p.reason+" — press ") +
		styleBadge.Render("i") + styleMuted.Render(" to install "+p.name)
}

// renderExtPrompt renders the blocking "install this extension?" screen.
// Called instead of the list when extPrompt.blocking is set.
func (m *Model) renderExtPrompt(s *screen, height int) string {
	p := s.extPrompt
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Extension required") + "\n\n")
	b.WriteString("  " + p.reason + "\n")
	b.WriteString("  " + styleMuted.Render("missing: "+p.name+" in database "+p.db) + "\n\n")
	switch {
	case s.installing:
		b.WriteString("  " + m.spinner.View() + " installing " + p.name + "…\n")
	case p.err != nil:
		b.WriteString("  " + styleErr.Render("install failed: "+p.err.Error()) + "\n")
		b.WriteString("  " + styleMuted.Render("press ") + styleBadge.Render("i") +
			styleMuted.Render(" to retry, or ") + styleBadge.Render("←") +
			styleMuted.Render(" to back out") + "\n")
	case p.installable:
		b.WriteString("  press " + styleBadge.Render("i") +
			" to run " + styleMuted.Render("CREATE EXTENSION "+p.name) + "\n")
		b.WriteString("  " + styleMuted.Render("(requires database-owner or superuser privileges)") + "\n")
	default:
		b.WriteString("  " + styleErr.Render(p.name+" is not available on this server — ask the DBA to install it") + "\n")
	}
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// barWidth picks the size-bar width for the current screen. We grow it with
// the terminal so wide windows aren't dominated by trailing whitespace, but
// cap it so very wide terminals don't turn the bar into ASCII art at the
// expense of the actual numeric columns.
func (m *Model) barWidth(s *screen) int {
	w := m.width - barReserve(s.level)
	if w < barWidthMin {
		return barWidthMin
	}
	if w > barWidthMax {
		return barWidthMax
	}
	return w
}

// Column-layout constants shared by barReserve and the renderers. Kept in one
// place so column widths and the inter-column gutter can't drift apart.
const (
	colGutter   = 2  // whitespace between adjacent columns (also brackets reserve)
	colCursor   = 2  // "▶ " selection marker
	colBrackets = 2  // "[" and "]" around the bar
	colSize     = 12 // humanize.Bytes value + slack
	colBloat    = 14 // " (NN% bloat)  "
	colMark     = 2  // "+ " child indicator
	colName     = 28 // typical relname budget
	colDetail   = 30 // generic detail-string budget
)

// barReserve is how many non-bar cells each level needs reserved for cursor,
// numeric columns and name/detail. Each level declares its own so new tools
// with different column shapes don't all have to share one global guess.
func barReserve(l level) int {
	switch l {
	case levelBufferTables:
		// cursor + bar(brackets) + buffered + total + cached + hit + name
		return colCursor + colBrackets +
			bufColBuffered + colGutter +
			bufColTotal + colGutter +
			bufColCached + colGutter +
			bufColHit + colGutter +
			colName
	case levelTables:
		// cursor + bar(brackets) + size + rows + bloat + mark + name + detail
		return colCursor + colBrackets + colSize + (rowsColW + colGutter) + colBloat + colMark + colName + colDetail
	case levelParts:
		// Parts detail strings can be long ("heap · 12k dead (5%) · vac 3h ago
		// · ana 2d ago" or "index · primary · unique · btree"), so bump the
		// detail budget so the bar shrinks earlier on narrow terminals
		// instead of pushing the detail off the right.
		const partsDetail = 50
		return colCursor + colBrackets + colSize + colBloat + colMark + colName + partsDetail
	case levelColumns, levelDatabases, levelSchemas:
		return colCursor + colBrackets + colSize + colMark + colName + colDetail
	}
	return colCursor + colBrackets + colSize + colMark + colName
}
