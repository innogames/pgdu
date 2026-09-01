package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pgdu/internal/pg"
)

func TestDecodeHexKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		hex  string
		want string
		ok   bool
	}{
		// Short-varlena text: leading 0x2f is the 1-byte length header, the
		// trailing 0x00 is padding — both stripped.
		{"text key", "2f 75 73 65 72 5f 32 30 34 31 35 40 65 78 61 6d 70 6c 65 2e 63 6f 6d 00",
			"user_20415@example.com", true},
		{"empty (minus infinity)", "", "", false},
		{"truncated marker ends parse", "2f 75 73 65 72 …", "user", true},
		// A non-printable byte in the payload means "not a clean text key" —
		// keep it hex.
		{"binary stays hex", "04 00 00 00", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeHexKey(tc.hex)
			if ok != tc.ok || got != tc.want {
				t.Errorf("decodeHexKey(%q) = (%q, %v), want (%q, %v)", tc.hex, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// hexText builds pageinspect-style space-separated hex for a short-varlena
// text value, mirroring what bt_page_items emits for a downlink separator.
func hexText(s string) *string {
	parts := make([]string, 0, len(s)+1)
	hdr := byte((len(s)+1)<<1 | 1) // short varlena header includes its own byte
	parts = append(parts, byteHex(hdr))
	for i := 0; i < len(s); i++ {
		parts = append(parts, byteHex(s[i]))
	}
	joined := strings.Join(parts, " ")
	return &joined
}

func byteHex(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0xf]})
}

func tupleItem(off int32, data *string) item {
	return item{data: pg.IndexTuple{ItemOffset: off, Data: data}}
}

func TestInternalDownlinkRanges(t *testing.T) {
	// Non-rightmost internal page layout: offset 1 is the high key, offset 2 is
	// the minus-infinity leftmost downlink, offsets 3+ are keyed downlinks in
	// key order. Feed the items out of offset order to prove sorting.
	items := []item{
		tupleItem(3, hexText("d")),
		tupleItem(1, hexText("m")), // high key — page upper bound
		tupleItem(2, nil),          // minus-infinity leftmost child
		tupleItem(4, hexText("h")),
	}
	got := internalDownlinkRanges(items, "i", nil, 200)

	want := map[int32]string{
		2: "−∞  …  d",
		3: "d  …  h",
		4: "h  …  m", // last downlink runs up to the page high key
	}
	for off, w := range want {
		plain := stripANSI(got[off])
		if plain != w {
			t.Errorf("range for off %d = %q, want %q", off, plain, w)
		}
	}
	if _, ok := got[1]; ok {
		t.Errorf("high key (off 1) should not get a range, got %q", got[1])
	}
}

func TestInternalDownlinkRangesRightmost(t *testing.T) {
	// Rightmost page: no high key, so offset 1 is the minus-infinity downlink
	// and the last range runs to +∞.
	items := []item{
		tupleItem(1, nil),
		tupleItem(2, hexText("k")),
	}
	got := internalDownlinkRanges(items, "i", nil, 200)
	if plain := stripANSI(got[1]); plain != "−∞  …  k" {
		t.Errorf("off 1 range = %q, want %q", plain, "−∞  …  k")
	}
	if plain := stripANSI(got[2]); plain != "k  …  +∞" {
		t.Errorf("off 2 range = %q, want %q", plain, "k  …  +∞")
	}
}

func TestInternalDownlinkRangesSkipsNonInternal(t *testing.T) {
	items := []item{tupleItem(1, hexText("a"))}
	if got := internalDownlinkRanges(items, "l", nil, 200); got != nil {
		t.Errorf("leaf page should return nil ranges, got %v", got)
	}
}

func stripANSI(s string) string { return ansi.Strip(s) }

func TestBtreeLevelsLine(t *testing.T) {
	const w = 120
	s := &screen{}
	if got := btreeLevelsLine(s, w); got != "" {
		t.Errorf("no scan issued: line = %q, want empty", got)
	}
	s.btreeLevelsLoading = true
	if got := stripANSI(btreeLevelsLine(s, w)); !strings.Contains(got, "counting") {
		t.Errorf("scan in flight: line = %q, want a counting placeholder", got)
	}
	s.btreeLevelsLoading = false
	s.btreeLevelsDone = true
	if got := btreeLevelsLine(s, w); got != "" {
		t.Errorf("empty census (metapage-only index): line = %q, want empty", got)
	}
	s.btreeLevelsErr = errors.New("permission denied for function bt_multi_page_stats")
	if got := stripANSI(btreeLevelsLine(s, w)); !strings.Contains(got, "unavailable — permission denied") {
		t.Errorf("failed scan: line = %q, want the failure reason inline", got)
	}
	s.btreeLevelsErr = nil
	s.btreeLevels = []pg.BtreeLevelCount{
		{Level: 2, Type: "r", Pages: 1},
		{Level: 1, Type: "i", Pages: 154},
		{Level: 0, Type: "l", Pages: 139538},
		{Level: 0, Type: "d", Pages: 3},
	}
	want := "  levels: L2 1 root  ·  L1 154  ·  L0 139538 leaf  ·  deleted 3"
	if got := stripANSI(btreeLevelsLine(s, w)); got != want {
		t.Errorf("levels line = %q, want %q", got, want)
	}
}

func TestIndexTuplePageType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   string
		level int32
		want  string
	}{
		{"single-page root is a leaf", "r", 0, "r"},
		{"multi-level root is internal", "r", 2, "i"},
		{"leaf unchanged", "l", 0, "l"},
		{"internal unchanged", "i", 1, "i"},
		{"deleted unchanged", "d", 3, "d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := indexTuplePageType(tc.typ, tc.level); got != tc.want {
				t.Errorf("indexTuplePageType(%q, %d) = %q, want %q", tc.typ, tc.level, got, tc.want)
			}
		})
	}
}

