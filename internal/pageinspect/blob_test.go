package pageinspect

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zlibBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zstdRawFrame hand-builds a single-segment zstd frame carrying one raw
// (stored) block — the one frame shape that can be produced without an
// encoder, and a real frame any zstd decoder accepts. Payload must be ≤ 255 B
// so the 1-byte Frame_Content_Size form applies.
func zstdRawFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	if len(payload) > 255 {
		t.Fatalf("payload too long for the 1-byte size form: %d", len(payload))
	}
	frame := append(append([]byte{}, zstdMagic...), 0x20, byte(len(payload))) // FHD: single segment, 1-byte FCS
	hdr := uint32(len(payload))<<3 | 0<<1 | 1                                 // block: last, raw, size
	frame = append(frame, byte(hdr), byte(hdr>>8), byte(hdr>>16))
	return append(frame, payload...)
}

func TestZstdFrameSize(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		want  int64
		ok    bool
	}{
		{
			// The header of a real application blob: FHD 0x60 = single
			// segment, 2-byte content size, no checksum, no dictionary.
			name:  "single segment, 2-byte size",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x60, 0xea, 0x10, 0x4d, 0x14, 0x00},
			want:  4586, // 0x10ea + the 256 bias
			ok:    true,
		},
		{
			name:  "single segment, 1-byte size",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x0c},
			want:  12,
			ok:    true,
		},
		{
			// FHD 0x84: 4-byte size, window descriptor present, 1-byte dict id.
			name:  "window descriptor and dictionary id",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x85, 0x50, 0x07, 0x40, 0x0d, 0x03, 0x00},
			want:  200_000,
			ok:    true,
		},
		{
			// FHD 0x00: multi-frame streaming output stores no size at all.
			name:  "size not stored",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x59},
			ok:    false,
		},
		{
			name:  "reserved bit set",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x28, 0x0c},
			ok:    false,
		},
		{
			name:  "header clipped",
			frame: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x60, 0xea},
			ok:    false,
		},
		{
			name:  "not zstd",
			frame: []byte{0x1f, 0x8b, 0x08, 0x00, 0x60, 0xea, 0x10},
			ok:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := zstdFrameSize(c.frame)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("zstdFrameSize = %d, %v; want %d, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestIsZstdFrame(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"data frame", []byte{0x28, 0xb5, 0x2f, 0xfd, 0x60}, true},
		{"skippable frame", []byte{0x5a, 0x2a, 0x4d, 0x18, 0x04}, true},
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00}, false},
		{"short", []byte{0x28, 0xb5}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isZstdFrame(c.b); got != c.want {
				t.Errorf("isZstdFrame = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCompressedBlobValue(t *testing.T) {
	json := []byte(`{"slots":[1,2,3]}`)
	cases := []struct {
		name    string
		payload []byte
		want    string
		ok      bool
	}{
		{"gzip json", gzipBytes(t, json), `gzip: {"slots":[1,2,3]}`, true},
		{"zlib json", zlibBytes(t, json), `zlib: {"slots":[1,2,3]}`, true},
		{"zstd raw block", zstdRawFrame(t, json), `zstd: {"slots":[1,2,3]}`, true},
		{
			// Only the frame header of the real blob: recognised as zstd and
			// sized from the header, but there is nothing to inflate.
			name:    "zstd header only",
			payload: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x60, 0xea, 0x10, 0x4d, 0x14, 0x00},
			want:    "zstd · 4.48 KB raw",
			ok:      true,
		},
		{
			name:    "zstd undecodable and unsized",
			payload: []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x59, 0x01},
			want:    "zstd · undecoded",
			ok:      true,
		},
		{
			// A gzip header whose deflate stream is garbage still names the
			// format and the size its trailer claims.
			name: "gzip corrupt body",
			// 0x07 is a deflate block header with the reserved BTYPE 0b11.
			payload: []byte{0x1f, 0x8b, 0x08, 0, 0, 0, 0, 0, 0, 0xff, 0x07, 0, 0, 0, 0, 0x0a, 0, 0, 0},
			want:    "gzip · 10 B raw",
			ok:      true,
		},
		{
			// 0x78 0x9c passes isZlib but the body is not deflate: no claim,
			// so the caller keeps rendering it as hex.
			name:    "zlib false positive",
			payload: []byte{0x78, 0x9c, 0x00, 0x11, 0x22, 0x33},
			ok:      false,
		},
		{"plain text", []byte("hello"), "", false},
		{"empty", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := compressedBlobValue(c.payload)
			if ok != c.ok || got != c.want {
				t.Errorf("compressedBlobValue = %q, %v; want %q, %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestCompressedBlobValueTruncates(t *testing.T) {
	got, ok := compressedBlobValue(gzipBytes(t, bytes.Repeat([]byte("x"), maxBlobDecode*3)))
	if !ok {
		t.Fatal("gzip payload not recognised")
	}
	want := "gzip: " + strings.Repeat("x", maxBlobDecode) + "…"
	if got != want {
		t.Errorf("truncated value = %q (%d bytes), want %d x's with an ellipsis", got, len(got), maxBlobDecode)
	}
}

// A payload cut mid-rune must still read as text: the prefix read has no idea
// where rune boundaries are.
func TestCompressedBlobValueSplitRune(t *testing.T) {
	got, ok := compressedBlobValue(gzipBytes(t, bytes.Repeat([]byte("ä"), maxBlobDecode)))
	if !ok {
		t.Fatal("gzip payload not recognised")
	}
	if want := "gzip: " + strings.Repeat("ä", maxBlobDecode/2) + "…"; got != want {
		t.Errorf("split-rune value = %.40q…, want text not hex", got)
	}
}

// Compressed payloads are unwrapped whatever the column type says, while a
// plain value of either category renders exactly as before.
func TestFormatVarlenaPayloadBlob(t *testing.T) {
	cases := []struct {
		name        string
		payload     []byte
		typCategory string
		want        string
	}{
		{"bytea holding gzip", gzipBytes(t, []byte("compressed text")), "U", "gzip: compressed text"},
		{"text column holding zlib", zlibBytes(t, []byte("compressed text")), "S", "zlib: compressed text"},
		{"plain text", []byte("plain"), "S", "plain"},
		{"plain bytea", []byte{0x00, 0x01, 0x02}, "U", "\\x000102"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatVarlenaPayload(c.payload, c.typCategory); got != c.want {
				t.Errorf("formatVarlenaPayload = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGzipRawSize(t *testing.T) {
	b := gzipBytes(t, bytes.Repeat([]byte("y"), 1234))
	got, ok := gzipRawSize(b)
	if !ok || got != 1234 {
		t.Errorf("gzipRawSize = %d, %v; want 1234, true", got, ok)
	}
	if _, ok := gzipRawSize(b[:10]); ok {
		t.Error("gzipRawSize on a header-only blob: want false")
	}
	// Sanity-check the fixture against the trailer's own encoding.
	if want := binary.LittleEndian.Uint32(b[len(b)-4:]); want != 1234 {
		t.Errorf("gzip ISIZE = %d, want 1234", want)
	}
}
