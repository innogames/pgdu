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
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderHeapPagesHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			p, _ := it.data.(pg.HeapPageStat)
			return renderHeapPageRow(it, p, barW, selected)
		})
}

func renderHeapPagesHeader(sort sortMode, sortDesc bool, barW int) string {
	// Header indent matches the row: cursor (2) + bar slot (barW+2) + "  "
	// before the flag column starts.
	line := headerIndent(barW) +
		padRight("!", heapPageFlagColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), heapPageUsedColW) + "  " +
		padRight("live/R/dead", heapPageLPColW) + "  " +
		padRight(sortMark("dead%", sort == sortByDeadRatio, sortDesc), heapPageDeadColW) + "  " +
		sortMark("page", sort == sortByBlkno, sortDesc)
	return styleMuted.Render(line)
}

func renderHeapPageRow(it item, p pg.HeapPageStat, barW int, selected bool) string {
	cursor := selectedCursor(selected)
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
	name := highlightName(it.name, selected)
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
	// Indentation matches the row: cursor (2) + "#NNNN" idx col (5) + gap.
	// The "● " dot+space takes 2 cells before the flag-name column.
	line := "  " + padRight(sortMark("lp", sort == sortByLP, sortDesc), 5) + "  " +
		padRight("lp_flags", 2+tupleFlagColW) + "  " +
		padRight(sortMark("len", sort == sortBySize, sortDesc), tupleLenColW) + "  " +
		padRight("xmin", tupleXidColW) + "  " +
		padRight("xmax", tupleXidColW) + "  " +
		padRight("ctid", tupleCtidColW) + "  " +
		"infomask flags"
	return styleMuted.Render(line)
}

func renderHeapTupleHeadline(t pg.HeapTuple, selected bool) string {
	cursor := selectedCursor(selected)
	dot, flagName := lpFlagDecoration(t.LPFlags)
	idx := highlightName(fmt.Sprintf("#%04d", t.LP), selected)
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
	// Value column gets every remaining cell.
	valW := max(m.width-(colCursor+tupleRowNameColW+colGutter+colGutter), 16)
	header := styleMuted.Render("  " + padRight("column", tupleRowNameColW) + "  " + "value")
	return m.renderRowList(s, height, header,
		func(it item, selected bool) string {
			c, _ := it.data.(pg.TupleCell)
			value := truncateValue(c.Value, valW)
			return selectedCursor(selected) + padRight(highlightName(c.Name, selected), tupleRowNameColW) + "  " + value
		})
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
