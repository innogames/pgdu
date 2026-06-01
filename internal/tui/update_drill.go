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
		next := &screen{level: levelDatabases, title: "databases", tool: t, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelDatabases:
		d := cur.data.(pg.Database)
		next := &screen{level: levelSchemas, title: "schemas", tool: s.tool, db: d.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelSchemas:
		sc := cur.data.(pg.Schema)
		var next *screen
		switch s.tool {
		case toolBuffers:
			next = &screen{level: levelBufferTables, title: "buffers", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
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
		next := &screen{
			level: levelTupleRow, title: "row", tool: s.tool,
			db: s.db, schema: s.schema, table: s.table,
			tupleCtid: *ht.Ctid,
			sort:      sortByName, sortDesc: false,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
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
	switch s.level {
	case levelTools:
		s.items = toolItems()
		s.loading = false
		s.loaded = true
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
		return m.loadTupleRowCmd(s.table, s.tupleCtid)
	}
	return nil
}

// heapWindowDefault is the number of heap pages loaded per page-inspector
// window. 2 000 pages is ~16 MiB of raw_page reads — fast on a warm cache
// and small enough that the resulting item list still scrolls comfortably.
// PgUp/PgDn slides the window in heapWindowDefault-sized steps.
const heapWindowDefault int32 = 2000
