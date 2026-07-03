package tui

import (
	"fmt"
	"sort"
	"strconv"
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
	sw := swatch
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "B-tree page reference")

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
	b.WriteString("    " + padRight("level", 10) +
		mu("btpo_level: depth from the leaves — L0 is a leaf, L1 sits one above, etc.") + "\n")
	b.WriteString("    " + padRight("used", 10) +
		mu("BLCKSZ − free_size, i.e. how much of the 8 KiB page is actually populated") + "\n")
	b.WriteString("    " + padRight("avg", 10) +
		mu("avg_item_size: mean bytes per item on the page") + "\n")
	b.WriteString("    " + padRight("live/dead", 10) +
		mu("counts from bt_page_stats — live and LP_DEAD items on this page") + "\n")
	b.WriteString("    " + padRight("free", 10) +
		mu("free_size as a percent of pagesize; high values on leaf pages signal bloat") + "\n")
	b.WriteString("    " + padRight("links", 10) +
		mu("btpo_prev↔btpo_next: the page's left/right siblings on its level (· = an end)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" flags ") + "  " +
		mu("the leftmost ! column flags rare structural states from btpo_flags") + "\n")
	b.WriteString("    " + styleBloat.Render("!") + "  " + padRight("", 6) +
		mu("incomplete split — a page split that never finished; needs cleanup") + "\n")
	b.WriteString("    " + styleBloat.Render("½") + "  " + padRight("", 6) +
		mu("half-dead — page emptied, deletion not yet completed by VACUUM") + "\n")
	b.WriteString("    " + styleMuted.Render("*") + "  " + padRight("", 6) +
		mu("has garbage — carries LP_DEAD items reclaimable on the next vacuum") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" banner ") + "  " +
		mu("the lines above the table summarise the whole index") + "\n")
	b.WriteString("    " + mu("keys: the search columns; ") + styleMuted.Render("include:") +
		mu(" lists covering (stored-but-not-searched) columns") + "\n")
	b.WriteString("    " + mu("root blk / height / dedup / version come from the metapage (bt_metap):") + "\n")
	b.WriteString("    " + mu("height is the tree depth above the leaves; dedup ") + styleBadge.Render("on") +
		mu(" means posting-list dedup is possible") + "\n\n")

	b.WriteString("  " + mu("PgUp/PgDn slides the load window ("+strconv.Itoa(int(heapWindowDefault))+" pages per step).") + "\n")
	b.WriteString("  " + mu("Within a window, j/k or arrows move the cursor; Enter drills into one page's items.") + "\n")
	b.WriteString("  " + mu("Block 0 is the metapage — skipped here; it carries the root pointer, not a tree page.") + "\n")

	return padInfo(&b, height)
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
	infoHeader(&b, "Index-tuple reference")

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
		mu("decoded key from the heap when reachable, else dimmed hex of the raw key bytes") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 8) +
		mu("on internal pages this becomes a ") + styleIndexSeg.Render("low … high") +
		mu(" range: the keys that child block holds") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 8) +
		mu("(") + styleIndexSeg.Render("−∞") + mu(" = leftmost child, ") +
		styleIndexSeg.Render("+∞") + mu(" = rightmost; the range runs to the next downlink's key)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" drilling ") + "  " +
		mu("which rows respond to Enter — and why others don't") + "\n")
	b.WriteString("    " + mu("Leaf entries whose ctid resolves to a live heap row drill into the per-column") + "\n")
	b.WriteString("    " + mu("row view. Internal-page downlinks (") + styleIndexSeg.Render("→ blk N") +
		mu(") descend one level into that child page,") + "\n")
	b.WriteString("    " + mu("so ENTER walks the tree structurally toward the leaves. Pivot high keys,") + "\n")
	b.WriteString("    " + mu("posting tuples, and entries whose heap row was vacuumed since the snapshot") + "\n")
	b.WriteString("    " + mu("don't drill — there's no single heap row to land on.") + "\n\n")
	b.WriteString("    " + mu("Reading bt_page_items / bt_metap needs a superuser (or pg_read_server_files).") + "\n")

	return padInfo(&b, height)
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
			return renderRelationRow(it, r, maxSize, barW, selected, relationBarStyle(r))
		})
}

func renderRelationsHeader(sort sortMode, sortDesc bool, barW int) string {
	// Offset matches the row: cursor(2) + bar+brackets(barW+2) + sep(2).
	line := headerIndent(barW) +
		padRight(sortMark("size", sort == sortBySize, sortDesc), 10) + "  " +
		padRight(sortMark("rows", sort == sortByRows, sortDesc), rowsColW) + "  " +
		padRight("pages", pagesColW) + "  " +
		padRight(sortMark("type", sort == sortByType, sortDesc), relTypeColW) + "  " +
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
	// The type tag is coloured per kind (same hue as the bar) and padded on the
	// raw label so the styling escape codes don't throw off the column width.
	typeStr := relationBarStyle(r).Render(padRight(relationTypeLabel(r), relTypeColW)) + "  "
	childMark := styleMuted.Render("+ ")
	parent := ""
	if r.ParentName != "" {
		parent = "  " + styleMuted.Render("→ "+r.ParentName)
	}
	return cursor + bar + "  " + padRight(sizeStr, 10) + "  " + rowsStr + pagesStr + typeStr + childMark + name + parent
}