func TestHeapTuplesHeaderPKColumn(t *testing.T) {
	with := stripANSI(renderHeapTuplesHeader(sortByLP, false, true))
	if !strings.Contains(with, "pk") {
		t.Errorf("header with pk = %q, want a pk column", with)
	}
	// pk sits between the physical address and the visibility verdict.
	if strings.Index(with, "ctid") > strings.Index(with, "pk") ||
		strings.Index(with, "pk") > strings.Index(with, "state") {
		t.Errorf("header column order = %q, want ctid … pk … state", with)
	}
	without := stripANSI(renderHeapTuplesHeader(sortByLP, false, false))
	if strings.Contains(without, "pk") {
		t.Errorf("header without pk = %q, want no pk column", without)
	}
}

func TestTuplePKCell(t *testing.T) {
	pk := "42"
	if got := stripANSI(tuplePKCell(pg.HeapTuple{PK: &pk})); got != "42" {
		t.Errorf("pk cell = %q, want 42", got)
	}
	// No visible row at this ctid (dead/aborted/uncommitted tuple).
	if got := stripANSI(tuplePKCell(pg.HeapTuple{})); got != "—" {
		t.Errorf("nil pk cell = %q, want —", got)
	}
	// A key wider than the column is clipped, not wrapped.
	long := strings.Repeat("x", tuplePKColW+20)
	if got := stripANSI(tuplePKCell(pg.HeapTuple{PK: &long})); len([]rune(got)) > tuplePKColW {
		t.Errorf("pk cell = %d cells, want <= %d", len([]rune(got)), tuplePKColW)
	}
}

func TestTupleKeyLine(t *testing.T) {
	pk := "42"
	single := stripANSI(tupleKeyLine(pg.HeapTuple{PK: &pk}, []string{"id"}, ""))
	if single != "key: id = 42" {
		t.Errorf("single-column key line = %q, want %q", single, "key: id = 42")
	}
	composite := "(7, 42)"
	multi := stripANSI(tupleKeyLine(pg.HeapTuple{PK: &composite}, []string{"tenant_id", "id"}, ""))
	if multi != "key: (tenant_id, id) = (7, 42)" {
		t.Errorf("composite key line = %q", multi)
	}
	// A tuple with no visible row says so instead of showing an empty value.
	missing := stripANSI(tupleKeyLine(pg.HeapTuple{}, []string{"id"}, ""))
	if !strings.Contains(missing, "no row visible") {
		t.Errorf("missing-row key line = %q, want a no-visible-row note", missing)
	}
}

