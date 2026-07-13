package tui

import (
	"testing"

	"pgdu/internal/pg"
)

func i32(v int32) *int32 { return &v }

// fixedAttr builds a fixed-width TupleAttr whose Value is `size` arbitrary
// non-zero bytes.
func fixedAttr(name string, size int32, align string) pg.TupleAttr {
	v := make([]byte, size)
	for i := range v {
		v[i] = 0xAA
	}
	return pg.TupleAttr{Name: name, TypeName: "fixed", Len: size, Align: align, Stored: true, Value: v}
}

func varAttr(name string, value []byte) pg.TupleAttr {
	return pg.TupleAttr{
		Name: name, TypeName: "text", Len: -1, Align: "i",
		TypName: "text", TypCategory: "S", Stored: true, Value: value,
	}
}

// buildTuple assembles a HeapTuple whose Data is the concatenation of the
// given runs (pads must be passed explicitly as zero bytes) and whose LPLen
// is hoff + len(data) + residue.
func buildTuple(hoff int, infomask, infomask2 int32, data []byte, residue int) pg.HeapTuple {
	return pg.HeapTuple{
		LPFlags:   pg.LPNormal,
		LPLen:     int32(hoff + len(data) + residue),
		Infomask:  infomask,
		Infomask2: infomask2,
		Hoff:      i32(int32(hoff)),
		Data:      data,
	}
}

// sumBytes checks the Σ invariant the overlay's footer line relies on.
func sumBytes(segs []tupleSeg) int {
	n := 0
	for _, s := range segs {
		n += s.bytes
	}
	return n
}

// headerFieldBytes are the seven fixed-header field sizes computeTupleLayout
// always emits first (t_xmin … t_hoff), totalling 23 B.
var headerFieldBytes = []int{4, 4, 4, 6, 2, 2, 1}

// assertHeaderFields checks the leading header-field segments and returns the
// remaining (bitmap/pad/column) tail for the test to inspect.
func assertHeaderFields(t *testing.T, segs []tupleSeg) []tupleSeg {
	t.Helper()
	if len(segs) < len(headerFieldBytes) {
		t.Fatalf("got %d segments, want at least %d header fields: %+v", len(segs), len(headerFieldBytes), segs)
	}
	start := 0
	for i, w := range headerFieldBytes {
		s := segs[i]
		if s.kind != segHeaderField || s.start != start || s.bytes != w {
			t.Fatalf("header field %d = {kind:%d start:%d bytes:%d}, want start %d bytes %d", i, s.kind, s.start, s.bytes, start, w)
		}
		start += w
	}
	return segs[len(headerFieldBytes):]
}

func TestComputeTupleLayoutFixedColumns(t *testing.T) {
	// int2 then int8: 2 B value, 6 B pad to align 8, 8 B value.
	data := append(append([]byte{0xAA, 0xAA}, make([]byte, 6)...),
		0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB, 0xBB)
	tup := buildTuple(24, 0, 2, data, 0)
	attrs := []pg.TupleAttr{fixedAttr("a", 2, "s"), fixedAttr("b", 8, "d")}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	rest := assertHeaderFields(t, segs)
	want := []struct {
		kind  tupleSegKind
		start int
		bytes int
		class string
	}{
		{segHeaderPad, 23, 1, "align to t_hoff"},
		{segColumn, 24, 2, "fixed 2 B"},
		{segPad, 26, 6, "align 8"},
		{segColumn, 32, 8, "fixed 8 B"},
	}
	if len(rest) != len(want) {
		t.Fatalf("got %d body segments, want %d: %+v", len(rest), len(want), rest)
	}
	for i, w := range want {
		s := rest[i]
		if s.kind != w.kind || s.start != w.start || s.bytes != w.bytes || s.class != w.class {
			t.Errorf("seg %d = {kind:%d start:%d bytes:%d class:%q}, want %+v", i, s.kind, s.start, s.bytes, s.class, w)
		}
	}
	if got := sumBytes(segs); got != int(tup.LPLen) {
		t.Errorf("Σ = %d, want lp_len %d", got, tup.LPLen)
	}
}

