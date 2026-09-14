package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// drillHeapPage opens the tuple list of the highlighted heap page.
func (m *Model) drillHeapPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.HeapPageStat)
	if !ok {
		return nil
	}
	next := &screen{
		level: levelHeapTuples, title: "tuples", tool: s.tool,
		db: s.db, schema: s.schema, table: s.table,
		pages: pageState{heapPageBlkno: int32(p.Blkno)},
		sort:  sortByLP, sortDesc: sortByLP.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillHeapTuple follows a REDIRECT hop, reassembles a TOAST value, or opens the tuple byte-layout overlay.
func (m *Model) drillHeapTuple(s *screen, cur item) tea.Cmd {
	ht, ok := cur.data.(pg.HeapTuple)
	if !ok {
		return nil
	}
	// REDIRECT hops within the same page: lp_off carries the target's
	// OffsetNumber, so jump the cursor to that lp instead of drilling.
	// Lets the user walk a HOT chain by repeatedly pressing Enter.
	if ht.LPFlags == pg.LPRedirect {
		for vi, idx := range s.visibleIndexes() {
			target, ok := s.items[idx].data.(pg.HeapTuple)
			if ok && target.LP == ht.LPOff {
				s.cursor = vi
				break
			}
		}
		return nil
	}
	if ht.LPFlags != pg.LPNormal || ht.Ctid == nil {
		return nil
	}
	// TOAST chunk rows: reassemble the full value from all chunks for this
	// chunk_id instead of showing one chunk's raw bytes.
	if ht.ChunkID != nil {
		next := &screen{
			level: levelTupleRow, title: "toast value", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			pages: pageState{toastChunkID: *ht.ChunkID},
			sort:  sortByName, sortDesc: false}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	if s.table.Schema == "pg_toast" {
		// A toast page whose LP didn't resolve to a chunk row (dead but
		// stored) — the classic ctid row view still applies.
		next := &screen{
			level: levelTupleRow, title: "row", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			pages: pageState{tupleCtid: *ht.Ctid},
			sort:  sortByName, sortDesc: false}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	// Regular heap tuples open the byte-layout overlay in place instead of
	// drilling — it shows the decoded values *and* their physical layout.
	if len(ht.Data) == 0 {
		m.notice = "no tuple body on this line pointer"
		return nil
	}
	return m.openTupleLayout(s, ht.LP)
}

// drillRelation opens the heap or index page list of the highlighted relation.
func (m *Model) drillRelation(s *screen, cur item) tea.Cmd {
	r, ok := cur.data.(pg.Relation)
	if !ok {
		return nil
	}
	switch r.Kind {
	case pg.RelTable, pg.RelToast:
		// Build the heap context the heap-pages flow expects. relpages
		// is filled in by loadHeapPagesCmd, so the EstRows here is
		// purely cosmetic for downstream views — fine to carry over.
		// For RelToast, Schema is "pg_toast" so the loaders use the
		// correct namespace when building the regclass.
		t := pg.Table{
			DB: r.DB, Schema: r.Schema, OID: r.OID, Name: r.Name,
			HeapBytes: r.SizeBytes, EstRows: r.EstRows,
		}
		title := "heap pages"
		if r.Kind == pg.RelToast {
			title = "toast pages"
		}
		next := &screen{
			level: levelHeapPages, title: title, tool: s.tool,
			db: t.DB, schema: t.Schema, table: t,
			pages: pageState{heapWindowStart: 0, heapWindowCount: heapWindowDefault},
			sort:  sortByBlkno, sortDesc: sortByBlkno.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case pg.RelBTreeIndex, pg.RelGist, pg.RelBrin, pg.RelGin:
		// Every drillable index AM uses the shared levelIndexPages screen;
		// the loader/renderer branch on r.AccessMethod from here on.
		// B-trees open level-first so the root sits at the top (read the tree
		// top-down); GiST/BRIN/GIN have no meaningful tree level, so a
		// level sort there would be an inert no-op — keep them block-ordered.
		pageSort := sortByBlkno
		if r.Kind == pg.RelBTreeIndex {
			pageSort = sortByLevel
		}
		next := &screen{
			level: levelIndexPages, title: "index pages", tool: s.tool,
			db: r.DB, schema: r.Schema, pages: pageState{index: r, heapWindowStart: 0, heapWindowCount: heapWindowDefault},
			sort: pageSort, sortDesc: pageSort.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	return nil
}

// drillIndexPage opens the highlighted index page's entries (AM-specific for GiST/BRIN/GIN).
func (m *Model) drillIndexPage(s *screen, cur item) tea.Cmd {
	switch s.pages.index.AccessMethod {
	case "gist":
		return m.drillGistPage(s, cur)
	case "brin":
		return m.drillBrinPage(s, cur)
	case "gin":
		return m.drillGinPage(s, cur)
	}
	p, ok := cur.data.(pg.IndexPageStat)
	if !ok {
		return nil
	}
	lvl := p.BtpoLevel
	next := indexTuplesScreen(s, "index tuples", p.Blkno, indexTuplePageType(p.Type, p.BtpoLevel))
	next.pages.indexPageLevel = &lvl
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillIndexTuple descends a B-tree downlink or follows a leaf entry to its heap tuple.
func (m *Model) drillIndexTuple(s *screen, cur item) tea.Cmd {
	switch s.pages.index.AccessMethod {
	case "gist":
		return m.drillGistItem(s, cur)
	case "brin":
		return m.drillBrinItem(s, cur)
	case "gin":
		return nil // GIN posting-list segments are terminal
	}
	if mem, ok := cur.data.(postingMember); ok {
		// A posting member is a plain heap pointer: same gate and target as
		// a singleton leaf entry.
		return m.drillIndexLeafEntry(s, mem.tuple)
	}
	t, ok := cur.data.(pg.IndexTuple)
	if !ok {
		return nil
	}
	if len(t.Posting) > 0 {
		// A posting-list tuple has no single heap row: Enter unfolds (or
		// folds) its member tids, each of which then drills on its own.
		m.togglePosting(s, t)
		return nil
	}
	// On an internal page every entry is a downlink: its ctid block names a
	// child index page. Enter descends one level toward the leaves so the
	// user can walk the tree structurally. Leaf high-key pivots live on leaf
	// pages, not here, so this only fires for real downlinks.
	if s.pages.indexPageType == "i" {
		if t.ItemOffset == 1 && internalHighKey(s.items, "i", s.pages.indexKeyCols) {
			m.notice = "high key — this page's upper bound, not a child downlink"
			return nil
		}
		child, ok := downlinkChildBlock(t, s.pages.heapPageCount, s.pages.indexPageBlkno)
		if !ok {
			return nil
		}
		// Child page type is unknown here; leave it empty and let
		// loadIndexTuplesCmd probe it (bt_page_stats) so the decode path
		// and further downlink navigation stay correct mid-descent.
		next := indexTuplesScreen(s, "index tuples", child, "")
		// A seek in progress follows the descent: the child page re-runs it
		// once loaded (onIndexTuplesLoaded), landing the cursor on the entry
		// covering the same key so walking root → leaf is Enter, Enter, Enter.
		next.pages.seekQuery = s.pages.seekQuery
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	return m.drillIndexLeafEntry(s, t)
}

// indexTuplePageType normalizes a B-tree page's bt_page_stats type for the
// tuple view. pageinspect reports the root as 'r' whether it's a single-page
// index (the root is also a leaf — its entries are heap pointers we can decode
// against the table) or the top of a taller tree (the root is internal — its
// entries are downlinks to child index pages). Only the level disambiguates: a
// non-leaf root (level > 0) must be treated exactly like an internal page, or
// its downlink ctids get looked up in the heap and resolve to unrelated rows,
// printing bogus, unsorted "keys". Mapping it to 'i' here flips both the decode
// path (ListIndexTuples skips the heap join) and the renderer (downlink ranges
// and "→ blk N" instead of a fake key column) in one place.
func indexTuplePageType(typ string, level int32) string {
	if typ == "r" && level > 0 {
		return "i"
	}
	return typ
}

// downlinkChildBlock resolves the child index page an internal-page downlink
// points at. On internal B-tree pages the item's ctid block IS the child block
// number (the offset word carries pivot flag bits, which we ignore). Returns
// false when the ctid doesn't parse, points at the meta page / itself, or — when
// the index's page count is known — lands past EOF, so a malformed downlink
// can't send get_raw_page off the end.
func downlinkChildBlock(t pg.IndexTuple, pageCount, current int32) (int32, bool) {
	blk, ok := parseCtidBlock(t.Ctid)
	if !ok || blk <= 0 || blk == current {
		return 0, false
	}
	if pageCount > 0 && blk >= pageCount {
		return 0, false
	}
	return blk, true
}

// indexTuplesScreen builds the per-page items screen every index drill pushes:
// same index/context as the parent page list (the keys banner and page count
// carry over — no refetch), positioned at blkno with the given resolved (or
// to-be-probed, when empty) page type. Callers set per-AM extras
// (indexPageLevel, brinMeta) on the result.
func indexTuplesScreen(s *screen, title string, blkno int32, pageType string) *screen {
	return &screen{
		level: levelIndexTuples, title: title, tool: s.tool,
		db: s.db, schema: s.schema, pages: pageState{index: s.pages.index, indexKeyCols: s.pages.indexKeyCols, heapPageCount: s.pages.heapPageCount, indexPageBlkno: blkno, indexPageType: pageType},
		sort: sortByLP, sortDesc: sortByLP.defaultDesc()}
}

// drillGistPage opens a GiST page's items. Leaf and internal pages both carry
// items worth showing; deleted/empty pages don't drill.
func (m *Model) drillGistPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.GistPageStat)
	if !ok {
		return nil
	}
	if p.IsDeleted {
		m.notice = "deleted page — emptied by VACUUM, nothing to inspect"
		return nil
	}
	if p.Items == 0 {
		m.notice = "empty page — no items to inspect"
		return nil
	}
	m.stack = append(m.stack, indexTuplesScreen(s, "index tuples", p.Blkno, gistPageRole(p.IsLeaf, p.IsDeleted)))
	return m.loadCurrent()
}

// drillGistItem walks a GiST internal-page downlink to its child page, or opens
// the heap row a leaf entry points at (mirrors the B-tree tree-walk).
func (m *Model) drillGistItem(s *screen, cur item) tea.Cmd {
	t, ok := cur.data.(pg.GistItem)
	if !ok {
		return nil
	}
	if s.pages.indexPageType == "intr" {
		child, ok := gistDownlinkChildBlock(t, s.pages.heapPageCount, s.pages.indexPageBlkno)
		if !ok {
			return nil
		}
		// Page type probed by loadGistItemsCmd mid-descent.
		m.stack = append(m.stack, indexTuplesScreen(s, "index tuples", child, ""))
		return m.loadCurrent()
	}
	if t.Dead || t.Ctid == nil {
		return nil
	}
	return m.drillHeapTupleAt(s, *t.Ctid)
}

// drillBrinPage opens a regular BRIN page's range summaries; meta/revmap pages
// have nothing to itemize. Carries the metapage down so the range column and
// block-seek know pages-per-range.
func (m *Model) drillBrinPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.BrinPageStat)
	if !ok {
		return nil
	}
	if p.PageType != "regular" {
		m.notice = p.PageType + " page holds no range summaries — open a regular page to see them"
		return nil
	}
	next := indexTuplesScreen(s, "brin ranges", p.Blkno, "regular")
	next.pages.brinMeta = s.pages.brinMeta
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillBrinItem jumps from a BRIN range summary to the heap pages of the block
// range it covers, positioning the heap-pages window at the range's start.
func (m *Model) drillBrinItem(s *screen, cur item) tea.Cmd {
	t, ok := cur.data.(pg.BrinItem)
	if !ok {
		return nil
	}
	parent := pg.Table{DB: s.db, Schema: s.schema, OID: s.pages.index.ParentOID, Name: s.pages.index.ParentName}
	start := max(int32(t.BlockNum), 0)
	start = (start / heapWindowDefault) * heapWindowDefault // align to the window grid
	next := &screen{
		level: levelHeapPages, title: "heap pages", tool: s.tool,
		db: s.db, schema: s.schema, table: parent,
		pages: pageState{heapWindowStart: start, heapWindowCount: heapWindowDefault},
		sort:  sortByBlkno, sortDesc: sortByBlkno.defaultDesc()}
	m.notice = fmt.Sprintf("heap pages from block %d — BRIN range start", t.BlockNum)
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillIndexLeafEntry opens the heap page behind a B-tree leaf entry — a
// singleton or one posting-list member — with the cursor on its tuple and the
// byte-layout overlay open, so the index → heap hop lands in the page
// inspector rather than a detached column/value list.
func (m *Model) drillIndexLeafEntry(s *screen, t pg.IndexTuple) tea.Cmd {
	if t.Ctid == nil || (t.Decoded == nil && t.HotDecoded == nil) {
		// Leaf entries drill only when a live heap row was projected.
		// Pivot/posting summary entries and vacuumed rows land here too; none
		// has a single heap row to show.
		return nil
	}
	// A HOT-updated row lives at the redirect target, not at the ctid the
	// index entry stores — open the tuple that actually holds the row.
	ctid := *t.Ctid
	if t.Decoded == nil && t.HotCtid != nil {
		ctid = *t.HotCtid
	}
	return m.drillHeapTupleAt(s, ctid)
}

// drillHeapTupleAt pushes the parent table's heap-tuples screen for the page a
// ctid names, asking the load to land on that line pointer (pageState.focusLP).
// Shared by the B-tree and GiST leaf-entry drills.
func (m *Model) drillHeapTupleAt(s *screen, ctid string) tea.Cmd {
	blk, ok := parseCtidBlock(&ctid)
	if !ok {
		return nil
	}
	off, _ := parseCtidOffset(&ctid)
	// The parent's schema matches the index's by Postgres rule — indexes
	// live in the same namespace as their table.
	parent := pg.Table{
		DB: s.db, Schema: s.schema,
		OID: s.pages.index.ParentOID, Name: s.pages.index.ParentName,
	}
	next := &screen{
		level: levelHeapTuples, title: "tuples", tool: s.tool,
		db: s.db, schema: s.schema, table: parent,
		pages: pageState{heapPageBlkno: blk, focusLP: off},
		sort:  sortByLP, sortDesc: sortByLP.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillGinPage opens a GIN data-leaf page's posting-list segments; entry-tree
// and other pages aren't itemizable via pageinspect.
func (m *Model) drillGinPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.GinPageStat)
	if !ok {
		return nil
	}
	if !ginPageIsDataLeaf(p.Flags) {
		// pageinspect can only list compressed data-leaf pages; entry-tree and
		// meta pages aren't itemizable. Point the user at the drillable kind.
		m.notice = "only data-leaf pages are itemizable (pageinspect can't read entry-tree keys) — n jumps to the next one"
		return nil
	}
	m.stack = append(m.stack, indexTuplesScreen(s, "gin posting lists", p.Blkno, "data-leaf"))
	return m.loadCurrent()
}

// gistDownlinkChildBlock resolves the child page a GiST internal-page downlink
// points at (its ctid block). Mirrors downlinkChildBlock's safety guards.
func gistDownlinkChildBlock(t pg.GistItem, pageCount, current int32) (int32, bool) {
	blk, ok := parseCtidBlock(t.Ctid)
	if !ok || blk <= 0 || blk == current {
		return 0, false
	}
	if pageCount > 0 && blk >= pageCount {
		return 0, false
	}
	return blk, true
}
