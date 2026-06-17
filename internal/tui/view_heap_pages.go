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
	b.WriteString("    " + mu("         their count shows in the R column (each of live / R / dead sorts on its own)") + "\n\n")

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
// the meaning of each column (lp, lp_flags, len, xmin/xmax, ctid, state), the
// visibility verdicts and structural-flag icons, and the three expanded-row
// lines. Sized to fill `height` lines so the help row stays pinned to the
// bottom; shown when the user toggles `?` on levelHeapTuples.
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
	b.WriteString("    " + padRight("ctid", 12) + mu("forward pointer: own (block,offset) for NORMAL · → #NNNN target lp for REDIRECT") + "\n")
	b.WriteString("    " + padRight("state", 12) + mu("visibility verdict decoded from xmin/xmax + commit bits (see below)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" state ") + "  " +
		mu("the infomask/xmin/xmax bits collapse to one verdict instead of a raw badge dump") + "\n")
	b.WriteString("    " + styleLPNormal.Render(padRight("live", 10)) +
		mu("inserter committed, no (committed) deleter — visible to current transactions") + "\n")
	b.WriteString("    " + styleLPNormal.Render(padRight("frozen", 10)) +
		mu("HEAP_XMIN_FROZEN — permanently visible, xmin no longer matters for wraparound") + "\n")
	b.WriteString("    " + styleBloat.Render(padRight("dead", 10)) +
		mu("deleted by a committed xmax (or a DEAD line pointer) — reclaimable by VACUUM") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("deleting", 10)) +
		mu("xmax set but not yet committed — an in-flight DELETE/UPDATE") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("locked", 10)) +
		mu("xmax is a MultiXactId — row is locked/updated by several transactions") + "\n")
	b.WriteString("    " + styleMuted.Render(padRight("aborted", 10)) +
		mu("inserting transaction aborted — the tuple was never visible to anyone") + "\n")
	b.WriteString("    " + styleMuted.Render(padRight("unused", 10)) +
		mu("free line pointer (UNUSED) — no tuple body") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" flags ") + "  " +
		mu("compact icons trail the verdict only when these structural bits are set") + "\n")
	b.WriteString("    " + styleHeapHot.Render(padRight("↟HOT", 10)) +
		mu("HEAP_HOT_UPDATED — the next row version is on the same page (no index update)") + "\n")
	b.WriteString("    " + styleHeapHot.Render(padRight("◦only", 10)) +
		mu("HEAP_ONLY_TUPLE — this version is reachable only via a HOT chain hop") + "\n")
	b.WriteString("    " + styleHeapToastTag.Render(padRight("⇲toast", 10)) +
		mu("HEAP_HASEXTERNAL — at least one value lives out-of-line in the TOAST relation") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" expanded row ") + "  " +
		mu("the selected row expands to three lines decoding its internals") + "\n")
	b.WriteString("    " + padRight("data:", 12) + mu("first bytes of t_data in hex, tagged with the payload byte count") + "\n")
	b.WriteString("    " + padRight("lifecycle:", 12) + mu("MVCC story — who inserted it (and if that committed), then deleted/locked/live") + "\n")
	b.WriteString("    " + padRight("layout:", 12) + mu("header vs payload bytes (split at t_hoff) with a bar, plus the page byte span") + "\n")

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
		padRight(sortMark("live", sort == sortByLiveLP, sortDesc), heapPageLiveColW) + "  " +
		padRight(sortMark("R", sort == sortByRedirectLP, sortDesc), heapPageRedirColW) + "  " +
		padRight(sortMark("dead", sort == sortByDeadLP, sortDesc), heapPageDeadLPColW) + "  " +
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
	// Each count echoes its colour from the page bar / flag glyphs: live in the
	// heap-segment hue, REDIRECT in the HOT-hop accent, dead in bloat-red (only
	// when non-zero, so a clean page stays quiet).
	liveStr := styleHeapSeg.Render(fmt.Sprintf("%d", p.LiveLP))
	redirStr := styleMuted.Render(fmt.Sprintf("%d", p.RedirectLP))
	if p.RedirectLP > 0 {
		redirStr = styleLPRedirect.Render(fmt.Sprintf("%d", p.RedirectLP))
	}
	deadStr := styleMuted.Render(fmt.Sprintf("%d", p.DeadLP))
	if p.DeadLP > 0 {
		deadStr = styleBloat.Render(fmt.Sprintf("%d", p.DeadLP))
	}
	deadPct := "—"
	if df := p.DeadFrac(); df >= 0 {
		deadPct = percentStyle(100 - df*100).Render(fmt.Sprintf("%.0f%%", df*100))
	}
	name := highlightName(it.name, selected)
	return cursor + bar + "  " +
		padRight(flag, heapPageFlagColW) + "  " +
		padRight(used, heapPageUsedColW) + "  " +
		padRight(liveStr, heapPageLiveColW) + "  " +
		padRight(redirStr, heapPageRedirColW) + "  " +
		padRight(deadStr, heapPageDeadLPColW) + "  " +
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
		"state"
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
	// The infomask/xmin/xmax bits collapse to a single visibility verdict
	// ("live"/"dead"/…) — far higher signal than the per-row badge dump, which
	// read identically on every tuple of a page. Only the genuinely varying
	// structural flags (HOT / heap-only / external) trail as compact icons.
	stateLabel, stateStyle := heapTupleState(t)
	state := stateStyle.Render(stateLabel)
	icons := heapTupleFlagIcons(t)
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
		state + icons + chunkInfo
}

