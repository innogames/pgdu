package tui

import (
	"encoding/binary"
	"testing"

	"pgdu/internal/pg"
)

// heapTupleBytes assembles the change data heap_insert logs: xl_heap_header
// (infomask2, infomask, hoff) followed by the tuple from t_bits onward.
func heapTupleBytes(infomask2, infomask uint16, bitmap []byte, attrs []byte) []byte {
	hoff := heapTupleHeaderSize + len(bitmap)
	hoff = (hoff + 7) &^ 7 // MAXALIGN, as heap_form_tuple does
	pad := make([]byte, hoff-heapTupleHeaderSize-len(bitmap))
	out := make([]byte, 0, heapHeaderSize+hoff+len(attrs))
	out = binary.LittleEndian.AppendUint16(out, infomask2)
	out = binary.LittleEndian.AppendUint16(out, infomask)
	out = append(out, byte(hoff))
	out = append(out, bitmap...)
	out = append(out, pad...)
	return append(out, attrs...)
}

var testAttrs = []pg.WALHeapAttr{
	{Attnum: 1, Name: "id", TypLen: 4, TypAlign: "i", TypName: "int4"},
	{Attnum: 2, Name: "gone", Dropped: true, TypLen: 2, TypAlign: "s", TypName: "int2"},
	{Attnum: 3, Name: "name", TypLen: -1, TypAlign: "i", TypName: "text", TypCategory: "S"},
	{Attnum: 4, Name: "flag", TypLen: 1, TypAlign: "c", TypName: "bool"},
}

func TestDecodeWALHeapTupleInsert(t *testing.T) {
	// id=42, gone=7 (dropped, skipped in output), name='abc' (short varlena), flag=true
	attrs := []byte{42, 0, 0, 0, 7, 0, byte((3+1)<<1 | 1), 'a', 'b', 'c', 1}
	b := heapTupleBytes(4, 0x0802, nil, attrs)
	d := pg.WALBlockDetail{
		Ref:    pg.WALBlockRef{Rmgr: "Heap", RecordType: "INSERT", Description: "off: 3, flags: 0x08"},
		RelOID: 1, RelKind: "r", RelAM: "heap", Attrs: testAttrs, BlockData: b,
	}
	tuples, note := decodeWALHeapPayload(d)
	if note != "" || len(tuples) != 1 {
		t.Fatalf("tuples=%d note=%q", len(tuples), note)
	}
	got := tuples[0]
	want := [][2]string{{"id", "42"}, {"name", "abc"}, {"flag", "t"}}
	if !got.complete || len(got.cols) != len(want) {
		t.Fatalf("cols=%v complete=%v", got.cols, got.complete)
	}
	for i := range want {
		if got.cols[i] != want[i] {
			t.Errorf("col %d = %v, want %v", i, got.cols[i], want[i])
		}
	}
}

func TestDecodeWALHeapTupleNullsAndAbsent(t *testing.T) {
	// natts=3 (flag added later → absent), name NULL via bitmap: bits 1,2 set, 3 clear.
	bitmap := []byte{0b011}
	attrs := []byte{42, 0, 0, 0, 7, 0}
	b := heapTupleBytes(3, 0x0001, bitmap, attrs)
	got, ok := decodeWALHeapTuple(b, testAttrs)
	if !ok {
		t.Fatal("decode failed")
	}
	want := [][2]string{{"id", "42"}, {"name", "NULL"}, {"flag", "NULL"}}
	if len(got.cols) != len(want) {
		t.Fatalf("cols=%v", got.cols)
	}
	for i := range want {
		if got.cols[i] != want[i] {
			t.Errorf("col %d = %v, want %v", i, got.cols[i], want[i])
		}
	}
}

func TestDecodeWALUpdatePrefixSuffixIsRaw(t *testing.T) {
	d := pg.WALBlockDetail{
		Ref:    pg.WALBlockRef{Rmgr: "Heap", RecordType: "HOT_UPDATE", BlockID: 0, Description: "off: 5, xmax: 100, flags: 0x0C, new off: 6, xmax 0"},
		RelOID: 1, RelKind: "r", Attrs: testAttrs,
		BlockData: []byte{8, 0, 3, 0, 4, 0, 0, 0, 24},
	}
	tuples, note := decodeWALHeapPayload(d)
	if tuples != nil || note == "" {
		t.Fatalf("expected a prefix/suffix note, got tuples=%v note=%q", tuples, note)
	}
	if d.Ref.BlockID == 0 && walDescFlags(d.Ref.Description) != 0x0C {
		t.Errorf("flags parse = %#x", walDescFlags(d.Ref.Description))
	}
}