// relationTypeLabel is the short kind tag shown in the relations "type" column:
// the storage kind for heap/toast, the access method for an index.
func relationTypeLabel(r pg.Relation) string {
	switch r.Kind {
	case pg.RelBTreeIndex:
		return "btree"
	case pg.RelGist:
		return "gist"
	case pg.RelBrin:
		return "brin"
	case pg.RelGin:
		return "gin"
	case pg.RelToast:
		return "toast"
	default:
		return "heap"
	}
}

// relationBarStyle tints both the row bar and the type tag by relation kind:
// heap cyan, toast white, and a distinct hue per index access method.
func relationBarStyle(r pg.Relation) lipgloss.Style {
	switch r.Kind {
	case pg.RelBTreeIndex:
		return styleIndexSeg
	case pg.RelGist:
		return styleGistSeg
	case pg.RelBrin:
		return styleBrinSeg
	case pg.RelGin:
		return styleGinSeg
	case pg.RelToast:
		return styleToastSeg
	default:
		return styleHeapSeg
	}
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
	// The metapage describes the whole index, so it belongs on the page list,
	// not on a single page's tuple view. Each access method has its own metapage
	// shape (GiST has none).
	if s.level == levelIndexPages {
		var meta string
		switch s.index.AccessMethod {
		case "brin":
			meta = brinMetaLine(s.brinMeta)
		case "gin":
			meta = ginMetaLine(s.ginMeta)
		case "gist":
			meta = "" // GiST has no metapage (block 0 is the root)
		default:
			meta = btreeMetaLine(s.btreeMeta)
		}
		if meta != "" {
			lines = append(lines, meta)
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
	// "!" (flag), avg and links are display-only columns — no sortMark.
	line := headerIndent(barW) +
		padRight("!", idxPageFlagColW) + "  " +
		padRight(sortMark("type", sort == sortByType, sortDesc), idxPageTypeColW) + "  " +
		padRight(sortMark("level", sort == sortByLevel, sortDesc), idxPageLevelColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight("avg", idxPageAvgColW) + "  " +
		padRight(sortMark("live/dead", sort == sortByDeadRatio, sortDesc), idxPageItemsColW) + "  " +
		padRight(sortMark("free", sort == sortByFreeSpace, sortDesc), idxPageFreeColW) + "  " +
		padRight("links", idxPageLinksColW) + "  " +
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
	flag := indexPageFlagGlyph(p.BtpoFlags)
	typ := indexPageTypeStyle(p.Type).Render(indexPageTypeLabel(p.Type))
	lvl := fmt.Sprintf("L%d", p.BtpoLevel)
	used := humanize.Bytes(it.size)

	// avg item size (bytes/item); 0 on meta/deleted pages with no items.
	avg := styleMuted.Render("—")
	if p.AvgItemSize > 0 {
		avg = strconv.Itoa(int(p.AvgItemSize))
	}
	links := siblingLinks(p.BtpoPrev, p.BtpoNext)

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
		padRight(flag, idxPageFlagColW) + "  " +
		padRight(typ, idxPageTypeColW) + "  " +
		padRight(lvl, idxPageLevelColW) + "  " +
		padRight(used, idxPageUsedColW) + "  " +
		padRight(avg, idxPageAvgColW) + "  " +
		padRight(items, idxPageItemsColW) + "  " +
		padRight(free, idxPageFreeColW) + "  " +
		padRight(links, idxPageLinksColW) + "  " +
		name
}

// indexPageFlagGlyph renders a single priority-ordered glyph from a B-tree
// page's btpo_flags, mirroring the heap-pages "!" column. Only the rare
// structural anomalies get a glyph; an ordinary page stays blank. Priority:
// incomplete-split and half-dead (real "cleanup pending" signals) outrank
// has-garbage, which the dead-items column already conveys.
func indexPageFlagGlyph(flags int32) string {
	switch {
	case flags&btpoIncompleteSplit != 0:
		return styleBloat.Render("!")
	case flags&btpoHalfDead != 0:
		return styleBloat.Render("½")
	case flags&btpoHasGarbage != 0:
		return styleMuted.Render("*")
	}
	return " "
}

// siblingLinks formats a B-tree page's left/right links (btpo_prev / btpo_next)
// as "prev↔next". pageinspect returns P_NONE (InvalidBlockNumber, 0xFFFFFFFF)
// as -1 after the ::int cast, so any negative value is a chain end (the
// leftmost / rightmost page on its level) and renders as "·".
func siblingLinks(prev, next int32) string {
	end := func(blk int32) string {
		if blk < 0 {
			return "·"
		}
		return strconv.Itoa(int(blk))
	}
	return styleMuted.Render(end(prev) + "↔" + end(next))
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
	// On an internal page each entry is a downlink whose key is the *lowest*
	// key in that child block; the block therefore covers [thisKey, nextKey).
	// Precompute that range per item so the key column can show what range of
	// keys lives behind each "→ blk N", rather than the bare separator key.
	ranges := internalDownlinkRanges(s.items, pageType, s.indexKeyCols, keyW)
	return m.renderRowList(s, height, renderIndexTuplesHeader(s.sort, s.sortDesc, pageType),
		func(it item, selected bool) string {
			t, _ := it.data.(pg.IndexTuple)
			return renderIndexTupleRow(t, pageType, ranges[t.ItemOffset], s.indexKeyCols, keyW, selected)
		})
}

func renderIndexTuplesHeader(sort sortMode, sortDesc bool, pageType string) string {
	keyCol := "key"
	if pageType == "i" {
		// Internal-page entries are downlinks; the column shows the key range
		// each child block covers, not a single heap key.
		keyCol = "key range (covered by child block)"
	}
	line := "  " + padRight(sortMark("off", sort == sortByLP, sortDesc), idxTupleOffColW) + "  " +
		padRight(sortMark("len", sort == sortBySize, sortDesc), idxTupleLenColW) + "  " +
		padRight("flags", idxTupleFlagsColW) + "  " +
		padRight("ctid", idxTupleCtidColW) + "  " +
		keyCol
	return styleMuted.Render(line)
}

func renderIndexTupleRow(t pg.IndexTuple, pageType, blockRange string, cols []pg.IndexKeyColumn, keyW int, selected bool) string {
	cursor := selectedCursor(selected)
	off := fmt.Sprintf("#%04d", t.ItemOffset)
	if selected {
		off = styleSelected.Render(off)
	}
	lenStr := strconv.Itoa(int(t.ItemLen))
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
	case blockRange != "":
		// Internal-page downlink: show the key range this child block covers
		// (precomputed from the neighbouring separators) instead of the bare
		// lower-bound separator key.
		key = blockRange
	case t.Decoded != nil:
		key = truncateValue(t.Decoded, keyW)
	case t.Data != nil && kind == idxTupleNormal:
		// Normal leaf entry whose ctid didn't resolve to a live heap row: the
		// row was deleted/updated since the last VACUUM. Decode the raw key
		// bytes type-aware when possible, else fall back to the hex.
		if s, ok := indexKeyText(t, cols); ok {
			key = deadTag + styleMuted.Render(truncateToWidth(s, keyW-5))
		} else {
			key = deadTag + styleMuted.Render(truncateValue(t.Data, keyW-5))
		}
	case t.Data != nil:
		if s, ok := indexKeyText(t, cols); ok {
			key = styleMuted.Render(truncateToWidth(s, keyW))
		} else {
			key = styleMuted.Render(truncateValue(t.Data, keyW))
		}
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

// btpo_flags bits from src/include/access/nbtree.h, decoded by
// indexPageFlagGlyph for the page-list "!" column. BTP_LEAF/ROOT/DELETED/META
// are already conveyed by the type tag, so only the diagnostic bits are named.
const (
	btpoHalfDead        = 0x10 // BTP_HALF_DEAD — emptied, deletion not yet finished
	btpoHasGarbage      = 0x40 // BTP_HAS_GARBAGE — has LP_DEAD items reclaimable on vacuum
	btpoIncompleteSplit = 0x80 // BTP_INCOMPLETE_SPLIT — split not yet completed
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

// internalDownlinkRanges computes, for each downlink on a B-tree internal
// page, the key range its child block covers: [thisSeparator, nextSeparator).
// nbtree stores downlinks in key order by offset, with two structural
// exceptions handled here: item #1 (offset 1) of a non-rightmost page is the
// page high key — the page's own upper bound, not a child range — and the
// leftmost downlink's key is truncated to "minus infinity" (no key bytes).
// The result is keyed by ItemOffset so the renderer can look each row up
// regardless of the active display sort. Returns nil for non-internal pages.
func internalDownlinkRanges(items []item, pageType string, cols []pg.IndexKeyColumn, keyW int) map[int32]string {
	if pageType != "i" {
		return nil
	}
	type entry struct {
		off    int32
		text   string
		hasKey bool
	}
	var all []entry
	for _, it := range items {
		t, ok := it.data.(pg.IndexTuple)
		if !ok {
			continue
		}
		text, hasKey := indexKeyText(t, cols)
		all = append(all, entry{t.ItemOffset, text, hasKey})
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].off < all[j].off })

	// The page high key sits at offset 1 and carries a real key; the leftmost
	// (minus-infinity) downlink carries none. So offset 1 is the high key only
	// when it has a key AND some keyless downlink follows it — on a rightmost
	// page there's no high key and offset 1 is the minus-infinity downlink, so
	// the range simply runs up to +∞.
	pageUpper := "+∞"
	downlinks := all
	if all[0].hasKey {
		for _, e := range all[1:] {
			if !e.hasKey {
				pageUpper = all[0].text
				downlinks = all[1:]
				break
			}
		}
	}

	side := max((keyW-5)/2, 6) // the "  …  " separator eats ~5 cols
	clip := func(s string) string { return truncateToWidth(s, side) }

	out := make(map[int32]string, len(downlinks))
	for i, d := range downlinks {
		lower := "−∞"
		if d.hasKey {
			lower = clip(d.text)
		}
		upper := pageUpper
		switch {
		case i+1 < len(downlinks):
			upper = clip(downlinks[i+1].text) // next separator (always keyed)
		case upper != "+∞":
			upper = clip(upper)
		}
		out[d.off] = styleMuted.Render(lower + "  …  " + upper)
	}
	return out
}

// indexKeyText returns a readable form of a tuple's key and whether it carries
// one at all. A decoded heap projection wins; otherwise the raw hex `data` is
// decoded type-aware against the index's key columns (see decodeIndexKey) —
// used on internal-page separators and dead leaf entries where no heap
// projection exists. With no column types available it falls back to the
// printable-ASCII heuristic (decodeHexKey), then to the raw hex verbatim. An
// empty/absent key — the minus-infinity leftmost downlink — reports
// hasKey == false.
func indexKeyText(t pg.IndexTuple, cols []pg.IndexKeyColumn) (string, bool) {
	if t.Decoded != nil && *t.Decoded != "" {
		return *t.Decoded, true
	}
	if t.Data == nil {
		return "", false
	}
	if s, ok := decodeIndexKey(*t.Data, cols); ok {
		return s, true
	}
	if s, ok := decodeHexKey(*t.Data); ok {
		return s, true
	}
	if raw := strings.TrimSpace(*t.Data); raw != "" {
		return raw, true
	}
	return "", false
}

// firstKeyColName returns the index's leading key column's definition (the
// column the seek feature compares against), or "" when no key columns are known.
func firstKeyColName(cols []pg.IndexKeyColumn) string {
	for _, c := range cols {
		if c.IsKey {
			return c.Def
		}
	}
	return ""
}

// decodeHexKey turns pageinspect's space-separated hex `data`
// (e.g. "2f 75 73 65 72") into readable text for the common single-text-column
// key. It strips a leading 1-byte short-varlena length header and trailing NUL
// padding, then accepts the result only when every remaining byte is printable
// ASCII; otherwise ok == false so the caller keeps the raw hex (int, composite
// and binary keys stay hex). A pageinspect "…" truncation marker ends parsing.
func decodeHexKey(hex string) (string, bool) {
	var b []byte
	for f := range strings.FieldsSeq(hex) {
		if strings.ContainsRune(f, '…') {
			break
		}
		if len(f) != 2 {
			return "", false
		}
		v, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			return "", false
		}
		b = append(b, byte(v))
	}
	if len(b) == 0 {
		return "", false
	}
	// Short varlena: the low bit of the first byte marks a 1-byte length header
	// (VARATT_IS_1B); drop it so the length isn't rendered as a character.
	if b[0]&1 == 1 && b[0] > 1 {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == 0x00 {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "", false
	}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return "", false
		}
	}
	return string(b), true
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
//
// Superseded on the heap-tuples headline by the higher-signal `state` verdict
// (heapTupleState) + lifecycle sentence; kept here, disabled, in case a future
// "raw bits" detail toggle wants the full badge decode back.
/*
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
*/

// ─── GiST page inspector ─────────────────────────────────────────────────────

// renderGistPagesList draws one row per GiST page. GiST has no tree-level
// column (its pages aren't strictly leveled like a B-tree); the columns are
// type (leaf/intr/del), used bytes, item count, free %, block.
func (m *Model) renderGistPagesList(s *screen, height int) string {
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderGistPagesHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			p, _ := it.data.(pg.GistPageStat)
			return renderGistPageRow(it, p, barW, selected)
		})
}

func renderGistPagesHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("type", sort == sortByType, sortDesc), idxPageTypeColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight("items", idxPageItemsColW) + "  " +
		padRight(sortMark("free", sort == sortByFreeSpace, sortDesc), idxPageFreeColW) + "  " +
		sortMark("page", sort == sortByBlkno, sortDesc)
	return styleMuted.Render(line)
}

func gistPageTypeLabel(p pg.GistPageStat) string {
	return gistPageRole(p.IsLeaf, p.IsDeleted)
}

func gistPageTypeStyle(p pg.GistPageStat) lipgloss.Style {
	switch {
	case p.IsDeleted:
		return styleBloat
	case !p.IsLeaf:
		return styleBarAlt // internal pages take the accent hue
	}
	return styleMuted // leaf stays muted (the common case)
}

func renderGistPageRow(it item, p pg.GistPageStat, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderSolidBar(it.size, heapPageBlockSize, barW, styleGistSeg)
	typ := gistPageTypeStyle(p).Render(gistPageTypeLabel(p))
	used := humanize.Bytes(it.size)
	items := styleMuted.Render(strconv.Itoa(int(p.Items)))
	free := styleMuted.Render("—")
	if p.PageSize > 0 {
		pct := float64(p.FreeSize) * 100 / float64(p.PageSize)
		freeStr := fmt.Sprintf("%.0f%%", pct)
		if p.IsLeaf {
			free = percentStyle(100 - pct).Render(freeStr)
		} else {
			free = styleMuted.Render(freeStr)
		}
	}
	name := highlightName(it.name, selected)
	return cursor + bar + "  " +
		padRight(typ, idxPageTypeColW) + "  " +
		padRight(used, idxPageUsedColW) + "  " +
		padRight(items, idxPageItemsColW) + "  " +
		padRight(free, idxPageFreeColW) + "  " +
		name
}