// heapTupleState collapses a line pointer's slot flag plus the tuple's
// xmin/xmax commit bits into a one-word visibility verdict and its tint.
// Slot state wins first (UNUSED/DEAD/REDIRECT line pointers carry no tuple
// body); for a NORMAL pointer the verdict is derived from the inserting and
// deleting transactions' commit fate as recorded in t_infomask.
func heapTupleState(t pg.HeapTuple) (string, lipgloss.Style) {
	switch t.LPFlags {
	case pg.LPUnused:
		return "unused", styleMuted
	case pg.LPDead:
		return "dead", styleBloat
	case pg.LPRedirect:
		return "redirect", styleLPRedirect
	}
	im := uint16(t.Infomask)
	xminCommitted := im&pg.HeapXminCommitted != 0
	xminInvalid := im&pg.HeapXminInvalid != 0
	// An aborted inserter means the tuple was never visible to anyone.
	if xminInvalid && !xminCommitted {
		return "aborted", styleMuted
	}
	// xmax names a deleter/locker; HEAP_XMAX_INVALID (or a zero xmax) means the
	// row was never deleted.
	deleted := t.Xmax != nil && *t.Xmax != 0 && im&pg.HeapXmaxInvalid == 0
	if deleted {
		switch {
		case im&pg.HeapXmaxIsMulti != 0:
			return "locked", styleBarAlt
		case im&pg.HeapXmaxCommitted != 0:
			return "dead", styleBloat
		default:
			return "deleting", styleBarAlt
		}
	}
	// HEAP_XMIN_FROZEN is encoded as both committed+invalid set together.
	if xminCommitted && xminInvalid {
		return "frozen", styleLPNormal
	}
	return "live", styleLPNormal
}