func TestDecodeWALMultiInsert(t *testing.T) {
	one := func(id byte) []byte {
		attrs := []byte{id, 0, 0, 0, 0, 0, byte((1+1)<<1 | 1), 'x', 1}
		tup := heapTupleBytes(4, 0x0802, nil, attrs)[heapHeaderSize:] // t_bits onward
		hdr := make([]byte, 0, 7)
		hdr = binary.LittleEndian.AppendUint16(hdr, uint16(len(tup)))
		hdr = binary.LittleEndian.AppendUint16(hdr, 4)
		hdr = binary.LittleEndian.AppendUint16(hdr, 0x0802)
		hdr = append(hdr, 24)
		return append(hdr, tup...)
	}
	var b []byte
	b = append(b, one(1)...)
	if len(b)%2 == 1 {
		b = append(b, 0) // SHORTALIGN between tuples
	}
	b = append(b, one(2)...)
	tuples, note := decodeWALMultiInsert(b, testAttrs)
	if note != "" || len(tuples) != 2 {
		t.Fatalf("tuples=%d note=%q", len(tuples), note)
	}
	if tuples[1].cols[0] != [2]string{"id", "2"} || tuples[1].cols[1] != [2]string{"name", "x"} {
		t.Errorf("second tuple = %v", tuples[1].cols)
	}
}

func TestWALDescOffset(t *testing.T) {
	cases := map[string]int{
		"off: 15, flags: 0x08": 15,
		"off 15 flags 0x00":    15,
		"off: 5, xmax: 100, flags: 0x00, new off: 6, xmax 0": 5,
	}
	for desc, want := range cases {
		got, ok := walDescOffset(desc)
		if !ok || got != want {
			t.Errorf("%q → %d,%v want %d", desc, got, ok, want)
		}
	}
	if _, ok := walDescOffset("nblocks: 3"); ok {
		t.Error("no offset should not match")
	}
}

func TestHexDumpItems(t *testing.T) {
	items := hexDumpItems([]byte("hello, world! 0123456789"))
	if len(items) != 2 || items[0].name != "0000" || items[1].name != "0010" {
		t.Fatalf("rows=%v", items)
	}
	if got := stripANSI(items[0].detail); !contains(got, "68 65 6c 6c") || !contains(got, "|hello, world! 01|") {
		t.Errorf("row 0 = %q", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDecodeWALBtreePayload(t *testing.T) {
	cols := []pg.IndexKeyColumn{
		{Ordinal: 1, Def: "player_id", IsKey: true, TypLen: 4, TypAlign: "i", TypName: "int4"},
		{Ordinal: 2, Def: "unit", IsKey: true, TypLen: -1, TypAlign: "i", TypName: "text", TypCategory: "S"},
	}
	// t_tid (3177,94), t_info: size 16 | INDEX_VAR_MASK; key: 7, 'ab'
	b := []byte{0, 0, 0x69, 0x0c, 94, 0}
	b = binary.LittleEndian.AppendUint16(b, 16|indexVarMask)
	b = append(b, 7, 0, 0, 0, byte((2+1)<<1|1), 'a', 'b', 0)
	d := pg.WALBlockDetail{
		Ref:    pg.WALBlockRef{Rmgr: "Btree", RecordType: "INSERT_LEAF", Description: "off: 94"},
		RelOID: 1, RelKind: "i", RelAM: "btree", IndexCols: cols, BlockData: b,
	}
	tuples, note := decodeWALBtreePayload(d)
	if note != "" || len(tuples) != 1 {
		t.Fatalf("tuples=%d note=%q", len(tuples), note)
	}
	got := tuples[0]
	if got.tid != "(3177,94)" || !got.index {
		t.Errorf("tid=%q index=%v", got.tid, got.index)
	}
	if len(got.cols) != 2 || got.cols[0] != [2]string{"player_id", "7"} || got.cols[1] != [2]string{"unit", "ab"} {
		t.Errorf("cols=%v", got.cols)
	}
	if s := btreeFlagsText(0x0001 | 0x0040); s != "LEAF HAS_GARBAGE" {
		t.Errorf("flags=%q", s)
	}
}

func TestDecodeWALBtreePayloadRejectsBogusSize(t *testing.T) {
	cols := []pg.IndexKeyColumn{{Ordinal: 1, Def: "id", TypLen: 4, TypAlign: "i", TypName: "int4"}}
	// t_info says 5 bytes: shorter than the 8-byte header. Must not panic.
	b := []byte{0, 0, 0, 0, 1, 0, 5, 0, 1, 2, 3, 4}
	d := pg.WALBlockDetail{
		Ref:    pg.WALBlockRef{Rmgr: "Btree", RecordType: "INSERT_LEAF"},
		RelOID: 1, RelKind: "i", RelAM: "btree", IndexCols: cols, BlockData: b,
	}
	tuples, note := decodeWALBtreePayload(d)
	if tuples != nil || note == "" {
		t.Fatalf("tuples=%v note=%q", tuples, note)
	}
}
