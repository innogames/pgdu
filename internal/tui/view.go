package tui

import (
	"fmt"
	"strings"
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

	if s.level == levelWALRelations && (s.extPrompt == nil || !s.extPrompt.blocking) &&
		s.loaded && s.err == nil && len(s.items) > 0 {
		hdr := m.renderWALRelationsHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	if s.level == levelStatements && (s.extPrompt == nil || !s.extPrompt.blocking) {
		hdr := m.renderStatementsHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	if s.level == levelActivity && s.loaded && s.actErr == nil {
		hdr := m.renderActivityHeader(s)
		b.WriteString(hdr)
		b.WriteString("\n")
		contentHeight -= strings.Count(hdr, "\n") + 1
	}

	// B-tree page/tuple views carry an index-context banner (key columns, and on
	// the page list the metapage summary). Suppressed under a blocking
	// extension prompt, which takes over the whole content area.
	if (s.level == levelIndexPages || s.level == levelIndexTuples) &&
		(s.extPrompt == nil || !s.extPrompt.blocking) {
		if banner := m.renderIndexKeyBanner(s); banner != "" {
			b.WriteString(banner)
			b.WriteString("\n")
			contentHeight -= strings.Count(banner, "\n") + 1
		}
	}

	// Non-blocking prompts (hints) render above the list and consume one
	// line of the content area. Blocking prompts take over the whole
	// content area in the switch below. levelDescribe is excluded: it renders
	// the pg_buffercache install affordance inside its cache-footprint section.
	if s.extPrompt != nil && !s.extPrompt.blocking && s.level != levelDescribe {
		b.WriteString(m.renderExtHint(s))
		b.WriteString("\n")
		contentHeight--
	}

	if banner := m.renderReindexBanner(s); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
		contentHeight--
	}

	if banner := m.renderVacuumBanner(s); banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
		contentHeight--
	}

	if s.level == levelActivity {
		if banner := activityPendingBanner(s); banner != "" {
			b.WriteString(banner)
			b.WriteString("\n")
			contentHeight--
		}
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
	case m.showActColumnConfig && s.level == levelActivity:
		b.WriteString(m.renderActColumnConfig(s, contentHeight))
	case m.showColumnConfig && s.level == levelStatements:
		b.WriteString(m.renderColumnConfig(s, contentHeight))
	case m.showDiagQuery && s.level == levelDiagnosticResult && s.diag != nil:
		b.WriteString(m.renderDiagQuery(s, contentHeight))
	case m.showInfo && m.hasInfoOverlay(s):
		// The ? reference overlays scroll through scrollWindow — some (e.g. the
		// maintenance reference) are taller than the screen. renderInfoOverlay
		// dispatches to the per-level body; m.infoOffset is the scroll position.
		b.WriteString(scrollWindow(m.renderInfoOverlay(s, contentHeight), &m.infoOffset, contentHeight))
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
		s.level != levelStatements && s.level != levelStatementDetail &&
		s.level != levelStatementResult && s.level != levelSnapshots &&
		s.level != levelBufferDetail && s.level != levelMaintenance && s.level != levelSettings &&
		s.level != levelActivity:
		// levelDescribe never populates items — it renders from s.describe.
		// levelDiagnosticResult and levelStatementResult with 0 items mean the
		// query returned no rows, which is valid; fall through to the renderer
		// which shows "(no rows)". levelStatements (empty = no activity in the
		// window yet) and levelStatementDetail (renders from s.statDetail) are
		// likewise valid with no items.
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
		case levelBufferDetail:
			b.WriteString(m.renderBufferDetail(s, contentHeight))
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
		case levelWALRelations:
			b.WriteString(m.renderWALRelationsList(s, contentHeight))
		case levelWALRelBlocks:
			// Relation block-refs reuse the per-record block-refs renderer —
			// the payload is the same pg.WALBlockRef.
			b.WriteString(m.renderWALBlocksList(s, contentHeight))
		case levelStatements:
			// The top-queries table is a generic diagnostic-style table.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelStatementDetail:
			b.WriteString(m.renderStatementDetail(s, contentHeight))
		case levelStatementSamples:
			b.WriteString(m.renderStatementSamples(s, contentHeight))
		case levelStatementResult:
			// Executed-query rows reuse the generic result-table renderer.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		case levelSnapshots:
			b.WriteString(m.renderStatementSnapshots(s, contentHeight))
		case levelParts:
			b.WriteString(m.renderPartsLevel(s, contentHeight))
		case levelMaintenance:
			b.WriteString(m.renderMaintenance(s, contentHeight))
		case levelSettings:
			b.WriteString(m.renderSettingsList(s, contentHeight))
		case levelActivity:
			// The activity table is a generic diagnostic-style table — same
			// renderer as levelStatements and levelDiagnosticResult.
			b.WriteString(m.renderDiagResult(s, contentHeight))
		default:
			b.WriteString(m.renderList(s, contentHeight))
		}
	}

	if legend != "" {
		b.WriteString(legend)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	// Trim the footer to the current screen's bindings (disabled keys are
	// skipped by the help component), matching what handleKey will dispatch.
	m.keys.applyContext(m.top())
	b.WriteString(styleHelp.Render(m.help.View(m.keys)))
	return b.String()
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
	if m.notice != "" {
		parts = append(parts, styleSelected.Render(m.notice))
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
	case levelWALRelations:
		return "window: " + shortLSN(s.walStart) + "–" + shortLSN(s.walEnd)
	case levelWALRelBlocks:
		return "relation: " + s.walRelLabel + "  ·  window: " + shortLSN(s.walStart) + "–" + shortLSN(s.walEnd)
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
		case levelWALRelations:
			parts = append(parts, "by relation")
		case levelWALRelBlocks:
			parts = append(parts, sc.walRelLabel)
		case levelActivity:
			parts = append(parts, "activity")
		case levelStatements:
			// The parent databases level already shows "queries" (the tool
			// name); show the chosen database here instead of repeating it.
			parts = append(parts, sc.db)
		case levelStatementDetail:
			if sc.statDetail != nil {
				parts = append(parts, fmt.Sprintf("query %d", sc.statDetail.QueryID))
			}
		case levelStatementResult:
			parts = append(parts, "result")
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
			name: it.name, detail: it.detail, detailStyled: it.detailStyled, selected: vi == s.cursor,
		}))
		b.WriteString("\n")
	}
	// Pad to fixed height so help line stays put.
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
