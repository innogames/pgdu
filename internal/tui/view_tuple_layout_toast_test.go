package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
)

// toastChunkScreen builds a levelHeapTuples screen of a TOAST relation holding
// one chunk row (chunk_id 42, seq 0) with its attr split loaded, the way the
// overlay sees it once sqlTupleAttrs has answered.
func toastChunkScreen(payload []byte) (*screen, pg.HeapTuple) {
	chunkID, seq := uint32(42), int32(0)
	ctid := "(1,1)"
	hoff := int32(24)
	data := append(append([]byte{42, 0, 0, 0}, 0, 0, 0, 0), varlena4B(payload)...)
	tup := pg.HeapTuple{
		LP: 1, LPFlags: pg.LPNormal, LPLen: hoff + int32(len(data)), Ctid: &ctid,
		Infomask2: 3, Infomask: pg.HeapHasVarWidth, Hoff: &hoff, Data: data,
		ChunkID: &chunkID, ChunkSeq: &seq}
	s := &screen{level: levelHeapTuples, db: "db", schema: "pg_toast",
		table: pg.Table{DB: "db", OID: 7, Schema: "pg_toast", Name: "pg_toast_5"},
		items: []item{heapTupleToItem(tup)}}
	s.pages.tupleAttrsLP = 1
	s.pages.tupleAttrs = []pg.TupleAttr{
		{Attnum: 1, Name: "chunk_id", TypeName: "oid", Len: 4, Align: "i", Stored: true, TypName: "oid", TypCategory: "N", Value: data[0:4]},
		{Attnum: 2, Name: "chunk_seq", TypeName: "integer", Len: 4, Align: "i", Stored: true, TypName: "int4", TypCategory: "N", Value: data[4:8]},
		{Attnum: 3, Name: "chunk_data", TypeName: "bytea", Len: -1, Align: "i", Stored: true, TypName: "bytea", TypCategory: "U", Value: data[8:]},
	}
	return s, tup
}

// parkOn moves the legend cursor to the column segment with the given name.
func parkOn(t *testing.T, m *Model, s *screen, name string) {
	t.Helper()
	for rank := range 32 {
		m.tupleLayoutCursor = rank
		if sg, ok := m.tupleLayoutSegUnderCursor(s); ok && sg.Kind == pageinspect.SegColumn && sg.Attr.Name == name {
			return
		}
	}
	t.Fatalf("no %s column in the layout", name)
}

// Enter on a TOAST chunk row opens the same byte-layout overlay a heap tuple
// gets — no screen is pushed — and the footer label says so.
func TestDrillToastChunkOpensTupleLayout(t *testing.T) {
	s, tup := toastChunkScreen([]byte("slice"))
	m := &Model{keys: defaultKeys(), stack: []*screen{s}}

	if label, ok := enterLabel(s); !ok || label != "byte layout" {
		t.Errorf("enterLabel = %q/%v, want byte layout", label, ok)
	}
	if cmd := m.drillHeapTuple(s, s.items[0]); cmd == nil {
		t.Fatal("drill returned no attr-split load")
	}
	if len(m.stack) != 1 || !m.showTupleLayout || s.pages.tupleAttrsLP != tup.LP {
		t.Fatalf("stack %d overlay %v attrsLP %d; want the overlay on the same screen", len(m.stack), m.showTupleLayout, s.pages.tupleAttrsLP)
	}
}

