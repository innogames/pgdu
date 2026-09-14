package tui

import (
	"fmt"
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// onToastTargetResolved completes the "ENTER on a TOASTed value" jump: the
// placeholder heap-pages screen (pushed with only the toast OID known) gets its
// full resolved Table and the window positioned at the chunk's block, and the
// chunk's page is pushed on top with focusLP armed, so the tuple load lands the
// cursor on the chunk row and opens its byte layout — the same landing the
// index-entry drill uses. Back then unwinds chunk → toast pages → the original
// tuple. When the chunks are gone (lp 0: vacuumed or updated since) only the
// page list opens, with a notice. Matches the placeholder by OID via findLevel
// (topmost), so a heap-pages screen for the original table deeper in the stack
// is not disturbed.
func (m *Model) onToastTargetResolved(msg toastTargetResolvedMsg) tea.Cmd {
	s := m.findLevel(levelHeapPages)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	if msg.err != nil {
		s.loading = false
		s.loaded = true
		s.err = msg.err
		return nil
	}
	s.table = msg.table
	s.pages.heapWindowStart = (msg.block / heapWindowDefault) * heapWindowDefault
	pagesCmd := m.loadHeapPagesCmd(s.table, s.pages.heapWindowStart, s.pages.heapWindowCount)
	if msg.lp == 0 {
		m.notice = fmt.Sprintf("toast value %d has no chunks left — showing the relation's pages", msg.chunkID)
		return pagesCmd
	}
	next := &screen{
		level: levelHeapTuples, title: "tuples", tool: s.tool,
		db: s.db, schema: s.schema, table: s.table,
		pages: pageState{heapPageBlkno: msg.block, focusLP: msg.lp},
		sort:  sortByLP, sortDesc: sortByLP.defaultDesc()}
	m.stack = append(m.stack, next)
	return tea.Batch(pagesCmd, m.loadCurrent())
}

// pageTempHint surfaces the non-blocking "install pg_buffercache" hint when
// the temperature side load failed on the missing extension. Anything else
// (privileges, transient errors) stays silent — the temp column just hides.
// Never clobbers an existing prompt; in particular a blocking pageinspect
// prompt must win over this decoration.
func pageTempHint(s *screen, bufsErr error) {
	ext := asMissingExt(bufsErr)
	if ext == nil || s.extPrompt != nil {
		return
	}
	s.extPrompt = &extPrompt{
		name:        extBufferCache,
		db:          ext.DB,
		installable: ext.Installable,
		reason:      extPromptReasonPageTemp,
		blocking:    false,
	}
}

// applyAMPages settles an access-method page-list load on an already
// identity-matched screen: banner data (key columns, per-AM metapage via
// setMeta), the buffer temperatures, and the n pages projected through toItem.
// Banner data rides along with the page list; both are nil on a best-effort
// failure, in which case the banner simply isn't drawn.
func (m *Model) applyAMPages(s *screen, err error, totalPages int32, keyCols []pg.IndexKeyColumn,
	bufs map[int64]pg.PageBuffer, bufsErr error, n int, toItem func(i int) item, setMeta func(*screen)) tea.Cmd {
	if cmd, stop := settleLoad(s, err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.pages.heapPageCount = totalPages
	s.pages.indexKeyCols = keyCols
	if setMeta != nil {
		setMeta(s)
	}
	s.pages.pageBufs = bufs
	pageTempHint(s, bufsErr)
	s.items = s.items[:0]
	for i := range n {
		s.items = append(s.items, toItem(i))
	}
	m.applySort(s)
	return nil
}

// applyAMItems settles an access-method page-items load on an already
// identity-matched levelIndexTuples screen: n entries projected through toItem.
func (m *Model) applyAMItems(s *screen, err error, n int, toItem func(i int) item) tea.Cmd {
	if cmd, stop := settleLoad(s, err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	for i := range n {
		s.items = append(s.items, toItem(i))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onHeapPagesLoaded(msg heapPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapPages)
	if s == nil || s.table.OID != msg.table.OID || s.pages.heapWindowStart != msg.start {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.pages.heapPageCount = msg.totalPages
	s.pages.pageBufs = msg.bufs
	pageTempHint(s, msg.bufsErr)
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, withPageBuf(heapPageToItem(p), msg.bufs))
	}
	m.applySort(s)
	return nil
}

// onToastValueLoaded lands the reassembled TOAST value behind the chunk_data
// value pane. The overlay it belongs to lives on the topmost levelHeapTuples
// screen; the OID + chunk_id guard drops a load the user has moved away from
// (overlay closed and reopened on another chunk, or another toast relation).
func (m *Model) onToastValueLoaded(msg toastValueLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	if s == nil || s.table.OID != msg.tableOID || s.pages.toastVal == nil || s.pages.toastVal.chunkID != msg.chunkID {
		return nil
	}
	s.pages.toastVal.loading = false
	s.pages.toastVal.val = msg.val
	s.pages.toastVal.err = msg.err
	return nil
}

func (m *Model) onHeapTuplesLoaded(msg heapTuplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	// A pick toggled again while this load ran makes it stale: the newer load
	// is on its way with the current pick.
	if s == nil || s.table.OID != msg.tableOID || s.pages.heapPageBlkno != msg.blkno || !slices.Equal(msg.pick, s.pages.tuplePick) {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.pages.tuplePKCols = msg.page.PKCols
	s.pages.tupleCols = msg.page.Columns
	s.pages.tupleShown = msg.page.Shown
	if s.pages.tupleCols != nil {
		// Materialise the server-resolved pick (default → the PK names, unknown
		// names gone) so the picker's checkboxes show what is on screen.
		s.pages.tuplePick = msg.page.ShownNames()
	}
	s.items = s.items[:0]
	for _, t := range msg.page.Tuples {
		s.items = append(s.items, heapTupleToItem(t))
	}
	m.applySort(s)
	if lp := s.pages.focusLP; lp != 0 {
		s.pages.focusLP = 0
		return m.focusHeapTuple(s, lp)
	}
	return nil
}

// focusHeapTuple lands the cursor on the line pointer an index entry pointed
// at and opens its byte-layout overlay, as if the user had walked to the row
// and pressed Enter. A REDIRECT is left selected without the overlay (the
// index entry named the chain root; Enter hops to the live tuple), and a line
// pointer that isn't on the page any more — vacuumed since the index page was
// read — just leaves the cursor at the top with a notice.
func (m *Model) focusHeapTuple(s *screen, lp int32) tea.Cmd {
	for vi, idx := range s.visibleIndexes() {
		ht, ok := s.items[idx].data.(pg.HeapTuple)
		if !ok || ht.LP != lp {
			continue
		}
		s.cursor = vi
		if ht.LPFlags != pg.LPNormal || len(ht.Data) == 0 {
			return nil
		}
		return m.openTupleLayout(s, lp)
	}
	m.notice = fmt.Sprintf("line pointer %d is no longer on this page", lp)
	return nil
}

func (m *Model) onTupleAttrsLoaded(msg tupleAttrsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	if s == nil || s.table.OID != msg.tableOID || s.pages.heapPageBlkno != msg.blkno || s.pages.tupleAttrsLP != msg.lp {
		return nil
	}
	s.pages.tupleAttrsLoading = false
	s.pages.tupleAttrs = msg.attrs
	s.pages.tupleAttrsErr = msg.err
	return nil
}

func (m *Model) onRelationsLoaded(msg relationsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelRelations)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, r := range msg.rels {
		s.items = append(s.items, relationToItem(r))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onIndexPagesLoaded(msg indexPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(indexPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.pages.btreeMeta = msg.meta })
}

func (m *Model) onBtreeLevelsLoaded(msg btreeLevelsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID {
		return nil
	}
	s.pages.btreeLevelsLoading = false
	s.pages.btreeLevelsDone = true
	// Best-effort banner decoration: a failure (privileges, census timeout on
	// a very large index) keeps its error for the banner to explain. Except a
	// missing pageinspect — the page list is showing its install prompt for
	// that; leaving the census un-done lets the post-install reload retry it.
	s.pages.btreeLevels = msg.counts
	s.pages.btreeLevelsErr = msg.err
	if msg.err != nil {
		s.pages.btreeLevels = nil
		if asMissingExt(msg.err) != nil {
			s.pages.btreeLevelsDone = false
			s.pages.btreeLevelsErr = nil
		}
	}
	return nil
}

func (m *Model) onIndexTuplesLoaded(msg indexTuplesLoadedMsg) tea.Cmd {
	// Match on (index, block) only — block uniquely identifies the page within
	// an index. The page type isn't part of the identity: a downlink descent
	// pushes the screen with an unknown type and the loader resolves it, so the
	// message's pageType can legitimately differ from the screen's.
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.indexPageBlkno != msg.blkno {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	// Adopt the resolved page type so the renderer's labels (→ blk N / pivot)
	// and the downlink-drill guard see the child page's real role.
	s.pages.indexPageType = msg.pageType
	// The probe (descent path) resolves the depth; -1 means it didn't run, in
	// which case the direct-drill level already set on the screen stands.
	if msg.level >= 0 {
		lv := msg.level
		s.pages.indexPageLevel = &lv
	}
	s.pages.indexTuples = msg.tuples
	m.rebuildIndexTupleItems(s)
	// A seek inherited from the parent page (downlink descent) or left from a
	// previous load of this page re-targets against the fresh rows.
	if s.pages.seekQuery != "" {
		seekApply(s)
	}
	return nil
}

// rebuildIndexTupleItems projects s.pages.indexTuples into rows: one per
// bt_page_items entry, plus one row per packed heap tid under each posting-list
// tuple the user has expanded (Enter toggles it, see togglePosting). Re-sorts,
// since applySort is what keeps members glued below their parent.
func (m *Model) rebuildIndexTupleItems(s *screen) {
	s.items = s.items[:0]
	for _, t := range s.pages.indexTuples {
		s.items = append(s.items, indexTupleToItem(t))
		if !s.pages.postingOpen[t.ItemOffset] {
			continue
		}
		for i, mem := range t.Posting {
			s.items = append(s.items, postingMemberToItem(t, i+1, mem))
		}
	}
	// On an internal page every entry is a downlink Enter descends, except the
	// high key at offset 1 (drillIndexTuple's gate). indexTupleToItem can't see
	// the page role — it flags by heap projection, which downlinks never have —
	// so the drill indicator is fixed up here where the role is known.
	if s.pages.indexPageType == "i" {
		highKey := internalHighKey(s.items, "i", s.pages.indexKeyCols)
		for i := range s.items {
			t, ok := s.items[i].data.(pg.IndexTuple)
			if !ok {
				continue
			}
			s.items[i].hasChildren = !highKey || t.ItemOffset != 1
		}
	}
	m.applySort(s)
}

// togglePosting folds or unfolds the member tids of the posting-list tuple t
// in place, keeping the cursor on t's own row so the list doesn't jump.
func (m *Model) togglePosting(s *screen, t pg.IndexTuple) {
	if s.pages.postingOpen == nil {
		s.pages.postingOpen = make(map[int32]bool)
	}
	s.pages.postingOpen[t.ItemOffset] = !s.pages.postingOpen[t.ItemOffset]
	m.rebuildIndexTupleItems(s)
	name := indexTupleToItem(t).name
	for pos, idx := range s.visibleIndexes() {
		if s.items[idx].name == name {
			s.cursor = pos
			break
		}
	}
}

func (m *Model) onGistPagesLoaded(msg gistPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(gistPageToItem(msg.pages[i]), msg.bufs) },
		nil)
}

func (m *Model) onGistItemsLoaded(msg gistItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.indexPageBlkno != msg.blkno {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.pages.indexPageType = msg.pageType
	s.items = s.items[:0]
	for _, it := range msg.items {
		s.items = append(s.items, gistItemToItem(it))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBrinPagesLoaded(msg brinPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(brinPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.pages.brinMeta = msg.meta })
}

func (m *Model) onBrinItemsLoaded(msg brinItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.indexPageBlkno != msg.blkno {
		return nil
	}
	return m.applyAMItems(s, msg.err, len(msg.items),
		func(i int) item { return brinItemToItem(msg.items[i]) })
}

func (m *Model) onGinPagesLoaded(msg ginPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.heapWindowStart != msg.start {
		return nil
	}
	cmd := m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(ginPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.pages.ginMeta = msg.meta })
	focusGinBlkno(s)
	return cmd
}

// focusGinBlkno lands the cursor on the page a GIN data-leaf jump asked for,
// once its window has loaded and been sorted. Consumed on the first window
// that arrives so a later PgDn/reload doesn't yank the cursor back.
func focusGinBlkno(s *screen) {
	want := s.pages.focusBlkno
	if want == 0 || !s.loaded {
		return
	}
	s.pages.focusBlkno = 0
	for i, idx := range s.visibleIndexes() {
		if p, ok := s.items[idx].data.(pg.GinPageStat); ok && p.Blkno == want {
			s.cursor = i
			return
		}
	}
}

// onGinDataLeafFound moves the GIN page window to the data-leaf page the
// server-side search returned and marks it for the cursor. A search that
// finds nothing is the normal shape of a GIN whose posting lists all fit
// inline, so it gets an explanatory notice rather than an error.
func (m *Model) onGinDataLeafFound(msg ginDataLeafFoundMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.pages.index.OID != msg.indexOID {
		return nil
	}
	s.pages.ginSeeking = false
	if msg.err != nil {
		m.notice = "data-leaf search failed: " + msg.err.Error()
		return nil
	}
	if !msg.found {
		m.notice = "no data-leaf pages: every posting list fits inline in its entry tuple, so pageinspect can't itemize anything in this index"
		return nil
	}
	if cur, ok := s.currentItem(); ok {
		if p, ok := cur.data.(pg.GinPageStat); ok && p.Blkno == msg.blkno {
			m.notice = fmt.Sprintf("page #%d is the only data-leaf page", msg.blkno)
			return nil
		}
	}
	s.pages.focusBlkno = msg.blkno
	if msg.blkno < s.pages.heapWindowStart || msg.blkno >= s.pages.heapWindowStart+s.pages.heapWindowCount {
		s.pages.heapWindowStart = msg.blkno
		s.resetCursor()
		return m.loadCurrent()
	}
	focusGinBlkno(s)
	return nil
}

func (m *Model) onGinItemsLoaded(msg ginItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.pages.index.OID != msg.indexOID || s.pages.indexPageBlkno != msg.blkno {
		return nil
	}
	return m.applyAMItems(s, msg.err, len(msg.items),
		func(i int) item { return ginItemToItem(msg.items[i]) })
}