// renderGistTuplesList draws one row per item on a GiST page: offset, len, a
// dead flag, the ctid (heap tid on a leaf, "→ blk N" downlink on an internal
// page), and pageinspect's opclass-decoded keys.
func (m *Model) renderGistTuplesList(s *screen, height int) string {
	const deadColW = 4
	keyW := max(m.width-(colCursor+idxTupleOffColW+colGutter+
		idxTupleLenColW+colGutter+deadColW+colGutter+
		idxTupleCtidColW+colGutter+4), 16)
	internal := s.indexPageType == "intr"
	return m.renderRowList(s, height, renderGistTuplesHeader(s.sort, s.sortDesc, internal),
		func(it item, selected bool) string {
			t, _ := it.data.(pg.GistItem)
			return renderGistTupleRow(t, internal, keyW, selected)
		})
}

func renderGistTuplesHeader(sort sortMode, sortDesc bool, internal bool) string {
	keyCol := "keys"
	if internal {
		keyCol = "keys (child bounding predicate)"
	}
	line := "  " + padRight(sortMark("off", sort == sortByLP, sortDesc), idxTupleOffColW) + "  " +
		padRight(sortMark("len", sort == sortBySize, sortDesc), idxTupleLenColW) + "  " +
		padRight("dead", 4) + "  " +
		padRight("ctid", idxTupleCtidColW) + "  " +
		keyCol
	return styleMuted.Render(line)
}

