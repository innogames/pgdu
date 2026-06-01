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
	case m.showInfo && s.level == levelHeapPages:
		b.WriteString(m.renderHeapPagesInfo(contentHeight))
	case m.showInfo && s.level == levelHeapTuples:
		b.WriteString(m.renderHeapTuplesInfo(contentHeight))
	case m.showInfo && s.level == levelIndexPages:
		b.WriteString(m.renderIndexPagesInfo(contentHeight))
	case m.showInfo && s.level == levelIndexTuples:
		b.WriteString(m.renderIndexTuplesInfo(contentHeight))
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
	case len(s.items) == 0 && s.level != levelDescribe:
		// levelDescribe never populates items — it renders from s.describe.
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
			swatch(styleIndexSeg, "btree index")
	case levelIndexPages:
		return "  " + swatch(styleIndexSeg, "live") + sep +
			swatch(styleBloat, "dead") + sep +
			styleMuted.Render("░ free")
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
	parts := []string{
		"sort: " + s.sort.label(s.sortDesc),
		positionLabel(s),
		"level: " + levelLabel(s.level),
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
		case levelHeapPages:
			parts = append(parts, sc.table.Name)
		case levelHeapTuples:
			parts = append(parts, fmt.Sprintf("page #%d", sc.heapPageBlkno))
		case levelTupleRow:
			parts = append(parts, "row "+sc.tupleCtid)
		case levelRelations:
			parts = append(parts, sc.schema)
		case levelIndexPages:
			parts = append(parts, sc.index.Name)
		case levelIndexTuples:
			parts = append(parts, fmt.Sprintf("page #%d", sc.indexPageBlkno))
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

// renderHeapPagesInfo draws a static explainer for the per-page bar segments
// and the flag column on the heap-pages view. Sized to fill `height` lines
// so the help row stays pinned to the bottom. Shown when the user toggles
// `?` on levelHeapPages.
func (m *Model) renderHeapPagesInfo(height int) string {
	sw := func(style lipgloss.Style) string { return style.Render("▇") }
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Page reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" page bar ") + "  " +
		mu("each row is one 8 KiB heap page — the bar shows how that page is packed") + "\n")
	b.WriteString("    " + sw(styleHeapSeg) + "  " + mu("live      bytes occupied by visible tuples (lp_flags = NORMAL)") + "\n")
	b.WriteString("    " + sw(styleBloat) + "  " + mu("dead      bytes occupied by tuples awaiting VACUUM (lp_flags = DEAD)") + "\n")
	b.WriteString("    " + mu("░  free      empty space between pd_lower and pd_upper inside the page") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" page flag ") + "  " +
		mu("one glyph per page summarising its tuple-mix state") + "\n")
	b.WriteString("    " + styleHeapHot.Render("H") + "  " +
		mu("hot-updated    at least one tuple was updated via the HOT path (same page, no index update)") + "\n")
	b.WriteString("    " + styleHeapToastTag.Render("T") + "  " +
		mu("has-external   at least one tuple has a TOAST pointer (a large value lives out-of-line)") + "\n")
	b.WriteString("    " + styleBloat.Render("!") + "  " +
		mu("more-dead-than-live  the page is mostly bloat — a candidate for VACUUM/VACUUM FULL/repack") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" lp_flags ") + "  " +
		mu("on the per-tuple drill, the coloured dot at the start of each row decodes lp_flags") + "\n")
	b.WriteString("    " + styleLPNormal.Render("●") + "  " + mu("NORMAL    a live tuple, fully formed") + "\n")
	b.WriteString("    " + styleLPRedirect.Render("●") + "  " + mu("REDIRECT  HOT chain hop — Enter jumps to the target lp on this page") + "\n")
	b.WriteString("    " + styleLPDead.Render("●") + "  " + mu("DEAD      reclaimable — VACUUM removes these (and their items)") + "\n")
	b.WriteString("    " + styleLPUnused.Render("●") + "  " + mu("UNUSED    line pointer is free for reuse") + "\n\n")

	b.WriteString("  " + mu("PgUp/PgDn slides the load window ("+fmt.Sprintf("%d", heapWindowDefault)+" pages per step).") + "\n")
	b.WriteString("  " + mu("Within a window, j/k or arrows move the cursor; Enter drills into one page.") + "\n")

	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderHeapTuplesInfo draws a static explainer for the per-tuple drill view:
// the meaning of each column (lp, lp_flags, len, xmin/xmax, ctid) and a
// decoded glossary of the infomask/infomask2 badges. Sized to fill `height`
// lines so the help row stays pinned to the bottom; shown when the user
// toggles `?` on levelHeapTuples.
func (m *Model) renderHeapTuplesInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Tuple reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per item-pointer in the page's LP array") + "\n")
	b.WriteString("    " + padRight("lp", 12) + mu("line-pointer index (1-based) — the offset into the page's ItemId array") + "\n")
	b.WriteString("    " + padRight("lp_flags", 12) +
		mu("slot state: ") + styleLPNormal.Render("NORMAL") + mu(" live · ") +
		styleLPRedirect.Render("REDIRECT") + mu(" HOT hop · ") +
		styleLPDead.Render("DEAD") + mu(" awaiting VACUUM · ") +
		styleLPUnused.Render("UNUSED") + mu(" reclaimed, reusable") + "\n")
	b.WriteString("    " + padRight("len", 12) + mu("lp_len — tuple length in bytes (header + nulls bitmap + data)") + "\n")
	b.WriteString("    " + padRight("xmin", 12) + mu("inserting transaction id (visible only to xacts after xmin commits)") + "\n")
	b.WriteString("    " + padRight("xmax", 12) + mu("deleting / locking xid; 0 means \"no xmax set\" — the tuple is still live") + "\n")
	b.WriteString("    " + padRight("ctid", 12) + mu("forward pointer: own (block,offset) for NORMAL · → #NNNN target lp for REDIRECT") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" infomask flags ") + "  " +
		mu("decoded bits of t_infomask / t_infomask2 — what's true about this tuple") + "\n")
	b.WriteString("    " + styleMuted.Render("[HASNULL]") + "      " +
		mu("at least one column is SQL NULL (a null bitmap follows the header)") + "\n")
	b.WriteString("    " + styleMuted.Render("[VARWIDTH]") + "     " +
		mu("at least one column is variable-width (text/bytea/numeric/…)") + "\n")
	b.WriteString("    " + styleHeapToastTag.Render("[HASEXTERNAL]") + "  " +
		mu("at least one column value lives out-of-line in the TOAST relation") + "\n")
	b.WriteString("    " + styleBadge.Render("[XMIN_CMT]") + "     " +
		mu("HEAP_XMIN_COMMITTED hint — xmin is known committed (fast-path visibility)") + "\n")
	b.WriteString("    " + styleMuted.Render("[XMIN_INV]") + "     " +
		mu("HEAP_XMIN_INVALID hint — xmin aborted, or xmin is frozen") + "\n")
	b.WriteString("    " + styleBadge.Render("[XMAX_CMT]") + "     " +
		mu("HEAP_XMAX_COMMITTED hint — xmax is known committed (tuple is dead)") + "\n")
	b.WriteString("    " + styleMuted.Render("[XMAX_INV]") + "     " +
		mu("HEAP_XMAX_INVALID — xmax aborted or never set; tuple is still live") + "\n")
	b.WriteString("    " + lipgloss.NewStyle().Foreground(colorAccent).Render("[XMAX_MULTI]") + "   " +
		mu("xmax holds a MultiXactId — multiple locks/updates rather than a single xid") + "\n")
	b.WriteString("    " + styleMuted.Render("[UPDATED]") + "      " +
		mu("HEAP_UPDATED — this tuple was UPDATEd (a newer version may exist via ctid)") + "\n")
	b.WriteString("    " + styleHeapHot.Render("[HOT]") + "          " +
		mu("HEAP_HOT_UPDATED — the next version is on the same page (no index update)") + "\n")
	b.WriteString("    " + styleHeapHot.Render("[HEAP_ONLY]") + "    " +
		mu("HEAP_ONLY_TUPLE — this version is reachable only via a HOT chain hop") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" expanded row ") + "  " +
		mu("the selected row expands to show internals not visible in the table") + "\n")
	b.WriteString("    " + padRight("data:", 12) + mu("first bytes of t_data in hex — header offsets are described by hoff/bits") + "\n")
	b.WriteString("    " + padRight("infomask", 12) + mu("raw infomask + infomask2 hex words and the null bitmap, when present") + "\n")
	b.WriteString("    " + padRight("hoff", 12) + mu("t_hoff — bytes from tuple start to user data (header + nulls bitmap, aligned)") + "\n")
	b.WriteString("    " + padRight("bits", 12) + mu("null bitmap: 1 = column has a value, 0 = SQL NULL (LSB = column 1)") + "\n")
	b.WriteString("    " + padRight("lp_off", 12) + mu("byte offset of the tuple from the start of the page") + "\n")

	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderIndexPagesInfo draws a static explainer for the per-page bar
