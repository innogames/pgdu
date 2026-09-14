package pageinspect

import (
	"bytes"
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// Hand-assembled pglz stream for "abcabcabcabc": control byte 0x08 = three
// literals (bits 0–2 clear) then one tag (bit 3): len nibble 6 (+3 = 9),
// offset 3 — a match that overlaps its own output.
var pglzABC = []byte{0x08, 'a', 'b', 'c', 0x06, 0x03}

func TestPglzDecompress(t *testing.T) {
	got, ok := pglzDecompress(pglzABC, 12)
	if !ok || string(got) != "abcabcabcabc" {
		t.Fatalf("got %q ok=%v", got, ok)
	}

	// A saturated length nibble (15 → 18) continues in a third byte: 'a' then
	// 200 copies of it at offset 1 → 201 bytes.
	long := []byte{0x02, 'a', 0x0f, 0x01, 200 - 18}
	got, ok = pglzDecompress(long, 201)
	if !ok || len(got) != 201 || bytes.Count(got, []byte{'a'}) != 201 {
		t.Fatalf("extended length: %d bytes ok=%v", len(got), ok)
	}

	for name, tc := range map[string]struct {
		src  []byte
		size int
	}{
		"size mismatch":     {pglzABC, 13},
		"short":             {pglzABC, 11},
		"offset before out": {[]byte{0x01, 0x06, 0x09}, 12},
		"zero offset":       {[]byte{0x02, 'a', 0x06, 0x00}, 10},
		"truncated tag":     {[]byte{0x02, 'a', 0x06}, 10},
	} {
		if _, ok := pglzDecompress(tc.src, tc.size); ok {
			t.Errorf("%s: accepted a stream it must reject", name)
		}
	}
}

// Hand-assembled LZ4 block for "abcabcabcabcXYZWV": token 0x35 = 3 literals,
// match nibble 5 (+4 = 9) at offset 3; then the mandatory literal-only last
// sequence, token 0x50 = 5 literals.
var lz4ABC = []byte{0x35, 'a', 'b', 'c', 0x03, 0x00, 0x50, 'X', 'Y', 'Z', 'W', 'V'}

func TestLZ4BlockDecompress(t *testing.T) {
	got, ok := lz4BlockDecompress(lz4ABC, 17)
	if !ok || string(got) != "abcabcabcabcXYZWV" {
		t.Fatalf("got %q ok=%v", got, ok)
	}

	// Both nibbles saturated: 15+5 = 20 literals, then a match of 15+10+4 = 29
	// at offset 1, then a literal-only tail.
	lits := bytes.Repeat([]byte{'z'}, 20)
	ext := append([]byte{0xff, 5}, lits...)
	ext = append(ext, 0x01, 0x00, 10, 0x10, 'q')
	got, ok = lz4BlockDecompress(ext, 20+29+1)
	if !ok || len(got) != 50 || got[49] != 'q' || bytes.Count(got, []byte{'z'}) != 49 {
		t.Fatalf("extended nibbles: %q ok=%v", got, ok)
	}

	for name, tc := range map[string]struct {
		src  []byte
		size int
	}{
		"size mismatch":     {lz4ABC, 18},
		"zero offset":       {[]byte{0x35, 'a', 'b', 'c', 0x00, 0x00, 0x10, 'x'}, 13},
		"offset too far":    {[]byte{0x35, 'a', 'b', 'c', 0x04, 0x00, 0x10, 'x'}, 13},
		"literals past end": {[]byte{0x50, 'a'}, 5},
		"dangling offset":   {[]byte{0x35, 'a', 'b', 'c', 0x03}, 12},
	} {
		if _, ok := lz4BlockDecompress(tc.src, tc.size); ok {
			t.Errorf("%s: accepted a block it must reject", name)
		}
	}
}

// tcinfo builds the va_tcinfo header a compressed TOAST value's chunks start
// with: method in the top two bits, inflated size below.
func tcinfo(method uint32, rawSize int) []byte {
	return le32b(method<<30 | uint32(rawSize))
}

func TestDecodeToastValueCompressed(t *testing.T) {
	for _, tc := range []struct {
		name, method, want string
		data               []byte
	}{
		{"pglz", "pglz", "abcabcabcabc", concat(tcinfo(0, 12), pglzABC)},
		{"lz4", "lz4", "abcabcabcabcXYZWV", concat(tcinfo(1, 17), lz4ABC)},
	} {
		d := DecodeToastValue(pg.ToastValue{Data: tc.data, StoredBytes: int64(len(tc.data)), Chunks: 1})
		if d.Method != tc.method || d.RawSize != int64(len(tc.want)) {
			t.Errorf("%s: method %q raw %d, want %q %d", tc.name, d.Method, d.RawSize, tc.method, len(tc.want))
		}
		if string(d.Payload) != tc.want || d.Text != tc.want {
			t.Errorf("%s: payload %q text %q, want %q", tc.name, d.Payload, d.Text, tc.want)
		}
		if d.Note != "" {
			t.Errorf("%s: unexpected note %q", tc.name, d.Note)
		}
	}
}

// An uncompressed value whose first bytes happen to read as a plausible header
// ("hell" → lz4, ~745 MB raw) must come through as stored, with the failed
// attempt explained rather than hidden.
func TestDecodeToastValueUncompressedLookalike(t *testing.T) {
	d := DecodeToastValue(pg.ToastValue{Data: []byte("hello world"), StoredBytes: 11, Chunks: 1})
	if d.Method != "" || d.RawSize != 0 {
		t.Errorf("claimed compression %q/%d for plain text", d.Method, d.RawSize)
	}
	if d.Text != "hello world" || string(d.Payload) != "hello world" {
		t.Errorf("text %q payload %q, want the stored bytes", d.Text, d.Payload)
	}
	if !strings.Contains(d.Note, "lz4") || !strings.Contains(d.Note, "did not inflate") {
		t.Errorf("note %q should explain the rejected header", d.Note)
	}

	// A header claiming less than what is stored is not even tried; bytes that
	// are neither jsonb nor UTF-8 end as hex.
	raw := concat(le32b(1), bytes.Repeat([]byte{0xff}, 8))
	d = DecodeToastValue(pg.ToastValue{Data: raw, StoredBytes: int64(len(raw)), Chunks: 1})
	if d.Note != "" || d.Method != "" || d.Text != `\x01000000ffffffffffffffff` {
		t.Errorf("implausible header: method %q note %q text %q", d.Method, d.Note, d.Text)
	}
}

// A truncated fetch of a value whose header reads as compressed can't be
// inflated, nor can the header be ruled out: the stored prefix is shown, marked
// unverified. A prefix with no such header is plain and rendered as such.
func TestDecodeToastValueTruncated(t *testing.T) {
	d := DecodeToastValue(pg.ToastValue{Data: concat(tcinfo(1, 5000), lz4ABC), StoredBytes: 9000, Truncated: true})
	if d.Method != "" || !d.Unverified || !strings.HasPrefix(d.Text, `\x`) {
		t.Errorf("truncated compressed: %+v", d)
	}
	if !strings.Contains(d.Note, "lz4") || !strings.Contains(d.Note, "prefix") {
		t.Errorf("note %q should name the header and say the stream is incomplete", d.Note)
	}

	// Four zero bytes can't be a header (raw size 0), so this prefix is plain.
	d = DecodeToastValue(pg.ToastValue{Data: concat(le32b(0), []byte("plain text pre")), StoredBytes: 100, Truncated: true})
	if d.Unverified || d.Text != `\x00\x00\x00\x00plain text pre` || !strings.Contains(d.Note, "first 18 B of 100 B") {
		t.Errorf("truncated plain: unverified %v text %q note %q", d.Unverified, d.Text, d.Note)
	}
}

// The owner's column type steers the rendering: one distinct type is trusted
// (bytea → hex even for UTF-8 bytes), no or ambiguous hints fall back to the
// self-validating jsonb → text → hex order.
func TestDecodeToastValueTypeHints(t *testing.T) {
	obj := concat(le32b(jbFObject|1), le32b(jEntryString|1), le32b(jEntryBoolTrue), []byte{'a'})

	d := DecodeToastValue(pg.ToastValue{Data: obj, StoredBytes: int64(len(obj)), Chunks: 1})
	if d.Text != `{"a": true}` {
		t.Errorf("no hint, jsonb payload: %q", d.Text)
	}
	d = DecodeToastValue(pg.ToastValue{Data: obj, StoredBytes: int64(len(obj)), Chunks: 1,
		OwnerTypes: []pg.ToastOwnerType{{TypName: "jsonb", TypCategory: "U"}, {TypName: "text", TypCategory: "S"}}})
	if d.Text != `{"a": true}` {
		t.Errorf("ambiguous hints, jsonb payload: %q", d.Text)
	}

	txt := []byte("hello")
	d = DecodeToastValue(pg.ToastValue{Data: txt, StoredBytes: 5, Chunks: 1,
		OwnerTypes: []pg.ToastOwnerType{{TypName: "bytea", TypCategory: "U"}}})
	if d.Text != `\x68656c6c6f` {
		t.Errorf("bytea hint: %q, want hex", d.Text)
	}
	d = DecodeToastValue(pg.ToastValue{Data: txt, StoredBytes: 5, Chunks: 1,
		OwnerTypes: []pg.ToastOwnerType{{TypName: "xml", TypCategory: "U"}}})
	if d.Text != "hello" {
		t.Errorf("xml hint: %q, want text", d.Text)
	}
}

func TestDecodeToastValueTextCap(t *testing.T) {
	big := bytes.Repeat([]byte("é"), toastDecodeMaxText) // 2 B per rune, twice the cap
	d := DecodeToastValue(pg.ToastValue{Data: big, StoredBytes: int64(len(big)), Chunks: 1})
	if len(d.Text) > toastDecodeMaxText+len("…") || !strings.HasSuffix(d.Text, "…") {
		t.Errorf("text not capped: %d bytes, suffix %q", len(d.Text), d.Text[len(d.Text)-3:])
	}
	if strings.Contains(d.Text[:len(d.Text)-len("…")], "�") || !strings.Contains(d.Note, "cut at") {
		t.Errorf("cap must land on a rune boundary and be noted: note %q", d.Note)
	}
}
