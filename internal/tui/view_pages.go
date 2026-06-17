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

	b.WriteString("  " + styleHeader.Render(" banner ") + "  " +
		mu("the lines above the table summarise the whole index") + "\n")
	b.WriteString("    " + mu("keys: the search columns; ") + styleMuted.Render("include:") +
		mu(" lists covering (stored-but-not-searched) columns") + "\n")
	b.WriteString("    " + mu("root blk / height / dedup / version come from the metapage (bt_metap):") + "\n")
	b.WriteString("    " + mu("height is the tree depth above the leaves; dedup ") + styleBadge.Render("on") +
		mu(" means posting-list dedup is possible") + "\n\n")

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
		mu("structural separator: item #1 of every non-rightmost leaf page (the high key).") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("On internal pages every entry is a downlink, shown as ") + styleIndexSeg.Render("→ blk N") +
		mu(" (the child") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("page); ENTER descends into it to walk the tree toward the leaves.") + "\n")
	b.WriteString("    " + styleHeapHot.Render("posting") + strings.Repeat(" ", 14-len("posting")) + "  " +
		mu("PG 13+ btree deduplication: one tuple packs many heap tids for one key.") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 18) +
		mu("Shown as ") + styleHeapHot.Render("posting ×N") + mu(" — N is the packed heap-tid count.") + "\n\n")

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
		mu("heap pointer (leaf entries), ") + styleIndexSeg.Render("→ blk N") +
		mu(" downlink (internal), or ") +
		styleHeapToastTag.Render("pivot") + mu(" / ") +
		styleHeapHot.Render("posting ×N") + mu(" labels") + "\n")
	b.WriteString("    " + padRight("key", 8) +
		mu("decoded key from the heap when reachable, else dimmed hex of the raw key bytes") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" drilling ") + "  " +
		mu("which rows respond to Enter — and why others don't") + "\n")
	b.WriteString("    " + mu("Leaf entries whose ctid resolves to a live heap row drill into the per-column") + "\n")
	b.WriteString("    " + mu("row view. Internal-page downlinks (") + styleIndexSeg.Render("→ blk N") +
		mu(") descend one level into that child page,") + "\n")
	b.WriteString("    " + mu("so ENTER walks the tree structurally toward the leaves. Pivot high keys,") + "\n")
	b.WriteString("    " + mu("posting tuples, and entries whose heap row was vacuumed since the snapshot") + "\n")
	b.WriteString("    " + mu("don't drill — there's no single heap row to land on.") + "\n\n")
	b.WriteString("    " + mu("Reading bt_page_items / bt_metap needs a superuser (or pg_read_server_files).") + "\n")

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
		padRight(sortMark("rows", sort == sortByRows, sortDesc), rowsColW) + "  " +
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
	rowsStr := styleMuted.Render(padRight(formatRows(it.rows), rowsColW)) + "  "
	pagesStr := styleMuted.Render(padRight(formatRows(it.pages)+"p", pagesColW)) + "  "
	childMark := styleMuted.Render("+ ")
	parent := ""
	if r.ParentName != "" {
		parent = "  " + styleMuted.Render("→ "+r.ParentName)
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + rowsStr + pagesStr + childMark + name + parent
}