// segments, page-type column and tree-level meaning on the B-tree page
// view. Sized to fill `height` lines so the help row stays pinned to the
// bottom. Shown when the user toggles `?` on levelIndexPages.
func (m *Model) renderIndexPagesInfo(height int) string {
	sw := func(style lipgloss.Style) string { return style.Render("▇") }
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("B-tree page reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" page bar ") + "  " +
		mu("each row is one 8 KiB index page — the bar shows how that page is packed") + "\n")
	b.WriteString("    " + sw(styleIndexSeg) + "  " + mu("live      bytes occupied by live index items (pointers + key data)") + "\n")
	b.WriteString("    " + sw(styleBloat) + "  " + mu("dead      bytes occupied by items marked LP_DEAD (reclaimable on next vacuum)") + "\n")
	b.WriteString("    " + mu("░  free      empty space between pd_lower and pd_upper inside the page") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" page type ") + "  " +
		mu("comes from bt_page_stats.type — what role this page plays in the tree") + "\n")
	b.WriteString("    " + padRight("leaf", 8) +
		mu("lowest level — items either point at heap rows (live) or are LP_DEAD") + "\n")
	b.WriteString("    " + padRight("intr", 8) +
		mu("internal — items are downlinks to child pages, not heap rows") + "\n")
	b.WriteString("    " + padRight("root", 8) +
		mu("the tree's root; in a small index this is also a leaf (root-only tree)") + "\n")
	b.WriteString("    " + padRight("del", 8) +
		mu("deleted page — emptied by VACUUM and waiting to be recycled") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per index page in the loaded window") + "\n")
	b.WriteString("    " + padRight("level", 8) +
		mu("btpo_level: depth from the leaves — L0 is a leaf, L1 sits one above, etc.") + "\n")
	b.WriteString("    " + padRight("used", 8) +
		mu("BLCKSZ − free_size, i.e. how much of the 8 KiB page is actually populated") + "\n")
	b.WriteString("    " + padRight("live/dead", 8) +
		mu("counts from bt_page_stats — live and LP_DEAD items on this page") + "\n")
	b.WriteString("    " + padRight("free", 8) +
		mu("free_size as a percent of pagesize; high values on leaf pages signal bloat") + "\n\n")

	b.WriteString("  " + mu("PgUp/PgDn slides the load window ("+fmt.Sprintf("%d", heapWindowDefault)+" pages per step).") + "\n")
	b.WriteString("  " + mu("Within a window, j/k or arrows move the cursor; Enter drills into one page's items.") + "\n")
	b.WriteString("  " + mu("Block 0 is the metapage — skipped here; it carries the root pointer, not a tree page.") + "\n")

	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderIndexTuplesInfo explains what the user is looking at on a B-tree
// leaf page: the three kinds of bt_page_items rows (regular leaf entry,
// pivot, posting list), why some can be drilled into and others can't,
// and what the columns mean. Sized to fill `height` lines so the help
// row stays pinned to the bottom; shown when the user toggles `?` on
// levelIndexTuples.
func (m *Model) renderIndexTuplesInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Index-tuple reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" tuple kinds ") + "  " +
		mu("the ctid column reveals which kind each row is") + "\n")
	b.WriteString("    " + styleLPNormal.Render("●") + " " + padRight("(blk,off)", 14) +
		mu("regular leaf entry: ctid is a real heap pointer, key is decoded from the heap,") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("and Enter drills into the per-column tuple-row view") + "\n")
	b.WriteString("    " + styleHeapToastTag.Render("pivot") + strings.Repeat(" ", 14-len("pivot")) + "  " +
		mu("structural separator: item #1 of every non-rightmost leaf page (the high key),") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("plus every entry on internal pages (downlinks). No heap row, so no decoded") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("key — only the raw key bytes; ENTER does nothing.") + "\n")
	b.WriteString("    " + styleHeapHot.Render("posting") + strings.Repeat(" ", 14-len("posting")) + "  " +
		mu("PG 13+ btree deduplication: one tuple packs many heap tids for the same key.") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("Look for big itemlens (40–272 bytes) — INDEX_ALT_TID_MASK is set") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per bt_page_items entry on the chosen page") + "\n")
	b.WriteString("    " + padRight("off", 8) +
		mu("itemoffset — 1-based slot index on the page (item #1 is the high key)") + "\n")
	b.WriteString("    " + padRight("len", 8) +
		mu("itemlen — bytes consumed by this entry; posting lists are much longer than singles") + "\n")
	b.WriteString("    " + padRight("flags", 8) +
		styleBadge.Render("N") + mu(" = has NULLs in the key  ·  ") +
		styleBadge.Render("V") + mu(" = has variable-width attributes") + "\n")
	b.WriteString("    " + padRight("ctid", 8) +
		mu("heap pointer (regular entries), or ") +
		styleHeapToastTag.Render("pivot") + mu(" / ") +
		styleHeapHot.Render("posting") + mu(" label for alt-tid kinds") + "\n")
	b.WriteString("    " + padRight("key", 8) +
		mu("decoded key from the heap when reachable, else dimmed hex of the raw key bytes") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" drilling ") + "  " +
		mu("which rows respond to Enter — and why others don't") + "\n")
	b.WriteString("    " + mu("Only leaf entries whose ctid still resolves to a live heap row drill in;") + "\n")
	b.WriteString("    " + mu("they're marked with the ") + styleMuted.Render("+ ") +
		mu("indicator. Pivot rows are structural, not data, so") + "\n")
	b.WriteString("    " + mu("there's nothing to show. Posting rows pack many heap tids — no single row") + "\n")
	b.WriteString("    " + mu("to land on. Entries whose heap tuple was vacuumed since the page snapshot") + "\n")
	b.WriteString("    " + mu("also can't drill (the heap projection returned NULL).") + "\n")

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
		otherUsed := max(sum.ServerMemBytes-sum.ServerMemAvailableBytes-sbTotal, 0)
		cache := max(sum.ServerMemAvailableBytes-sum.ServerMemFreeBytes, 0)
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
	remainder := max(sum.ThisDBBytes-sliceTotal, 0)
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
			pages: it.pages, hasPages: it.hasPages,
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
	}
	return colCursor + colBrackets + colSize + colMark + colName
}

