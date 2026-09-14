package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// jumpToPageInspector opens the page-inspector view for the part under the
// cursor on levelParts: heap → heap pages of the table, toast → heap pages of
// the TOAST relation, index → index pages (btree/gist/brin/gin). On the
// shared-buffers screens it opens the heap pages of the highlighted (or
// inspected) table, so a hot/dirty table can be read page by page; on a table
// describe panel it opens the described table's heap pages. The pushed
// screen is stamped toolPageInspect so it renders and lays out exactly as if
// reached through the page-inspector tool; q pops back to where it came from.
func (m *Model) jumpToPageInspector(s *screen) tea.Cmd {
	switch s.level {
	case levelBufferTables, levelBufferDetail:
		target, ok := bufDescribeTarget(s)
		if !ok {
			return nil
		}
		// TotalBytes covers indexes and toast too; HeapBytes is cosmetic for the
		// heap-pages view (relpages is loaded), so the same reconstruction serves.
		t := target.table
		t.HeapBytes = t.TotalBytes
		m.stack = append(m.stack, heapPagesScreen(t, "heap pages"))
		return m.loadCurrent()
	case levelDescribe:
		if !describeHasHeap(s) {
			return nil
		}
		m.stack = append(m.stack, heapPagesScreen(s.table, "heap pages"))
		return m.loadCurrent()
	case levelParts:
	default:
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
			db: r.DB, schema: r.Schema, pages: pageState{index: r, heapWindowStart: 0, heapWindowCount: heapWindowDefault},
			sort: pageSort, sortDesc: pageSort.defaultDesc()}
	default:
		return nil
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// describeHasHeap reports whether a describe screen shows a table whose heap
// the page inspector can open: a loaded table describe with a resolved
// pg.Table behind it (index describes have no heap of their own).
func describeHasHeap(s *screen) bool {
	return s.level == levelDescribe && s.loaded && s.desc.info != nil &&
		s.desc.info.Kind == pg.DescribeTable && s.table.OID != 0 && s.table.OID == s.desc.info.OID
}

// heapPagesScreen is the first page-inspector window over a heap (table or
// TOAST relation), identical to what the page-inspector tool pushes itself.
func heapPagesScreen(t pg.Table, title string) *screen {
	return &screen{
		level: levelHeapPages, title: title, tool: toolPageInspect,
		db: t.DB, schema: t.Schema, table: t,
		pages: pageState{heapWindowStart: 0, heapWindowCount: heapWindowDefault},
		sort:  sortByBlkno, sortDesc: sortByBlkno.defaultDesc()}
}

// pagesDescribeTarget names the relation behind a page-inspector screen for `d`: the row's relation on the relation list, the screen's table or index below it.
func pagesDescribeTarget(s *screen) (descTarget, bool) {
	curItem := s.currentItem
	switch s.level {
	case levelRelations:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		r, ok := it.data.(pg.Relation)
		if !ok {
			return descTarget{}, false
		}
		switch r.Kind {
		case pg.RelTable, pg.RelToast:
			return descTarget{table: pg.Table{
				DB: r.DB, Schema: r.Schema, OID: r.OID, Name: r.Name,
				TotalBytes: r.SizeBytes, EstRows: r.EstRows,
			}}, true
		case pg.RelBTreeIndex, pg.RelGist, pg.RelBrin, pg.RelGin:
			return descTarget{
				isIndex:   true,
				db:        r.DB,
				indexOID:  r.OID,
				indexName: r.Qualified(),
			}, true
		}
		return descTarget{}, false
	case levelHeapPages, levelHeapTuples, levelTupleRow:
		return descTarget{table: s.table}, true
	case levelIndexPages, levelIndexTuples:
		return descTarget{
			isIndex:   true,
			db:        s.db,
			indexOID:  s.pages.index.OID,
			indexName: s.pages.index.Qualified(),
		}, true
	}
	return descTarget{}, false
}

// jumpGinDataLeaf (n on a GIN page list) starts the server-side search for the
// next posting-tree leaf page after the cursor's block — the only GIN pages
// pageinspect can itemize, and typically a handful scattered through tens of
// thousands of entry pages, so no in-window sort can surface them. The result
// lands in onGinDataLeafFound, which moves the window and the cursor.
func (m *Model) jumpGinDataLeaf(s *screen) tea.Cmd {
	if s.level != levelIndexPages || s.pages.index.AccessMethod != "gin" || s.pages.ginSeeking {
		return nil
	}
	from := s.pages.heapWindowStart
	if cur, ok := s.currentItem(); ok {
		if p, ok := cur.data.(pg.GinPageStat); ok {
			from = p.Blkno + 1
		}
	}
	if s.pages.ginMeta != nil && s.pages.ginMeta.DataPages == 0 {
		// The metapage already says there is nothing to find; skip the scan.
		m.notice = "no data-leaf pages: every posting list fits inline in its entry tuple, so pageinspect can't itemize anything in this index"
		return nil
	}
	s.pages.ginSeeking = true
	return m.findGinDataLeafCmd(s.pages.index, from)
}