// renderIndexKeyBanner builds the context banner shown above the B-tree page
// and tuple lists: the index's key columns (with INCLUDE columns split out)
// and — on the page list only — a metapage summary (root block, tree height,
// dedup capability, version). Returns "" when nothing has loaded yet so View
// can skip the line entirely. Both inputs are best-effort, so either half may
// be absent.
func (m *Model) renderIndexKeyBanner(s *screen) string {
	var lines []string
	if line := indexKeyLine(s.indexKeyCols); line != "" {
		lines = append(lines, line)
	}
	// The metapage describes the whole tree, so it belongs on the page list, not
	// on a single page's tuple view.
	if s.level == levelIndexPages {
		if line := btreeMetaLine(s.btreeMeta); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// indexKeyLine renders "keys: (a, lower(b))  include: (c)" from the index's
// column definitions. Key columns are tinted in the index hue; INCLUDE columns
// stay muted to read as "stored but not searched".
func indexKeyLine(cols []pg.IndexKeyColumn) string {
	if len(cols) == 0 {
		return ""
	}
	var keys, incl []string
	for _, c := range cols {
		if c.IsKey {
			keys = append(keys, c.Def)
		} else {
			incl = append(incl, c.Def)
		}
	}
	out := "  " + styleMuted.Render("keys: ") +
		styleIndexSeg.Render("("+strings.Join(keys, ", ")+")")
	if len(incl) > 0 {
		out += styleMuted.Render("  include: (" + strings.Join(incl, ", ") + ")")
	}
	return out
}

// btreeMetaLine renders the bt_metap summary: root block, tree height (0 = a
// single-page root), whether deduplication is possible (allequalimage), and the
// nbtree on-disk version. dedup "on" is badged so an enabled index stands out.
func btreeMetaLine(meta *pg.BtreeMeta) string {
	if meta == nil {
		return ""
	}
	mu := styleMuted.Render
	dedup := mu("off")
	if meta.AllEqualImage {
		dedup = styleBadge.Render("on")
	}
	return "  " + mu(fmt.Sprintf("root blk %d", meta.Root)) + mu("  ·  ") +
		mu(fmt.Sprintf("height %d", meta.Level)) + mu("  ·  ") +
		mu("dedup ") + dedup + mu("  ·  ") +
		mu(fmt.Sprintf("v%d", meta.Version))
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
		padRight(sortMark("type", sort == sortByType, sortDesc), idxPageTypeColW) + "  " +
		padRight(sortMark("level", sort == sortByLevel, sortDesc), idxPageLevelColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight(sortMark("live/dead", sort == sortByDeadRatio, sortDesc), idxPageItemsColW) + "  " +
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

// indexPageTypeRank orders page types for sortByType: leaf → internal → root →
// deleted. Unknown codes sort last.
func indexPageTypeRank(t string) int {
	switch t {
	case "l":
		return 0
	case "i":
		return 1
	case "r":
		return 2
	case "d":
		return 3
	}
	return 4
}

// indexPageTypeStyle tints the page-type tag by role so the structural pages
// stand out from the sea of leaves: leaf stays muted (the common case),
// internal/root take the accent hue, and a deleted page reads red.
func indexPageTypeStyle(t string) lipgloss.Style {
	switch t {
	case "i", "r":
		return styleBarAlt
	case "d":
		return styleBloat
	}
	return styleMuted
}

func renderIndexPageRow(it item, p pg.IndexPageStat, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderIndexPageBar(p, barW)
	typ := indexPageTypeStyle(p.Type).Render(indexPageTypeLabel(p.Type))
	lvl := fmt.Sprintf("L%d", p.BtpoLevel)
	used := humanize.Bytes(it.size)

	// live/dead echo the bar's colours: live stays plain, dead turns the bloat
	// red whenever a page is carrying reclaimable items so it pops at a glance.
	deadStr := fmt.Sprintf("%3dD", p.DeadItems)
	if p.DeadItems > 0 {
		deadStr = styleBloat.Render(deadStr)
	} else {
		deadStr = styleMuted.Render(deadStr)
	}
	items := fmt.Sprintf("%3dL ", p.LiveItems) + deadStr

	free := styleMuted.Render("—")
	if p.PageSize > 0 {
		pct := float64(p.FreeSize) * 100 / float64(p.PageSize)
		freeStr := fmt.Sprintf("%.0f%%", pct)
		// High free on a leaf/root (data) page reads as bloat — grade it like the
		// heap view's dead% column: packed pages stay cool, sparse pages go red.
		// Internal/deleted pages legitimately have free space, so keep those
		// muted to avoid a false bloat signal.
		if p.Type == "l" || p.Type == "r" {
			free = percentStyle(100 - pct).Render(freeStr)
		} else {
			free = styleMuted.Render(freeStr)
		}
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
		// On an internal page the ctid block is a downlink to a child page —
		// far more useful than "pivot", and it mirrors what ENTER will drill
		// into. The leaf high-key (item #1 of a non-rightmost leaf) is a real
		// separator with no child, so it keeps the "pivot" tag.
		if blk, ok := parseCtidBlock(t.Ctid); ok && pageType == "i" {
			ctidLabel = styleIndexSeg.Render(fmt.Sprintf("→ blk %d", blk))
		} else {
			ctidLabel = styleHeapToastTag.Render("pivot")
		}
	case idxTuplePosting:
		// Surface how many heap tids this one tuple packs (PG13+ dedup).
		if n := postingTupleCount(t.Ctid); n > 0 {
			ctidLabel = styleHeapHot.Render(fmt.Sprintf("posting ×%d", n))
		} else {
			ctidLabel = styleHeapHot.Render("posting")
		}
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
	btOffsetMask       = 0x0FFF // BT_OFFSET_MASK — low bits of a posting tuple's
	// ctid offset word hold its heap-tid count (BTreeTupleGetNPosting).
)

// parseCtidBlock extracts the block number from a "(blk,off)" ctid text. On a
// B-tree internal page this block is the downlink to a child index page; on a
// leaf page it's the heap block. Returns false when ctid is nil/unparseable.
func parseCtidBlock(ctid *string) (int32, bool) {
	if ctid == nil {
		return 0, false
	}
	var blk, off int
	if _, err := fmt.Sscanf(*ctid, "(%d,%d)", &blk, &off); err != nil {
		return 0, false
	}
	_ = off
	return int32(blk), true
}

// postingTupleCount decodes how many heap tids a posting-list tuple packs. The
// count lives in the low bits of the tuple's ctid offset word (ip_posid):
// INDEX_ALT_TID_MASK marks it a posting tuple and BT_OFFSET_MASK selects the
// count (BTreeTupleGetNPosting). Returns 0 when the ctid doesn't parse.
func postingTupleCount(ctid *string) int {
	if ctid == nil {
		return 0
	}
	var blk, off int
	if _, err := fmt.Sscanf(*ctid, "(%d,%d)", &blk, &off); err != nil {
		return 0
	}
	_ = blk
	return off & btOffsetMask
}

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