// Column widths shared by the heap-pages header and rows. Centralised here
// so the header columns and the row body never drift.
const (
	heapPageFlagColW = 1
	heapPageUsedColW = 10
	heapPageLPColW   = 12 // "###L ###D"
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

// heapPageTableLabel reports the qualified relation name for the status line
// on the page-inspector levels. The breadcrumb already shows it, but having
// it on the status row keeps the relation identity visible while drilled
// deep (per-page, per-tuple, per-row) where the breadcrumb trail gets long.
func heapPageTableLabel(s *screen) string {
	switch s.level {
	case levelHeapPages, levelHeapTuples, levelTupleRow:
		return "table: " + s.table.Qualified()
	case levelIndexPages, levelIndexTuples:
		return "index: " + s.index.Qualified()
	}
	return ""
}

// heapPageWindowLabel reports the currently-loaded page window for the
// status line, e.g. "pages 0–1999 / 12345". Returns "" off-level or with no
// page data so the status row stays terse when there's nothing to show.
func heapPageWindowLabel(s *screen) string {
	if s.heapPageCount == 0 {
		return ""
	}
	if s.level != levelHeapPages && s.level != levelIndexPages {
		return ""
	}
	end := max(s.heapWindowStart+int32(len(s.items))-1, s.heapWindowStart)
	return fmt.Sprintf("pages %d–%d / %d", s.heapWindowStart, end, s.heapPageCount)
}

// renderHeapPagesList draws one row per heap page with a fixed-scale bar
// (BLCKSZ-relative live/dead/free), a flag column carrying V/F/H/T glyphs,
// and per-page numeric columns. The bar reads as "how packed is this page?"
// rather than "how big is it compared to siblings", which is what you want
// when scanning a heap for hotspots.
func (m *Model) renderHeapPagesList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	barW := m.barWidth(s)

	var b strings.Builder
	b.WriteString(renderHeapPagesHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		p, _ := it.data.(pg.HeapPageStat)
		b.WriteString(renderHeapPageRow(it, p, barW, vi == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderHeapPagesHeader(sort sortMode, sortDesc bool, barW int) string {
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
	// Header indent matches the row: cursor (2) + bar slot (barW+2) + "  "
	// before the flag column starts.
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight("!", heapPageFlagColW) + "  " +
		padRight(mark("used", sort == sortBySize), heapPageUsedColW) + "  " +
		padRight("live/dead", heapPageLPColW) + "  " +
		padRight(mark("dead%", sort == sortByDeadRatio), heapPageDeadColW) + "  " +
		mark("page", sort == sortByBlkno)
	return styleMuted.Render(line)
}

func renderHeapPageRow(it item, p pg.HeapPageStat, barW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	bar := renderHeapPageBar(p.LiveBytes, p.DeadBytes, barW)

	flag := " "
	switch {
	case p.DeadLP > p.LiveLP && p.LiveLP+p.DeadLP > 0:
		flag = styleBloat.Render("!")
	case p.HotUpdated > 0:
		flag = styleHeapHot.Render("H")
	case p.HasExternal > 0:
		flag = styleHeapToastTag.Render("T")
	}

	used := humanize.Bytes(it.size)
	lpStr := fmt.Sprintf("%3dL %3dD", p.LiveLP, p.DeadLP)
	deadPct := "—"
	if df := p.DeadFrac(); df >= 0 {
		deadPct = percentStyle(100 - df*100).Render(fmt.Sprintf("%.0f%%", df*100))
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	return cursor + bar + "  " +
		padRight(flag, heapPageFlagColW) + "  " +
		padRight(used, heapPageUsedColW) + "  " +
		padRight(lpStr, heapPageLPColW) + "  " +
		padRight(deadPct, heapPageDeadColW) + "  " +
		name
}

// renderHeapTuplesList draws one row per line-pointer. The selected row
// expands to three additional lines (data preview, infomask details,
// lp_off/raw_len) so the user can see decoded internals without a separate
// detail pane; other rows render only the headline.
func (m *Model) renderHeapTuplesList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	var b strings.Builder
	b.WriteString(renderHeapTuplesHeader(s.sort, s.sortDesc))
	b.WriteString("\n")
	lines := 0
	for vi := s.offset; vi < end && lines < rowsH; vi++ {
		it := s.items[vis[vi]]
		t, _ := it.data.(pg.HeapTuple)
		selected := vi == s.cursor
		b.WriteString(renderHeapTupleHeadline(t, selected))
		b.WriteString("\n")
		lines++
		if selected && lines+3 <= rowsH {
			for _, l := range renderHeapTupleExpand(t) {
				b.WriteString(l)
				b.WriteString("\n")
				lines++
			}
		}
	}
	for ; lines < rowsH; lines++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderHeapTuplesHeader(sort sortMode, sortDesc bool) string {
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
	// Indentation matches the row: cursor (2) + "#NNNN" idx col (5) + gap.
	// The "● " dot+space takes 2 cells before the flag-name column.
	line := "  " + padRight(mark("lp", sort == sortByLP), 5) + "  " +
		padRight("lp_flags", 2+tupleFlagColW) + "  " +
		padRight(mark("len", sort == sortBySize), tupleLenColW) + "  " +
		padRight("xmin", tupleXidColW) + "  " +
		padRight("xmax", tupleXidColW) + "  " +
		padRight("ctid", tupleCtidColW) + "  " +
		"infomask flags"
	return styleMuted.Render(line)
}

func renderHeapTupleHeadline(t pg.HeapTuple, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	dot, flagName := lpFlagDecoration(t.LPFlags)
	idx := fmt.Sprintf("#%04d", t.LP)
	if selected {
		idx = styleSelected.Render(idx)
	}
	xmin := xidString(t.Xmin)
	xmax := xidString(t.Xmax)
	ctid := "—"
	if t.Ctid != nil {
		ctid = *t.Ctid
	} else if t.LPFlags == pg.LPRedirect {
		// REDIRECT line pointers stash the target's OffsetNumber (1-based) in
		// lp_off rather than a real ctid — surface it so the HOT-chain hop
		// is readable without dropping into the expanded detail block.
		ctid = fmt.Sprintf("→ #%04d", t.LPOff)
	}
	badges := tupleInfomaskBadges(t.Infomask, t.Infomask2)
	return cursor + idx + "  " +
		dot + " " + padRight(flagName, tupleFlagColW) + "  " +
		padRight(fmt.Sprintf("%d", t.LPLen), tupleLenColW) + "  " +
		padRight(xmin, tupleXidColW) + "  " +
		padRight(xmax, tupleXidColW) + "  " +
		padRight(ctid, tupleCtidColW) + "  " +
		badges
}

func renderHeapTupleExpand(t pg.HeapTuple) []string {
	indent := "       "
	dataLine := indent + styleMuted.Render("data: ") + previewBytes(t.Data, 48)
	bits := "—"
	if t.Bits != nil && *t.Bits != "" {
		s := *t.Bits
		if len(s) > 32 {
			s = s[:32] + "…"
		}
		bits = s
	}
	hoff := "—"
	if t.Hoff != nil {
		hoff = fmt.Sprintf("%d", *t.Hoff)
	}
	infoLine := indent + styleMuted.Render(fmt.Sprintf(
		"infomask 0x%04x  ·  infomask2 0x%04x  ·  hoff %s  ·  bits %s",
		uint16(t.Infomask), uint16(t.Infomask2), hoff, bits,
	))
	rawLine := indent + styleMuted.Render(fmt.Sprintf("lp_off %d  raw_len %d", t.LPOff, t.LPLen))
	return []string{dataLine, infoLine, rawLine}
}

// lpFlagDecoration returns the coloured LP dot and the four-letter label for
// a given t_lp_flags value. Unknown values produce a muted dot — defensive
// only; pageinspect never returns flags outside 0..3.
func lpFlagDecoration(flags int32) (string, string) {
	switch flags {
	case pg.LPNormal:
		return styleLPNormal.Render("●"), "NORMAL"
	case pg.LPRedirect:
		return styleLPRedirect.Render("●"), "REDIRECT"
	case pg.LPDead:
		return styleLPDead.Render("●"), "DEAD"
	case pg.LPUnused:
		return styleLPUnused.Render("●"), "UNUSED"
	}
	return styleMuted.Render("●"), "?"
}

func xidString(x *uint32) string {
	if x == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *x)
}

// previewBytes formats the first N bytes of a tuple's t_data as a compact
// hex string with a trailing ellipsis when truncated. Empty/nil reads "—"
// so the line never collapses to a stray colon.
func previewBytes(b []byte, n int) string {
	if len(b) == 0 {
		return styleMuted.Render("—")
	}
	if len(b) <= n {
		return fmt.Sprintf("\\x%x", b)
	}
	return fmt.Sprintf("\\x%x…", b[:n])
}

// renderTupleRowList draws one row per column of the heap row the user
// drilled into: the column name (padded) and the value (NULL rendered as
// "NULL" in the muted style, the rest plain). Values are truncated to fit
// the terminal so wide jsonb / bytea columns don't blow up the layout —
// the user can still tell what they're looking at and how long it is.
func (m *Model) renderTupleRowList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	// Value column gets every remaining cell.
	valW := max(m.width-(colCursor+tupleRowNameColW+colGutter+colGutter), 16)

	var b strings.Builder
	header := "  " + padRight("column", tupleRowNameColW) + "  " + "value"
	b.WriteString(styleMuted.Render(header))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		c, _ := it.data.(pg.TupleCell)
		selected := vi == s.cursor
		cursor := "  "
		if selected {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		name := c.Name
		if selected {
			name = styleSelected.Render(name)
		}
		value := truncateValue(c.Value, valW)
		b.WriteString(cursor + padRight(name, tupleRowNameColW) + "  " + value)
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// truncateValue renders a column value for the row-detail view: NULL gets
// the muted style, anything else is clipped to width with a trailing
// ellipsis so wide jsonb/bytea columns don't break alignment.
func truncateValue(v *string, width int) string {
	if v == nil {
		return styleMuted.Render("NULL")
	}
	s := *v
	if lipgloss.Width(s) <= width {
		return s
	}
	// Trim runewise so we don't slice into a UTF-8 sequence.
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// renderDescribe draws the \d-style description panel for levelDescribe. It
// is a plain-text free-form view (no bars) padded to `height` lines so the
// help row stays pinned to the bottom. Switches on s.describe.Kind to render
// either a table (columns / indexes / constraints / summary) or an index
// (definition, method, parent, optional partial predicate).
func (m *Model) renderDescribe(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	d := s.describe
	if d == nil {
		// Should not happen: the loading guard in View fires before this,
		// but defend anyway.
		for i := 0; i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	b.WriteString("\n")

	// truncLine clips a string to m.width so wide index defs don't wrap and
	// unpins the help row.
	truncLine := func(v string) string {
		if m.width <= 4 {
			return v
		}
		w := m.width - 4 // 4 = leading "    " indent
		if lipgloss.Width(v) <= w {
			return v
		}
		r := []rune(v)
		for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
			r = r[:len(r)-1]
		}
		return string(r) + "…"
	}

	switch d.Kind {
	case pg.DescribeTable:
		b.WriteString("  " + styleSelected.Render(d.Title) + "\n\n")

		// --- columns ---
		b.WriteString("  " + styleHeader.Render(" columns ") + "\n")
		if len(d.Columns) == 0 {
			b.WriteString("    " + mu("(none)") + "\n")
		} else {
			// Compute column-name width for alignment.
			nameW := 4
			for _, col := range d.Columns {
				if n := lipgloss.Width(col.Name); n > nameW {
					nameW = n
				}
			}
			for _, col := range d.Columns {
				line := padRight(col.Name, nameW) + "  " + col.Type
				if col.NotNull {
					line += "  " + styleBadge.Render("not null")
				}
				if col.Default != "" {
					line += "  " + mu("default "+col.Default)
				}
				b.WriteString("    " + line + "\n")
			}
		}

		// --- indexes ---
		if len(d.Indexes) > 0 {
			b.WriteString("\n  " + styleHeader.Render(" indexes ") + "\n")
			for _, idx := range d.Indexes {
				badges := ""
				if idx.IsPrimary {
					badges += " " + styleBadge.Render("primary")
				} else if idx.IsUnique {
					badges += " " + styleBadge.Render("unique")
				}
				b.WriteString("    " + idx.Name + badges + "\n")
				b.WriteString("      " + mu(truncLine(idx.Def)) + "\n")
			}
		}

		// --- constraints ---
		if len(d.Constraints) > 0 {
			b.WriteString("\n  " + styleHeader.Render(" constraints ") + "\n")
			for _, con := range d.Constraints {
				b.WriteString("    " + con.Name + "\n")
				b.WriteString("      " + mu(truncLine(con.Def)) + "\n")
			}
		}

		// --- summary ---
		b.WriteString("\n  " + mu(fmt.Sprintf(
			"%s total · ~%s rows",
			humanize.Bytes(d.SizeBytes),
			formatRows(d.EstRows),
		)) + "\n")

	case pg.DescribeIndex:
		b.WriteString("  " + styleSelected.Render(d.Title) + "\n\n")
		b.WriteString("  " + mu("on ") + d.ParentTable + "\n")

		methodLine := "  " + mu("method ") + d.AccessMethod
		if d.IdxPrimary {
			methodLine += "  " + styleBadge.Render("primary")
		}
		if d.IdxUnique && !d.IdxPrimary {
			methodLine += "  " + styleBadge.Render("unique")
		}
		b.WriteString(methodLine + "\n")

		b.WriteString("\n  " + styleHeader.Render(" definition ") + "\n")
		b.WriteString("    " + mu(truncLine(d.IndexDef)) + "\n")

		if d.Predicate != "" {
			b.WriteString("\n  " + styleHeader.Render(" partial predicate ") + "\n")
			b.WriteString("    " + mu(truncLine(d.Predicate)) + "\n")
		}
	}

	// Pad to fill the content area so the help row stays pinned.
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderRelationsList draws the page-inspector tool's flat list of heap
// tables and B-tree indexes — mixed, sorted by the active sort mode. Bar
// is solid per row, coloured by relation kind (cyan = table, green =
// index). Each row carries its own size + est rows + page count columns;
// index rows additionally show a muted "→ parent_table" tail.
func (m *Model) renderRelationsList(s *screen, height int) string {
	vis := s.visibleIndexes()
	max := maxItemSize(s.items, vis)
	s.offset, _ = viewportRange(s.cursor, s.offset, height, len(vis))
	end := min(s.offset+height, len(vis))
	barW := m.barWidth(s)

	var b strings.Builder
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		r, _ := it.data.(pg.Relation)
		style := styleHeapSeg
		if r.Kind == pg.RelBTreeIndex {
			style = styleIndexSeg
		}
		b.WriteString(renderRelationRow(it, r, max, barW, vi == s.cursor, style))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderRelationRow(it item, r pg.Relation, maxSize int64, barW int, selected bool, style lipgloss.Style) string {
	bar := renderSolidBar(it.size, maxSize, barW, style)
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	sizeStr := humanize.Bytes(it.size)
	rowsStr := styleMuted.Render(padRight("~"+formatRows(it.rows), rowsColW)) + "  "
	pagesStr := styleMuted.Render(padRight(formatRows(it.pages)+"p", pagesColW)) + "  "
	childMark := styleMuted.Render("+ ")
	parent := ""
	if r.Kind == pg.RelBTreeIndex && r.ParentName != "" {
		parent = "  " + styleMuted.Render("→ "+r.ParentName)
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + rowsStr + pagesStr + childMark + name + parent
}

// renderIndexPagesList draws one row per B-tree page in the loaded window.
// Bar shows live/dead/free packed against BLCKSZ — same fixed-scale story
// as the heap-pages view. Per-page columns: type (leaf/intr/root/del),
// level (0 = leaf), used bytes, live/dead item counts, free %.
func (m *Model) renderIndexPagesList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	barW := m.barWidth(s)

	var b strings.Builder
	b.WriteString(renderIndexPagesHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		p, _ := it.data.(pg.IndexPageStat)
		b.WriteString(renderIndexPageRow(it, p, barW, vi == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderIndexPagesHeader(sort sortMode, sortDesc bool, barW int) string {
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
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight("type", idxPageTypeColW) + "  " +
		padRight(mark("level", sort == sortByLevel), idxPageLevelColW) + "  " +
		padRight(mark("used", sort == sortBySize), idxPageUsedColW) + "  " +
		padRight("live/dead", idxPageItemsColW) + "  " +
		padRight(mark("free", sort == sortByFreeSpace), idxPageFreeColW) + "  " +
		mark("page", sort == sortByBlkno)
	return styleMuted.Render(line)
}

// indexPageTypeLabel maps the single-character bt_page_stats type to a
// readable 3–4 letter tag. Unknown codes pass through verbatim — defensive
// only; pageinspect only emits l/r/i/d.
func indexPageTypeLabel(t string) string {
	switch t {
	case "l":
		return "leaf"
	case "i":
		return "intr"
	case "r":
		return "root"
	case "d":
		return "del"
	}
	return t
}

func renderIndexPageRow(it item, p pg.IndexPageStat, barW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	bar := renderIndexPageBar(p, barW)
	typ := indexPageTypeLabel(p.Type)
	lvl := fmt.Sprintf("L%d", p.BtpoLevel)
	used := humanize.Bytes(it.size)
	items := fmt.Sprintf("%3dL %3dD", p.LiveItems, p.DeadItems)
	free := "—"
	if p.PageSize > 0 {
		pct := float64(p.FreeSize) * 100 / float64(p.PageSize)
		free = fmt.Sprintf("%.0f%%", pct)
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	return cursor + bar + "  " +
		padRight(typ, idxPageTypeColW) + "  " +
		padRight(lvl, idxPageLevelColW) + "  " +
		padRight(used, idxPageUsedColW) + "  " +
		padRight(items, idxPageItemsColW) + "  " +
		padRight(free, idxPageFreeColW) + "  " +
		name
}

// renderIndexPageBar paints one B-tree page as live | dead | free, scaled
// to BLCKSZ. live cells use the index-green hue (so it visually matches
// the index colour from the relations level), dead reuses the bloat red.
func renderIndexPageBar(p pg.IndexPageStat, width int) string {
	const blockSize int64 = 8192
	used := max(blockSize-int64(p.FreeSize), 0)
	total := int64(p.LiveItems) + int64(p.DeadItems)
	var live, dead int64
	if total > 0 {
		dead = used * int64(p.DeadItems) / total
		live = used - dead
	} else {
		live = used
	}
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
		d = max0(width - l)
	}
	return paintBar(width,
		barSegment{cells: l, style: styleIndexSeg},
		barSegment{cells: d, style: styleBloat},
	)
}

// renderIndexTuplesList draws one row per item-pointer on a B-tree page:
// offset, itemlen, nulls/vars flags, ctid (heap tid on leaf pages,
// downlink on internal pages), and the decoded key data.
func (m *Model) renderIndexTuplesList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	// Distinguish leaf vs. internal pages for the ctid label: on leaf
	// pages it's a heap tid; on internal pages it's a downlink. The page
	// type comes from the parent screen's items via the stack so the
	// renderer doesn't have to fetch it again — but we don't have it here;
	// instead we expose the convention in the legend / header.
	keyW := max(m.width-(colCursor+idxTupleOffColW+colGutter+
		idxTupleLenColW+colGutter+idxTupleFlagsColW+colGutter+
		idxTupleCtidColW+colGutter+4), 16)

	var b strings.Builder
	b.WriteString(renderIndexTuplesHeader(s.sort, s.sortDesc))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		t, _ := it.data.(pg.IndexTuple)
		selected := vi == s.cursor
		b.WriteString(renderIndexTupleRow(t, s.indexPageType, keyW, selected))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderIndexTuplesHeader(sort sortMode, sortDesc bool) string {
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
	line := "  " + padRight(mark("off", sort == sortByLP), idxTupleOffColW) + "  " +
		padRight(mark("len", sort == sortBySize), idxTupleLenColW) + "  " +
		padRight("flags", idxTupleFlagsColW) + "  " +
		padRight("ctid", idxTupleCtidColW) + "  " +
		"key"
	return styleMuted.Render(line)
}

func renderIndexTupleRow(t pg.IndexTuple, pageType string, keyW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	off := fmt.Sprintf("#%04d", t.ItemOffset)
	if selected {
		off = styleSelected.Render(off)
	}
	lenStr := fmt.Sprintf("%d", t.ItemLen)
	flags := boolFlag("N", t.Nulls) + boolFlag("V", t.Vars)
	if flags == "" {
		flags = styleMuted.Render("—")
	}
	// The ctid column carries different meaning depending on the tuple kind:
	// for normal leaf entries it's a real heap pointer; for pivot tuples
	// (high keys / separators) it's the page-boundary key, with the high
	// bits encoding a heap_tid attr; for posting-list tuples (PG 13+ btree
	// deduplication) it's an opaque packed marker, with offset bits
	// encoding the posting count. Surface those kinds directly — the raw
	// "(blk,off)" value is misleading on the alt-tid kinds.
	kind := classifyIndexTuple(t, pageType)
	var ctidLabel string
	switch kind {
	case idxTuplePivot:
		ctidLabel = styleHeapToastTag.Render("pivot")
	case idxTuplePosting:
		ctidLabel = styleHeapHot.Render("posting")
	default:
		if t.Ctid != nil {
			ctidLabel = *t.Ctid
		} else {
			ctidLabel = "—"
		}
	}
	// Prefer the heap-projected decoded value (e.g. "(42,alice)") — the hex
	// `data` is still useful when the heap join missed (pivot/posting/dead
	// entries) so we fall back to it, dimmed to signal "raw bytes, not a
	// decoded value".
	var key string
	switch {
	case t.Decoded != nil:
		key = truncateValue(t.Decoded, keyW)
	case t.Data != nil:
		key = styleMuted.Render(truncateValue(t.Data, keyW))
	default:
		key = styleMuted.Render("—")
	}
	return cursor + padRight(off, idxTupleOffColW) + "  " +
		padRight(lenStr, idxTupleLenColW) + "  " +
		padRight(flags, idxTupleFlagsColW) + "  " +
		padRight(ctidLabel, idxTupleCtidColW) + "  " +
		key
}

// indexTupleKind classifies a bt_page_items row by the high bits of its
// ctid offset — the way nbtree encodes "this isn't a real heap pointer":
//
//   - INDEX_ALT_TID_MASK (0x2000): posting-list tuple (PG 13+ dedup —
//     one logical entry packing N heap tids).
//   - BT_PIVOT_HEAP_TID_ATTR (0x1000): pivot tuple carrying a heap tid
//     as part of the high-key separator (or downlink on internal pages).
//
// Everything else is a regular leaf entry whose ctid points to a heap
// row we can project and drill into.
type indexTupleKind int

const (
	idxTupleNormal indexTupleKind = iota
	idxTuplePivot
	idxTuplePosting
)

// Offset-word bits from src/include/access/nbtree.h. Used to spot
// nbtree's "this isn't a real heap pointer" tuples in bt_page_items
// output without parsing the raw tuple bytes.
const (
	btIndexAltTIDMask  = 0x2000 // INDEX_ALT_TID_MASK   — posting-list entry (PG 13+)
	btPivotHeapTIDAttr = 0x1000 // BT_PIVOT_HEAP_TID_ATTR — pivot with heap-tid attr
)

func classifyIndexTuple(t pg.IndexTuple, pageType string) indexTupleKind {
	if t.Ctid != nil {
		var blk, off int
		if _, err := fmt.Sscanf(*t.Ctid, "(%d,%d)", &blk, &off); err == nil {
			_ = blk
			switch {
			case off&btIndexAltTIDMask != 0:
				return idxTuplePosting
			case off&btPivotHeapTIDAttr != 0:
				return idxTuplePivot
			}
		}
	}
	// Truncated pivots set neither bit — the index tuple stores only the
	// key, with no heap-tid attribute, so the t_tid slot reads as some
	// stale (block,offset). They sit at item #1 of every non-rightmost
	// leaf page (the high-key separator), and as every entry on internal
	// pages (downlinks). Decoded == nil means the heap projection missed;
	// combined with "item #1 on a leaf" or "any item on an internal page"
	// that pins down a pivot vs. a regular entry whose heap row was
	// vacuumed (which would only mislabel item #1 of the rightmost leaf —
	// rare, and the user can see the still-meaningful hex key bytes).
	if t.Decoded == nil {
		switch pageType {
		case "i":
			return idxTuplePivot
		case "l", "r":
			if t.ItemOffset == 1 {
				return idxTuplePivot
			}
		}
	}
	return idxTupleNormal
}

// boolFlag renders a one-letter flag tag in the OK style when the pointer
// is non-nil and true, otherwise an empty string. Used for nulls/vars on
// the index-tuples row so set bits are visually scannable.
func boolFlag(letter string, b *bool) string {
	if b == nil || !*b {
		return ""
	}
	return styleBadge.Render(letter)
}

// tupleInfomaskBadges renders the decoded infomask/infomask2 bits as a
// bracketed sequence. Categories: commit-state (green), invalid (muted),
// HOT (magenta), external (toast yellow), structural (default).
func tupleInfomaskBadges(infomask, infomask2 int32) string {
	im := uint16(infomask)
	im2 := uint16(infomask2)
	var parts []string

	badge := func(label string, style lipgloss.Style) {
		parts = append(parts, style.Render("["+label+"]"))
	}

	if im&pg.HeapHasNull != 0 {
		badge("HASNULL", styleMuted)
	}
	if im&pg.HeapHasVarWidth != 0 {
		badge("VARWIDTH", styleMuted)
	}
	if im&pg.HeapHasExternal != 0 {
		badge("HASEXTERNAL", styleHeapToastTag)
	}
	if im&pg.HeapXminCommitted != 0 {
		badge("XMIN_CMT", lipgloss.NewStyle().Foreground(colorOK))
	}
	if im&pg.HeapXminInvalid != 0 {
		badge("XMIN_INV", styleMuted)
	}
	if im&pg.HeapXmaxCommitted != 0 {
		badge("XMAX_CMT", lipgloss.NewStyle().Foreground(colorOK))
	}
	if im&pg.HeapXmaxInvalid != 0 {
		badge("XMAX_INV", styleMuted)
	}
	if im&pg.HeapXmaxIsMulti != 0 {
		badge("XMAX_MULTI", lipgloss.NewStyle().Foreground(colorAccent))
	}
	if im&pg.HeapUpdated != 0 {
		badge("UPDATED", styleMuted)
	}
	if im2&pg.HeapHotUpdated2 != 0 {
		badge("HOT", styleHeapHot)
	}
	if im2&pg.HeapOnlyTuple2 != 0 {
		badge("HEAP_ONLY", styleHeapHot)
	}
	return strings.Join(parts, "")
}
