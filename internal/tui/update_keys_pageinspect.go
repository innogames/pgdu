package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// jumpToPageInspector opens the page-inspector view for the part under the
// cursor on levelParts: heap → heap pages of the table, toast → heap pages of
// the TOAST relation, index → index pages (btree/gist/brin/gin). The pushed
// screen is stamped toolPageInspect so it renders and lays out exactly as if
// reached through the page-inspector tool; q pops back to the parts list.
func (m *Model) jumpToPageInspector(s *screen) tea.Cmd {
	if s.level != levelParts {
		return nil
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return nil
	}
	p, ok := s.items[vis[s.cursor]].data.(pg.Part)
	if !ok {
		return nil
	}
	var next *screen
	switch p.Kind {
	case pg.PartHeap:
		next = heapPagesScreen(s.table, "heap pages")
	case pg.PartToast:
		if s.table.ToastOID == 0 {
			return nil
		}
		// ToastName is qualified ("pg_toast.pg_toast_16438"); the loaders build
		// the regclass from Schema + Name, so split it back apart.
		schema, name := "pg_toast", p.ToastName
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			schema, name = name[:i], name[i+1:]
		}
		t := pg.Table{
			DB: s.db, Schema: schema, OID: s.table.ToastOID, Name: name,
			HeapBytes: p.SizeBytes,
		}
		next = heapPagesScreen(t, "toast pages")
	case pg.PartIndex:
		r := pg.Relation{
			DB: s.db, Schema: s.schema, OID: p.OID, Name: p.Name,
			SizeBytes: p.SizeBytes, AccessMethod: p.AccessMethod,
			ParentOID: s.table.OID, ParentName: s.table.Name,
		}
		pageSort := sortByBlkno
		switch p.AccessMethod {
		case "btree":
			r.Kind = pg.RelBTreeIndex
			pageSort = sortByLevel // root first, same as the relations drill
		case "gist":
			r.Kind = pg.RelGist
		case "brin":
			r.Kind = pg.RelBrin
		case "gin":
			r.Kind = pg.RelGin
		default:
			m.notice = p.AccessMethod + " indexes can't be inspected — pageinspect only decodes btree/gist/brin/gin pages"
			return nil
		}
		next = &screen{
			level: levelIndexPages, title: "index pages", tool: toolPageInspect,
			db: r.DB, schema: r.Schema, index: r,
			heapWindowStart: 0, heapWindowCount: heapWindowDefault,
			sort: pageSort, sortDesc: pageSort.defaultDesc(),
		}
	default:
		return nil
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// heapPagesScreen is the first page-inspector window over a heap (table or
// TOAST relation), identical to what the page-inspector tool pushes itself.
func heapPagesScreen(t pg.Table, title string) *screen {
	return &screen{
		level: levelHeapPages, title: title, tool: toolPageInspect,
		db: t.DB, schema: t.Schema, table: t,
		heapWindowStart: 0, heapWindowCount: heapWindowDefault,
		sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
	}
}
