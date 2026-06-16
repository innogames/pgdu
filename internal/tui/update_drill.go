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
		m.stack = append(m.stack, m.toolEntryScreen(t))
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
			// Fresh visit to a table: drop a previous run's finished vacuum
			// output so stale logs don't reappear when the pane is keyed by OID.
			// Leave a still-running vacuum alone — its pane should stay live.
			if !m.vacuum.running {
				m.vacuum = vacuumState{}
			}
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

// the picker (and when a --<tool> flag opens one directly at startup). The
// cluster-wide tools (tools/wal/maintenance/activity) drill straight to their
// leaf; disk/buffers/queries open the database picker first — pg_stat_statements
// is read from whichever database is picked, so toolQueries goes there too.
func (m *Model) toolEntryScreen(t tool) *screen {
	switch t {
	case toolTools:
		// toolTools goes directly to the flat diagnostic-query list, not
		// through a database picker — all queries run against the default DB.
		return &screen{level: levelDiagnostics, title: "tools", tool: toolTools, sort: sortByName, sortDesc: sortByName.defaultDesc()}
	case toolWAL:
		// WAL is cluster-wide, so it skips the database picker too. The
		// concrete default DB is pinned on the screen (not "") so the
		// extension-install prompt can name a real database.
		return &screen{level: levelWAL, title: "wal", tool: toolWAL, db: m.client.DefaultDB(), sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	case toolMaintenance:
		// Maintenance dashboard is cluster-wide: skip the database picker.
		return &screen{level: levelMaintenance, title: "maintenance", tool: toolMaintenance, db: m.client.DefaultDB()}
	case toolActivity:
		// Activity tool is cluster-wide: skip the database picker and go
		// directly to the live pg_stat_activity list.
		return &screen{
			level:     levelActivity,
			title:     "activity",
			tool:      toolActivity,
			db:        m.client.DefaultDB(),
			actFilter: pg.ActivityActiveWaiting,
			actHosts:  make(map[string]string),
		}
	default:
		return &screen{level: levelDatabases, title: "databases", tool: t, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	}
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

// heapWindowDefault is the number of heap pages loaded per page-inspector
// window. 2 000 pages is ~16 MiB of raw_page reads — fast on a warm cache
// and small enough that the resulting item list still scrolls comfortably.
// PgUp/PgDn slides the window in heapWindowDefault-sized steps.
const heapWindowDefault int32 = 2000
