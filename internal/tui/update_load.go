package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// armActivityTick appends the self-rescheduling activity refresh tick to cmds
// unless one is already running (the loop is shared by the activity table, the
// lock tree and the progress monitor).
func (m *Model) armActivityTick(cmds []tea.Cmd) []tea.Cmd {
	if !m.activityTicking {
		if tick := m.activityTick(); tick != nil {
			m.activityTicking = true
			cmds = append(cmds, tick)
		}
	}
	return cmds
}

// loadCurrent issues the right load command for the top screen and resets any
// transient affordances (extPrompt, install spinner, buffer-summary cache)
// so a refresh shows a clean state.
func (m *Model) loadCurrent() tea.Cmd {
	s := m.top()
	// A frozen A→B window rebuilds from its two loaded snapshots — no DB read,
	// so it's handled before the generic loading path sets the spinner.
	if s.level == levelStatements && s.stat.endSnap != nil {
		m.populateFrozenWindow(s)
		return nil
	}
	switch s.level {
	case levelTools:
		s.items = toolItems(m.pgbAvailable)
		s.itemsRev++ // doesn't go through applySort; invalidate the filter cache
		s.loading = false
		s.loaded = true
		return nil
	case levelDiagnostics:
		s.items = diagnosticItems(s.diagCatFilter)
		s.itemsRev++ // doesn't go through applySort; invalidate the filter cache
		s.loading = false
		s.loaded = true
		return nil
	case levelStatementDetail:
		// The detail panel renders synchronously from s.statDetail (set when we
		// drilled in); only the sample call is fetched async. Handled here —
		// before the generic loading path below — so the metrics show
		// immediately instead of behind a spinner.
		s.loading = false
		s.loaded = true
		if s.stat.detail == nil {
			return nil
		}
		// A stale call must not stay runnable while its sources are re-resolved.
		s.stat.resetSample()
		var cmds []tea.Cmd
		// HOT update ratio for the statement's main table (cumulative, from
		// pg_stat_user_tables). Reset first so a refresh re-fetches and a stale
		// value from a previous query never lingers; the cmd is nil-safe when no
		// table parses out.
		s.stat.hotStats = nil
		s.stat.hotErr = nil
		if c := m.loadStatementTableHotCmd(s.db, s.stat.detail.Query); c != nil {
			cmds = append(cmds, c)
		}
		if pg.ExplainableQuery(s.stat.detail.Query) {
			// Resolve the sample call first, then auto-run the plan once it's
			// known: a complete call takes a plain EXPLAIN, no call the generic
			// plan. onStatementSampleLoaded fires the EXPLAIN while explaining is
			// set. ANALYZE stays opt-in (Enter) because it executes the query.
			s.stat.explaining = true
			s.stat.explain = ""
			s.stat.explainErr = nil
			s.stat.explainAnalyze = false
			cmds = append(cmds, m.loadStatementSampleCmd(s.db, s.stat.detail.QueryID, s.stat.detail.Query))
		}
		return tea.Batch(cmds...)
	}
	s.loading = true
	s.loaded = false
	// Clear any extPrompt — it'll be re-populated by the load result or
	// the extension-status probe if still relevant. Avoids stale prompts
	// surviving a refresh after the user installed the extension out of
	// band (e.g. via psql).
	s.extPrompt = nil
	s.installing = false
	switch s.level {
	case levelDatabases:
		return m.loadDatabasesCmd()
	case levelSchemas:
		return m.loadSchemasCmd(s.db)
	case levelTables:
		return m.loadTablesCmd(s.db, s.schema)
	case levelBufferTables:
		s.buf.summary = nil
		s.buf.summaryErr = nil
		return tea.Batch(
			m.loadBufferStatsCmd(s.db, s.schema),
			m.loadBufferSummaryCmd(s.db),
		)
	case levelBufferDetail:
		if s.buf.detail == nil {
			s.loading = false
			s.loaded = true
			return nil
		}
		s.buf.usage = nil
		s.buf.usageErr = nil
		return m.loadBufferDetailCmd(s.db, s.buf.detail.OID)
	case levelShmem:
		return m.loadShmemCmd(s.db)
	case levelParts:
		// Probe pgstattuple alongside the parts load. The probe is cheap
		// (one pg_extension / pg_available_extensions lookup) and lets the
		// view offer an install when exact bloat would be measurable but
		// the extension isn't there yet. Also fetch per-table maintenance
		// stats (autovacuum triggers, live/dead tuples, scan history) for
		// the maintenance panel shown alongside the parts list.
		return tea.Batch(
			m.loadPartsCmd(s.table),
			m.probeExtensionCmd(s.db, extPgStatTuple),
			m.loadTableStatsCmd(s.table),
		)
	case levelColumns:
		return m.loadColumnsCmd(s.table)
	case levelHeapPages:
		return m.loadHeapPagesCmd(s.table, s.pages.heapWindowStart, s.pages.heapWindowCount)
	case levelHeapTuples:
		return m.loadHeapTuplesCmd(s.table, s.pages.heapPageBlkno, s.pages.tuplePick)
	case levelRelations:
		return m.loadRelationsCmd(s.db, s.schema)
	case levelIndexPages:
		switch s.pages.index.AccessMethod {
		case "gist":
			return m.loadGistPagesCmd(s.pages.index, s.pages.heapWindowStart, s.pages.heapWindowCount)
		case "brin":
			return m.loadBrinPagesCmd(s.pages.index, s.pages.heapWindowStart, s.pages.heapWindowCount)
		case "gin":
			return m.loadGinPagesCmd(s.pages.index, s.pages.heapWindowStart, s.pages.heapWindowCount)
		default:
			// The whole-tree level census reads every page of the index, so it
			// runs once per screen — window moves and refreshes reuse the cache.
			if !s.pages.btreeLevelsDone && !s.pages.btreeLevelsLoading {
				s.pages.btreeLevelsLoading = true
				return tea.Batch(
					m.loadIndexPagesCmd(s.pages.index, s.pages.heapWindowStart, s.pages.heapWindowCount),
					m.loadBtreeLevelCountsCmd(s.pages.index),
				)
			}
			return m.loadIndexPagesCmd(s.pages.index, s.pages.heapWindowStart, s.pages.heapWindowCount)
		}
	case levelIndexTuples:
		switch s.pages.index.AccessMethod {
		case "gist":
			return m.loadGistItemsCmd(s.pages.index, s.pages.indexPageBlkno, s.pages.indexPageType)
		case "brin":
			return m.loadBrinItemsCmd(s.pages.index, s.pages.indexPageBlkno)
		case "gin":
			return m.loadGinItemsCmd(s.pages.index, s.pages.indexPageBlkno)
		default:
			return m.loadIndexTuplesCmd(s.pages.index, s.pages.indexPageBlkno, s.pages.indexPageType)
		}
	case levelDescribe:
		// Re-issue the right loader on Refresh. On first push s.describe is nil
		// so we identify the target from s.table (table describe) or s.index
		// (index describe — s.table.OID is 0 for index targets). The
		// cache-footprint section is (re)loaded from onDescribeLoaded once the
		// describe result lands, so all push paths and refresh share one trigger.
		if s.desc.info != nil {
			switch s.desc.info.Kind {
			case pg.DescribeIndex:
				return m.loadDescribeIndexCmd(s.db, s.desc.info.OID, s.desc.info.Title)
			default:
				return m.loadDescribeTableCmd(s.table)
			}
		}
		// First load: derive from screen context set during push.
		if s.table.OID != 0 {
			return m.loadDescribeTableCmd(s.table)
		}
	case levelDiagnosticResult:
		if s.diag != nil {
			// Reset generic-table state so a Refresh shows a clean load.
			s.diagCols = nil
			s.diagBarCol = -1
			s.diagResult = nil
			s.diagTotalRow = nil
			if s.diagAllDBs {
				return m.loadDiagnosticAllDBsCmd(*s.diag)
			}
			return m.loadDiagnosticCmd(*s.diag, s.db)
		}
	case levelWAL:
		// Clear the header cache so a Refresh re-resolves the window and
		// re-reads the snapshot against the now-current write position. The
		// relation table follows the overview (onWALOverviewLoaded chains it),
		// so it is only reset here.
		s.wal.summary = nil
		s.wal.summaryErr = nil
		s.wal.checkpoint = nil
		s.wal.rels = nil
		s.wal.relsErr = nil
		s.wal.relsLoading = false
		return tea.Batch(
			m.loadWALOverviewCmd(s.db),
			m.loadWALSummaryCmd(s.db),
			m.loadWALCheckpointCmd(s.db),
		)
	case levelWALRecords:
		s.wal.recTypeStats = nil
		return m.loadWALRecordsCmd(s.db, s.wal.start, s.wal.end, s.wal.rmgr)
	case levelWALBlocks:
		return m.loadWALBlocksCmd(s.db, s.wal.recLSN, s.wal.recEnd)
	case levelWALRelBlocks:
		return m.loadWALRelBlocksCmd(s.db, s.wal.start, s.wal.end, s.wal.relFilenode)
	case levelWALBlockDetail:
		if s.wal.blockRef == nil {
			return nil
		}
		s.wal.detail = nil
		return m.loadWALBlockDetailCmd(s.db, *s.wal.blockRef)
	case levelStatements:
		// Kick a snapshot and, unless one is already running, start the
		// self-rescheduling refresh tick. The first snapshot becomes the
		// window baseline (see onStatementsLoaded).
		cmds := []tea.Cmd{m.loadStatementsCmd(s.db)}
		if !m.statTicking {
			if tick := m.statementsTick(); tick != nil {
				m.statTicking = true
				cmds = append(cmds, tick)
			}
		}
		return tea.Batch(cmds...)
	case levelSnapshots:
		return m.listSnapshotsCmd(m.snapshotDir, s.db)
	case levelMaintenance:
		// The catalog sweep is expensive, so it rides only on the explicit
		// loads (open, space, after a reset); the auto-refresh tick re-samples
		// MaintenanceInfo alone (onMaintTick).
		s.maintenance.schemaLoading = true
		return tea.Batch(m.armMaintTick([]tea.Cmd{m.loadMaintenanceCmd(s.db), m.loadMaintSchemaCmd(s.db)})...)
	case levelSettings:
		return m.loadSettingsCmd(s.db)
	case levelActivity:
		// Kick an immediate snapshot and, unless one is already running, start
		// the self-rescheduling refresh tick. Pattern mirrors levelStatements.
		return tea.Batch(m.armActivityTick([]tea.Cmd{m.loadActivityCmd(s.db, s.act.filter)})...)
	case levelLockTree:
		// Same live-refresh pattern as the activity table, reusing its tick loop.
		return tea.Batch(m.armActivityTick([]tea.Cmd{m.loadLockTreeCmd(s.db)})...)
	case levelTableStats:
		return m.loadTableOverviewCmd(s.db, s.schema)
	case levelPgBouncers:
		return tea.Batch(m.armPgbTick([]tea.Cmd{m.discoverPgBouncersCmd()})...)
	case levelPgBouncer:
		if s.pgb.inst == nil {
			return nil
		}
		return tea.Batch(m.armPgbTick([]tea.Cmd{m.loadPgbOverviewCmd(*s.pgb.inst)})...)
	case levelPgBouncerShow:
		if s.pgb.inst == nil {
			return nil
		}
		return tea.Batch(m.armPgbTick([]tea.Cmd{m.loadPgbShowCmd(*s.pgb.inst, s.pgb.show)})...)
	case levelLogFiles:
		return m.discoverLogsCmd()
	case levelLogs:
		return tea.Batch(m.armLogTick([]tea.Cmd{m.loadLogCmd(s, false)})...)
	case levelLogGroup, levelLogEntry:
		// Both render from the parent levelLogs report already in memory.
		m.rebuildLogChild(s)
		s.loading = false
		s.loaded = true
		return nil
	case levelProgress:
		// Same live-refresh pattern as the activity table, reusing its tick loop.
		return tea.Batch(m.armActivityTick([]tea.Cmd{m.loadProgressCmd(s.db)})...)
	}
	return nil
}
