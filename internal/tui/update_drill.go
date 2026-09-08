package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

func (m *Model) drillIn() tea.Cmd {
	s := m.top()
	if !s.loaded {
		return nil
	}
	cur, ok := s.currentItem()
	if !ok {
		return nil
	}
	switch s.level {
	case levelTools:
		t := cur.data.(tool)
		m.stack = append(m.stack, m.toolEntryScreen(t))
		return m.loadCurrent()
	case levelDiagnostics:
		d := cur.data.(pg.Diagnostic)
		// Per-database queries only describe the connected database, so ask
		// which one to run against via the common database picker (with an
		// "all databases" option). Cluster-wide queries run straight away.
		if d.PerDB {
			m.stack = append(m.stack, &screen{
				level: levelDatabases, title: "database", tool: toolTools, diag: &d,
				sort: sortBySize, sortDesc: sortBySize.defaultDesc()})
			return m.loadCurrent()
		}
		m.stack = append(m.stack, diagnosticResultScreen(&d, "", false))
		return m.loadCurrent()
	case levelDatabases:
		// In the diagnostics tool the database list is a picker for a
		// per-database query, not the disk-usage tree: drill into the result.
		if s.tool == toolTools && s.diag != nil {
			var next *screen
			if _, ok := cur.data.(allDBsChoice); ok {
				next = diagnosticResultScreen(s.diag, "", true)
			} else {
				next = diagnosticResultScreen(s.diag, cur.data.(pg.Database).Name, false)
			}
			m.stack = append(m.stack, next)
			return m.loadCurrent()
		}
		d := cur.data.(pg.Database)
		m.stack = append(m.stack, databaseChildScreens(s.tool, d.Name)...)
		return m.loadCurrent()
	case levelSchemas:
		sc := cur.data.(pg.Schema)
		m.stack = append(m.stack, schemaChildScreen(s.tool, sc))
		return m.loadCurrent()
	case levelTables:
		return m.drillTable(s, cur)
	case levelTableStats:
		return m.drillTableStat(s, cur)
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
			db: s.db, schema: st.Schema, buf: bufState{detail: &stat}}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelHeapPages:
		return m.drillHeapPage(s, cur)
	case levelHeapTuples:
		return m.drillHeapTuple(s, cur)
	case levelRelations:
		return m.drillRelation(s, cur)
	case levelIndexPages:
		return m.drillIndexPage(s, cur)
	case levelIndexTuples:
		return m.drillIndexTuple(s, cur)
	case levelWAL:
		return m.drillWAL(s, cur)
	case levelWALRecords:
		return m.drillWALRecord(s, cur)
	case levelWALBlocks, levelWALRelBlocks:
		return m.drillWALBlock(s, cur)
	case levelStatements:
		return m.drillStatement(s, cur)
	case levelSnapshots:
		return m.loadSelectedSnapshot(s, cur)
	case levelActivity:
		return m.drillActivityStatement(s, cur)
	case levelPgBouncers:
		if cur.pgbIdx <= 0 || cur.pgbIdx > len(s.pgb.insts) {
			return nil
		}
		m.stack = append(m.stack, m.pgbOverviewScreen(s.pgb.insts[cur.pgbIdx-1]))
		return m.loadCurrent()
	case levelPgBouncer:
		switch d := cur.data.(type) {
		case pgbShow:
			m.stack = append(m.stack, m.pgbShowScreen(s, d))
			return m.loadCurrent()
		case pgbLogRow:
			if s.pgb.inst == nil || s.pgb.inst.Logfile == "" {
				return nil
			}
			m.stack = append(m.stack, m.logScreen(pglog.OpenLocal(s.pgb.inst.Logfile)))
			return m.loadCurrent()
		}
		return nil
	case levelLogFiles:
		if cur.logIdx <= 0 || cur.logIdx > len(s.log.cands) {
			return nil
		}
		m.stack = append(m.stack, m.logScreen(s.log.cands[cur.logIdx-1].Open()))
		return m.loadCurrent()
	case levelLogs:
		if s.log.view.table() {
			if e := s.logEntryOf(cur); e != nil {
				m.stack = append(m.stack, m.logEntryScreen(s, e))
				return m.loadCurrent()
			}
			return nil
		}
		g, ok := cur.data.(*pglog.Group)
		if !ok {
			return nil // section header rows are inert
		}
		m.stack = append(m.stack, m.logGroupScreen(s, g))
		return m.loadCurrent()
	case levelLogGroup:
		e := s.logEntryOf(cur)
		if e == nil {
			return nil
		}
		if _, table := cur.data.([]pg.DiagCell); table {
			// A parameter row opens the entries behind it; the key is recomputed
			// from the row's newest entry so nothing but the entry index rides on
			// the item.
			first := s.log.params == logParamsFirst
			child := m.logGroupScreen(s, s.log.group)
			child.title = "parameters"
			child.log.paramKey = pglog.ParamKey(e, first)
			child.log.paramFirst = first
			m.stack = append(m.stack, child)
			return m.loadCurrent()
		}
		m.stack = append(m.stack, m.logEntryScreen(s, e))
		return m.loadCurrent()
	case levelTriage:
		return m.drillTriage(s, cur)
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
			sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	return nil
}

