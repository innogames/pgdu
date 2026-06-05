package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

	if s.level == levelWAL && (s.extPrompt == nil || !s.extPrompt.blocking) &&
		(s.walSummary != nil || s.walSummaryErr != nil) {
		summary := m.renderWALSummary(s)
		b.WriteString(summary)
		b.WriteString("\n")
		contentHeight -= strings.Count(summary, "\n") + 1
	}

	if s.level == levelWALRecords && (s.extPrompt == nil || !s.extPrompt.blocking) &&
		len(s.walRecTypeStats) > 0 {
		stats := m.renderWALRecTypeStats(s)
		b.WriteString(stats)
		b.WriteString("\n")
		contentHeight -= strings.Count(stats, "\n") + 1
	}

	if s.level == levelStatements && (s.extPrompt == nil || !s.extPrompt.blocking) {
		hdr := m.renderStatementsHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
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
	case m.showInfo && s.level == levelHeapPages:
		b.WriteString(m.renderHeapPagesInfo(contentHeight))
	case m.showInfo && s.level == levelHeapTuples:
		b.WriteString(m.renderHeapTuplesInfo(contentHeight))
	case m.showInfo && s.level == levelIndexPages:
		b.WriteString(m.renderIndexPagesInfo(contentHeight))
	case m.showInfo && s.level == levelIndexTuples:
		b.WriteString(m.renderIndexTuplesInfo(contentHeight))
	case m.showInfo && s.level == levelWAL:
		b.WriteString(m.renderWALInfo(contentHeight))
	case m.showInfo && s.level == levelWALRecords:
		b.WriteString(m.renderWALRecordsInfo(contentHeight))
	case m.showInfo && s.level == levelWALBlocks:
		b.WriteString(m.renderWALBlocksInfo(contentHeight))
	case m.showInfo && (s.level == levelStatements || s.level == levelStatementDetail):
		b.WriteString(m.renderStatementsInfo(contentHeight))
	case s.extPrompt != nil && s.extPrompt.blocking:
		b.WriteString(m.renderExtPrompt(s, contentHeight))
	case s.loading || !s.loaded:
		fmt.Fprintf(&b, "  %s loading %s…\n", m.spinner.View(), s.title)
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case s.err != nil:
		b.WriteString(styleErr.Render("  error: "+s.err.Error()) + "\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case len(s.items) == 0 && s.level != levelDescribe && s.level != levelDiagnosticResult &&
		s.level != levelStatements && s.level != levelStatementDetail:
		// levelDescribe never populates items — it renders from s.describe.
		// levelDiagnosticResult with 0 items means the query returned no rows,
		// which is valid; fall through to the renderer which shows "(no rows)".
		// levelStatements (empty = no activity in the window yet) and
		// levelStatementDetail (renders from s.statDetail) are likewise valid
		// with no items.
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
		case levelHeapPages:
			b.WriteString(m.renderHeapPagesList(s, contentHeight))
		case levelHeapTuples:
			b.WriteString(m.renderHeapTuplesList(s, contentHeight))
		case levelTupleRow:
			b.WriteString(m.renderTupleRowList(s, contentHeight))
		case levelRelations:
			b.WriteString(m.renderRelationsList(s, contentHeight))
		case levelIndexPages:
			b.WriteString(m.renderIndexPagesList(s, contentHeight))
		case levelIndexTuples:
			b.WriteString(m.renderIndexTuplesList(s, contentHeight))
		case levelDescribe:
			b.WriteString(m.renderDescribe(s, contentHeight))
		case levelDiagnostics:
			b.WriteString(m.renderDiagnosticList(s, contentHeight))
		case levelDiagnosticResult:
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelWAL:
			b.WriteString(m.renderWALList(s, contentHeight))
		case levelWALRecords:
			b.WriteString(m.renderWALRecordsList(s, contentHeight))
		case levelWALBlocks:
			b.WriteString(m.renderWALBlocksList(s, contentHeight))
		case levelStatements:
			// The top-queries table is a generic diagnostic-style table.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelStatementDetail:
			b.WriteString(m.renderStatementDetail(s, contentHeight))
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
		// Page-inspector tables show a solid heap-only bar; the segmented
		// legend would mislead, so suppress it on that flow.
		if s.tool == toolPageInspect {
			return ""
		}
		return "  " + swatch(styleHeapSeg, "heap") + sep +
			swatch(styleIndexSeg, "index") + sep +
			swatch(styleToastSeg, "toast")
	case levelParts:
		return "  " + swatch(styleBar, "size") + sep +
			swatch(styleBloat, "bloat")
	case levelHeapPages:
		return "  " + swatch(styleHeapSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free") + sep +
			styleHeapHot.Render("H") + " " + styleMuted.Render("hot-updated") + sep +
			styleHeapToastTag.Render("T") + " " + styleMuted.Render("has-external")
	case levelHeapTuples:
		return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("normal") + sep +
			styleLPRedirect.Render("●") + " " + styleMuted.Render("redirect") + sep +
			styleLPDead.Render("●") + " " + styleMuted.Render("dead") + sep +
			styleLPUnused.Render("●") + " " + styleMuted.Render("unused")
	case levelRelations:
		return "  " + swatch(styleHeapSeg, "table") + sep +
			swatch(styleIndexSeg, "btree index") + sep +
			swatch(styleToastSeg, "toast")
	case levelIndexPages:
		return "  " + swatch(styleIndexSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free")
	case levelWAL, levelWALRecords:
		return "  " + swatch(styleBar, "record bytes") + sep +
			swatch(styleBarAlt, "FPI bytes (full-page images)")
	case levelWALBlocks:
		return "  " + swatch(styleBarAlt, "FPI bytes") + sep +
			styleMuted.Render("░ no full-page image")
	case levelIndexTuples:
		// Three kinds of bt_page_items rows the user will run into on a
		// modern leaf page: regular entries (pointing at a heap row, so
		// the decoded key resolves and ENTER drills); the high-key
		// pivot at the start of the page (a structural separator, not a
		// row); and posting-list tuples (PG 13+ dedup — one entry packs
		// many heap tids for the same key). The latter two have no
		// single heap row to project, so they show their raw hex data.
		return "  " + styleLPNormal.Render("●") + " " + styleMuted.Render("leaf entry → heap row") + sep +
			styleHeapToastTag.Render("pivot") + " " + styleMuted.Render("high-key separator") + sep +
			styleHeapHot.Render("posting") + " " + styleMuted.Render("packed heap-tid list")
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
	sortLabel := s.sort.label(s.sortDesc)
	if s.diagCols != nil && s.diagSortCol < len(s.diagCols) {
		// Generic diagnostic-table: show the active column name and direction
		// instead of the sortMode label (which is meaningless here).
		arrow := "↑"
		if s.sortDesc {
			arrow = "↓"
		}
		sortLabel = s.diagCols[s.diagSortCol].Name + arrow
	}
	parts := []string{
		"sort: " + sortLabel,
		positionLabel(s),
		"level: " + levelLabel(s.level),
	}
	if s.level == levelDiagnosticResult && s.diag != nil {
		parts = append(parts, "query: "+s.diag.Title)
	}
	if (s.level == levelParts || s.level == levelColumns) && s.table.Name != "" {
		parts = append(parts, "table: "+s.table.Name)
	}
	if bs := bloatScanLabel(s); bs != "" {
		parts = append(parts, bs)
	}
	if tl := heapPageTableLabel(s); tl != "" {
		parts = append(parts, tl)
	}
	if pw := heapPageWindowLabel(s); pw != "" {
		parts = append(parts, pw)
	}
	if wl := walStatusLabel(s); wl != "" {
		parts = append(parts, wl)
	}
	return strings.Join(parts, "  ·  ")
}

// walStatusLabel keeps the WAL context (resource manager, record LSN, window)
// on the status row where the summary header isn't shown — i.e. on the
// records and block-refs levels the breadcrumb gets long, so the rmgr and
// LSN window stay visible here.
func walStatusLabel(s *screen) string {
	switch s.level {
	case levelWALRecords:
		return "rmgr: " + s.walRmgr + "  ·  window: " + shortLSN(s.walStart) + "–" + shortLSN(s.walEnd)
	case levelWALBlocks:
		return "rmgr: " + s.walRmgr + "  ·  record: " + s.walRecLSN
	}
	return ""
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
		case levelHeapPages:
			parts = append(parts, sc.table.Name)
		case levelHeapTuples:
			parts = append(parts, fmt.Sprintf("page #%d", sc.heapPageBlkno))
		case levelTupleRow:
			if sc.toastChunkID != 0 {
				parts = append(parts, fmt.Sprintf("chunk %d", sc.toastChunkID))
			} else {
				parts = append(parts, "row "+sc.tupleCtid)
			}
		case levelRelations:
			parts = append(parts, sc.schema)
		case levelIndexPages:
			parts = append(parts, sc.index.Name)
		case levelIndexTuples:
			parts = append(parts, fmt.Sprintf("page #%d", sc.indexPageBlkno))
		case levelDiagnostics:
			parts = append(parts, "tools")
		case levelDiagnosticResult:
			if sc.diag != nil {
				parts = append(parts, sc.diag.Title)
			}
		case levelWAL:
			parts = append(parts, "wal")
		case levelWALRecords:
			parts = append(parts, sc.walRmgr)
		case levelWALBlocks:
			parts = append(parts, "rec "+shortLSN(sc.walRecLSN))
		case levelStatements:
			// The parent databases level already shows "queries" (the tool
			// name); show the chosen database here instead of repeating it.
			parts = append(parts, sc.db)
		case levelStatementDetail:
			if sc.statDetail != nil {
				parts = append(parts, fmt.Sprintf("query %d", sc.statDetail.QueryID))
			}
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

func (m *Model) renderList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	rowsH := height

	var b strings.Builder
	if s.level == levelTables {
		rowsH = max(height-1, 0)
		b.WriteString(renderTablesHeader(s, barW))
		b.WriteString("\n")
	}

	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		b.WriteString(renderRow(row{
			size: it.size, bloat: it.bloat, hasBloat: it.hasBloat, hasChildren: it.hasChildren, maxSize: maxSz,
			barW: barW,
			heap: it.heap, idx: it.idx, toast: it.toast,
			rows: it.rows, hasRows: it.hasRows,
			pages: it.pages, hasPages: it.hasPages,
			name: it.name, detail: it.detail, selected: vi == s.cursor,
		}))
		b.WriteString("\n")
	}
	// Pad to fixed height so help line stays put.
	for i := end - s.offset; i < rowsH; i++ {
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
	w := m.width - barReserve(s.level, s.tool)
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
// Tool is consulted on levels whose columns differ per tool — at the tables
// level, the page-inspector swaps the toast/index detail string for a pages
// column.
func barReserve(l level, tl tool) int {
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
		if tl == toolPageInspect {
			// Page-inspector tables: no bloat overlay and no toast/idx detail
			// string — instead a pages column sits next to rows.
			return colCursor + colBrackets + colSize +
				(rowsColW + colGutter) + (pagesColW + colGutter) +
				colMark + colName
		}
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
	case levelHeapPages:
		// cursor + bar(brackets) + flag + used + live/dead + dead% + page name
		return colCursor + colBrackets + heapPageFlagColW + colGutter +
			heapPageUsedColW + colGutter + heapPageLPColW + colGutter +
			heapPageDeadColW + colGutter + heapPageNameColW
	case levelHeapTuples:
		// cursor + dot + lp idx + flag word + len + xmin + xmax + ctid + slack
		const tupleReserve = 2 + 2 + 6 + 10 + 8 + 12 + 12 + 14 + 6
		return tupleReserve
	case levelTupleRow:
		// cursor + column-name col + value gutter. The renderer prints
		// name and (potentially long) value as plain text — no bar, so
		// the reserve is just the column-name budget.
		return colCursor + tupleRowNameColW + colGutter
	case levelRelations:
		// Mirrors the page-inspector tables reserve, plus a parent-name
		// budget for the muted "→ <table>" tail shown on index rows.
		return colCursor + colBrackets + colSize +
			(rowsColW + colGutter) + (pagesColW + colGutter) +
			colMark + colName + relParentColW
	case levelIndexPages:
		// cursor + bar(brackets) + type + level + used + items + free% + page name
		return colCursor + colBrackets + idxPageTypeColW + colGutter +
			idxPageLevelColW + colGutter + idxPageUsedColW + colGutter +
			idxPageItemsColW + colGutter + idxPageFreeColW + colGutter +
			idxPageNameColW
	case levelIndexTuples:
		// cursor + offset + len + nulls/vars flags + ctid + key preview
		const idxTupleReserve = 2 + 6 + 8 + 8 + 14 + 4
		return idxTupleReserve
	case levelDescribe:
		// Plain-text panel — no bar drawn, so no space needs reserving.
		return 0
	case levelWAL:
		// cursor + bar(brackets) + combined + record + fpi + count + mark + name
		return colCursor + colBrackets + walColCombined + colGutter +
			walColRecord + colGutter + walColFPI + colGutter +
			walColCount + colGutter + colMark + colName
	case levelWALRecords:
		// cursor + bar(brackets) + size + fpi + lsn + mark + name + description
		return colCursor + colBrackets + walRecSizeColW + colGutter +
			walRecFPIColW + colGutter + walRecLSNColW + colGutter +
			colMark + colName + colDetail
	case levelWALBlocks:
		// cursor + bar(brackets) + fpi + data + name + detail
		return colCursor + colBrackets + walBlkFPIColW + colGutter +
			walBlkDataColW + colGutter + colName + colDetail
	}
	return colCursor + colBrackets + colSize + colMark + colName
}

// Column widths shared by the heap-pages header and rows. Centralised here
// so the header columns and the row body never drift.
const (
	heapPageFlagColW = 1
	heapPageUsedColW = 10
	heapPageLPColW   = 12 // "###L ##R ##D"
	heapPageDeadColW = 7
	heapPageNameColW = 16
)

// Column widths for the heap-tuples header and rows. Same rationale.
const (
	tupleFlagColW = 9
	tupleLenColW  = 6
	tupleXidColW  = 10
	tupleCtidColW = 10
)

// Column widths shared by the index-pages header and rows.
const (
	idxPageTypeColW  = 4 // "leaf" / "intr" / "root" / "del"
	idxPageLevelColW = 5 // "L 12"
	idxPageUsedColW  = 10
	idxPageItemsColW = 12 // "###L ###D"
	idxPageFreeColW  = 7
	idxPageNameColW  = 16
)

// Column widths shared by the index-tuples header and rows.
const (
	idxTupleOffColW   = 5 // "#NNNN"
	idxTupleLenColW   = 6
	idxTupleFlagsColW = 8  // "N/V"
	idxTupleCtidColW  = 14 // "(blkno,off)" with room for big blocks
)

// Parent-name column on levelRelations: muted "→ <table>" tail on index
// rows so the user can correlate an index back to its table when sort
// interleaves the list.
const relParentColW = 24

// Column width for the tuple-row column-name slot. Wide enough for most
// SQL identifiers without truncation; the value column gets all the
// remaining horizontal space.
const tupleRowNameColW = 28

// truncateToWidth clips a rendered (ANSI-styled) line to at most width
// terminal cells. It must be ANSI-aware: the input contains escape sequences
// from styled cells and coloured bars, and a naive rune-based cut can sever the
// trailing reset of the last styled segment — leaving a style "open" that then
// bleeds into the start of the following lines (the cursor highlight smearing
// across rows). ansi.Truncate never breaks an escape sequence; the appended
// reset guarantees no style survives past the cut regardless of where it lands.
func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…") + "\x1b[0m"
}
