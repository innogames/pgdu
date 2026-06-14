package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

func (m *Model) drillIn() tea.Cmd {
	s := m.top()
	if !s.loaded || len(s.items) == 0 {
		return nil
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return nil
	}
	cur := s.items[vis[s.cursor]]
	switch s.level {
	case levelTools:
		t := cur.data.(tool)
		if t == toolTools {
			// toolTools goes directly to the flat diagnostic-query list, not
			// through a database picker — all queries run against the default DB.
			next := &screen{level: levelDiagnostics, title: "tools", tool: toolTools, sort: sortByName, sortDesc: sortByName.defaultDesc()}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		if t == toolWAL {
			// WAL is cluster-wide, so it skips the database picker too. The
			// concrete default DB is pinned on the screen (not "") so the
			// extension-install prompt can name a real database.
			next := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: m.client.DefaultDB(), sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		if t == toolMaintenance {
			// Maintenance dashboard is cluster-wide: skip the database picker.
			next := &screen{level: levelMaintenance, title: "maintenance", tool: toolMaintenance, db: m.client.DefaultDB()}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		if t == toolActivity {
			// Activity tool is cluster-wide: skip the database picker and go
			// directly to the live pg_stat_activity list.
			next := &screen{
				level:     levelActivity,
				title:     "activity",
				tool:      toolActivity,
				db:        m.client.DefaultDB(),
				actFilter: pg.ActivityActiveWaiting,
				actHosts:  make(map[string]string),
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		// toolQueries falls through to the database picker like the disk/buffers
		// tools: pg_stat_statements is read from whichever database you pick
		// (its dbid filter scopes the snapshot to that connection's database).
		next := &screen{level: levelDatabases, title: "databases", tool: t, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelDiagnostics:
		d := cur.data.(pg.Diagnostic)
		next := &screen{
			level:      levelDiagnosticResult,
			title:      d.Title,
			tool:       toolTools,
			diag:       &d,
			diagBarCol: -1,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelDatabases:
		d := cur.data.(pg.Database)
		if s.tool == toolQueries {
			// Queries has no schema/table hierarchy — drill straight to the
			// top-queries table for the chosen database.
			next := &screen{level: levelStatements, title: "queries", tool: toolQueries, db: d.Name}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		next := &screen{level: levelSchemas, title: "schemas", tool: s.tool, db: d.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelSchemas:
		sc := cur.data.(pg.Schema)
		m.stack = append(m.stack, schemaChildScreen(s.tool, sc))
		return m.loadCurrent()
	case levelTables:
		t := cur.data.(pg.Table)
		var next *screen
		switch s.tool {
		case toolPageInspect:
			next = &screen{
				level: levelHeapPages, title: "heap pages", tool: s.tool,
				db: t.DB, schema: t.Schema, table: t,
				heapWindowStart: 0, heapWindowCount: heapWindowDefault,
				sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
			}
		default:
			next = &screen{
				level: levelParts, title: "parts", tool: s.tool,
				db: t.DB, schema: t.Schema, table: t,
				sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
			}
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelBufferTables:
		st, ok := cur.data.(pg.TableBufferStat)
		if !ok {
			return nil
		}
		// Carry the row's stat onto the detail screen so the cache-footprint
		// figures render synchronously; only the temperature histogram is fetched.
		stat := st
		next := &screen{
			level: levelBufferDetail, title: st.Schema + "." + st.Name, tool: s.tool,
			db: s.db, schema: st.Schema, bufDetail: &stat,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelHeapPages:
		p, ok := cur.data.(pg.HeapPageStat)
		if !ok {
			return nil
		}
		next := &screen{
			level: levelHeapTuples, title: "tuples", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			heapPageBlkno: int32(p.Blkno),
			sort:          sortByLP, sortDesc: sortByLP.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelHeapTuples:
		ht, ok := cur.data.(pg.HeapTuple)
		if !ok {
			return nil
		}
		// REDIRECT hops within the same page: lp_off carries the target's
		// OffsetNumber, so jump the cursor to that lp instead of drilling.
		// Lets the user walk a HOT chain by repeatedly pressing Enter.
		if ht.LPFlags == pg.LPRedirect {
			for vi, idx := range vis {
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
				toastChunkID: *ht.ChunkID,
				sort:         sortByName, sortDesc: false,
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		next := &screen{
			level: levelTupleRow, title: "row", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			tupleCtid: *ht.Ctid,
			sort:      sortByName, sortDesc: false,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelRelations:
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
				heapWindowStart: 0, heapWindowCount: heapWindowDefault,
				sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		case pg.RelBTreeIndex:
			next := &screen{
				level: levelIndexPages, title: "index pages", tool: s.tool,
				db: r.DB, schema: r.Schema, index: r,
				heapWindowStart: 0, heapWindowCount: heapWindowDefault,
				sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		return nil
	case levelIndexPages:
		p, ok := cur.data.(pg.IndexPageStat)
		if !ok {
			return nil
		}
		next := &screen{
			level: levelIndexTuples, title: "index tuples", tool: s.tool,
			db: s.db, schema: s.schema, index: s.index,
			indexPageBlkno: p.Blkno,
			indexPageType:  p.Type,
			sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelIndexTuples:
		t, ok := cur.data.(pg.IndexTuple)
		if !ok || t.Ctid == nil || t.Decoded == nil {
			// Only drillable when a live heap row was projected. Internal
			// pages and vacuumed entries land here too; both have nothing
			// to show in the per-column row view.
			return nil
		}
		// The parent's schema matches the index's by Postgres rule — indexes
		// live in the same namespace as their table.
		parent := pg.Table{
			DB: s.db, Schema: s.schema,
			OID: s.index.ParentOID, Name: s.index.ParentName,
		}
		next := &screen{
			level: levelTupleRow, title: "row", tool: s.tool,
			db: s.db, schema: s.schema, table: parent,
			tupleCtid: *t.Ctid,
			sort:      sortByName, sortDesc: false,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelWAL:
		st, ok := cur.data.(pg.WALRmgrStat)
		if !ok || st.Count == 0 {
			return nil
		}
		next := &screen{
			level: levelWALRecords, title: "wal records", tool: s.tool,
			db: s.db, walRmgr: st.Name, walStart: s.walStart, walEnd: s.walEnd,
			sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelWALRecords:
		r, ok := cur.data.(pg.WALRecord)
		if !ok {
			return nil
		}
		next := &screen{
			level: levelWALBlocks, title: "wal blocks", tool: s.tool,
			db: s.db, walRmgr: s.walRmgr, walRecLSN: r.StartLSN, walRecEnd: r.EndLSN,
			sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelStatements:
		// Resolve the row's queryid back to its full window-delta QueryStat.
		var qs *pg.QueryStat
		for i := range s.statRows {
			if s.statRows[i].QueryID == cur.statQueryID {
				qs = &s.statRows[i]
				break
			}
		}
		if qs == nil {
			return nil
		}
		next := &screen{
			level: levelStatementDetail, title: "query", tool: s.tool,
			db: s.db, statDetail: qs, statWindowExecMs: s.statWindowExecMs,
			// Carry the parent's track_planning state so the plan-time line
			// matches the overview's plan_ms column; without it the detail view
			// defaults to false and wrongly reports "track_planning off".
			statTrackPlanning: s.statTrackPlanning,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelSnapshots:
		return m.loadSelectedSnapshot(s, cur)
	case levelMaintenance:
		// Enter on the maintenance dashboard opens the pg_settings browser.
		next := &screen{level: levelSettings, title: "settings", tool: toolMaintenance, db: s.db}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelActivity:
		// Enter drills into the top-queries detail view for the highlighted
		// backend's query (requires pg_stat_statements; a soft hint is shown
		// when query_id is zero). The item's statQueryID reuses the same field
		// that levelStatements uses for the same purpose.
		if cur.statQueryID == 0 {
			m.notice = "no query_id — pg_stat_statements not tracking this query"
			return nil
		}
		// Need the query text to build a sample call; it lives in the ActivityRow
		// stored in screen.actRows, matched by PID embedded in the item (we use
		// the pid cell display as the lookup key).  Resolve it via actRows directly.
		if len(s.actRows) == 0 {
			return nil
		}
		// Find the ActivityRow by QueryID (first match).
		var queryText string
		var backendPID int32
		for _, r := range s.actRows {
			if r.QueryID == cur.statQueryID {
				queryText = r.Query
				backendPID = r.PID
				break
			}
		}
		// Push a loading placeholder while we fetch the QueryStat snapshot.
		next := &screen{level: levelStatementDetail, title: "query", tool: s.tool, db: s.db, loading: true}
		m.stack = append(m.stack, next)
		return m.loadActivityStatementCmd(s.db, backendPID, cur.statQueryID, queryText)
	case levelParts:
		// Only the heap row drills further — into per-column space estimates.
		// Toast and index rows have no meaningful sub-breakdown.
		p, ok := cur.data.(pg.Part)
		if !ok || p.Kind != pg.PartHeap {
			return nil
		}
		next := &screen{
			level: levelColumns, title: "columns", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	return nil
}

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
		return m.loadIndexPagesCmd(s.index, s.heapWindowStart, s.heapWindowCount)
	case levelIndexTuples:
		return m.loadIndexTuplesCmd(s.index, s.indexPageBlkno, s.indexPageType)
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
		return tea.Batch(
			m.loadWALOverviewCmd(s.db),
			m.loadWALSummaryCmd(s.db),
		)
	case levelWALRecords:
		s.walRecTypeStats = nil
		return m.loadWALRecordsCmd(s.db, s.walStart, s.walEnd, s.walRmgr)
	case levelWALBlocks:
		return m.loadWALBlocksCmd(s.db, s.walRecLSN, s.walRecEnd)
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

// schemaChildScreen builds the next screen when drilling into a schema, varying
// by tool. Used by drillIn and the single-schema fast path in onSchemasLoaded.
func schemaChildScreen(t tool, sc pg.Schema) *screen {
	switch t {
	case toolBuffers:
		return &screen{level: levelBufferTables, title: "buffers", tool: t, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	case toolPageInspect:
		return &screen{level: levelRelations, title: "relations", tool: t, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	default:
		return &screen{level: levelTables, title: "tables", tool: t, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	}
}

// loadSelectedSnapshot acts on Enter in the snapshots browser. The browser is a
// timeline range picker: the applied window's start is the anchor a pick pairs
// with. With no anchor (the default session window or a fresh R re-base) the
// pick becomes the start and the end is "now" (live). With an anchor the window
// spans the time-ordered range between anchor and pick — frozen unless the pick
// is "now". A pick that lands as the start pops back to the table (the one-key
// "since this snapshot" flow); one that lands as a frozen end keeps the browser
// open so the matching start can be picked next.
//
// The synthetic sentinel anchors (@now / @session / @reset) are valid endpoints
// and are always compatible (they don't carry a server/db identity). Real
// snapshot rows are checked against the current target/db.
func (m *Model) loadSelectedSnapshot(s *screen, cur item) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}

	// The anchor is the applied window's start; "" (no explicit start) and a
	// re-picked anchor both mean the pick spans pick → now.
	anchor, _ := m.appliedWindowPaths(st, s)
	startPath, endPath := cur.snapPath, snapNow
	if anchor != "" && anchor != snapSession && anchor != cur.snapPath {
		startPath, endPath = anchor, cur.snapPath
	}

	// Compatibility check: real snapshots must match the current server/db. The
	// synthetic anchors (now / session / reset) carry no server-db identity and are
	// always usable for the current database.
	for _, path := range []string{startPath, endPath} {
		if path == snapNow || path == snapReset || path == snapSession {
			continue
		}
		meta, ok := metaByPath(s.statSnapMetas, path)
		if !ok {
			return nil
		}
		if meta.Target != m.target || meta.Database != st.db {
			m.notice = "snapshot is from a different server/database — can't diff this window"
			return nil
		}
	}

	// Order oldest→newest so the delta (end − start) is non-negative. The pick
	// "lands" as whichever side it ends up on; an end pick keeps the browser open.
	stay := false
	if m.snapTime(s, endPath).Before(m.snapTime(s, startPath)) {
		startPath, endPath = endPath, startPath
	}
	if endPath == cur.snapPath && endPath != snapNow {
		stay = true
	}

	// The session anchor is an in-memory baseline with no Stats slice, so it can
	// only restore the live "since session start → now" window — never a frozen
	// endpoint. Reject any pairing that would put it on either side of a diff.
	if (startPath == snapSession || endPath == snapSession) && (startPath != snapSession || endPath != snapNow) {
		m.notice = "session start can only be diffed against now"
		return nil
	}

	// Dispatch by end. snapNow → live window; snapshot → frozen diff.
	switch endPath {
	case snapNow:
		switch startPath {
		case snapReset:
			// Cumulative live: empty baseline, table grows with each refresh tick.
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = true
			st.statBaseline = map[int64]pg.QueryStat{}
			st.statBaselineAt = time.Time{} // will be updated by the first statementsLoadedMsg
			m.popToStatements()
			return m.loadCurrent()
		case snapSession:
			// Restore the original session window: re-install the preserved baseline
			// and re-sample live, so the table shows everything since the tool opened.
			st.statBaseline = st.statSessionBaseline
			st.statBaselineAt = st.statSessionStart
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = false
			m.popToStatements()
			return m.loadCurrent()
		case snapNow:
			// Both now → fresh live-from-now (equivalent to R).
			st.statBaseline = nil
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = false
			m.popToStatements()
			return m.loadCurrent()
		default:
			// Snapshot → now: existing base→live flow.
			return m.loadSnapshotBaseCmd(startPath)
		}
	default:
		// Frozen window: load snapshots for the diff (snapReset base handled inside).
		return m.loadSnapshotFrozenCmd(startPath, endPath, stay)
	}
}

// appliedWindowPaths maps the statements screen's window state onto snapshot
// browser row paths, so the key handler (the pairing anchor) and the view (the
// start/end markers) agree on which rows the applied window touches. The start
// is "" for a window with no representable row — a fresh R re-base, whose
// baseline is neither the session anchor nor any snapshot.
func (m *Model) appliedWindowPaths(st, s *screen) (startPath, endPath string) {
	endPath = snapNow
	if st.statEndSnap != nil {
		if meta, ok := metaByCapturedAt(s.statSnapMetas, st.statEndSnap.CapturedAt); ok {
			endPath = meta.Path
		} else {
			endPath = ""
		}
	}
	switch {
	case st.statCumulative:
		startPath = snapReset
	case st.statBaseSnap != nil:
		if meta, ok := metaByCapturedAt(s.statSnapMetas, st.statBaseSnap.CapturedAt); ok {
			startPath = meta.Path
		}
	case !st.statSessionStart.IsZero() && st.statBaselineAt.Equal(st.statSessionStart):
		startPath = snapSession
	}
	return startPath, endPath
}

// metaByCapturedAt finds the snapshot meta with the given capture time — how a
// loaded *pg.Snapshot (which doesn't carry its file path) is matched back to
// its browser row.
func metaByCapturedAt(metas []pg.SnapshotMeta, at time.Time) (pg.SnapshotMeta, bool) {
	for _, meta := range metas {
		if meta.CapturedAt.Equal(at) {
			return meta, true
		}
	}
	return pg.SnapshotMeta{}, false
}

// snapTime returns the time associated with a snapshot browser path for the
// purpose of ordering a start/end pair. snapReset maps to the zero time (earliest);
// snapNow maps to a far-future time (latest); snapSession to the recorded session
// start; real snapshot paths use CapturedAt.
func (m *Model) snapTime(s *screen, path string) time.Time {
	switch path {
	case snapReset:
		return time.Time{} // zero = year 1 = earliest
	case snapNow:
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	case snapSession:
		if st := m.findLevel(levelStatements); st != nil {
			return st.statSessionStart
		}
		return time.Time{}
	default:
		meta, ok := metaByPath(s.statSnapMetas, path)
		if !ok {
			return time.Time{}
		}
		return meta.CapturedAt
	}
}

// metaByPath finds the snapshot meta with the given file path.
func metaByPath(metas []pg.SnapshotMeta, path string) (pg.SnapshotMeta, bool) {
	for _, meta := range metas {
		if meta.Path == path {
			return meta, true
		}
	}
	return pg.SnapshotMeta{}, false
}

// heapWindowDefault is the number of heap pages loaded per page-inspector
// window. 2 000 pages is ~16 MiB of raw_page reads — fast on a warm cache
// and small enough that the resulting item list still scrolls comfortably.
// PgUp/PgDn slides the window in heapWindowDefault-sized steps.
const heapWindowDefault int32 = 2000
