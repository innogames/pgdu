package tui

import (
	"encoding/binary"
	"testing"

	"pgdu/internal/pg"
)

func TestDecodeAttrValue(t *testing.T) {
	le32 := func(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
	le64 := func(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

	// varatt_external for chunk 42: rawsize 10004 (payload 10000 + header),
	// extinfo = rawsize-4 (uncompressed), toastrelid arbitrary.
	toast := append([]byte{0x01, 0x12}, le32(10004)...)
	toast = append(toast, le32(10000)...)
	toast = append(toast, le32(42)...)
	toast = append(toast, le32(99999)...)

	// 4B compressed header: (total<<2)|2, then va_tcinfo rawsize 5000.
	compressed := append(le32(12<<2|2), le32(5000)...)
	compressed = append(compressed, 'z', 'z', 'z', 'z')

	cases := []struct {
		name string
		attr pg.TupleAttr
		want string
	}{
		{"int4", pg.TupleAttr{Len: 4, TypName: "int4", Value: le32(123)}, "123"},
		{"int8 negative", pg.TupleAttr{Len: 8, TypName: "int8", Value: le64(^uint64(0))}, "-1"},
		{"bool", pg.TupleAttr{Len: 1, TypName: "bool", Value: []byte{1}}, "t"},
		{"timestamp epoch", pg.TupleAttr{Len: 8, TypName: "timestamp", Value: le64(0)}, "2000-01-01 00:00:00"},
		{"text short header", pg.TupleAttr{Len: -1, TypCategory: "S", Value: []byte{0x09, 'a', 'b', 'c'}}, "abc"},
		{"empty string", pg.TupleAttr{Len: -1, TypCategory: "S", Value: []byte{0x03}}, "''"},
		{"json decodes as text", pg.TupleAttr{Len: -1, TypName: "json", TypCategory: "U", Value: []byte{0x1d, '{', '"', 'a', '"', ':', ' ', '1', '2', '3', '4', '5', '6', '}'}}, `{"a": 123456}`},
		{"uuid", pg.TupleAttr{Len: 16, TypName: "uuid", Value: []byte{
			0x52, 0xd1, 0x91, 0x88, 0x55, 0xfd, 0x42, 0x8a,
			0xa2, 0x44, 0x59, 0x74, 0xf2, 0xd5, 0xfb, 0x43,
		}}, "52d19188-55fd-428a-a244-5974f2d5fb43"},
		{"text 4B header", pg.TupleAttr{Len: -1, TypCategory: "S", Value: append(le32(8<<2), 'a', 'b', 'c', 'd')}, "abcd"},
		{"bytea short header", pg.TupleAttr{Len: -1, TypCategory: "U", Value: []byte{0x07, 0x00, 0xff}}, `\x00ff`},
		{"toast pointer", pg.TupleAttr{Len: -1, TypCategory: "S", Value: toast}, "→ toast chunk 42 · 9.77 KB"},
		{"compressed inline", pg.TupleAttr{Len: -1, TypCategory: "S", Value: compressed}, "compressed · 4.88 KB raw"},
		{"fixed length mismatch", pg.TupleAttr{Len: 4, TypName: "int4", Value: le64(1)}, ""},
		{"cstring undecodable", pg.TupleAttr{Len: -2, Value: []byte{'x', 0}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeAttrValue(tc.attr); got != tc.want {
				t.Errorf("decodeAttrValue = %q, want %q", got, tc.want)
			}
		})
	}
}