func TestHeapTupleExpandLeadsWithKey(t *testing.T) {
	pk := "42"
	tup := pg.HeapTuple{LP: 1, LPFlags: pg.LPNormal, LPLen: 40, PK: &pk}
	withKey := renderHeapTupleExpand(tup, []string{"id"})
	if len(withKey) != 4 || !strings.Contains(stripANSI(withKey[0]), "key: id = 42") {
		t.Fatalf("expand with pk = %d lines, first %q", len(withKey), stripANSI(withKey[0]))
	}
	// No primary key → no key line, and the block keeps its original height.
	if plain := renderHeapTupleExpand(tup, nil); len(plain) != 3 {
		t.Errorf("expand without pk = %d lines, want 3", len(plain))
	}
	// Bodyless line pointers keep their one-liner regardless of the key.
	if red := renderHeapTupleExpand(pg.HeapTuple{LPFlags: pg.LPRedirect, LPOff: 7}, []string{"id"}); len(red) != 1 {
		t.Errorf("REDIRECT expand = %d lines, want 1", len(red))
	}
}

// A HOT-updated row keeps its index entry pointing at the chain root, which
// pruning turned into an LP_REDIRECT. The row is alive, so the entry must show
// the resolved key and the hop — never the dead tag.
func TestIndexTupleRowHotRedirect(t *testing.T) {
	root, live, key := "(0,50)", "(0,112)", "allies-129ece0"
	row := stripANSI(renderIndexTupleRow(pg.IndexTuple{
		ItemOffset: 2, ItemLen: 56, Ctid: &root,
		Data: hexText("allies-129ece0"), HotCtid: &live, HotDecoded: &key,
	}, "l", "", nil, 60, false))
	if !strings.Contains(row, "(0,50)▸112") {
		t.Errorf("row = %q, want the ctid to show the redirect hop (0,50)▸112", row)
	}
	if !strings.Contains(row, key) {
		t.Errorf("row = %q, want the key resolved through the redirect", row)
	}
	if strings.Contains(row, "dead") {
		t.Errorf("row = %q, want no dead tag on a live HOT-updated entry", row)
	}
}

// The dead tag tracks bt_page_items' LP_DEAD bit and nothing else: an entry
// whose ctid simply isn't visible from our snapshot is not a dead entry.
func TestIndexTupleRowDeadTagFollowsLPDead(t *testing.T) {
	ctid := "(0,50)"
	tup := pg.IndexTuple{ItemOffset: 2, ItemLen: 56, Ctid: &ctid, Data: hexText("allies-129ece0")}

	unresolved := stripANSI(renderIndexTupleRow(tup, "l", "", nil, 60, false))
	if strings.Contains(unresolved, "dead") {
		t.Errorf("row = %q, want no dead tag when the heap join merely missed", unresolved)
	}
	if !strings.Contains(unresolved, "allies-129ece0") {
		t.Errorf("row = %q, want the key decoded from the raw index bytes", unresolved)
	}

	tup.Dead = true
	dead := stripANSI(renderIndexTupleRow(tup, "l", "", nil, 60, false))
	if !strings.Contains(dead, "dead") {
		t.Errorf("row = %q, want a dead tag when LP_DEAD is set", dead)
	}
}

// The directly-decoded key wins over the redirect projection, and a pivot's
// ctid keeps its label even if a redirect happened to resolve underneath it.
func TestIndexTupleRowDecodedPrecedence(t *testing.T) {
	ctid, live, direct, hot := "(0,50)", "(0,112)", "direct", "viaredirect"
	row := stripANSI(renderIndexTupleRow(pg.IndexTuple{
		ItemOffset: 2, ItemLen: 56, Ctid: &ctid,
		Decoded: &direct, HotCtid: &live, HotDecoded: &hot,
	}, "l", "", nil, 60, false))
	if !strings.Contains(row, direct) || strings.Contains(row, hot) {
		t.Errorf("row = %q, want the directly-decoded key", row)
	}
}
