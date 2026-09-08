package tui

import (
	"fmt"
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// Fixtures mirror pageinspect's indexkey_test.go: the range column is rendered
// here (view_pages.go) but decoded there, so both packages exercise the same
// column shapes.
func idxCol(typname, typalign string, typlen int32, typcat string) pg.IndexKeyColumn {
	return pg.IndexKeyColumn{
		Def: typname, IsKey: true,
		TypLen: typlen, TypAlign: typalign, TypName: typname, TypCategory: typcat,
	}
}

var (
	int4Col = idxCol("int4", "i", 4, "N")
	int8Col = idxCol("int8", "d", 8, "N")
	textCol = idxCol("text", "i", -1, "S")
)

// rawBytes is the space-separated hex form pageinspect's data column uses.
func rawBytes(b ...byte) *string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	s := strings.Join(parts, " ")
	return &s
}

// TestInternalDownlinkRangesTyped proves the range column decodes integer
// separators via the key-column types rather than dumping raw hex.
func TestInternalDownlinkRangesTyped(t *testing.T) {
	le8 := func(v byte) *string { return rawBytes(v, 0, 0, 0, 0, 0, 0, 0) }
	items := []item{
		tupleItem(1, le8(44)), // high key — page upper bound
		tupleItem(2, nil),     // minus-infinity leftmost child
		tupleItem(3, le8(10)),
		tupleItem(4, le8(20)),
	}
	got := internalDownlinkRanges(items, "i", []pg.IndexKeyColumn{int8Col}, 200)
	want := map[int32]string{
		2: "−∞  …  10",
		3: "10  …  20",
		4: "20  …  44",
	}
	for off, w := range want {
		if plain := stripANSI(got[off]); plain != w {
			t.Errorf("range for off %d = %q, want %q", off, plain, w)
		}
	}
}

func pivotItem(off int32, ctid string, data *string) item {
	return item{data: pg.IndexTuple{ItemOffset: off, Ctid: &ctid, Data: data}}
}

// TestInternalDownlinkRangesTruncated proves the range column renders a
// suffix-truncated separator (natts=1 in the downlink's ctid offset word)
// with its dropped column as −∞, parenthesized like its full neighbours.
func TestInternalDownlinkRangesTruncated(t *testing.T) {
	cols := []pg.IndexKeyColumn{int4Col, textCol}
	full := func(id byte, s string) *string {
		b := make([]byte, 0, 5+len(s))
		b = append(b, id, 0, 0, 0, byte((len(s)+1)<<1|1))
		b = append(b, s...)
		return rawBytes(b...)
	}
	trunc := func(id byte) *string { return rawBytes(id, 0, 0, 0, 0, 0, 0, 0) }
	items := []item{
		pivotItem(1, "(93,2)", full(44, "zz")), // high key — page upper bound
		pivotItem(2, "(628,0)", nil),           // minus-infinity leftmost child
		pivotItem(3, "(629,2)", full(10, "aa")),
		pivotItem(4, "(630,1)", trunc(20)), // suffix-truncated: text col dropped
	}
	got := internalDownlinkRanges(items, "i", cols, 200)
	want := map[int32]string{
		2: "−∞       …  (10,aa)",
		3: "(10,aa)  …  (20,−∞)",
		4: "(20,−∞)  …  (44,zz)",
	}
	for off, w := range want {
		if plain := stripANSI(got[off]); plain != w {
			t.Errorf("range for off %d = %q, want %q", off, plain, w)
		}
	}
}
