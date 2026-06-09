package tui

import (
	"fmt"
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
		return setExtensionPrompt(s, ext, extPromptReasonBufferCache)
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
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
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
		return setExtensionPrompt(s, ext, extPromptReasonPageInspect)
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
	s.diagMetricsDirty = true
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
		s.diagCols = nil
		return setExtensionPrompt(s, ext, extPromptReasonStatStatements)
	}
	s.err = msg.err
	if msg.err != nil {
		return nil
	}
	s.statSampledAt = time.Now()
	s.statTrackPlanning = msg.trackPlanning

	// First snapshot becomes the baseline: the window opens here, so there are
	// no deltas to show yet — the table fills in as queries run. A disk baseline
	// (statBaseSnap) is installed before this load, so statBaseline is non-nil
	// and we skip straight to the diff path below.
	if s.statBaseline == nil {
		s.statBaseline = make(map[int64]pg.QueryStat, len(msg.stats))
		for _, q := range msg.stats {
			s.statBaseline[q.QueryID] = q
		}
		s.statBaselineAt = s.statSampledAt
		s.statRows = nil
		s.items = s.items[:0]
		s.statWindowExecMs = 0
		descs := m.visibleStmtCols(stmtCtx{trackPlanning: s.statTrackPlanning})
		s.stmtCols = descs
		s.diagCols = diagColumnsFrom(descs)
		s.diagBarCol = -1
		m.stmtSortColID = colTotalMs
		s.sortDesc = true
		m.syncStmtSort(s, descs)
		return nil
	}

	// A disk baseline can produce negative deltas if the counters were reset
	// between capture and now; clamp them. (Snapshots invalidated this way are
	// already filtered out of the L browser, so this is just defence in depth.)
	if s.statBaseSnap != nil {
		s.statRows = pg.DiffStatementsClamped(s.statBaseline, msg.stats)
	} else {
		s.statRows = pg.DiffStatements(s.statBaseline, msg.stats)
	}
	// rebuildStatementItems preserves the user's chosen sort column (tracked by id)
	// and the current column visibility across refreshes.
	m.rebuildStatementItems(s)
	return nil
}

// rebuildStatementItems regenerates the top-queries table from the already-fetched
// window deltas (s.statRows) for the current column-visibility set and
// track_planning state — no DB round-trip. Used by every load site and by the C
// column-config toggles so the columns, cells, footer and sort stay consistent.
func (m *Model) rebuildStatementItems(s *screen) {
	items, descs, windowMs, total := m.buildStatementItems(s.statRows, s.statTrackPlanning)
	s.items = items
	s.stmtCols = descs
	s.diagCols = diagColumnsFrom(descs)
	s.statWindowExecMs = windowMs
	s.diagTotalRow = total
	s.diagBarCol = -1
	s.diagMetricsDirty = true
	m.syncStmtSort(s, descs)
	m.applySort(s)
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
	next := m.statementsTick()
	if next == nil {
		// Auto-refresh was disabled or paused while the tool was open; stop the
		// loop. A resume (t) or re-entry restarts it.
		m.statTicking = false
		return nil
	}
	if top.level == levelStatements {
		// A frozen A→B diff has no "now" to re-sample — keep the tick alive (so it
		// resumes if the user returns to a live window) but don't reload.
		if top.statEndSnap != nil {
			return next
		}
		return tea.Batch(m.loadStatementsCmd(top.db), next)
	}
	return next
}

// onSnapshotSaved reports the dump's path (or error) in the transient notice.
func (m *Model) onSnapshotSaved(msg snapshotSavedMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "snapshot failed: " + msg.err.Error()
		return nil
	}
	m.notice = "snapshot saved → " + msg.path
	return nil
}

