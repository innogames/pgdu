package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// jumpToPageInspector opens the page-inspector view for the part under the
// cursor on levelParts: heap → heap pages of the table, toast → heap pages of
// the TOAST relation, index → index pages (btree/gist/brin/gin). On the
// shared-buffers screens it opens the highlighted (or inspected) row the same
// way: a table's heap pages, or a split-view relation's own pages, so a
// hot/dirty table or index can be read page by page; on a table describe panel
// it opens the described table's heap pages. The pushed screen is stamped
// toolPageInspect so it renders and lays out exactly as if reached through the
// page-inspector tool; q pops back to where it came from.
func (m *Model) jumpToPageInspector(s *screen) tea.Cmd {
	var t pg.Table
	part := pg.Part{Kind: pg.PartHeap}
	switch s.level {
	case levelBufferTables, levelBufferDetail:
		st, ok := bufStatAt(s)
		if !ok {
			return nil
		}
		// TotalBytes is the whole table (or the split-view relation); HeapBytes
		// is cosmetic for the heap-pages view (relpages is loaded), so it serves.
		t = bufStatTable(st)
		t.HeapBytes = t.TotalBytes
		if st.Rel != nil {
			part = bufRelPart(st.Rel)
			part.SizeBytes = st.TotalBytes
			if part.Kind == pg.PartToast {
				t.ToastOID, part.ToastName = st.Rel.OID, st.Rel.Name
			}
		}
	case levelDescribe:
		if !describeHasHeap(s) {
			return nil
		}
		t = s.table
	case levelParts:
		vis := s.visibleIndexes()
		if s.cursor < 0 || s.cursor >= len(vis) {
			return nil
		}
		p, ok := s.items[vis[s.cursor]].data.(pg.Part)
		if !ok {
			return nil
		}
		t, part = s.table, p
	default:
		return nil
	}
	next := m.partPagesScreen(t, part)
	if next == nil {
		return nil
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// partPagesScreen is the page-inspector screen over one storage part of table
// t: its heap, its TOAST heap (t.ToastOID, named by p.ToastName, qualified or
// bare in pg_toast) or the index p. nil when there is nothing to open; an index
// AM pageinspect can't decode also leaves a notice.
func (m *Model) partPagesScreen(t pg.Table, p pg.Part) *screen {
	switch p.Kind {
	case pg.PartHeap:
		return heapPagesScreen(t, "heap pages")
	case pg.PartToast:
		if t.ToastOID == 0 {
			return nil
		}
		// The loaders build the regclass from Schema + Name, so split a
		// qualified ToastName ("pg_toast.pg_toast_16438") back apart.
		schema, name := "pg_toast", p.ToastName
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			schema, name = name[:i], name[i+1:]
		}
		return heapPagesScreen(pg.Table{
			DB: t.DB, Schema: schema, OID: t.ToastOID, Name: name,
			HeapBytes: p.SizeBytes,
		}, "toast pages")
	case pg.PartIndex:
		r := pg.Relation{
			DB: t.DB, Schema: t.Schema, OID: p.OID, Name: p.Name,
			SizeBytes: p.SizeBytes, AccessMethod: p.AccessMethod,
			ParentOID: t.OID, ParentName: t.Name,
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
		return &screen{
			level: levelIndexPages, title: "index pages", tool: toolPageInspect,
			db: r.DB, schema: r.Schema, pages: pageState{index: r, heapWindowStart: 0, heapWindowCount: heapWindowDefault},
			sort: pageSort, sortDesc: pageSort.defaultDesc()}
	}
	return nil
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
	case levelHeapPages, levelHeapTuples:
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
