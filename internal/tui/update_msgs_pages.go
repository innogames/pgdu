package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) onHeapPagesLoaded(msg heapPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapPages)
	if s == nil || s.table.OID != msg.table.OID || s.heapWindowStart != msg.start {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.heapPageCount = msg.totalPages
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, heapPageToItem(p))
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.heapPageCount = msg.totalPages
	// Banner data rides along with the page list; both are nil on a best-effort
	// failure, in which case the banner simply isn't drawn.
	s.indexKeyCols = msg.keyCols
	s.btreeMeta = msg.meta
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, indexPageToItem(p))
	}
	m.applySort(s)
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.heapPageCount = msg.totalPages
	s.indexKeyCols = msg.keyCols
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, gistPageToItem(p))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onGistItemsLoaded(msg gistItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.heapPageCount = msg.totalPages
	s.indexKeyCols = msg.keyCols
	s.brinMeta = msg.meta
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, brinPageToItem(p))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBrinItemsLoaded(msg brinItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, it := range msg.items {
		s.items = append(s.items, brinItemToItem(it))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onGinPagesLoaded(msg ginPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexPages)
	if s == nil || s.index.OID != msg.indexOID || s.heapWindowStart != msg.start {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.heapPageCount = msg.totalPages
	s.indexKeyCols = msg.keyCols
	s.ginMeta = msg.meta
	s.items = s.items[:0]
	for _, p := range msg.pages {
		s.items = append(s.items, ginPageToItem(p))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onGinItemsLoaded(msg ginItemsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, it := range msg.items {
		s.items = append(s.items, ginItemToItem(it))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onWALOverviewLoaded(msg walOverviewLoadedMsg) tea.Cmd {
	s := m.findLevel(levelWAL)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonWALInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		s.walRecTypeStats = nil
		return setExtensionPrompt(s, ext, extPromptReasonWALInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonWALInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonWALInspect)
	}
	s.err = msg.err
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
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		return setExtensionPrompt(s, ext, extPromptReasonWALInspect)
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
	return nil
}