// onSnapshotsListed fills the snapshots browser with the directory listing.
// Snapshots from the current server/database whose counters have since been
// reset (CapturedAt predates the live stats_reset) are dropped silently — they
// can't serve as a baseline, so there's nothing to load. Snapshots from a
// different server/database are kept but flagged incompatible (dimmed, not
// loadable), since we can't judge their validity.
func (m *Model) onSnapshotsListed(msg snapshotsListedMsg) tea.Cmd {
	s := m.findLevel(levelSnapshots)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err

	st := m.findLevel(levelStatements)
	curDB := ""
	if st != nil {
		curDB = st.db
	}

	metas := make([]pg.SnapshotMeta, 0, len(msg.metas))
	for _, meta := range msg.metas {
		compatible := meta.Target == m.target && meta.Database == curDB
		if compatible && !msg.liveReset.IsZero() && meta.CapturedAt.Before(msg.liveReset) {
			continue // invalidated by a stats reset since capture
		}
		metas = append(metas, meta)
	}

	s.statSnapMetas = metas
	s.items = make([]item, len(metas))
	for i, meta := range metas {
		s.items[i] = item{
			name:     snapshotLabel(meta),
			size:     int64(meta.QueryCount),
			snapPath: meta.Path,
		}
	}
	// Clamp the cursor: a delete (or filter) can shrink the list out from under it.
	if s.cursor >= len(s.items) {
		s.cursor = max(len(s.items)-1, 0)
	}
	// A marked base that's no longer listed (deleted or filtered) can't be used.
	if s.statMarkBase != "" && !snapshotPathPresent(metas, s.statMarkBase) {
		s.statMarkBase = ""
	}
	return nil
}

// onSnapshotBaseLoaded installs a disk snapshot as the live window's baseline,
// then re-samples so the table shows everything since the snapshot till now.
func (m *Model) onSnapshotBaseLoaded(msg snapshotBaseLoadedMsg) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}
	if msg.err != nil || msg.snap == nil {
		m.notice = "load snapshot failed: " + errText(msg.err)
		m.popToStatements()
		return nil
	}
	st.statBaseSnap = msg.snap
	st.statEndSnap = nil
	st.statBaseline = msg.snap.BaselineMap()
	st.statBaselineAt = msg.snap.CapturedAt
	m.popToStatements()
	return m.loadCurrent()
}

// onSnapshotFrozenLoaded builds a frozen A→B diff between two snapshots: no live
// re-sampling, the table is the delta of end relative to base.
func (m *Model) onSnapshotFrozenLoaded(msg snapshotFrozenLoadedMsg) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}
	if msg.err != nil || msg.base == nil || msg.end == nil {
		m.notice = "load snapshots failed: " + errText(msg.err)
		m.popToStatements()
		return nil
	}
	st.statBaseSnap = msg.base
	st.statEndSnap = msg.end
	st.statBaseline = msg.base.BaselineMap()
	st.statBaselineAt = msg.base.CapturedAt
	st.statSampledAt = msg.end.CapturedAt
	st.statTrackPlanning = msg.base.TrackPlanning && msg.end.TrackPlanning
	// A reset between the two captures yields negative deltas; clamping in
	// populateFrozenWindow (DiffStatementsClamped) floors them silently.
	m.populateFrozenWindow(st)
	m.popToStatements()
	return nil
}

// populateFrozenWindow recomputes a frozen window's rows/items from the two
// loaded snapshots. Split out so loadCurrent can rebuild on a Refresh without DB.
func (m *Model) populateFrozenWindow(st *screen) {
	st.statRows = pg.DiffStatementsClamped(st.statBaseSnap.BaselineMap(), st.statEndSnap.Stats)
	m.rebuildStatementItems(st)
	st.loading = false
	st.loaded = true
}