func renderGistTupleRow(t pg.GistItem, internal bool, keyW int, selected bool) string {
	cursor := selectedCursor(selected)
	off := fmt.Sprintf("#%04d", t.ItemOffset)
	if selected {
		off = styleSelected.Render(off)
	}
	lenStr := strconv.Itoa(int(t.ItemLen))
	dead := styleMuted.Render("—")
	if t.Dead {
		dead = styleBloat.Render("dead")
	}
	var ctidLabel string
	switch {
	case internal:
		if blk, ok := parseCtidBlock(t.Ctid); ok {
			ctidLabel = styleGistSeg.Render(fmt.Sprintf("→ blk %d", blk))
		} else {
			ctidLabel = styleMuted.Render("—")
		}
	case t.Ctid != nil:
		ctidLabel = *t.Ctid
	default:
		ctidLabel = styleMuted.Render("—")
	}
	key := styleMuted.Render("—")
	if t.Keys != nil && *t.Keys != "" {
		key = truncateValue(t.Keys, keyW)
	}
	return cursor + padRight(off, idxTupleOffColW) + "  " +
		padRight(lenStr, idxTupleLenColW) + "  " +
		padRight(dead, 4) + "  " +
		padRight(ctidLabel, idxTupleCtidColW) + "  " +
		key
}

// ─── BRIN page inspector ─────────────────────────────────────────────────────

// brinMetaLine renders the BRIN metapage summary above the page list: the
// heap-block span each summary covers (pages/range), the on-disk version, the
// last revmap block, and the magic.
func brinMetaLine(meta *pg.BrinMeta) string {
	if meta == nil {
		return ""
	}
	mu := styleMuted.Render
	return "  " + mu(fmt.Sprintf("pages/range %d", meta.PagesPerRange)) + mu("  ·  ") +
		mu(fmt.Sprintf("v%d", meta.Version)) + mu("  ·  ") +
		mu(fmt.Sprintf("last revmap blk %d", meta.LastRevmapPage)) + mu("  ·  ") +
		mu("magic "+meta.Magic)
}

// renderBrinPagesList draws one row per BRIN page: type (meta/revmap/regular),
// used bytes, free %, block. Item counts are omitted (see BrinPageStat).
func (m *Model) renderBrinPagesList(s *screen, height int) string {
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderBrinPagesHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			p, _ := it.data.(pg.BrinPageStat)
			return renderBrinPageRow(it, p, barW, selected)
		})
}

func renderBrinPagesHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("type", sort == sortByType, sortDesc), brinPageTypeColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight(sortMark("free", sort == sortByFreeSpace, sortDesc), idxPageFreeColW) + "  " +
		sortMark("page", sort == sortByBlkno, sortDesc)
	return styleMuted.Render(line)
}

func brinPageTypeStyle(t string) lipgloss.Style {
	switch t {
	case "meta", "revmap":
		return styleBarAlt // structural pages take the accent hue
	}
	return styleMuted // regular (data) pages stay muted — the common case
}

func renderBrinPageRow(it item, p pg.BrinPageStat, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderSolidBar(it.size, heapPageBlockSize, barW, styleBrinSeg)
	typ := brinPageTypeStyle(p.PageType).Render(p.PageType)
	used := humanize.Bytes(it.size)
	free := styleMuted.Render("—")
	if p.PageSize > 0 {
		pct := float64(p.FreeSize) * 100 / float64(p.PageSize)
		free = styleMuted.Render(fmt.Sprintf("%.0f%%", pct))
	}
	name := highlightName(it.name, selected)
	return cursor + bar + "  " +
		padRight(typ, brinPageTypeColW) + "  " +
		padRight(used, idxPageUsedColW) + "  " +
		padRight(free, idxPageFreeColW) + "  " +
		name
}

