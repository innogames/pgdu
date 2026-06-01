package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

func (m *Model) onDatabasesLoaded(msg databasesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDatabases)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, d := range msg.dbs {
		s.items = append(s.items, item{name: d.Name, size: d.SizeBytes, hasChildren: true, data: d})
	}
	m.applySort(s)
	return nil
}

func (m *Model) onSchemasLoaded(msg schemasLoadedMsg) tea.Cmd {
	s := m.findLevel(levelSchemas)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, sc := range msg.schemas {
		s.items = append(s.items, item{name: sc.Name, size: sc.SizeBytes, hasChildren: true, detail: schemaDetail(sc), data: sc})
	}
	m.applySort(s)
	return nil
}

func (m *Model) onTablesLoaded(msg tablesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTables)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, t := range msg.tables {
		s.items = append(s.items, tableToItem(t, s.tool))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onPartsLoaded(msg partsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, p := range msg.parts {
		s.items = append(s.items, partToItem(p))
	}
	m.applySort(s)
	if m.fetchBloat && msg.err == nil {
		s.bloatScanning = true
		return m.fillBloatCmd(msg.table, msg.parts)
	}
	return nil
}

func (m *Model) onBufferStatsLoaded(msg bufferStatsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferTables)
	if s == nil || s.db != msg.db || s.schema != msg.schema {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		// Promote to a blocking install prompt instead of an opaque error.
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonBufferCache,
			blocking:    true,
		}
		s.items = s.items[:0]
		return nil
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, st := range msg.stats {
		s.items = append(s.items, bufferStatToItem(st))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBufferSummaryLoaded(msg bufferSummaryLoadedMsg) tea.Cmd {
	s := m.findLevel(levelBufferTables)
	if s == nil || s.db != msg.db {
		return nil
	}
	if ext := asMissingExt(msg.err); ext != nil {
		// The summary error is swallowed; the blocking prompt set by
		// onBufferStatsLoaded already covers the user-visible state.
		return nil
	}
	if msg.err != nil {
		s.bufferSummaryErr = msg.err
		s.bufferSummary = nil
	} else {
		sum := msg.summary
		s.bufferSummary = &sum
		s.bufferSummaryErr = nil
	}
	return nil
}

func (m *Model) onColumnsLoaded(msg columnsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelColumns)
	if s == nil || s.table.OID != msg.tableOID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	for _, col := range msg.columns {
		s.items = append(s.items, columnToItem(col))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onBloatFilled(msg bloatFilledMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.table.OID {
		return nil
	}
	s.bloatScanning = false
	if msg.err != nil {
		s.err = msg.err
		return nil
	}
	// applySort reorders s.items after partsLoadedMsg, so indexing by the
	// original msg.parts position is wrong. Match by name (heap/toast and
	// each index name are unique within a table).
	byName := make(map[string]pg.Part, len(msg.parts))
	for _, p := range msg.parts {
		byName[p.Name] = p
	}
	for i := range s.items {
		if p, ok := byName[s.items[i].name]; ok {
			s.items[i].bloat = p.WastedBytes
			s.items[i].hasBloat = p.HasBloat
		}
	}
	m.applySort(s)
	return nil
}

func (m *Model) onExtStatus(msg extStatusMsg) tea.Cmd {
	// Dispatched by (level, ext): each consumer surfaces its own prompt or
	// flips its own ready flag. Anything not listed here is ignored, since
	// the same probe Cmd may run from multiple screens.
	switch msg.ext {
	case extPgStatTuple:
		s := m.findLevel(levelParts)
		if s == nil || s.db != msg.db {
			return nil
		}
		if msg.err == nil && msg.status.Available && !msg.status.Installed {
			s.extPrompt = &extPrompt{
				name:        msg.ext,
				db:          msg.db,
				installable: true,
				reason:      extPromptReasonPgStatTuple,
				blocking:    false,
			}
		}
	}
	return nil
}

func (m *Model) onHeapPagesLoaded(msg heapPagesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapPages)
	if s == nil || s.table.OID != msg.table.OID || s.heapWindowStart != msg.start {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonPageInspect,
			blocking:    true,
		}
		s.items = s.items[:0]
		return nil
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

func (m *Model) onHeapTuplesLoaded(msg heapTuplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelHeapTuples)
	if s == nil || s.table.OID != msg.tableOID || s.heapPageBlkno != msg.blkno {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonPageInspect,
			blocking:    true,
		}
		s.items = s.items[:0]
		return nil
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, t := range msg.tuples {
		s.items = append(s.items, heapTupleToItem(t))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onExtInstalled(msg extInstalledMsg) tea.Cmd {
	// Find the screen that asked for this install. We don't carry the
	// level in the message — just match on prompt name + db.
	for _, sc := range m.stack {
		if sc.extPrompt != nil && sc.extPrompt.name == msg.ext && sc.extPrompt.db == msg.db {
			sc.installing = false
			if msg.err != nil {
				sc.extPrompt.err = msg.err
				return nil
			}
			sc.extPrompt = nil
			// Re-enter the current screen so the (now-working) extension
			// is used. Only meaningful when the install was on the
			// currently active screen — otherwise the stale data on the
			// background screen will refresh next time the user revisits.
			if sc == m.top() {
				return m.loadCurrent()
			}
			return nil
		}
	}
	return nil
}

func (m *Model) onReindexDone(msg reindexDoneMsg) tea.Cmd {
	s := m.findLevel(levelParts)
	if s == nil || s.table.OID != msg.tableOID {
		return nil
	}
	s.reindexing = ""
	if msg.err != nil {
		s.reindexErr = msg.err
		return nil
	}
	s.reindexErr = nil
	// Refresh: the index has been rebuilt, so size and bloat have changed.
	return m.loadCurrent()
}
