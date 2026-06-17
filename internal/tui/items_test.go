package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// The item extractors below feed the sort comparators (update_sort.go): the
// bool return decides whether a row can be ranked on that key at all, so the
// "not this data type" → (0, false) path matters as much as the value path.

func TestItemBlkno(t *testing.T) {
	cases := []struct {
		name   string
		it     item
		wantN  int64
		wantOK bool
	}{
		{"heap page", item{data: pg.HeapPageStat{Blkno: 5}}, 5, true},
		{"index page", item{data: pg.IndexPageStat{Blkno: 7}}, 7, true},
		{"wrong type", item{data: pg.WALRmgrStat{}}, 0, false},
		{"nil data", item{}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := itemBlkno(c.it)
			if n != c.wantN || ok != c.wantOK {
				t.Errorf("itemBlkno = (%d,%v), want (%d,%v)", n, ok, c.wantN, c.wantOK)
			}
		})
	}
}

func TestItemDeadRatio(t *testing.T) {
	cases := []struct {
		name   string
		it     item
		want   float64
		wantOK bool
	}{
		{"heap quarter dead", item{data: pg.HeapPageStat{LiveLP: 3, DeadLP: 1}}, 0.25, true},
		{"heap empty page undefined", item{data: pg.HeapPageStat{}}, 0, false},
		{"index half dead", item{data: pg.IndexPageStat{LiveItems: 1, DeadItems: 1}}, 0.5, true},
		{"index empty page undefined", item{data: pg.IndexPageStat{}}, 0, false},
		{"wrong type", item{data: pg.Table{}}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := itemDeadRatio(c.it)
			if r != c.want || ok != c.wantOK {
				t.Errorf("itemDeadRatio = (%v,%v), want (%v,%v)", r, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestItemFreeSpace(t *testing.T) {
	cases := []struct {
		name   string
		it     item
		wantN  int64
		wantOK bool
	}{
		{"heap free bytes", item{data: pg.HeapPageStat{FreeBytes: 100}}, 100, true},
		{"index free size", item{data: pg.IndexPageStat{FreeSize: 200}}, 200, true},
		{"wrong type", item{data: pg.WALRecord{}}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := itemFreeSpace(c.it)
			if n != c.wantN || ok != c.wantOK {
				t.Errorf("itemFreeSpace = (%d,%v), want (%d,%v)", n, ok, c.wantN, c.wantOK)
			}
		})
	}
}

func TestItemLP(t *testing.T) {
	cases := []struct {
		name   string
		it     item
		wantN  int64
		wantOK bool
	}{
		{"heap tuple line pointer", item{data: pg.HeapTuple{LP: 9}}, 9, true},
		{"index tuple item offset", item{data: pg.IndexTuple{ItemOffset: 4}}, 4, true},
		{"wrong type", item{data: pg.HeapPageStat{}}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, ok := itemLP(c.it)
			if n != c.wantN || ok != c.wantOK {
				t.Errorf("itemLP = (%d,%v), want (%d,%v)", n, ok, c.wantN, c.wantOK)
			}
		})
	}
}

func TestItemTreeLevel(t *testing.T) {
	if n, ok := itemTreeLevel(item{data: pg.IndexPageStat{BtpoLevel: 2}}); n != 2 || !ok {
		t.Errorf("itemTreeLevel(index) = (%d,%v), want (2,true)", n, ok)
	}
	if _, ok := itemTreeLevel(item{data: pg.HeapPageStat{}}); ok {
		t.Error("itemTreeLevel(heap page) should be undefined")
	}
}

func TestItemWALCountAndFPI(t *testing.T) {
	if n, ok := itemWALCount(item{data: pg.WALRmgrStat{Count: 42}}); n != 42 || !ok {
		t.Errorf("itemWALCount = (%d,%v), want (42,true)", n, ok)
	}
	if _, ok := itemWALCount(item{data: pg.WALRecord{}}); ok {
		t.Error("itemWALCount(record) should be undefined")
	}

	if n, ok := itemWALFPI(item{data: pg.WALRmgrStat{FPISize: 128}}); n != 128 || !ok {
		t.Errorf("itemWALFPI(rmgr) = (%d,%v), want (128,true)", n, ok)
	}
	if n, ok := itemWALFPI(item{data: pg.WALRecord{FPILength: 64}}); n != 64 || !ok {
		t.Errorf("itemWALFPI(record) = (%d,%v), want (64,true)", n, ok)
	}
	if _, ok := itemWALFPI(item{data: pg.HeapPageStat{}}); ok {
		t.Error("itemWALFPI(wrong type) should be undefined")
	}
}

func TestSchemaDetail(t *testing.T) {
	if got := schemaDetail(pg.Schema{TableCount: 7}); got != "7 tables" {
		t.Errorf("schemaDetail = %q, want %q", got, "7 tables")
	}
}

func TestHeapPageToItem(t *testing.T) {
	// used = BLCKSZ(8192) - FreeBytes, clamped at 0.
	it := heapPageToItem(pg.HeapPageStat{Blkno: 5, FreeBytes: 192})
	if it.size != 8000 {
		t.Errorf("size = %d, want 8000 (8192-192)", it.size)
	}
	if it.name != "page #0000005" {
		t.Errorf("name = %q, want %q", it.name, "page #0000005")
	}
}

func TestHeapTupleToItem(t *testing.T) {
	ctid := "(0,1)"
	// NORMAL line pointer with a ctid → drillable.
	normal := heapTupleToItem(pg.HeapTuple{LP: 5, LPLen: 20, LPFlags: pg.LPNormal, Ctid: &ctid})
	if normal.name != "#0005" || normal.size != 20 {
		t.Errorf("normal tuple = name %q size %d, want #0005 / 20", normal.name, normal.size)
	}
	if !normal.hasChildren {
		t.Error("NORMAL tuple with ctid should be drillable")
	}
	// DEAD line pointer → not drillable even with a ctid.
	dead := heapTupleToItem(pg.HeapTuple{LP: 6, LPFlags: pg.LPDead, Ctid: &ctid})
	if dead.hasChildren {
		t.Error("DEAD tuple should not be drillable")
	}
	// NORMAL but no ctid (NULL from pageinspect) → not drillable.
	noCtid := heapTupleToItem(pg.HeapTuple{LP: 7, LPFlags: pg.LPNormal})
	if noCtid.hasChildren {
		t.Error("NORMAL tuple without ctid should not be drillable")
	}
}

func TestWALRecordToItem(t *testing.T) {
	it := walRecordToItem(pg.WALRecord{RecordType: "INSERT", RecordLength: 10, FPILength: 5, Description: "heap insert"})
	if it.name != "INSERT" || it.size != 15 || it.detail != "heap insert" || !it.hasChildren {
		t.Errorf("walRecordToItem = %+v, want name INSERT size 15 detail 'heap insert' hasChildren true", it)
	}
}

func TestWALBlockToItem(t *testing.T) {
	cases := []struct {
		name     string
		ref      pg.WALBlockRef
		wantName string
		wantSize int64
	}{
		{
			name:     "resolved relation name, main fork",
			ref:      pg.WALBlockRef{RelName: "mytable", ForkNumber: 0, BlockNumber: 12, FPILength: 99},
			wantName: "rel mytable/main blk 12",
			wantSize: 99,
		},
		{
			name:     "unresolved falls back to relfilenode, fsm fork",
			ref:      pg.WALBlockRef{RelFileNode: 16384, ForkNumber: 1, BlockNumber: 3},
			wantName: "rel 16384/fsm blk 3",
			wantSize: 0,
		},
		{
			name:     "toast relation tagged",
			ref:      pg.WALBlockRef{RelName: "docs", IsToast: true, ForkNumber: 0, BlockNumber: 1},
			wantName: "rel docs (toast)/main blk 1",
			wantSize: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := walBlockToItem(c.ref)
			if it.name != c.wantName || it.size != c.wantSize {
				t.Errorf("walBlockToItem = name %q size %d, want %q / %d", it.name, it.size, c.wantName, c.wantSize)
			}
		})
	}
}

func TestBufferStatToItem(t *testing.T) {
	it := bufferStatToItem(pg.TableBufferStat{Schema: "public", Name: "orders", BufferedBytes: 4096})
	if it.name != "public.orders" || it.size != 4096 {
		t.Errorf("bufferStatToItem = name %q size %d, want public.orders / 4096", it.name, it.size)
	}
}

func TestTableToItem(t *testing.T) {
	// Default tools size the row by total-relation-size and carry the
	// heap/idx/toast breakdown for the segmented bar.
	tbl := pg.Table{
		Name: "events", HeapBytes: 100, IndexesBytes: 50, ToastBytes: 0,
		TotalBytes: 150, EstRows: 9,
	}
	it := tableToItem(tbl, toolDisk)
	if it.size != 150 || !it.hasChildren || it.heap != 100 || it.idx != 50 {
		t.Errorf("tableToItem default = %+v, want size 150 heap 100 idx 50 hasChildren", it)
	}
	if !it.hasRows || it.rows != 9 {
		t.Errorf("tableToItem rows = (%d, hasRows %v), want (9, true)", it.rows, it.hasRows)
	}
	// heap/idx/toast render as bar segments + columns, not a detail string —
	// the breakdown is carried on the item fields and detail stays empty.
	toasted := tableToItem(pg.Table{Name: "t", ToastBytes: 2 << 20}, toolDisk)
	if toasted.toast != 2<<20 {
		t.Errorf("toast bytes = %d, want %d", toasted.toast, 2<<20)
	}
	if toasted.detail != "" {
		t.Errorf("disk-tool table detail should be empty (breakdown is columnar), got %q", toasted.detail)
	}

	// Page-inspector tool: sized by heap only, page count rounds up.
	if it := tableToItem(pg.Table{Name: "p", HeapBytes: 8192}, toolPageInspect); !it.hasPages || it.pages != 1 || it.size != 8192 {
		t.Errorf("page-inspect exact block = pages %d size %d hasPages %v, want 1 / 8192 / true", it.pages, it.size, it.hasPages)
	}
	if it := tableToItem(pg.Table{Name: "p", HeapBytes: 8193}, toolPageInspect); it.pages != 2 {
		t.Errorf("page-inspect partial block = pages %d, want 2 (rounded up)", it.pages)
	}
}

func TestColumnToItem(t *testing.T) {
	it := columnToItem(pg.Column{Name: "body", Type: "text", AvgWidth: 16, EstBytes: 1234})
	if it.name != "body" || it.size != 1234 {
		t.Errorf("columnToItem = name %q size %d, want body / 1234", it.name, it.size)
	}
	if !strings.Contains(it.detail, "text") {
		t.Errorf("detail %q should mention the column type", it.detail)
	}
	// A small, non-toastable column carries no bread marker.
	if strings.Contains(it.detail, "🍞") {
		t.Errorf("non-toasted column should not be flagged, got %q", it.detail)
	}
	// Toastable AND wide (≥2KB avg) → flagged.
	toasted := columnToItem(pg.Column{Name: "blob", Type: "bytea", AvgWidth: 4096, Toastable: true})
	if !strings.Contains(toasted.detail, "🍞") {
		t.Errorf("wide toastable column should be flagged, got %q", toasted.detail)
	}
}
