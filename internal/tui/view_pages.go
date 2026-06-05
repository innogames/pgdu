package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

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
	b.WriteString("    " + mu("░  free      empty space between pd_lower and pd_upper inside the page") + "\n")
	b.WriteString("    " + mu("         REDIRECT (HOT hop) slots have no tuple bytes so they don't appear in the bar;") + "\n")
	b.WriteString("    " + mu("         their count shows as the R column in live/R/dead") + "\n\n")

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
		padRight("live/R/dead", heapPageLPColW) + "  " +
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
	lpStr := fmt.Sprintf("%3dL %2dR %2dD", p.LiveLP, p.RedirectLP, p.DeadLP)
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
	chunkInfo := ""
	if t.ChunkID != nil && t.ChunkSeq != nil {
		// TOAST chunk: append muted chunk_id / seq so the user can identify
		// which chunk object this row belongs to without drilling in.
		chunkInfo = "  " + styleMuted.Render(fmt.Sprintf("chunk %d  seq %d", *t.ChunkID, *t.ChunkSeq))
	}
	return cursor + idx + "  " +
		dot + " " + padRight(flagName, tupleFlagColW) + "  " +
		padRight(fmt.Sprintf("%d", t.LPLen), tupleLenColW) + "  " +
		padRight(xmin, tupleXidColW) + "  " +
		padRight(xmax, tupleXidColW) + "  " +
		padRight(ctid, tupleCtidColW) + "  " +
		badges + chunkInfo
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

// renderRelationsList draws the page-inspector tool's flat list of heap
// tables, B-tree indexes, and TOAST heaps — mixed, sorted by the active sort
// mode. Bar is solid per row, coloured by relation kind (cyan = table, green =
// index, white = toast). Each row carries its own size + est rows + page count
// columns; index and toast rows additionally show a muted "→ parent_table" tail.
func (m *Model) renderRelationsList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSize := maxItemSize(s.items, vis)
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	barW := m.barWidth(s)

	var b strings.Builder
	b.WriteString(renderRelationsHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		r, _ := it.data.(pg.Relation)
		style := styleHeapSeg
		switch r.Kind {
		case pg.RelBTreeIndex:
			style = styleIndexSeg
		case pg.RelToast:
			style = styleToastSeg
		}
		b.WriteString(renderRelationRow(it, r, maxSize, barW, vi == s.cursor, style))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderRelationsHeader(sort sortMode, sortDesc bool, barW int) string {
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
	// Offset matches the row: cursor(2) + bar+brackets(barW+2) + sep(2).
	line := strings.Repeat(" ", colCursor) + strings.Repeat(" ", barW+colBrackets) + "  " +
		padRight(mark("size", sort == sortBySize), 10) + "  " +
		padRight(mark("~rows", sort == sortByRows), rowsColW) + "  " +
		padRight("pages", pagesColW) + "  " +
		"  " + // child mark column ("+ ")
		mark("name", sort == sortByName)
	return styleMuted.Render(line)
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
	if r.ParentName != "" {
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
	deadTag := styleBloat.Render("dead") + " "
	var key string
	switch {
	case t.Decoded != nil:
		key = truncateValue(t.Decoded, keyW)
	case t.Data != nil && kind == idxTupleNormal:
		// Normal leaf entry whose ctid didn't resolve to a live heap row:
		// the row was deleted/updated since the last VACUUM.
		key = deadTag + styleMuted.Render(truncateValue(t.Data, keyW-5))
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