func TestComputeTupleLayoutShortVarlenaUnaligned(t *testing.T) {
	// int2 followed by a 1-byte-header varlena: the non-zero header byte at
	// the cursor means no alignment padding (att_align_pointer rule).
	short := []byte{0x09, 'a', 'b', 'c'} // 1B header, total len 4
	data := append([]byte{0xAA, 0xAA}, short...)
	tup := buildTuple(24, 0, 2, data, 0)
	attrs := []pg.TupleAttr{fixedAttr("a", 2, "s"), varAttr("v", short)}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	rest := assertHeaderFields(t, segs)
	// headerPad, a, v — crucially no segPad before v.
	if len(rest) != 3 {
		t.Fatalf("got %d body segments, want 3: %+v", len(rest), rest)
	}
	v := rest[2]
	if v.kind != segColumn || v.start != 26 || v.bytes != 4 || v.class != "varlena 1B-hdr" {
		t.Errorf("varlena seg = %+v", v)
	}
	if v.value != "abc" {
		t.Errorf("decoded value = %q, want \"abc\"", v.value)
	}
}

func TestComputeTupleLayoutLongVarlenaAligned(t *testing.T) {
	// int2 followed by a 4-byte-header varlena: two zero pad bytes, then the
	// header (first byte = length LSB with low bits 00).
	long := []byte{0x28, 0x00, 0x00, 0x00, 'x', 'x', 'x', 'x', 'x', 'x'} // len bits say 40, only sample bytes here
	data := append(append([]byte{0xAA, 0xAA}, 0x00, 0x00), long...)
	tup := buildTuple(24, 0, 2, data, 0)
	attrs := []pg.TupleAttr{fixedAttr("a", 2, "s"), varAttr("v", long)}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	rest := assertHeaderFields(t, segs)
	if len(rest) != 4 {
		t.Fatalf("got %d body segments, want 4: %+v", len(rest), rest)
	}
	if p := rest[2]; p.kind != segPad || p.start != 26 || p.bytes != 2 || p.class != "align 4" {
		t.Errorf("pad seg = %+v", p)
	}
	if v := rest[3]; v.kind != segColumn || v.start != 28 || v.bytes != len(long) || v.class != "varlena 4B-hdr" {
		t.Errorf("varlena seg = %+v", v)
	}
}

func TestComputeTupleLayoutNullBitmapAndNulls(t *testing.T) {
	// Three attrs, middle one NULL: bitmap byte + pad to hoff 24, then two
	// int4 values back to back.
	data := []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xBB, 0xBB, 0xBB, 0xBB}
	tup := buildTuple(24, pg.HeapHasNull, 3, data, 0)
	null := pg.TupleAttr{Name: "n", TypeName: "int4", Len: 4, Align: "i", Stored: true}
	attrs := []pg.TupleAttr{fixedAttr("a", 4, "i"), null, fixedAttr("b", 4, "i")}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	rest := assertHeaderFields(t, segs)
	// bitmap, no headerPad (23+1 == 24), a, n (0 B), b
	if len(rest) != 4 {
		t.Fatalf("got %d body segments, want 4: %+v", len(rest), rest)
	}
	if bm := rest[0]; bm.kind != segNullBitmap || bm.start != 23 || bm.bytes != 1 || bm.class != "3 attrs, 1 null" {
		t.Errorf("bitmap seg = %+v", bm)
	}
	if n := rest[2]; n.kind != segColumn || n.bytes != 0 || n.class != "NULL" {
		t.Errorf("null seg = %+v", n)
	}
	if b := rest[3]; b.start != 28 || b.bytes != 4 {
		t.Errorf("post-null seg = %+v", b)
	}
}

func TestComputeTupleLayoutNotStoredTrailingAttr(t *testing.T) {
	// Tuple written with 1 attr; a column added later shows as not stored.
	data := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	tup := buildTuple(24, 0, 1, data, 0)
	added := pg.TupleAttr{Name: "later", TypeName: "int8", Len: 8, Align: "d", Stored: false}
	attrs := []pg.TupleAttr{fixedAttr("a", 4, "i"), added}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	last := segs[len(segs)-1]
	if last.kind != segColumn || last.bytes != 0 || last.class != "not stored (added later)" {
		t.Errorf("not-stored seg = %+v", last)
	}
}