// renderBrinTuplesList draws one row per BRIN summary tuple: the heap block range
// it covers, the indexed attribute, the null/placeholder/empty flags, and the
// opclass-rendered summary value. The range end is shown when pages-per-range is
// known (carried down from the page list's metapage).
func (m *Model) renderBrinTuplesList(s *screen, height int) string {
	ppr := int64(0)
	if s.brinMeta != nil {
		ppr = int64(s.brinMeta.PagesPerRange)
	}
	const offW, blkW, attW, flagsW = 6, 18, 6, 10
	valW := max(m.width-(colCursor+offW+colGutter+blkW+colGutter+
		attW+colGutter+flagsW+colGutter+2), 16)
	return m.renderRowList(s, height, renderBrinTuplesHeader(s.sort, s.sortDesc, offW, blkW, attW, flagsW),
		func(it item, selected bool) string {
			t, _ := it.data.(pg.BrinItem)
			return renderBrinTupleRow(t, ppr, valW, offW, blkW, attW, flagsW, selected)
		})
}

func renderBrinTuplesHeader(sort sortMode, sortDesc bool, offW, blkW, attW, flagsW int) string {
	line := "  " + padRight(sortMark("off", sort == sortByLP, sortDesc), offW) + "  " +
		padRight(sortMark("block range", sort == sortBySize, sortDesc), blkW) + "  " +
		padRight("att", attW) + "  " +
		padRight("flags", flagsW) + "  " +
		"summary value"
	return styleMuted.Render(line)
}

func renderBrinTupleRow(t pg.BrinItem, ppr int64, valW, offW, blkW, attW, flagsW int, selected bool) string {
	cursor := selectedCursor(selected)
	off := fmt.Sprintf("#%04d", t.ItemOffset)
	if selected {
		off = styleSelected.Render(off)
	}
	blk := strconv.FormatInt(t.BlockNum, 10)
	if ppr > 0 {
		blk = fmt.Sprintf("%d…%d", t.BlockNum, t.BlockNum+ppr-1)
	}
	att := fmt.Sprintf("att%d", t.AttNum)
	flags := brinFlagTags(t)
	val := styleMuted.Render("—")
	switch {
	case t.AllNulls:
		val = styleMuted.Render("(all nulls)")
	case t.Value != nil && *t.Value != "":
		val = truncateValue(t.Value, valW)
	}
	return cursor + padRight(off, offW) + "  " +
		padRight(styleIndexSeg.Render(blk), blkW) + "  " +
		padRight(att, attW) + "  " +
		padRight(flags, flagsW) + "  " +
		val
}

// brinFlagTags renders a BRIN summary tuple's boolean flags as compact badges.
func brinFlagTags(t pg.BrinItem) string {
	var parts []string
	if t.HasNulls {
		parts = append(parts, styleBadge.Render("N"))
	}
	if t.Placeholder {
		parts = append(parts, styleHeapToastTag.Render("P"))
	}
	if t.Empty != nil && *t.Empty {
		parts = append(parts, styleMuted.Render("E"))
	}
	if len(parts) == 0 {
		return styleMuted.Render("—")
	}
	return strings.Join(parts, " ")
}

// ─── GIN page inspector ──────────────────────────────────────────────────────

// ginMetaLine renders the GIN metapage summary above the page list: entry-tree
// and posting-tree page counts, total entries, pending-list size, version.
func ginMetaLine(meta *pg.GinMeta) string {
	if meta == nil {
		return ""
	}
	mu := styleMuted.Render
	out := "  " + mu(fmt.Sprintf("entries %d", meta.Entries)) + mu("  ·  ") +
		mu(fmt.Sprintf("entry pages %d", meta.EntryPages)) + mu("  ·  ") +
		mu(fmt.Sprintf("data pages %d", meta.DataPages))
	if meta.PendingPages > 0 {
		out += mu("  ·  ") + styleHeapToastTag.Render(fmt.Sprintf("pending %d", meta.PendingPages))
	}
	out += mu(fmt.Sprintf("  ·  v%d", meta.Version))
	return out
}

// ginPageTypeLabel maps a GIN page's opaque flags to a short role tag.
func ginPageTypeLabel(flags string) string {
	switch {
	case strings.Contains(flags, "meta"):
		return "meta"
	case ginPageIsDataLeaf(flags):
		return "data-leaf"
	case strings.Contains(flags, "data"):
		return "data"
	default:
		return "entry"
	}
}

func ginPageTypeStyle(flags string) lipgloss.Style {
	switch {
	case strings.Contains(flags, "meta"):
		return styleBarAlt
	case ginPageIsDataLeaf(flags):
		return styleMuted // the drillable common case
	default:
		return styleBarAlt // entry-tree / non-leaf data pages
	}
}