// The overlay names the chunk in its title, advertises Enter on chunk_data as
// the way to the whole value, and the pane loads that value once: it spins
// while loading, drops a stale answer, renders the decoded value, and a second
// visit after esc does not refetch.
func TestTupleLayoutToastValuePane(t *testing.T) {
	s, _ := toastChunkScreen([]byte("slice"))
	m := &Model{width: 120, height: 40, keys: defaultKeys(), stack: []*screen{s}}
	m.showTupleLayout = true

	title := stripANSI(m.renderTupleLayout(s, 30))
	if !strings.Contains(title, "chunk 42 · seq 0") {
		t.Errorf("title must name the chunk:\n%s", title)
	}
	if !strings.Contains(title, "stores 3 of 3 attrs") || !strings.Contains(title, "chunk_data") || !strings.Contains(title, "t_xmin") {
		t.Errorf("toast chunk must get the full header + column legend:\n%s", title)
	}

	parkOn(t, m, s, "chunk_data")
	if _, ok := m.tupleLayoutToastValueUnderCursor(s); !ok {
		t.Fatal("chunk_data must offer the toast value")
	}
	if got := stripANSI(m.renderTupleLayout(s, 30)); !strings.Contains(got, "↵ → toast value") {
		t.Error("title must advertise Enter on chunk_data as the toast value")
	}

	cmd := m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter})
	tv := s.pages.toastVal
	if cmd == nil || !m.showTupleValue || tv == nil || tv.chunkID != 42 || !tv.loading {
		t.Fatalf("enter on chunk_data: cmd=%v pane=%v state=%+v; want the value loading", cmd != nil, m.showTupleValue, tv)
	}
	if body := stripANSI(m.renderTupleValue(s)); !strings.Contains(body, "reassembling") || !strings.Contains(body, " stored bytes ") {
		t.Errorf("loading pane must spin above the chunk's own bytes:\n%s", body)
	}

	// Another chunk's (or table's) answer is not ours.
	m.onToastValueLoaded(toastValueLoadedMsg{tableOID: 7, chunkID: 43, val: pg.ToastValue{Chunks: 1}})
	m.onToastValueLoaded(toastValueLoadedMsg{tableOID: 8, chunkID: 42, val: pg.ToastValue{Chunks: 1}})
	if !tv.loading {
		t.Fatal("a stale toast value landed")
	}

	val := pg.ToastValue{ChunkID: 42, Chunks: 2, StoredBytes: 11, Data: []byte("hello world")}
	m.onToastValueLoaded(toastValueLoadedMsg{tableOID: 7, chunkID: 42, val: val})
	body := stripANSI(m.renderTupleValue(s))
	toast, dump, split := strings.Cut(body, " stored bytes ")
	if !split {
		t.Fatalf("pane lost the chunk's own bytes:\n%s", body)
	}
	if !strings.Contains(toast, "chunk_id 42") || !strings.Contains(toast, "2 chunks") || !strings.Contains(toast, "hello world") {
		t.Errorf("toast section must give the value's facts and its decoding:\n%s", toast)
	}
	if !strings.Contains(dump, "slice|") {
		t.Errorf("the chunk's stored bytes must still be dumped:\n%s", dump)
	}

	// esc back to the legend, enter again: the value is already here.
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTupleValue || s.pages.toastVal == nil {
		t.Fatalf("esc: pane=%v state kept=%v", m.showTupleValue, s.pages.toastVal != nil)
	}
	if cmd := m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || !m.showTupleValue {
		t.Error("re-entering the pane refetched a value already loaded")
	}

	// Closing the overlay forgets the value: a reopened overlay starts clean.
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEsc})
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEsc})
	if m.showTupleLayout || s.pages.toastVal != nil {
		t.Errorf("closing the overlay: open=%v toastVal kept=%v", m.showTupleLayout, s.pages.toastVal != nil)
	}
}

// A failed value load is shown in the pane; the chunk's own bytes stay.
func TestTupleLayoutToastValueError(t *testing.T) {
	s, _ := toastChunkScreen([]byte("slice"))
	m := &Model{width: 120, height: 40, keys: defaultKeys(), stack: []*screen{s}}
	m.showTupleLayout = true
	parkOn(t, m, s, "chunk_data")
	m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter})
	m.onToastValueLoaded(toastValueLoadedMsg{tableOID: 7, chunkID: 42, err: errors.New("permission denied")})
	body := stripANSI(m.renderTupleValue(s))
	if !strings.Contains(body, "permission denied") || !strings.Contains(body, "slice|") {
		t.Errorf("error pane:\n%s", body)
	}
}