// popToStatements unwinds the screen stack back to the top-queries table,
// dropping any snapshots-browser screen pushed on top of it.
func (m *Model) popToStatements() {
	for len(m.stack) > 1 && m.top().level != levelStatements {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

// snapshotPathPresent reports whether path is still among the listed snapshots.
func snapshotPathPresent(metas []pg.SnapshotMeta, path string) bool {
	for _, meta := range metas {
		if meta.Path == path {
			return true
		}
	}
	return false
}

func errText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func (m *Model) onStatementSampleLoaded(msg statementSampleLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.statDetail == nil || s.statDetail.Query != msg.query {
		return nil
	}
	s.statSampleCall = msg.sample
	s.statSampleReal = msg.real
	s.statSampleFromData = msg.fromData
	s.statQualstats = msg.qualstats
	s.statSampleErr = msg.err
	// Offer a one-key install when pg_qualstats is absent but already preloaded —
	// then CREATE EXTENSION alone unlocks real values. Otherwise drop any stale
	// qualstats prompt (e.g. after the user just installed it out of band).
	if !msg.qualstats && msg.installable {
		s.extPrompt = &extPrompt{
			name:        extQualstats,
			db:          s.db,
			installable: true,
			reason:      extPromptReasonQualstats,
			blocking:    false,
		}
	} else if s.extPrompt != nil && s.extPrompt.name == extQualstats {
		s.extPrompt = nil
	}
	// Auto-run the plan once the sample source is known (set up at drill-in,
	// where statExplaining was flipped on). A real sample → plain EXPLAIN on it;
	// otherwise the generic plan, which doesn't need the sample at all, so it
	// still runs when parameter inference failed.
	if s.statExplaining {
		return m.statementPlanCmd(s)
	}
	return nil
}

func (m *Model) onExportDone(msg exportDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "export failed: " + msg.err.Error()
		return nil
	}
	m.notice = fmt.Sprintf("exported %d rows → %s", msg.rows, msg.path)
	return nil
}

func (m *Model) onStatementSamplesLoaded(msg statementSamplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementSamples)
	if s == nil || s.statDetail == nil || s.statDetail.QueryID != msg.queryID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = sampleItems(msg.samples)
	s.cursor, s.offset = 0, 0
	return nil
}

// sampleItems maps captured pg_qualstats constants onto list items. The bar
// magnitude (item.size) is the occurrence count, so the frequency pattern is
// visible at a glance; data carries the QualSample for the Enter action. name
// is the readable predicate, which also drives the fuzzy filter.
func sampleItems(samples []pg.QualSample) []item {
	out := make([]item, len(samples))
	for i, sm := range samples {
		out[i] = item{name: sampleLabel(sm), size: sm.Occurrences, data: sm}
	}
	return out
}

// sampleLabel renders a captured qual as "table.column op value", falling back
// to bare value (then "=") when pg_qualstats couldn't resolve the left side.
func sampleLabel(sm pg.QualSample) string {
	if sm.Column == "" {
		return sm.ConstValue
	}
	col := sm.Column
	if sm.Relation != "" {
		col = sm.Relation + "." + sm.Column
	}
	op := sm.Operator
	if op == "" {
		op = "="
	}
	return col + " " + op + " " + sm.ConstValue
}

func (m *Model) onStatementExplainLoaded(msg statementExplainLoadedMsg) tea.Cmd {
	// The EXPLAIN can be launched from either the detail view or the captured-
	// values (samples) view — each carries its own statExplaining/statExplain.
	// Route the result to the screen that actually started it, otherwise the
	// samples view stays stuck on "running EXPLAIN ANALYZE…" while the plan lands
	// on the hidden detail screen below it.
	s := m.findExplainTarget(msg.query)
	if s == nil {
		return nil
	}
	s.statExplaining = false
	s.statExplain = msg.plan
	s.statExplainErr = msg.err
	s.statExplainAnalyze = msg.analyze
	return nil
}

// findExplainTarget returns the topmost statement screen whose EXPLAIN is in
// flight for the given normalized query — the one that issued the request.
func (m *Model) findExplainTarget(query string) *screen {
	for i := len(m.stack) - 1; i >= 0; i-- {
		s := m.stack[i]
		if s.level != levelStatementDetail && s.level != levelStatementSamples {
			continue
		}
		if s.statExplaining && s.statDetail != nil && s.statDetail.Query == query {
			return s
		}
	}
	return nil
}
