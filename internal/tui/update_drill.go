package tui

import (
	"fmt"

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
	case levelTableStats:
		// Drill into the disk "parts" view (heap/index/toast + per-index bloat)
		// for the table under the cursor. The row carries its OID in statQueryID;
		// resolve it back to the loaded TableStat (sort-order independent) and
		// reconstruct a pg.Table. The parts screen is stamped toolDisk — the only
		// tool that drives levelParts — so it behaves exactly like a disk drill.
		var ts *pg.TableStat
		for i := range s.tblRows {
			if int64(s.tblRows[i].OID) == cur.statQueryID {
				ts = &s.tblRows[i]
				break
			}
		}
		if ts == nil {
			return nil
		}
		t := ts.AsTable()
		if !m.vacuum.running {
			m.vacuum = vacuumState{}
		}
		next := &screen{
			level: levelParts, title: "parts", tool: toolDisk,
			db: t.DB, schema: t.Schema, table: t,
			sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
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
		case pg.RelBTreeIndex, pg.RelGist, pg.RelBrin, pg.RelGin:
			// Every drillable index AM uses the shared levelIndexPages screen;
			// the loader/renderer branch on r.AccessMethod from here on.
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
		switch s.index.AccessMethod {
		case "gist":
			return m.drillGistPage(s, cur)
		case "brin":
			return m.drillBrinPage(s, cur)
		case "gin":
			return m.drillGinPage(s, cur)
		}
		p, ok := cur.data.(pg.IndexPageStat)
		if !ok {
			return nil
		}
		next := &screen{
			level: levelIndexTuples, title: "index tuples", tool: s.tool,
			db: s.db, schema: s.schema, index: s.index,
			indexKeyCols:   s.indexKeyCols, // carry the keys banner; no refetch
			heapPageCount:  s.heapPageCount,
			indexPageBlkno: p.Blkno,
			indexPageType:  indexTuplePageType(p.Type, p.BtpoLevel),
			sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelIndexTuples:
		switch s.index.AccessMethod {
		case "gist":
			return m.drillGistItem(s, cur)
		case "brin":
			return m.drillBrinItem(s, cur)
		case "gin":
			return nil // GIN posting-list segments are terminal
		}
		t, ok := cur.data.(pg.IndexTuple)
		if !ok {
			return nil
		}
		// On an internal page every entry is a downlink: its ctid block names a
		// child index page. Enter descends one level toward the leaves so the
		// user can walk the tree structurally. Leaf high-key pivots live on leaf
		// pages, not here, so this only fires for real downlinks.
		if s.indexPageType == "i" {
			child, ok := downlinkChildBlock(t, s.heapPageCount, s.indexPageBlkno)
			if !ok {
				return nil
			}
			next := &screen{
				level: levelIndexTuples, title: "index tuples", tool: s.tool,
				db: s.db, schema: s.schema, index: s.index,
				indexKeyCols:   s.indexKeyCols,
				heapPageCount:  s.heapPageCount,
				indexPageBlkno: child,
				// Child page type is unknown here; leave it empty and let
				// loadIndexTuplesCmd probe it (bt_page_stats) so the decode path
				// and further downlink navigation stay correct mid-descent.
				indexPageType: "",
				sort:          sortByLP, sortDesc: sortByLP.defaultDesc(),
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		if t.Ctid == nil || t.Decoded == nil {
			// Leaf entries drill only when a live heap row was projected.
			// Pivot/posting entries and vacuumed rows land here too; none has a
			// single heap row to show in the per-column view.
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
	case levelWALRelations:
		st, ok := cur.data.(pg.WALRelStat)
		if !ok || st.RecCount == 0 {
			return nil
		}
		// The block-refs list is keyed on relfilenode; carry a human label for
		// the breadcrumb/status row (resolved name, or the numeric fallback).
		label := st.RelName
		if label == "" {
			label = fmt.Sprintf("relfilenode %d", st.RelFileNode)
		}
		if st.IsToast {
			label += " (toast)"
		}
		next := &screen{
			level: levelWALRelBlocks, title: "wal rel blocks", tool: s.tool,
			db: s.db, walStart: s.walStart, walEnd: s.walEnd,
			walRelFilenode: st.RelFileNode, walRelLabel: label,
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

// indexTuplePageType normalizes a B-tree page's bt_page_stats type for the
// tuple view. pageinspect reports the root as 'r' whether it's a single-page
// index (the root is also a leaf — its entries are heap pointers we can decode
// against the table) or the top of a taller tree (the root is internal — its
// entries are downlinks to child index pages). Only the level disambiguates: a
// non-leaf root (level > 0) must be treated exactly like an internal page, or
// its downlink ctids get looked up in the heap and resolve to unrelated rows,
// printing bogus, unsorted "keys". Mapping it to 'i' here flips both the decode
// path (ListIndexTuples skips the heap join) and the renderer (downlink ranges
// and "→ blk N" instead of a fake key column) in one place.
func indexTuplePageType(typ string, level int32) string {
	if typ == "r" && level > 0 {
		return "i"
	}
	return typ
}

// downlinkChildBlock resolves the child index page an internal-page downlink
// points at. On internal B-tree pages the item's ctid block IS the child block
// number (the offset word carries pivot flag bits, which we ignore). Returns
// false when the ctid doesn't parse, points at the meta page / itself, or — when
// the index's page count is known — lands past EOF, so a malformed downlink
// can't send get_raw_page off the end.
func downlinkChildBlock(t pg.IndexTuple, pageCount, current int32) (int32, bool) {
	blk, ok := parseCtidBlock(t.Ctid)
	if !ok || blk <= 0 || blk == current {
		return 0, false
	}
	if pageCount > 0 && blk >= pageCount {
		return 0, false
	}
	return blk, true
}

// --- GiST / BRIN / GIN drill handlers (called from drillIn) ---

// drillGistPage opens a GiST page's items. Leaf and internal pages both carry
// items worth showing; deleted/empty pages don't drill.
func (m *Model) drillGistPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.GistPageStat)
	if !ok {
		return nil
	}
	if p.IsDeleted {
		m.notice = "deleted page — emptied by VACUUM, nothing to inspect"
		return nil
	}
	if p.Items == 0 {
		m.notice = "empty page — no items to inspect"
		return nil
	}
	next := &screen{
		level: levelIndexTuples, title: "index tuples", tool: s.tool,
		db: s.db, schema: s.schema, index: s.index,
		indexKeyCols:   s.indexKeyCols,
		heapPageCount:  s.heapPageCount,
		indexPageBlkno: p.Blkno,
		indexPageType:  gistPageRole(p.IsLeaf, p.IsDeleted),
		sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillGistItem walks a GiST internal-page downlink to its child page, or opens
// the heap row a leaf entry points at (mirrors the B-tree tree-walk).
func (m *Model) drillGistItem(s *screen, cur item) tea.Cmd {
	t, ok := cur.data.(pg.GistItem)
	if !ok {
		return nil
	}
	if s.indexPageType == "intr" {
		child, ok := gistDownlinkChildBlock(t, s.heapPageCount, s.indexPageBlkno)
		if !ok {
			return nil
		}
		next := &screen{
			level: levelIndexTuples, title: "index tuples", tool: s.tool,
			db: s.db, schema: s.schema, index: s.index,
			indexKeyCols:   s.indexKeyCols,
			heapPageCount:  s.heapPageCount,
			indexPageBlkno: child,
			indexPageType:  "", // probed by loadGistItemsCmd mid-descent
			sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	if t.Dead || t.Ctid == nil {
		return nil
	}
	parent := pg.Table{DB: s.db, Schema: s.schema, OID: s.index.ParentOID, Name: s.index.ParentName}
	next := &screen{
		level: levelTupleRow, title: "row", tool: s.tool,
		db: s.db, schema: s.schema, table: parent,
		tupleCtid: *t.Ctid,
		sort:      sortByName, sortDesc: false,
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// gistDownlinkChildBlock resolves the child page a GiST internal-page downlink
// points at (its ctid block). Mirrors downlinkChildBlock's safety guards.
func gistDownlinkChildBlock(t pg.GistItem, pageCount, current int32) (int32, bool) {
	blk, ok := parseCtidBlock(t.Ctid)
	if !ok || blk <= 0 || blk == current {
		return 0, false
	}
	if pageCount > 0 && blk >= pageCount {
		return 0, false
	}
	return blk, true
}

// drillBrinPage opens a regular BRIN page's range summaries; meta/revmap pages
// have nothing to itemize. Carries the metapage down so the range column and
// block-seek know pages-per-range.
func (m *Model) drillBrinPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.BrinPageStat)
	if !ok {
		return nil
	}
	if p.PageType != "regular" {
		m.notice = p.PageType + " page holds no range summaries — open a regular page to see them"
		return nil
	}
	next := &screen{
		level: levelIndexTuples, title: "brin ranges", tool: s.tool,
		db: s.db, schema: s.schema, index: s.index,
		indexKeyCols:   s.indexKeyCols,
		heapPageCount:  s.heapPageCount,
		indexPageBlkno: p.Blkno,
		indexPageType:  "regular",
		brinMeta:       s.brinMeta,
		sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillBrinItem jumps from a BRIN range summary to the heap pages of the block
// range it covers, positioning the heap-pages window at the range's start.
func (m *Model) drillBrinItem(s *screen, cur item) tea.Cmd {
	t, ok := cur.data.(pg.BrinItem)
	if !ok {
		return nil
	}
	parent := pg.Table{DB: s.db, Schema: s.schema, OID: s.index.ParentOID, Name: s.index.ParentName}
	start := max(int32(t.BlockNum), 0)
	start = (start / heapWindowDefault) * heapWindowDefault // align to the window grid
	next := &screen{
		level: levelHeapPages, title: "heap pages", tool: s.tool,
		db: s.db, schema: s.schema, table: parent,
		heapWindowStart: start, heapWindowCount: heapWindowDefault,
		sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
	}
	m.notice = fmt.Sprintf("heap pages from block %d — BRIN range start", t.BlockNum)
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillGinPage opens a GIN data-leaf page's posting-list segments; entry-tree
// and other pages aren't itemizable via pageinspect.
func (m *Model) drillGinPage(s *screen, cur item) tea.Cmd {
	p, ok := cur.data.(pg.GinPageStat)
	if !ok {
		return nil
	}
	if !ginPageIsDataLeaf(p.Flags) {
		// pageinspect can only list compressed data-leaf pages; entry-tree and
		// meta pages aren't itemizable. Point the user at the drillable kind.
		m.notice = "only data-leaf pages are itemizable (pageinspect can't read entry-tree keys) — sort by type (→) to find them"
		return nil
	}
	next := &screen{
		level: levelIndexTuples, title: "gin posting lists", tool: s.tool,
		db: s.db, schema: s.schema, index: s.index,
		indexKeyCols:   s.indexKeyCols,
		heapPageCount:  s.heapPageCount,
		indexPageBlkno: p.Blkno,
		indexPageType:  "data-leaf",
		sort:           sortByLP, sortDesc: sortByLP.defaultDesc(),
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
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
		return &screen{level: levelMaintenance, title: "system overview", tool: toolMaintenance, db: m.client.DefaultDB()}
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
	case toolTableStats:
		// The table-overview list is a generic (diagCols) table sorted by column,
		// so the sortMode here is unused — the column sort is tracked separately.
		return &screen{level: levelTableStats, title: "table overview", tool: t, db: sc.DB, schema: sc.Name}
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