// Other columns of a chunk row (chunk_id, chunk_seq) and a user table's column
// that merely happens to be named chunk_data keep the plain value pane.
func TestTupleLayoutToastValueOnlyForChunkData(t *testing.T) {
	s, _ := toastChunkScreen([]byte("slice"))
	m := &Model{width: 120, height: 40, keys: defaultKeys(), stack: []*screen{s}}
	m.showTupleLayout = true
	parkOn(t, m, s, "chunk_id")
	if _, ok := m.tupleLayoutToastValueUnderCursor(s); ok {
		t.Error("chunk_id is not a slice of a value")
	}
	if cmd := m.handleTupleLayoutKey(s, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || s.pages.toastVal != nil || !m.showTupleValue {
		t.Error("enter on chunk_id must open the plain pane without a load")
	}

	s.table.Schema = "public"
	m.showTupleValue = false
	parkOn(t, m, s, "chunk_data")
	if _, ok := m.tupleLayoutToastValueUnderCursor(s); ok {
		t.Error("a user table's chunk_data column is not a TOAST slice")
	}
}

// Enter on a TOAST pointer lands in the chunk's byte layout: the resolved
// target fills the toast pages placeholder, pushes the chunk's page with the
// line pointer armed, and the tuple load opens the overlay on that row. A value
// whose chunks are gone stops at the page list with a notice.
func TestToastPointerJumpLandsOnChunkLayout(t *testing.T) {
	heap := &screen{level: levelHeapTuples, db: "db", schema: "public", table: pg.Table{OID: 1, Schema: "public", Name: "battle"}}
	m := &Model{width: 120, height: 40, keys: defaultKeys(), stack: []*screen{heap}}
	m.showTupleLayout = true

	if cmd := m.openToastChunkNav(heap, 77, 1513786039); cmd == nil || m.showTupleLayout {
		t.Fatalf("jump: cmd=%v overlay=%v; want the resolve issued and the overlay closed", cmd != nil, m.showTupleLayout)
	}
	toast := pg.Table{DB: "db", OID: 77, Schema: "pg_toast", Name: "pg_toast_1"}
	cmd := m.onToastTargetResolved(toastTargetResolvedMsg{table: toast, block: 3, lp: 2, chunkID: 1513786039})
	if cmd == nil || len(m.stack) != 3 {
		t.Fatalf("resolved: cmd=%v stack=%d; want pages + chunk page pushed", cmd != nil, len(m.stack))
	}
	pages, tuples := m.stack[1], m.stack[2]
	if pages.level != levelHeapPages || pages.table.Name != "pg_toast_1" {
		t.Errorf("placeholder not filled in: %+v", pages.table)
	}
	if tuples.level != levelHeapTuples || tuples.table.OID != 77 || tuples.pages.heapPageBlkno != 3 || tuples.pages.focusLP != 2 {
		t.Fatalf("chunk page = level %v table %d blk %d focusLP %d; want toast tuples of block 3, lp 2",
			tuples.level, tuples.table.OID, tuples.pages.heapPageBlkno, tuples.pages.focusLP)
	}

	chunkID, seq := uint32(1513786039), int32(0)
	ctid := "(3,2)"
	rows := []pg.HeapTuple{
		{LP: 1, LPFlags: pg.LPNormal, Ctid: &ctid, Data: []byte{1}},
		{LP: 2, LPFlags: pg.LPNormal, Ctid: &ctid, Data: []byte{1}, ChunkID: &chunkID, ChunkSeq: &seq},
	}
	m.onHeapTuplesLoaded(heapTuplesLoadedMsg{tableOID: 77, blkno: 3, page: pg.HeapPageTuples{Tuples: rows}})
	if tuples.cursor != 1 || !m.showTupleLayout || tuples.pages.tupleAttrsLP != 2 {
		t.Errorf("landing: cursor %d overlay %v attrsLP %d; want the chunk row's layout open", tuples.cursor, m.showTupleLayout, tuples.pages.tupleAttrsLP)
	}

	// Chunks gone: only the page list, and the user is told why.
	m.closeTupleLayout(tuples)
	m.stack = m.stack[:1]
	m.openToastChunkNav(heap, 77, 5)
	m.onToastTargetResolved(toastTargetResolvedMsg{table: toast, block: 0, lp: 0, chunkID: 5})
	if len(m.stack) != 2 || !strings.Contains(m.notice, "no chunks") {
		t.Errorf("gone value: stack %d notice %q", len(m.stack), m.notice)
	}
}
