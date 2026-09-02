package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// onToastTargetResolved completes the "ENTER on a TOASTed value" jump: the
// placeholder heap-pages screen (pushed with only the toast OID known) gets its
// full resolved Table and the window positioned at the chunk's block, then the
// normal heap-pages load runs. Matches the placeholder by OID via findLevel
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
	s.heapWindowStart = (msg.block / heapWindowDefault) * heapWindowDefault
	m.notice = fmt.Sprintf("toast value %d — heap page %d", msg.chunkID, msg.block)
	return m.loadHeapPagesCmd(s.table, s.heapWindowStart, s.heapWindowCount)
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
	s.heapPageCount = totalPages
	s.indexKeyCols = keyCols
	if setMeta != nil {
		setMeta(s)
	}
	s.pageBufs = bufs
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
	if s == nil || s.table.OID != msg.table.OID || s.heapWindowStart != msg.start {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.heapPageCount = msg.totalPages
	s.pageBufs = msg.bufs
	pageTempHint(s, msg.bufsErr)
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, withPageBuf(heapPageToItem(p), msg.bufs))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onTupleRowLoaded(msg tupleRowLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTupleRow)
	if s == nil || s.table.OID != msg.tableOID || s.tupleCtid != msg.ctid {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, c := range msg.cells {
		s.items = append(s.items, tupleCellToItem(c))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onToastValueLoaded(msg toastValueLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTupleRow)
	if s == nil || s.table.OID != msg.tableOID || s.toastChunkID != msg.chunkID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, c := range msg.cells {
		s.items = append(s.items, tupleCellToItem(c))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onHeapTuplesLoaded(msg heapTuplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	if s == nil || s.table.OID != msg.tableOID || s.heapPageBlkno != msg.blkno {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.tuplePKCols = msg.pkCols
	s.items = s.items[:0]
	for _, t := range msg.tuples {
		s.items = append(s.items, heapTupleToItem(t))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onTupleAttrsLoaded(msg tupleAttrsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	if s == nil || s.table.OID != msg.tableOID || s.heapPageBlkno != msg.blkno || s.tupleAttrsLP != msg.lp {
		return nil
	}
	s.tupleAttrsLoading = false
	s.tupleAttrs = msg.attrs
	s.tupleAttrsErr = msg.err
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
	if s == nil || s.index.OID != msg.indexOID || s.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(indexPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.btreeMeta = msg.meta })
}

func (m *Model) onBtreeLevelsLoaded(msg btreeLevelsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.index.OID != msg.indexOID {
		return nil
	}
	s.btreeLevelsLoading = false
	s.btreeLevelsDone = true
	// Best-effort banner decoration: a failure (privileges, census timeout on
	// a very large index) keeps its error for the banner to explain. Except a
	// missing pageinspect — the page list is showing its install prompt for
	// that; leaving the census un-done lets the post-install reload retry it.
	s.btreeLevels = msg.counts
	s.btreeLevelsErr = msg.err
	if msg.err != nil {
		s.btreeLevels = nil
		if asMissingExt(msg.err) != nil {
			s.btreeLevelsDone = false
			s.btreeLevelsErr = nil
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
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	// Adopt the resolved page type so the renderer's labels (→ blk N / pivot)
	// and the downlink-drill guard see the child page's real role.
	s.indexPageType = msg.pageType
	// The probe (descent path) resolves the depth; -1 means it didn't run, in
	// which case the direct-drill level already set on the screen stands.
	if msg.level >= 0 {
		lv := msg.level
		s.indexPageLevel = &lv
	}
	s.items = s.items[:0]
	for _, t := range msg.tuples {
		s.items = append(s.items, indexTupleToItem(t))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onGistPagesLoaded(msg gistPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.index.OID != msg.indexOID || s.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(gistPageToItem(msg.pages[i]), msg.bufs) },
		nil)
}

func (m *Model) onGistItemsLoaded(msg gistItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonPageInspect); stop {
		return cmd
	}
	s.indexPageType = msg.pageType
	s.items = s.items[:0]
	for _, it := range msg.items {
		s.items = append(s.items, gistItemToItem(it))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBrinPagesLoaded(msg brinPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.index.OID != msg.indexOID || s.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(brinPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.brinMeta = msg.meta })
}

func (m *Model) onBrinItemsLoaded(msg brinItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	return m.applyAMItems(s, msg.err, len(msg.items),
		func(i int) item { return brinItemToItem(msg.items[i]) })
}

func (m *Model) onGinPagesLoaded(msg ginPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.index.OID != msg.indexOID || s.heapWindowStart != msg.start {
		return nil
	}
	return m.applyAMPages(s, msg.err, msg.totalPages, msg.keyCols, msg.bufs, msg.bufsErr,
		len(msg.pages),
		func(i int) item { return withPageBuf(ginPageToItem(msg.pages[i]), msg.bufs) },
		func(s *screen) { s.ginMeta = msg.meta })
}

func (m *Model) onGinItemsLoaded(msg ginItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	return m.applyAMItems(s, msg.err, len(msg.items),
		func(i int) item { return ginItemToItem(msg.items[i]) })
}

func (m *Model) onWALOverviewLoaded(msg walOverviewLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.walStart = msg.start
	s.walEnd = msg.end
	s.items = s.items[:0]
	for _, st := range msg.stats {
		s.items = append(s.items, walRmgrToItem(st))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALSummaryLoaded(msg walSummaryLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	// Summary failure is non-fatal: the header sources (pg_ls_waldir /
	// pg_stat_wal) need a monitoring role the user may lack even when the
	// pg_walinspect rmgr list works. A missing-extension error here is
	// already covered by onWALOverviewLoaded's blocking prompt, so swallow it.
	if asMissingExt(msg.err) != nil {
		return nil
	}
	if msg.err != nil {
		s.walSummaryErr = msg.err
		s.walSummary = nil
		return nil
	}
	sum := msg.summary
	sum.StartLSN = s.walStart
	sum.EndLSN = s.walEnd
	sum.WindowBytes = walWindowBytes
	s.walSummary = &sum
	s.walSummaryErr = nil
	return nil
}

func (m *Model) onWALRecordsLoaded(msg walRecordsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRecords)
	if s == nil || s.db != msg.db || s.walRmgr != msg.rmgr {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		s.walRecTypeStats = nil
		return cmd
	}
	s.walRecTypeStats = msg.typeStats
	s.items = s.items[:0]
	for _, r := range msg.records {
		s.items = append(s.items, walRecordToItem(r))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALBlocksLoaded(msg walBlocksLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALBlocks)
	if s == nil || s.db != msg.db || s.walRecLSN != msg.recLSN {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
	return nil
}

// onWALCheckpointLoaded caches the best-effort checkpoint context for the
// levelWAL header. Failure is non-fatal and not surfaced — the header's other
// lines (and the rmgr list) still render; the checkpoint lines just stay hidden.
func (m *Model) onWALCheckpointLoaded(msg walCheckpointLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	if msg.err != nil {
		return nil
	}
	info := msg.info
	s.walCheckpoint = &info
	return nil
}

func (m *Model) onWALRelationsLoaded(msg walRelationsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRelations)
	if s == nil || s.db != msg.db {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.walStart = msg.start
	s.walEnd = msg.end
	s.items = s.items[:0]
	for _, st := range msg.rels {
		s.items = append(s.items, walRelStatToItem(st))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALRelBlocksLoaded(msg walRelBlocksLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWALRelBlocks)
	if s == nil || s.db != msg.db || s.walRelFilenode != msg.relfilenode {
		return nil
	}
	if cmd, stop := settleLoad(s, msg.err, extPromptReasonWALInspect); stop {
		return cmd
	}
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
	return nil
}
