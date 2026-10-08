package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// bufSplitScreen is a loaded buffers list over two tables: orders (heap 300,
// pkey 100, toast 50, a hash index 10) and items (heap 200, pkey 20).
func bufSplitScreen() *screen {
	rel := func(tab uint32, table string, kind pg.PartKind, oid uint32, name, am string, buffered int64) pg.TableBufferStat {
		return pg.TableBufferStat{
			DB: "shop", Schema: "app", Name: table, OID: tab, BufferedBytes: buffered, TotalBytes: 2 * buffered,
			Rel: &pg.BufferRel{Kind: kind, OID: oid, Name: name, AccessMethod: am},
		}
	}
	s := &screen{
		level: levelBufferTables, tool: toolBuffers, db: "shop", schema: "app",
		sort: sortBySize, sortDesc: sortBySize.defaultDesc(), loaded: true,
		buf: bufState{rels: []pg.TableBufferStat{
			rel(1, "orders", pg.PartHeap, 1, "orders", "", 300),
			rel(1, "orders", pg.PartIndex, 11, "orders_pkey", "btree", 100),
			rel(1, "orders", pg.PartToast, 12, "pg_toast_1", "", 50),
			rel(1, "orders", pg.PartIndex, 13, "orders_doc", "hash", 10),
			rel(2, "items", pg.PartHeap, 2, "items", "", 200),
			rel(2, "items", pg.PartIndex, 21, "items_pkey", "btree", 20),
		}},
	}
	s.items = buildBufferItems(s)
	return s
}

func itemNames(s *screen) []string {
	vis := s.visibleIndexes()
	out := make([]string, 0, len(vis))
	for _, vi := range vis {
		out = append(out, s.items[vi].name)
	}
	return out
}

// Tab re-projects the loaded rows in place and keeps the cursor on its table.
func TestBufferSplitToggle(t *testing.T) {
	s := bufSplitScreen()
	m := newTestModel(s)
	m.applySort(s)
	if got := strings.Join(itemNames(s), ", "); got != "app.orders, app.items" {
		t.Fatalf("grouped rows = %s", got)
	}
	if st := s.items[0].data.(pg.TableBufferStat); st.BufferedBytes != 460 || st.Rel != nil {
		t.Errorf("grouped orders row = %+v, want 460 B rolled up", st)
	}

	s.cursor = 1 // items
	m.toggleBufSplit(s)
	want := "app.orders, app.items, app.orders › orders_pkey, app.orders › pg_toast_1, app.items › items_pkey, app.orders › orders_doc"
	if got := strings.Join(itemNames(s), ", "); got != want {
		t.Fatalf("split rows = %s\nwant         %s", got, want)
	}
	if !s.buf.byRel || s.cursor != 1 {
		t.Errorf("split: byRel %v cursor %d, want true / 1 (items' heap)", s.buf.byRel, s.cursor)
	}

	s.cursor = 3 // orders' toast
	m.toggleBufSplit(s)
	if s.buf.byRel || len(s.items) != 2 || s.cursor != 0 {
		t.Errorf("folded back: byRel %v rows %d cursor %d, want false / 2 / 0 (orders)", s.buf.byRel, len(s.items), s.cursor)
	}
}

// The Σ footer counts what the view lists, over the same summed bytes.
func TestBufferTotalsLabel(t *testing.T) {
	s := bufSplitScreen()
	grouped := renderBufferTotals(s.items, 10, 0)
	s.buf.byRel = true
	split := renderBufferTotals(buildBufferItems(s), 10, 12)
	if !strings.Contains(grouped, "Σ 2 tables") || !strings.Contains(split, "Σ 6 relations") {
		t.Errorf("totals labels: %q / %q", grouped, split)
	}
	if !strings.Contains(grouped, "680 B") || !strings.Contains(split, "680 B") {
		t.Errorf("both views should sum to 680 B buffered: %q / %q", grouped, split)
	}
}