// renderGinPagesList draws one row per GIN page: type tag (from opaque flags),
// item count (maxoff), used bytes, free %, block. Only data-leaf pages drill.
func (m *Model) renderGinPagesList(s *screen, height int) string {
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderGinPagesHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			p, _ := it.data.(pg.GinPageStat)
			return renderGinPageRow(it, p, barW, selected)
		})
}

func renderGinPagesHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("type", sort == sortByType, sortDesc), ginPageTypeColW) + "  " +
		padRight("items", idxPageItemsColW) + "  " +
		padRight(sortMark("used", sort == sortBySize, sortDesc), idxPageUsedColW) + "  " +
		padRight(sortMark("free", sort == sortByFreeSpace, sortDesc), idxPageFreeColW) + "  " +
		sortMark("page", sort == sortByBlkno, sortDesc)
	return styleMuted.Render(line)
}

func renderGinPageRow(it item, p pg.GinPageStat, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderSolidBar(it.size, heapPageBlockSize, barW, styleGinSeg)
	typ := ginPageTypeStyle(p.Flags).Render(ginPageTypeLabel(p.Flags))
	items := styleMuted.Render(strconv.Itoa(int(p.MaxOff)))
	used := humanize.Bytes(it.size)
	free := styleMuted.Render("—")
	if p.PageSize > 0 {
		pct := float64(p.FreeSize) * 100 / float64(p.PageSize)
		free = styleMuted.Render(fmt.Sprintf("%.0f%%", pct))
	}
	name := highlightName(it.name, selected)
	return cursor + bar + "  " +
		padRight(typ, ginPageTypeColW) + "  " +
		padRight(items, idxPageItemsColW) + "  " +
		padRight(used, idxPageUsedColW) + "  " +
		padRight(free, idxPageFreeColW) + "  " +
		name
}

// renderGinTuplesList draws one row per posting-list segment on a GIN data-leaf
// page: the segment's first heap tid, its compressed byte size, the number of
// TIDs it packs, and a sample of those TIDs.
func (m *Model) renderGinTuplesList(s *screen, height int) string {
	const tidW, bytesW, countW = 14, 8, 8
	sampleW := max(m.width-(colCursor+tidW+colGutter+bytesW+colGutter+
		countW+colGutter+2), 16)
	return m.renderRowList(s, height, renderGinTuplesHeader(s.sort, s.sortDesc, tidW, bytesW, countW),
		func(it item, selected bool) string {
			t, _ := it.data.(pg.GinItem)
			return renderGinTupleRow(t, sampleW, tidW, bytesW, countW, selected)
		})
}

func renderGinTuplesHeader(sort sortMode, sortDesc bool, tidW, bytesW, countW int) string {
	line := "  " + padRight("first tid", tidW) + "  " +
		padRight(sortMark("bytes", sort == sortBySize, sortDesc), bytesW) + "  " +
		padRight("tids", countW) + "  " +
		"heap tids (sample)"
	return styleMuted.Render(line)
}

func renderGinTupleRow(t pg.GinItem, sampleW, tidW, bytesW, countW int, selected bool) string {
	cursor := selectedCursor(selected)
	tid := t.FirstTid
	if selected {
		tid = styleSelected.Render(tid)
	}
	bytesStr := humanize.Bytes(int64(t.NBytes))
	count := styleMuted.Render(fmt.Sprintf("×%d", t.TidCount))
	sample := t.TidsText
	if t.TidCount > 20 && sample != "" {
		sample += " …"
	}
	sample = styleMuted.Render(truncateToWidth(sample, sampleW))
	return cursor + padRight(tid, tidW) + "  " +
		padRight(bytesStr, bytesW) + "  " +
		padRight(count, countW) + "  " +
		sample
}

// ─── per-AM ? reference overlays ─────────────────────────────────────────────

func (m *Model) renderGistInfo(height int, tuples bool) string {
	mu := styleMuted.Render
	var b strings.Builder
	if tuples {
		infoHeader(&b, "GiST item reference")
		b.WriteString("  " + mu("Each row is one entry on a GiST page (gist_page_items).") + "\n\n")
		b.WriteString("    " + padRight("keys", 8) + mu("the opclass-decoded key pageinspect renders directly (e.g. a bounding box) —") + "\n")
		b.WriteString("    " + strings.Repeat(" ", 8) + mu("on internal pages it's the bounding predicate covering the child block") + "\n")
		b.WriteString("    " + padRight("ctid", 8) + mu("heap pointer on a leaf, ") + styleGistSeg.Render("→ blk N") + mu(" downlink on an internal page") + "\n")
		b.WriteString("    " + padRight("dead", 8) + mu("entry marked dead (reclaimable on the next vacuum)") + "\n\n")
		b.WriteString("  " + mu("Enter descends an internal downlink toward the leaves, or opens the heap row a") + "\n")
		b.WriteString("  " + mu("leaf entry points at. GiST keys have no total order, so there's no key-seek —") + "\n")
		b.WriteString("  " + mu("use the ") + styleBadge.Render("/") + mu(" filter to search the rendered keys text.") + "\n")
		return padInfo(&b, height)
	}
	infoHeader(&b, "GiST page reference")
	b.WriteString("  " + mu("GiST has no metapage — block 0 is the root, so every page is browsable.") + "\n\n")
	b.WriteString("    " + padRight("type", 8) + styleMuted.Render("leaf") + mu(" data page  ·  ") + styleBarAlt.Render("intr") + mu(" internal (downlinks)  ·  ") + styleBloat.Render("del") + mu(" deleted") + "\n")
	b.WriteString("    " + padRight("used", 8) + mu("BLCKSZ − free; the bar shows how packed the page is") + "\n")
	b.WriteString("    " + padRight("items", 8) + mu("entry count on the page") + "\n")
	b.WriteString("    " + padRight("free", 8) + mu("free space as a percent of the page") + "\n\n")
	b.WriteString("  " + mu("PgUp/PgDn slides the load window ("+strconv.Itoa(int(heapWindowDefault))+" pages per step); Enter drills a page's items.") + "\n")
	b.WriteString("  " + mu("Reading gist_page_* needs a superuser (or pg_read_server_files).") + "\n")
	return padInfo(&b, height)
}