// heapTupleFlagIcons renders the structural infomask bits that actually vary
// from row to row — HOT-updated, heap-only, and has-external (TOAST pointer) —
// as a compact icon cluster. Returns "" (no leading gap) when none are set, so
// the common case stays clean.
func heapTupleFlagIcons(t pg.HeapTuple) string {
	im := uint16(t.Infomask)
	im2 := uint16(t.Infomask2)
	var parts []string
	if im2&pg.HeapHotUpdated2 != 0 {
		parts = append(parts, styleHeapHot.Render("↟HOT"))
	}
	if im2&pg.HeapOnlyTuple2 != 0 {
		parts = append(parts, styleHeapHot.Render("◦only"))
	}
	if im&pg.HeapHasExternal != 0 {
		parts = append(parts, styleHeapToastTag.Render("⇲toast"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + strings.Join(parts, " ")
}

// renderHeapTupleExpand turns the selected line pointer's raw page internals
// into readable lines: a data preview, a plain-English lifecycle sentence
// (replacing the old infomask/xmin/xmax hex), and a byte-anatomy breakdown with
// a tiny overhead-vs-payload bar (replacing the bare lp_off/raw_len numbers).
// Line pointers with no tuple body — REDIRECT/DEAD/UNUSED — get a single
// purposeful one-liner instead of a meaningless hex dump.
func renderHeapTupleExpand(t pg.HeapTuple) []string {
	indent := "       "
	switch t.LPFlags {
	case pg.LPRedirect:
		return []string{indent + styleMuted.Render("redirect → ") +
			styleLPRedirect.Render(fmt.Sprintf("#%04d", t.LPOff)) +
			styleMuted.Render("  ·  HOT-chain hop to a live tuple later on this page")}
	case pg.LPDead:
		return []string{indent +
			styleMuted.Render("dead line pointer  ·  tuple pruned; slot reclaimable on next vacuum")}
	case pg.LPUnused:
		return []string{indent +
			styleMuted.Render("unused slot  ·  free line pointer, available for a new tuple")}
	}
	return []string{
		tupleDataLine(t, indent),
		tupleLifecycleLine(t, indent),
		tupleAnatomyLine(t, indent),
	}
}

// tupleDataLine previews the raw t_data bytes (still the most direct look at
// the payload) and tags it with the payload byte count.
func tupleDataLine(t pg.HeapTuple, indent string) string {
	return indent + styleMuted.Render("data: ") + previewBytes(t.Data, 48) +
		styleMuted.Render(fmt.Sprintf("  (%d B payload)", len(t.Data)))
}

// tupleLifecycleLine narrates the tuple's MVCC story from xmin/xmax and the
// commit bits in t_infomask: who inserted it and whether that committed, then
// whether it was deleted / locked / is still live, then any structural notes.
func tupleLifecycleLine(t pg.HeapTuple, indent string) string {
	im := uint16(t.Infomask)
	im2 := uint16(t.Infomask2)
	var parts []string

	ins := "inserted " + xidLabel(t.Xmin)
	switch {
	case im&pg.HeapXminCommitted != 0 && im&pg.HeapXminInvalid != 0:
		ins += " (frozen)"
	case im&pg.HeapXminCommitted != 0:
		ins += " (committed)"
	case im&pg.HeapXminInvalid != 0:
		ins += " (aborted)"
	default:
		ins += " (in progress)"
	}
	parts = append(parts, ins)

	switch {
	case t.Xmax == nil || *t.Xmax == 0 || im&pg.HeapXmaxInvalid != 0:
		parts = append(parts, "not deleted")
	case im&pg.HeapXmaxIsMulti != 0:
		parts = append(parts, "locked (multixact "+xidLabel(t.Xmax)+")")
	case im&pg.HeapXmaxCommitted != 0:
		parts = append(parts, "deleted "+xidLabel(t.Xmax)+" (committed)")
	default:
		parts = append(parts, "deleting "+xidLabel(t.Xmax))
	}

	if im2&pg.HeapHotUpdated2 != 0 {
		parts = append(parts, "HOT-updated")
	}
	if im2&pg.HeapOnlyTuple2 != 0 {
		parts = append(parts, "heap-only")
	}
	if im&pg.HeapHasExternal != 0 {
		parts = append(parts, "has external (TOAST)")
	}

	return indent + styleMuted.Render("lifecycle: "+strings.Join(parts, " · "))
}

// tupleAnatomyLine breaks lp_len down into header overhead vs. user payload
// (split at t_hoff), draws a tiny proportional bar, and locates the tuple in
// the page by byte span — turning the old bare lp_off / raw_len numbers into a
// disk-efficiency story. The header byte count is graded by payload share so
// overhead-heavy tuples (many tiny rows) read warm.
func tupleAnatomyLine(t pg.HeapTuple, indent string) string {
	total := int(t.LPLen)
	header := 0
	if t.Hoff != nil {
		header = int(*t.Hoff)
	}
	data := max(total-header, 0)
	bar := renderTupleAnatomyBar(header, total, 10)

	headStr := fmt.Sprintf("%d B header", header)
	if total > 0 {
		headStr = percentStyle(float64(data) * 100 / float64(total)).Render(headStr)
	}
	nullNote := ""
	if im := uint16(t.Infomask); im&pg.HeapHasNull != 0 {
		nullNote = styleMuted.Render(" incl null-map")
	}
	return indent + bar + "  " + headStr + nullNote +
		styleMuted.Render(fmt.Sprintf(" + %d B data = %d B", data, total)) +
		styleMuted.Render(fmt.Sprintf("  ·  page bytes %d–%d", t.LPOff, int(t.LPOff)+total))
}

// renderTupleAnatomyBar paints header overhead (toast-yellow) against payload
// (heap-cyan) scaled to the tuple length, matching the page bars' visual idiom.
func renderTupleAnatomyBar(header, total, width int) string {
	if total <= 0 {
		return paintBar(width)
	}
	h := min(max(int(float64(width)*float64(header)/float64(total)), 0), width)
	return paintBar(width,
		barSegment{cells: h, style: styleHeapToastTag},
		barSegment{cells: width - h, style: styleHeapSeg},
	)
}

// xidLabel formats a transaction id as "t<NNN>" for the lifecycle sentence,
// or "t?" when pageinspect reported NULL (only happens on bodyless pointers,
// which take a different render path).
func xidLabel(x *uint32) string {
	if x == nil {
		return "t?"
	}
	return fmt.Sprintf("t%d", *x)
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
