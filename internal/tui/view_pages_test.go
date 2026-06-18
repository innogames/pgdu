package tui

import (
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

func strptr(s string) *string { return &s }

// hexText builds pageinspect-style space-separated hex for a short-varlena
// text value, mirroring what bt_page_items emits for a downlink separator.
func hexText(s string) *string {
	var parts []string
	hdr := byte((len(s)+1)<<1 | 1) // short varlena header includes its own byte
	parts = append(parts, byteHex(hdr))
	for i := 0; i < len(s); i++ {
		parts = append(parts, byteHex(s[i]))
	}
	return strptr(strings.Join(parts, " "))
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