// The split view's type, table and relation columns replace the name budget,
// sized to the loaded names, and the bar gives way to them.
func TestBufferSplitBarReserve(t *testing.T) {
	s := bufSplitScreen()
	grouped := barReserve(s)
	if tw, rw := bufSplitWidths(s); tw != 0 || rw != 0 {
		t.Errorf("per-table view widths = %d/%d, want none", tw, rw)
	}
	s.buf.byRel = true
	s.items = buildBufferItems(s)
	tw, rw := bufSplitWidths(s)
	if tw != len("app.orders") || rw != len("orders_pkey") {
		t.Errorf("split widths = %d/%d, want the longest table / relname", tw, rw)
	}
	if got, want := barReserve(s), grouped-colName+partTypeColW+colGutter+tw+colGutter+rw; got != want {
		t.Errorf("split reserve = %d, want %d", got, want)
	}
}

// A split row shows its table and its own relname in separate columns, the
// table clipped to the column, aligned under the header.
func TestBufferSplitRow(t *testing.T) {
	st := pg.TableBufferStat{Schema: "app", Name: "orders", BufferedBytes: 100, TotalBytes: 200,
		Rel: &pg.BufferRel{Kind: pg.PartToast, OID: 12, Name: "pg_toast_1"}}
	row := stripANSI(renderBufferRow(bufferStatToItem(st), st, 100, 0, 10, false, styleBar, 6))
	if !strings.HasSuffix(row, "toast    app.o…  ↵ pg_toast_1") {
		t.Errorf("split row = %q, want type, clipped table, then the relname", row)
	}
	head := stripANSI(renderBufferHeader(sortByName, false, 10, 6))
	if !strings.HasSuffix(head, "type     table↑    relation") {
		t.Errorf("split header = %q", head)
	}
	headPre, _, _ := strings.Cut(head, "relation")
	rowPre, _, _ := strings.Cut(row, "pg_toast_1")
	if displayWidth(headPre) != displayWidth(rowPre) {
		t.Errorf("relation header misaligned:\n%s\n%s", head, row)
	}
}

// d and p act on a split-view row's own relation: an index describes and
// opens as the index, toast opens its TOAST heap, and an AM pageinspect can't
// decode leaves a notice instead of a screen.
func TestBufferSplitTargets(t *testing.T) {
	s := bufSplitScreen()
	m := newTestModel(s)
	s.buf.byRel = true
	s.items = buildBufferItems(s)
	m.applySort(s)
	at := func(name string) {
		t.Helper()
		for i, n := range itemNames(s) {
			if n == name {
				s.cursor = i
				return
			}
		}
		t.Fatalf("no row %q", name)
	}

	at("app.orders › orders_pkey")
	if d, ok := bufDescribeTarget(s); !ok || !d.isIndex || d.indexOID != 11 || d.indexName != "app.orders_pkey" {
		t.Errorf("describe target = %+v, want index 11 app.orders_pkey", d)
	}
	m.jumpToPageInspector(s)
	top := m.stack[len(m.stack)-1]
	if top.level != levelIndexPages || top.pages.index.OID != 11 || top.pages.index.ParentOID != 1 {
		t.Errorf("p on the pkey → level %v index %+v, want its index pages", top.level, top.pages.index)
	}
	m.stack = m.stack[:len(m.stack)-1]

	at("app.orders › pg_toast_1")
	if d, ok := bufDescribeTarget(s); !ok || d.isIndex || d.table.OID != 1 {
		t.Errorf("toast describe target = %+v, want the orders table", d)
	}
	m.jumpToPageInspector(s)
	top = m.stack[len(m.stack)-1]
	if top.level != levelHeapPages || top.table.OID != 12 || top.table.Schema != "pg_toast" || top.table.Name != "pg_toast_1" {
		t.Errorf("p on toast → level %v table %+v, want pg_toast.pg_toast_1 heap pages", top.level, top.table)
	}
	m.stack = m.stack[:len(m.stack)-1]

	at("app.orders › orders_doc")
	depth := len(m.stack)
	if m.jumpToPageInspector(s); len(m.stack) != depth || !strings.Contains(m.notice, "hash") {
		t.Errorf("p on a hash index pushed a screen (depth %d→%d) / notice %q", depth, len(m.stack), m.notice)
	}
}