// --- GiST / BRIN / GIN drill handlers (called from drillIn) ---

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
	case toolTriage:
		// Triage runs its battery cluster-wide: skip the database picker and go
		// straight to the report, loading asynchronously (loadTriageCmd).
		return &screen{level: levelTriage, title: "triage", tool: toolTriage, db: m.client.DefaultDB(), loading: true}
	case toolLogs:
		// An explicit --log-file skips the picker; otherwise discover candidates
		// (pg_current_logfile, /var/log/postgresql, server log dir) first.
		if m.logFile != "" {
			return m.logScreen(pglog.OpenLocal(m.logFile))
		}
		return &screen{level: levelLogFiles, title: "log files", tool: toolLogs, db: m.client.DefaultDB(), loading: true}
	case toolPgBouncer:
		// Instances are discovered (not picked from a database), so the list
		// loads asynchronously; a single instance auto-drills into its overview.
		return &screen{level: levelPgBouncers, title: "pgbouncer", tool: toolPgBouncer, db: m.client.DefaultDB(), loading: true}
	case toolActivity:
		// Activity tool is cluster-wide: skip the database picker and go
		// directly to the live pg_stat_activity list.
		return &screen{
			level: levelActivity,
			title: "activity",
			tool:  toolActivity,
			db:    m.client.DefaultDB(),
			act:   actState{filter: pg.ActivityActiveWaiting, hosts: make(map[string]string)}}
	default:
		return &screen{level: levelDatabases, title: "databases", tool: t, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	}
}

// diagnosticResultScreen builds the levelDiagnosticResult screen for diagnostic
// d against database db (empty = default DB). allDBs runs it across every
// connectable database, merging the results under a leading "database" column.
func diagnosticResultScreen(d *pg.Diagnostic, db string, allDBs bool) *screen {
	return &screen{
		level:      levelDiagnosticResult,
		title:      d.Title,
		tool:       toolTools,
		diag:       d,
		db:         db,
		diagAllDBs: allDBs,
		diagBarCol: -1}
}

// databaseChildScreens builds the screens pushed when drilling into a database,
// varying by tool; the last one is the screen loadCurrent acts on. Used by
// drillIn and the single-database fast path in onDatabasesLoaded.
func databaseChildScreens(t tool, db string) []*screen {
	if t == toolQueries {
		// Queries has no schema/table hierarchy — the top-queries table for the
		// chosen database comes next, but it opens behind the snapshot browser
		// in entry mode: the table's window has no time axis of its own, so the
		// user first picks its base (session start by default, a saved snapshot
		// or since last reset) and the table loads only once that pick lands.
		return []*screen{
			{level: levelStatements, title: "queries", tool: toolQueries, db: db},
			{level: levelSnapshots, title: "snapshots", tool: toolQueries, db: db, loading: true, stat: stmtState{entry: true}},
		}
	}
	return []*screen{{level: levelSchemas, title: "schemas", tool: t, db: db, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}}
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
// with. With no anchor (the entry picker, the default session window or a fresh R re-base) the
// pick becomes the start and the end is "now" (live). With an anchor the window
// spans the time-ordered range between anchor and pick — frozen unless the pick
// is "now". A pick that lands as the start pops back to the table (the one-key

// heapWindowDefault is the number of heap pages loaded per page-inspector
// window. 2 000 pages is ~16 MiB of raw_page reads — fast on a warm cache
// and small enough that the resulting item list still scrolls comfortably.
// PgUp/PgDn slides the window in heapWindowDefault-sized steps.
const heapWindowDefault int32 = 2000

// drillTable opens a table's parts (disk) or heap pages (page inspector).
func (m *Model) drillTable(s *screen, cur item) tea.Cmd {
	t := cur.data.(pg.Table)
	var next *screen
	switch s.tool {
	case toolPageInspect:
		next = &screen{
			level: levelHeapPages, title: "heap pages", tool: s.tool,
			db: t.DB, schema: t.Schema, table: t,
			pages: pageState{heapWindowStart: 0, heapWindowCount: heapWindowDefault},
			sort:  sortByBlkno, sortDesc: sortByBlkno.defaultDesc()}
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
			sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}
