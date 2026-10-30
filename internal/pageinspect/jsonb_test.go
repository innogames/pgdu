package pageinspect

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"pgdu/internal/pg"
)

func le16(v uint16) []byte  { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func le32b(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// vlNumeric wraps an on-disk numeric (n_header + optional weight + digits) in
// the 4-byte varlena header the way jsonb stores it.
func vlNumeric(body []byte) []byte {
	total := 4 + len(body)
	return concat(le32b(uint32(total)<<2), body)
}

func TestDecodeOnDiskNumeric(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		// 1: short, pos, weight 0, dscale 0, digit [1].
		{"one", vlNumeric(concat(le16(0x8000), le16(1))), "1"},
		// 123456 = 12*10000 + 3456: short, weight 1, digits [12, 3456].
		{"big", vlNumeric(concat(le16(0x8001), le16(12), le16(3456))), "123456"},
		// -12.5: short, neg, dscale 1, weight 0, digits [12, 5000].
		{"neg frac", vlNumeric(concat(le16(0xA080), le16(12), le16(5000))), "-12.5"},
		// 0: short, weight 0, dscale 0, no digits.
		{"zero", vlNumeric(le16(0x8000)), "0"},
		// 10000 = 1*10000: short, weight 1, single digit [1] (trailing group implicit 0).
		{"ten thousand", vlNumeric(concat(le16(0x8001), le16(1))), "10000"},
		// NaN: special.
		{"nan", vlNumeric(le16(0xC000)), "NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeOnDiskNumeric(tc.in)
			if !ok || got != tc.want {
				t.Errorf("decodeOnDiskNumeric = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestDecodeJsonb(t *testing.T) {
	// {"a": true}
	obj := concat(
		le32b(jbFObject|1),
		le32b(jEntryString|1), // key "a"
		le32b(jEntryBoolTrue), // value true
		[]byte{'a'},
	)
	// [null, "x"]
	arr := concat(
		le32b(jbFArray|2),
		le32b(jEntryNull),
		le32b(jEntryString|1),
		[]byte{'x'},
	)
	// scalar "hi"
	scalar := concat(
		le32b(jbFArray|jbFScalar|1),
		le32b(jEntryString|2),
		[]byte{'h', 'i'},
	)
	// ["ab", 1] — exercises INTALIGN padding before the embedded numeric.
	num1 := vlNumeric(concat(le16(0x8000), le16(1)))
	withNum := concat(
		le32b(jbFArray|2),
		le32b(jEntryString|2),                    // "ab", off 0, len 2
		le32b(jEntryNumeric|uint32(2+len(num1))), // pad 2 + numeric, off 2
		[]byte{'a', 'b'},
		[]byte{0x00, 0x00}, // INTALIGN pad
		num1,
	)
	// [[]] — nested container.
	nested := concat(
		le32b(jbFArray|1),
		le32b(jEntryContainer|4),
		le32b(jbFArray), // zero elements
	)

	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"object", obj, `{"a": true}`},
		{"array", arr, `[null, "x"]`},
		{"scalar string", scalar, `"hi"`},
		{"empty object", le32b(jbFObject), `{}`},
		{"empty array", le32b(jbFArray), `[]`},
		{"numeric elem", withNum, `["ab", 1]`},
		{"nested", nested, `[[]]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeJsonb(tc.in)
			if !ok || got != tc.want {
				t.Errorf("decodeJsonb = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestDecodeAttrValueJsonb(t *testing.T) {
	// {"a": true} container wrapped in a 4-byte varlena header, as a jsonb attr.
	container := concat(
		le32b(jbFObject|1),
		le32b(jEntryString|1),
		le32b(jEntryBoolTrue),
		[]byte{'a'},
	)
	val := concat(le32b(uint32(4+len(container))<<2), container)
	attr := pg.TupleAttr{Len: -1, TypName: "jsonb", TypCategory: "U", Value: val}
	if got := DecodeAttrValue(attr); got != `{"a": true}` {
		t.Errorf("decodeAttrValue = %q, want %q", got, `{"a": true}`)
	}
}

// TestDecodeJsonbOffsetStride covers the JENTRY_HAS_OFF path, which every
// other fixture is too short to reach: PostgreSQL stores an end offset instead
// of a length on every JB_OFFSET_STRIDE-th entry, so a child past the first
// stride is located by summing lengths back to the nearest offset-bearing one.
// The array here holds 40 one-byte strings, so entries 0 and 32 carry offsets.
func TestDecodeJsonbOffsetStride(t *testing.T) {
	const n = 40
	entries := make([][]byte, 0, n)
	data := make([]byte, 0, n)
	for i := range n {
		e := uint32(jEntryString | 1)
		if i%jbOffsetStride == 0 {
			// An offset-bearing entry stores the element's END offset.
			e = jEntryString | jEntryHasOff | uint32(i+1)
		}
		entries = append(entries, le32b(e))
		data = append(data, byte('a'+i%26))
	}
	arr := concat(append([][]byte{le32b(jbFArray | n)}, entries...)...)
	arr = concat(arr, data)

	elems := make([]string, 0, n)
	for i := range n {
		elems = append(elems, `"`+string(rune('a'+i%26))+`"`)
	}
	want := "[" + strings.Join(elems, ", ") + "]"

	got, ok := decodeJsonb(arr)
	if !ok || got != want {
		t.Errorf("decodeJsonb over %d entries = %q, %v; want %q", n, got, ok, want)
	}
}

// A container whose entries never bear an offset within the stride is not
// jsonb PostgreSQL wrote; walking it anyway is quadratic in the entry count,
// so the decoder must refuse rather than scan.
func TestDecodeJsonbRejectsUnstridedOffsets(t *testing.T) {
	const n = jbOffsetStride + 8
	parts := make([][]byte, 0, 1+n)
	parts = append(parts, le32b(jbFArray|n))
	for range n {
		parts = append(parts, le32b(jEntryString|1)) // all lengths, no offsets
	}
	arr := concat(concat(parts...), bytes.Repeat([]byte{'a'}, n))
	if got, ok := decodeJsonb(arr); ok {
		t.Errorf("decoded an unstrided container: %q", got)
	}
}
