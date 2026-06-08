package tui

import (
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
		var next *screen
		switch s.tool {
		case toolBuffers:
			next = &screen{level: levelBufferTables, title: "buffers", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		case toolPageInspect:
			next = &screen{level: levelRelations, title: "relations", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		default:
			next = &screen{level: levelTables, title: "tables", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		}
		m.stack = append(m.stack, next)
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
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelSnapshots:
		return m.loadSelectedSnapshot(s, cur)
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
		s.loading = false
		s.loaded = true
		return nil
	case levelDiagnostics:
		s.items = diagnosticItems()
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
	case levelParts:
		// Probe pgstattuple alongside the parts load. The probe is cheap
		// (one pg_extension / pg_available_extensions lookup) and lets the
		// view offer an install when exact bloat would be measurable but
		// the extension isn't there yet.
		return tea.Batch(
			m.loadPartsCmd(s.table),
			m.probeExtensionCmd(s.db, extPgStatTuple),
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
		// (index describe — s.table.OID is 0 for index targets).
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
	}
	return nil
}

// loadSelectedSnapshot acts on Enter in the snapshots browser. With no marked
// base it loads the highlighted snapshot as the live window's baseline
// (base→now); with a marked base it builds a frozen A→B diff between the two
// (ordered oldest→newest). Incompatible snapshots (different server/database than
// the in-view top-queries screen, or each other) are refused with a notice.
func (m *Model) loadSelectedSnapshot(s *screen, cur item) tea.Cmd {
	meta, ok := metaByPath(s.statSnapMetas, cur.snapPath)
	if !ok {
		return nil
	}
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}
	if meta.Target != m.target || meta.Database != st.db {
		m.notice = "snapshot is from a different server/database — can't diff this window"
		return nil
	}
	if s.statMarkBase == "" || s.statMarkBase == meta.Path {
		return m.loadSnapshotBaseCmd(meta.Path)
	}
	base, ok := metaByPath(s.statSnapMetas, s.statMarkBase)
	if !ok {
		return m.loadSnapshotBaseCmd(meta.Path)
	}
	if base.Target != meta.Target || base.Database != meta.Database {
		m.notice = "the two snapshots are from different servers/databases — can't diff"
		return nil
	}
	// Order oldest→newest so the A→B delta (end − base) is non-negative.
	older, newer := base, meta
	if newer.CapturedAt.Before(older.CapturedAt) {
		older, newer = newer, older
	}
	return m.loadSnapshotFrozenCmd(older.Path, newer.Path)
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