func (m *Model) renderBrinInfo(height int, tuples bool) string {
	mu := styleMuted.Render
	var b strings.Builder
	if tuples {
		infoHeader(&b, "BRIN range reference")
		b.WriteString("  " + mu("Each row is one summary tuple (brin_page_items): the per-attribute summary for a") + "\n")
		b.WriteString("  " + mu("range of heap blocks. A BRIN index stores one summary per pages-per-range span.") + "\n\n")
		b.WriteString("    " + padRight("block range", 12) + mu("the heap blocks this summary covers (start…end)") + "\n")
		b.WriteString("    " + padRight("att", 12) + mu("which indexed attribute this summary is for") + "\n")
		b.WriteString("    " + padRight("flags", 12) + styleBadge.Render("N") + mu(" has-nulls  ·  ") + styleHeapToastTag.Render("P") + mu(" placeholder  ·  ") + mu("E empty") + "\n")
		b.WriteString("    " + padRight("summary", 12) + mu("the opclass-rendered summary value (e.g. a min…max range)") + "\n\n")
		b.WriteString("  " + mu("Press ") + styleBadge.Render("s") + mu(" to seek to the range covering a heap block number.") + "\n")
		b.WriteString("  " + mu("Enter jumps to the heap pages of the summarised block range.") + "\n")
		return padInfo(&b, height)
	}
	infoHeader(&b, "BRIN page reference")
	b.WriteString("  " + mu("BRIN pages come in three kinds; the banner above carries the metapage summary.") + "\n\n")
	b.WriteString("    " + styleMuted.Render(padRight("regular", 9)) + mu("holds the range-summary tuples — Enter drills into these") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("revmap", 9)) + mu("range map: points each block range at its summary tuple") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("meta", 9)) + mu("metapage (block 0): pages-per-range, version") + "\n\n")
	b.WriteString("  " + mu("PgUp/PgDn slides the load window; Enter on a regular page lists its summaries.") + "\n")
	b.WriteString("  " + mu("Reading brin_* needs a superuser (or pg_read_server_files).") + "\n")
	return padInfo(&b, height)
}

func (m *Model) renderGinInfo(height int, tuples bool) string {
	mu := styleMuted.Render
	var b strings.Builder
	if tuples {
		infoHeader(&b, "GIN posting-list reference")
		b.WriteString("  " + mu("Each row is one posting-list segment on a compressed data-leaf page") + "\n")
		b.WriteString("  " + mu("(gin_leafpage_items): a run of heap TIDs sharing a starting tid.") + "\n\n")
		b.WriteString("    " + padRight("first tid", 12) + mu("the segment's first heap pointer") + "\n")
		b.WriteString("    " + padRight("bytes", 12) + mu("compressed on-disk size of the segment") + "\n")
		b.WriteString("    " + padRight("tids", 12) + mu("number of heap TIDs packed into the segment") + "\n\n")
		b.WriteString("  " + styleHeapToastTag.Render("Note") + mu(": pageinspect cannot list GIN entry-tree keys, so only data-leaf") + "\n")
		b.WriteString("  " + mu("pages are itemizable. These rows are terminal (no per-row drill).") + "\n")
		return padInfo(&b, height)
	}
	infoHeader(&b, "GIN page reference")
	b.WriteString("  " + mu("A GIN index is an entry tree (keys) over posting trees/lists (heap tids).") + "\n\n")
	b.WriteString("    " + styleMuted.Render(padRight("data-leaf", 10)) + mu("compressed posting lists — the only itemizable pages (Enter drills)") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("data", 10)) + mu("posting-tree internal page") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("entry", 10)) + mu("entry-tree page (keys) — not itemizable via pageinspect") + "\n")
	b.WriteString("    " + styleBarAlt.Render(padRight("meta", 10)) + mu("metapage (block 0): entry/data page counts, pending list") + "\n\n")
	b.WriteString("  " + mu("The banner shows entry/data page counts and pending-list size. PgUp/PgDn slides") + "\n")
	b.WriteString("  " + mu("the window; Enter on a data-leaf page lists its posting segments.") + "\n")
	return padInfo(&b, height)
}
