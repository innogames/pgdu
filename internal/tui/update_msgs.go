package tui

import (
	"strings"
	"time"

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
		s.items = append(s.items, indexPageToItem(p))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onIndexTuplesLoaded(msg indexTuplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelIndexTuples)
	if s == nil || s.index.OID != msg.indexOID || s.indexPageBlkno != msg.blkno || s.indexPageType != msg.pageType {
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
		s.items = append(s.items, indexTupleToItem(t))
	}
	m.applySort(s)
	return nil
}

func (m *Model) onDescribeLoaded(msg describeLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDescribe)
	if s == nil {
		return nil
	}
	// Guard against stale messages: accept when this is the first load
	// (s.describe == nil) or when the OID matches a refresh.
	if s.describe != nil && s.describe.OID != msg.oid {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.describe = msg.desc
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
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonWALInspect,
			blocking:    true,
		}
		s.items = s.items[:0]
		return nil
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
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonWALInspect,
			blocking:    true,
		}
		s.items = s.items[:0]
		s.walRecTypeStats = nil
		return nil
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
		s.err = nil
		s.extPrompt = &extPrompt{
			name:        ext.Extension,
			db:          ext.DB,
			installable: ext.Installable,
			reason:      extPromptReasonWALInspect,
			blocking:    true,
		}
		s.items = s.items[:0]
		return nil
	}
	s.err = msg.err
	s.items = s.items[:0]
	for _, b := range msg.blocks {
		s.items = append(s.items, walBlockToItem(b))
	}
	m.applySort(s)
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

func (m *Model) onDiagnosticLoaded(msg diagnosticLoadedMsg) tea.Cmd {
	s := m.findLevel(levelDiagnosticResult)
	if s == nil || s.diag == nil || s.diag.Key != msg.key {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	if msg.err != nil || msg.result == nil {
		return nil
	}
	s.diagCols = msg.result.Columns
	s.diagBarCol = msg.result.BarCol
	// Default sort: the result's SortCol (biggest first) if present, else col 0.
	if msg.result.SortCol >= 0 {
		s.diagSortCol = msg.result.SortCol
		s.sortDesc = true
	} else {
		s.diagSortCol = 0
		s.sortDesc = false
	}
	// Convert each result row to an item. item.name is the space-joined cell
	// display so the existing fuzzy filter can match any column value.
	for _, row := range msg.result.Rows {
		parts := make([]string, len(row))
		for i, cell := range row {
			parts[i] = cell.Display
		}
		s.items = append(s.items, item{
			name: strings.Join(parts, " "),
			data: row, // []pg.DiagCell
		})
	}
	m.applySort(s)
	return nil
}

func (m *Model) onStatementsLoaded(msg statementsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatements)
	if s == nil || s.db != msg.db {
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
			reason:      extPromptReasonStatStatements,
			blocking:    true,
		}
		s.items = s.items[:0]
		s.diagCols = nil
		return nil
	}
	s.err = msg.err
	if msg.err != nil {
		return nil
	}
	s.statSampledAt = time.Now()
	s.statTrackPlanning = msg.trackPlanning

	// First snapshot becomes the baseline: the window opens here, so there are
	// no deltas to show yet — the table fills in as queries run.
	if s.statBaseline == nil {
		s.statBaseline = make(map[int64]pg.QueryStat, len(msg.stats))
		for _, q := range msg.stats {
			s.statBaseline[q.QueryID] = q
		}
		s.statBaselineAt = s.statSampledAt
		s.statRows = nil
		s.items = s.items[:0]
		s.statWindowExecMs = 0
		s.diagCols = statementColumns(s.statTrackPlanning)
		s.diagBarCol = -1
		s.diagSortCol = colStmtTotalMs
		s.sortDesc = true
		return nil
	}

	s.statRows = pg.DiffStatements(s.statBaseline, msg.stats)
	items, windowMs := buildStatementItems(s.statRows, s.statTrackPlanning)
	s.statWindowExecMs = windowMs
	s.diagCols = statementColumns(s.statTrackPlanning)
	s.diagBarCol = -1
	s.items = items
	// Preserve the user's chosen sort column/direction across refreshes (set on
	// the first/baseline load); applySort re-orders to match.
	m.applySort(s)
	return nil
}

// onStatementsTick keeps the live window fresh. It re-samples only while the
// top-queries table is on top, but keeps rescheduling while the user is in its
// detail view too, so the window resumes updating when they return. When the
// user leaves the tool entirely the loop stops (statTicking flips false) until
// loadCurrent restarts it on re-entry.
func (m *Model) onStatementsTick() tea.Cmd {
	top := m.top()
	if top.level != levelStatements && top.level != levelStatementDetail {
		m.statTicking = false
		return nil
	}
	if top.level == levelStatements {
		return tea.Batch(m.loadStatementsCmd(top.db), statementsTick())
	}
	return statementsTick()
}

func (m *Model) onStatementSampleLoaded(msg statementSampleLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.statDetail == nil || s.statDetail.Query != msg.query {
		return nil
	}
	s.statSampleCall = msg.sample
	s.statSampleErr = msg.err
	return nil
}

func (m *Model) onStatementExplainLoaded(msg statementExplainLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.statDetail == nil || s.statDetail.Query != msg.query {
		return nil
	}
	s.statExplaining = false
	s.statExplain = msg.plan
	s.statExplainErr = msg.err
	s.statExplainAnalyze = msg.analyze
	return nil
}
