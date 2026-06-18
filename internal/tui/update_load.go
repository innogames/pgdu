package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// loadCurrent issues the right load command for the top screen and resets any
// transient affordances (extPrompt, install spinner, buffer-summary cache)
// so a refresh shows a clean state.
func (m *Model) loadCurrent() tea.Cmd {
	s := m.top()
	// A frozen A→B window rebuilds from its two loaded snapshots — no DB read,
	// so it's handled before the generic loading path sets the spinner.
	if s.level == levelStatements && s.statEndSnap != nil {
		m.populateFrozenWindow(s)
		return nil
	}
	switch s.level {
	case levelTools:
		s.items = toolItems()
		s.itemsRev++ // doesn't go through applySort; invalidate the filter cache
		s.loading = false
		s.loaded = true
		return nil
	case levelDiagnostics:
		s.items = diagnosticItems()
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
		if s.statDetail != nil && pg.ExplainableQuery(s.statDetail.Query) {
			// Resolve the sample call first, then auto-run the plan once it's
			// known: a real pg_qualstats example takes a plain EXPLAIN, a
			// synthesized one the generic plan. onStatementSampleLoaded fires the
			// EXPLAIN while statExplaining is set. ANALYZE stays opt-in (Enter)
			// because it executes the query.
			s.statExplaining = true
			s.statExplain = ""
			s.statExplainErr = nil
			s.statExplainAnalyze = false
			return m.loadStatementSampleCmd(s.db, s.statDetail.QueryID, s.statDetail.Query)
		}
		return nil
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
		s.bufferSummary = nil
		s.bufferSummaryErr = nil
		return tea.Batch(
			m.loadBufferStatsCmd(s.db, s.schema),
			m.loadBufferSummaryCmd(s.db),
		)
	case levelBufferDetail:
		if s.bufDetail == nil {
			s.loading = false
			s.loaded = true
			return nil
		}
		s.bufUsage = nil
		s.bufUsageErr = nil
		return m.loadBufferDetailCmd(s.db, s.bufDetail.OID)
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
		return m.loadHeapPagesCmd(s.table, s.heapWindowStart, s.heapWindowCount)
	case levelHeapTuples:
		return m.loadHeapTuplesCmd(s.table, s.heapPageBlkno)
	case levelTupleRow:
		if s.toastChunkID != 0 {
			return m.loadToastValueCmd(s.table, s.toastChunkID)
		}
		return m.loadTupleRowCmd(s.table, s.tupleCtid)
	case levelRelations:
		return m.loadRelationsCmd(s.db, s.schema)
	case levelIndexPages:
		switch s.index.AccessMethod {
		case "gist":
			return m.loadGistPagesCmd(s.index, s.heapWindowStart, s.heapWindowCount)
		case "brin":
			return m.loadBrinPagesCmd(s.index, s.heapWindowStart, s.heapWindowCount)
		case "gin":
			return m.loadGinPagesCmd(s.index, s.heapWindowStart, s.heapWindowCount)
		default:
			return m.loadIndexPagesCmd(s.index, s.heapWindowStart, s.heapWindowCount)
		}
	case levelIndexTuples:
		switch s.index.AccessMethod {
		case "gist":
			return m.loadGistItemsCmd(s.index, s.indexPageBlkno, s.indexPageType)
		case "brin":
			return m.loadBrinItemsCmd(s.index, s.indexPageBlkno)
		case "gin":
			return m.loadGinItemsCmd(s.index, s.indexPageBlkno)
		default:
			return m.loadIndexTuplesCmd(s.index, s.indexPageBlkno, s.indexPageType)
		}
	case levelDescribe:
		// Re-issue the right loader on Refresh. On first push s.describe is nil
		// so we identify the target from s.table (table describe) or s.index
		// (index describe — s.table.OID is 0 for index targets). The
		// cache-footprint section is (re)loaded from onDescribeLoaded once the
		// describe result lands, so all push paths and refresh share one trigger.
		if s.describe != nil {
			switch s.describe.Kind {
			case pg.DescribeIndex:
				return m.loadDescribeIndexCmd(s.db, s.describe.OID, s.describe.Title)
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
			return m.loadDiagnosticCmd(*s.diag, s.db)
		}
	case levelWAL:
		// Clear the header cache so a Refresh re-resolves the window and
		// re-reads the snapshot against the now-current write position.
		s.walSummary = nil
		s.walSummaryErr = nil
		s.walCheckpoint = nil
		return tea.Batch(
			m.loadWALOverviewCmd(s.db),
			m.loadWALSummaryCmd(s.db),
			m.loadWALCheckpointCmd(s.db),
		)
	case levelWALRecords:
		s.walRecTypeStats = nil
		return m.loadWALRecordsCmd(s.db, s.walStart, s.walEnd, s.walRmgr)
	case levelWALBlocks:
		return m.loadWALBlocksCmd(s.db, s.walRecLSN, s.walRecEnd)
	case levelWALRelations:
		return m.loadWALRelationsCmd(s.db, s.walStart, s.walEnd)
	case levelWALRelBlocks:
		return m.loadWALRelBlocksCmd(s.db, s.walStart, s.walEnd, s.walRelFilenode)
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
		return m.loadMaintenanceCmd(s.db)
	case levelSettings:
		return m.loadSettingsCmd(s.db)
	case levelActivity:
		// Kick an immediate snapshot and, unless one is already running, start
		// the self-rescheduling refresh tick. Pattern mirrors levelStatements.
		cmds := []tea.Cmd{m.loadActivityCmd(s.db, s.actFilter)}
		if !m.activityTicking {
			if tick := m.activityTick(); tick != nil {
				m.activityTicking = true
				cmds = append(cmds, tick)
			}
		}
		return tea.Batch(cmds...)
	}
	return nil
}