func TestComputeTupleLayoutToastPointer(t *testing.T) {
	ptr := make([]byte, 18)
	ptr[0] = 0x01
	ptr[1] = 0x12 // va_tag VARTAG_ONDISK
	data := append([]byte{0xAA, 0xAA, 0xAA, 0xAA}, ptr...)
	tup := buildTuple(24, pg.HeapHasExternal, 2, data, 0)
	attrs := []pg.TupleAttr{fixedAttr("a", 4, "i"), varAttr("big", ptr)}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	last := segs[len(segs)-1]
	if last.class != "TOAST pointer" || last.bytes != 18 {
		t.Errorf("toast seg = %+v", last)
	}
}

func TestComputeTupleLayoutResidueAndOverrun(t *testing.T) {
	data := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	attrs := []pg.TupleAttr{fixedAttr("a", 4, "i")}

	// 5 B the walk can't explain → explicit unaccounted tail, still ok.
	tup := buildTuple(24, 0, 1, data, 5)
	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("residue should stay trustworthy")
	}
	last := segs[len(segs)-1]
	if last.kind != segUnaccounted || last.start != 28 || last.bytes != 5 {
		t.Errorf("residue seg = %+v", last)
	}
	if got := sumBytes(segs); got != int(tup.LPLen) {
		t.Errorf("Σ = %d, want lp_len %d", got, tup.LPLen)
	}

	// Walk overruns lp_len → fallback: per-column picture dropped.
	tup = buildTuple(24, 0, 1, data, 0)
	tup.LPLen = 26
	segs, ok = computeTupleLayout(tup, attrs)
	if ok {
		t.Fatal("overrun must not be trustworthy")
	}
	last = segs[len(segs)-1]
	if last.kind != segUnaccounted {
		t.Errorf("fallback tail = %+v", last)
	}

	// Missing t_hoff → single unaccounted run.
	tup = buildTuple(24, 0, 1, data, 0)
	tup.Hoff = nil
	segs, ok = computeTupleLayout(tup, attrs)
	if ok || len(segs) != 1 || segs[0].kind != segUnaccounted || segs[0].bytes != int(tup.LPLen) {
		t.Errorf("nil hoff fallback = ok:%v %+v", ok, segs)
	}
}

func TestComputeTupleLayoutDroppedWithoutBytesHidden(t *testing.T) {
	// A dropped column whose slot holds no bytes (NULL in this tuple) is
	// hidden; the layout must still reconcile.
	data := []byte{0xAA, 0xAA, 0xAA, 0xAA}
	tup := buildTuple(24, pg.HeapHasNull, 2, data, 0)
	dropped := pg.TupleAttr{Name: "........pg.dropped.2........", Dropped: true, Len: -1, Align: "i", Stored: true}
	attrs := []pg.TupleAttr{fixedAttr("a", 4, "i"), dropped}

	segs, ok := computeTupleLayout(tup, attrs)
	if !ok {
		t.Fatal("expected trustworthy layout")
	}
	for _, sg := range segs {
		if sg.kind == segColumn && sg.attr.Dropped {
			t.Errorf("dropped 0 B column should be hidden: %+v", sg)
		}
	}
	if got := sumBytes(segs); got != int(tup.LPLen) {
		t.Errorf("Σ = %d, want lp_len %d", got, tup.LPLen)
	}
}

func TestSortedTupleSegIdx(t *testing.T) {
	segs := []tupleSeg{
		{kind: segColumn, attr: &pg.TupleAttr{Name: "b"}, start: 0, bytes: 8},
		{kind: segColumn, attr: &pg.TupleAttr{Name: "a"}, start: 8, bytes: 2},
		{kind: segColumn, attr: &pg.TupleAttr{Name: "c"}, start: 10, bytes: 8},
	}

	if got := sortedTupleSegIdx(segs, tlSortOffset, false); got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("offset asc = %v, want [0 1 2]", got)
	}
	if got := sortedTupleSegIdx(segs, tlSortColumn, false); got[0] != 1 || got[1] != 0 || got[2] != 2 {
		t.Errorf("column asc = %v, want [1 0 2]", got)
	}
	// bytes desc: the two 8 B segments tie — physical order must survive the
	// reversed direction.
	if got := sortedTupleSegIdx(segs, tlSortBytes, true); got[0] != 0 || got[1] != 2 || got[2] != 1 {
		t.Errorf("bytes desc = %v, want [0 2 1]", got)
	}
}
