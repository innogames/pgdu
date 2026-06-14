package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

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

// renderRelationsList draws the page-inspector tool's flat list of heap
// tables, B-tree indexes, and TOAST heaps — mixed, sorted by the active sort
// mode. Bar is solid per row, coloured by relation kind (cyan = table, green =
// index, white = toast). Each row carries its own size + est rows + page count
// columns; index and toast rows additionally show a muted "→ parent_table" tail.
func (m *Model) renderRelationsList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSize := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderRelationsHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			r, _ := it.data.(pg.Relation)
			style := styleHeapSeg
			switch r.Kind {
			case pg.RelBTreeIndex:
				style = styleIndexSeg
			case pg.RelToast:
				style = styleToastSeg
			}
			return renderRelationRow(it, r, maxSize, barW, selected, style)
		})
}

func renderRelationsHeader(sort sortMode, sortDesc bool, barW int) string {
	// Offset matches the row: cursor(2) + bar+brackets(barW+2) + sep(2).
	line := headerIndent(barW) +
		padRight(sortMark("size", sort == sortBySize, sortDesc), 10) + "  " +
		padRight(sortMark("~rows", sort == sortByRows, sortDesc), rowsColW) + "  " +
		padRight("pages", pagesColW) + "  " +
		"  " + // child mark column ("+ ")
		sortMark("name", sort == sortByName, sortDesc)
	return styleMuted.Render(line)
}

func renderRelationRow(it item, r pg.Relation, maxSize int64, barW int, selected bool, style lipgloss.Style) string {
	bar := renderSolidBar(it.size, maxSize, barW, style)
	cursor := selectedCursor(selected)
	name := highlightName(it.name, selected)
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
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderIndexPagesHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			p, _ := it.data.(pg.IndexPageStat)
			return renderIndexPageRow(it, p, barW, selected)
		})
}

func renderIndexPagesHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight("type", idxPageTypeColW) + "  " +
		padRight(sortMark("level", sort == sortByLevel, sortDesc), idxPageLevelColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight("live/dead", idxPageItemsColW) + "  " +
		padRight(sortMark("free", sort == sortByFreeSpace, sortDesc), idxPageFreeColW) + "  " +
		sortMark("page", sort == sortByBlkno, sortDesc)
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
	cursor := selectedCursor(selected)
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
	name := highlightName(it.name, selected)
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
	// Distinguish leaf vs. internal pages for the ctid label: on leaf
	// pages it's a heap tid; on internal pages it's a downlink. The page
	// type comes from the parent screen's items via the stack so the
	// renderer doesn't have to fetch it again — but we don't have it here;
	// instead we expose the convention in the legend / header.
	keyW := max(m.width-(colCursor+idxTupleOffColW+colGutter+
		idxTupleLenColW+colGutter+idxTupleFlagsColW+colGutter+
		idxTupleCtidColW+colGutter+4), 16)
	pageType := s.indexPageType
	return m.renderRowList(s, height, renderIndexTuplesHeader(s.sort, s.sortDesc),
		func(it item, selected bool) string {
			t, _ := it.data.(pg.IndexTuple)
			return renderIndexTupleRow(t, pageType, keyW, selected)
		})
}

func renderIndexTuplesHeader(sort sortMode, sortDesc bool) string {
	line := "  " + padRight(sortMark("off", sort == sortByLP, sortDesc), idxTupleOffColW) + "  " +
		padRight(sortMark("len", sort == sortBySize, sortDesc), idxTupleLenColW) + "  " +
		padRight("flags", idxTupleFlagsColW) + "  " +
		padRight("ctid", idxTupleCtidColW) + "  " +
		"key"
	return styleMuted.Render(line)
}

func renderIndexTupleRow(t pg.IndexTuple, pageType string, keyW int, selected bool) string {
	cursor := selectedCursor(selected)
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
