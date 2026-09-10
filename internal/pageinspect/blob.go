package pageinspect

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"io"
	"math"
	"unicode/utf8"

	"pgdu/internal/humanize"
)

// Applications routinely compress a payload themselves and store the result in
// a bytea — which decodes to a wall of hex here, hiding what is really a JSON
// document or a text blob one layer down. The container formats all carry a
// magic number in their first bytes, so a payload can be recognised with no
// column-level configuration and unwrapped for display.
//
// maxBlobDecode caps how much of the decompressed payload is kept. The value
// lands on a single line (and this runs per visible row, per render), while a
// few KB of compressed bytea can expand to gigabytes — so decoding is a bounded
// prefix read, never a full inflate. 1 KiB is several times the widest terminal.
const maxBlobDecode = 1024

// compressedBlobValue renders a varlena payload that is itself a compressed
// stream as "<format>: <content>", falling back to "<format> · N raw" when the
// stream announces its size but cannot be decoded (an unvendored or failing
// decoder, a truncated blob). Reports false when the payload is not a
// recognised container, leaving the caller's text/hex rendering untouched.
func compressedBlobValue(payload []byte) (string, bool) {
	switch {
	case isZstdFrame(payload):
		size, sized := zstdFrameSize(payload)
		data, truncated, ok := decompressZstd(payload)
		return blobText("zstd", size, sized, data, truncated, ok), true
	case isGzip(payload):
		size, sized := gzipRawSize(payload)
		data, truncated, ok := decompressGzip(payload)
		return blobText("gzip", size, sized, data, truncated, ok), true
	case isZlib(payload):
		// zlib has no magic number, only a two-byte sanity check that random
		// bytes pass now and then, so it is claimed only once it decodes —
		// otherwise the value falls back to its normal text/hex rendering.
		if data, truncated, ok := decompressZlib(payload); ok {
			return blobText("zlib", 0, false, data, truncated, ok), true
		}
	}
	return "", false
}

// blobText assembles the rendered value from a decode attempt. A decode that
// produced nothing still leaves the format name — knowing a column holds zstd
// is most of the answer — plus the raw size when the container declares one.
func blobText(kind string, size int64, sized bool, data []byte, truncated, ok bool) string {
	if !ok {
		if sized {
			return kind + " · " + humanize.Bytes(size) + " raw"
		}
		return kind + " · undecoded"
	}
	s := kind + ": " + blobPayloadText(data)
	if truncated {
		s += "…"
	}
	return s
}

// blobPayloadText renders decompressed bytes the way the payload's own type
// would be rendered: as escaped text when it is valid UTF-8 (the common case —
// JSON, XML, serialised text), else as hex. A prefix read can cut a multi-byte
// rune in half, which would sink an otherwise textual payload to hex, so the
// trailing partial rune is dropped first.
func blobPayloadText(data []byte) string {
	if len(data) == 0 {
		return "''"
	}
	if t := trimPartialRune(data); utf8.Valid(t) {
		return escapeControlBytes(t)
	}
	return hexString(data)
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence (at most the 3
// bytes a 4-byte rune can leave behind).
func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// readBounded reads at most maxBlobDecode bytes out of a decompressor. Hitting
// the cap is a success, not an error: the extra byte read distinguishes "the
// payload ends here" from "there is more we chose not to inflate". An error
// after some output still yields that output — a blob whose tail is corrupt or
// clipped is worth showing as far as it decoded.
func readBounded(r io.Reader) (data []byte, truncated, ok bool) {
	buf := make([]byte, maxBlobDecode+1)
	n, err := io.ReadFull(r, buf)
	switch {
	case n > maxBlobDecode:
		return buf[:maxBlobDecode], true, true
	case n > 0:
		return buf[:n], false, true
	case err == nil:
		return nil, false, true
	}
	return nil, false, false
}

// zstdMagic is the zstandard frame magic (RFC 8878 §3.1.1), little-endian
// 0xFD2FB528. Skippable frames use 0x184D2A50..5F and may precede the data
// frames, so a blob starting with one is zstd too.
var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

func isZstdFrame(b []byte) bool {
	if bytes.HasPrefix(b, zstdMagic) {
		return true
	}
	return len(b) >= 4 && b[0]&0xF0 == 0x50 && b[1] == 0x2A && b[2] == 0x4D && b[3] == 0x18
}

// zstdFrameSize reads Frame_Content_Size out of a zstd frame header (RFC 8878
// §3.1.1: magic, one descriptor byte, then the optional window descriptor,
// dictionary id and size fields it selects). Reports false when the frame does
// not store its size, when the header is clipped, or when the reserved bit is
// set — which also makes this a stronger "this really is zstd" check than the
// magic alone. A skippable frame declares nothing, so it reports false as well.
func zstdFrameSize(b []byte) (int64, bool) {
	if !bytes.HasPrefix(b, zstdMagic) || len(b) < 5 {
		return 0, false
	}
	fhd := b[4]
	if fhd&0x08 != 0 { // reserved bit
		return 0, false
	}
	i := 5
	if fhd&0x20 == 0 { // not single-segment: window descriptor present
		i++
	}
	switch fhd & 0x03 { // Dictionary_ID_flag → 0/1/2/4 bytes
	case 1:
		i++
	case 2:
		i += 2
	case 3:
		i += 4
	}
	var n int
	switch fhd >> 6 { // Frame_Content_Size_flag
	case 0:
		if fhd&0x20 == 0 {
			return 0, false // field absent unless single-segment
		}
		n = 1
	case 1:
		n = 2
	case 2:
		n = 4
	default:
		n = 8
	}
	if i+n > len(b) {
		return 0, false
	}
	switch n {
	case 1:
		return int64(b[i]), true
	case 2:
		// The 2-byte form is stored biased by 256 (sizes 0..255 use the 1-byte form).
		return int64(binary.LittleEndian.Uint16(b[i:])) + 256, true
	case 4:
		return int64(binary.LittleEndian.Uint32(b[i:])), true
	}
	if u := binary.LittleEndian.Uint64(b[i:]); u <= math.MaxInt64 {
		return int64(u), true
	}
	return 0, false
}

func isGzip(b []byte) bool {
	return len(b) >= 3 && b[0] == 0x1F && b[1] == 0x8B && b[2] == 0x08 // deflate is the only defined CM
}

// gzipRawSize reads ISIZE from the gzip trailer. It is the uncompressed length
// mod 2³², so it is a lower bound for payloads ≥ 4 GiB — irrelevant for a value
// that fits inline in a heap tuple. 18 bytes is the smallest possible member
// (10 B header + 8 B trailer).
func gzipRawSize(b []byte) (int64, bool) {
	if len(b) < 18 {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint32(b[len(b)-4:])), true
}

// isZlib recognises a zlib stream (RFC 1950) from its two-byte header: deflate
// compression method, a window size within spec, and the 31-check over both
// bytes. There is no magic number, so this is a heuristic — but one that only
// 1 in ~500 arbitrary byte pairs passes, and the decode below has to succeed
// before anything is claimed to be zlib.
func isZlib(b []byte) bool {
	return len(b) >= 2 && b[0]&0x0F == 0x08 && b[0]>>4 <= 7 &&
		(uint16(b[0])<<8|uint16(b[1]))%31 == 0
}

func decompressGzip(b []byte) (data []byte, truncated, ok bool) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false, false
	}
	defer r.Close()
	return readBounded(r)
}

func decompressZlib(b []byte) (data []byte, truncated, ok bool) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false, false
	}
	defer r.Close()
	return readBounded(r)
}
