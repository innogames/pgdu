package tui

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"pgdu/internal/pg"
)

// pgEpochUnix is 2000-01-01T00:00:00Z as a Unix timestamp — the zero point for
// PostgreSQL's internal timestamp/date storage.
const pgEpochUnix = 946684800

// decodeIndexKey decodes pageinspect's space-separated hex `data` (the raw
// IndexTuple key bytes, with the null bitmap already stripped by bt_page_items)
// into a readable value, using the index's per-column physical types in `cols`.
// It mirrors PostgreSQL's tuple deform: align each attribute by its typalign,
// then read typlen bytes (fixed) or a varlena header+payload, formatting by
// type. A single column renders bare ("23"); multiple columns join as
// "(a,b)" to match the heap-projected IndexTuple.Decoded style.
//
// This is the fallback path for internal-page separators and dead/pivot leaf
// entries, where the heap projection is unavailable. It degrades gracefully: a
// separator suffix-truncated to fewer columns, or a column of a type we can't
// size, stops the walk rather than emitting garbage from misaligned bytes —
// better to show fewer columns than wrong ones. The caller falls back to raw
// hex when ok == false (nothing decoded).
//
// Assumes a little-endian server: pageinspect returns the bytes in the server's
// native byte order and there is no way to probe it over the wire. Every
// production PostgreSQL platform is little-endian, matching the on-wire data.
func decodeIndexKey(hexData string, cols []pg.IndexKeyColumn) (string, bool) {
	parts, complete, ok := decodeIndexKeyParts(hexData, cols)
	if !ok {
		return "", false
	}
	out := parts[0]
	if len(parts) > 1 {
		out = "(" + strings.Join(parts, ",") + ")"
	}
	// Signal there's more we couldn't show: pageinspect cut the hex short, or a
	// column type/length stopped the walk before every column was consumed.
	if !complete {
		out += "…"
	}
	return out, true
}

// decodeIndexKeyParts decodes the per-column values of an index key from
// pageinspect's hex `data` (see decodeIndexKey for the deform rules). complete
// is false when not every column was consumed — pageinspect truncated the hex,
// or a column's type/length stopped the walk; callers that join for display
// append a "…" then. ok is false only when nothing at all decoded (empty data —
// e.g. the minus-infinity downlink — or unknown column types). The leading
// element (parts[0]) is the index's first key column, which the seek feature
// compares against.
func decodeIndexKeyParts(hexData string, cols []pg.IndexKeyColumn) (parts []string, complete, ok bool) {
	if len(cols) == 0 {
		return nil, false, false
	}
	b, truncated := parseHexBytes(hexData)
	if len(b) == 0 {
		return nil, false, false
	}

	off := 0
	complete = !truncated
	for _, c := range cols {
		if off >= len(b) {
			complete = false // suffix-truncated separator: fewer attrs than cols
			break
		}
		s, next, decodeOK := decodeIndexColumn(b, off, c)
		if !decodeOK {
			complete = false
			break
		}
		parts = append(parts, s)
		off = next
	}
	if len(parts) == 0 {
		return nil, false, false
	}
	return parts, complete, true
}

// decodeIndexColumn decodes one attribute starting at absolute offset off within
// b (absolute because alignment padding is computed from the tuple-data start).
// Returns the formatted value and the offset just past it, or ok == false when
// the value can't be sized/read (caller stops the walk).
func decodeIndexColumn(b []byte, off int, c pg.IndexKeyColumn) (string, int, bool) {
	switch {
	case c.TypLen > 0:
		off = alignOffset(off, c.TypAlign)
		end := off + int(c.TypLen)
		if end > len(b) {
			return "", 0, false // partial — data shorter than the fixed width
		}
		return formatFixed(b[off:end], c.TypName), end, true
	case c.TypLen == -1:
		return decodeVarlena(b, off, c)
	default:
		// cstring (-2) and anything unexpected: not safely sizeable here.
		return "", 0, false
	}
}

// alignOffset rounds off up to the boundary for a pg attalign code
// ('c'=1, 's'=2, 'i'=4, 'd'=8).
func alignOffset(off int, typalign string) int {
	a := 1
	switch typalign {
	case "s":
		a = 2
	case "i":
		a = 4
	case "d":
		a = 8
	}
	if a <= 1 {
		return off
	}
	return (off + a - 1) &^ (a - 1)
}

// formatFixed renders a fixed-width attribute's bytes (little-endian). Integers
// are signed unless the type is an unsigned oid-family type; float4/float8 are
// reinterpreted from their bit pattern; bool and "char" get their natural form.
// Other fixed-width types (uuid, name, timestamps, …) fall back to text/hex of
// the raw bytes — a stable, length-correct rendering even when not pretty.
func formatFixed(v []byte, typName string) string {
	switch len(v) {
	case 1:
		switch typName {
		case "bool":
			if v[0] != 0 {
				return "t"
			}
			return "f"
		case "char": // the internal single-byte "char" type
			if v[0] >= 0x20 && v[0] <= 0x7e {
				return string(v)
			}
			return strconv.Itoa(int(int8(v[0])))
		default:
			return strconv.Itoa(int(int8(v[0])))
		}
	case 2:
		u := binary.LittleEndian.Uint16(v)
		if isUnsignedType(typName) {
			return strconv.FormatUint(uint64(u), 10)
		}
		return strconv.FormatInt(int64(int16(u)), 10)
	case 4:
		u := binary.LittleEndian.Uint32(v)
		switch {
		case typName == "float4":
			return strconv.FormatFloat(float64(math.Float32frombits(u)), 'g', -1, 32)
		case typName == "date":
			return formatPGDate(int32(u))
		case isUnsignedType(typName):
			return strconv.FormatUint(uint64(u), 10)
		default:
			return strconv.FormatInt(int64(int32(u)), 10)
		}
	case 8:
		u := binary.LittleEndian.Uint64(v)
		switch {
		case typName == "float8":
			return strconv.FormatFloat(math.Float64frombits(u), 'g', -1, 64)
		case typName == "timestamp" || typName == "timestamptz":
			return formatPGTimestamp(int64(u))
		case typName == "time":
			return formatPGTime(int64(u))
		case isUnsignedType(typName):
			return strconv.FormatUint(u, 10)
		default:
			return strconv.FormatInt(int64(u), 10)
		}
	case 16:
		if typName == "uuid" {
			// Stored byte-for-byte in text order, so hex + dashes is the
			// canonical 8-4-4-4-12 form.
			return fmt.Sprintf("%x-%x-%x-%x-%x", v[0:4], v[4:6], v[6:8], v[8:10], v[10:16])
		}
		return formatBytesText(v)
	default:
		return formatBytesText(v)
	}
}

// formatPGTimestamp renders an int64 timestamp (microseconds since the
// 2000-01-01 PG epoch) as ISO text in UTC. timestamptz is stored in UTC too, so
// it shares this path — displayed in UTC since the session time zone isn't known
// here. PG's ±infinity sentinels map to their textual form.
func formatPGTimestamp(micros int64) string {
	switch micros {
	case math.MaxInt64:
		return "infinity"
	case math.MinInt64:
		return "-infinity"
	}
	// Split into seconds + remainder so the nanosecond conversion can't overflow
	// int64 for far-future timestamps (micros*1000 would). time.Unix normalizes a
	// negative remainder, so pre-2000 timestamps work too.
	secs, usec := micros/1_000_000, micros%1_000_000
	return time.Unix(pgEpochUnix+secs, usec*1000).UTC().Format("2006-01-02 15:04:05.999999")
}

// formatPGDate renders an int32 date (days since the 2000-01-01 PG epoch) as
// ISO text. PG's ±infinity sentinels map to their textual form.
func formatPGDate(days int32) string {
	switch days {
	case math.MaxInt32:
		return "infinity"
	case math.MinInt32:
		return "-infinity"
	}
	t := time.Unix(pgEpochUnix, 0).UTC().AddDate(0, 0, int(days))
	return t.Format("2006-01-02")
}

// formatPGTime renders an int64 time-of-day (microseconds since midnight) as
// HH:MM:SS[.ffffff].
func formatPGTime(micros int64) string {
	secs, usec := micros/1_000_000, micros%1_000_000
	return time.Unix(secs, usec*1000).UTC().Format("15:04:05.999999")
}

// decodeVarlena reads a varlena (typlen -1) attribute at offset off. It honours
// att_align_pointer: a value with a 1-byte short header (any non-zero leading
// byte) is stored without padding; only a 4-byte-header value that begins on a
// pad byte (0x00) is aligned first. TOAST pointers and compressed-inline values
// stop the walk (we can't read their value here).
func decodeVarlena(b []byte, off int, c pg.IndexKeyColumn) (string, int, bool) {
	if off < len(b) && b[off] == 0x00 {
		off = alignOffset(off, c.TypAlign)
	}
	if off >= len(b) {
		return "", 0, false
	}
	h := b[off]
	switch {
	case h == 0x01:
		// VARATT_IS_1B_E: out-of-line TOAST pointer — value isn't inline.
		return "", 0, false
	case h&0x01 == 0x01:
		// VARATT_IS_1B: 1-byte short header; total length incl header is h>>1.
		total := int(h >> 1)
		if total < 1 || off+total > len(b) {
			return "", 0, false
		}
		return formatVarlenaPayload(b[off+1:off+total], c.TypCategory), off + total, true
	case h&0x03 == 0x00:
		// VARATT_IS_4B_U: 4-byte uncompressed header; total length incl header
		// is the low 30 bits of the LE uint32 >> 2.
		if off+4 > len(b) {
			return "", 0, false
		}
		total := int((binary.LittleEndian.Uint32(b[off:]) >> 2) & 0x3FFFFFFF)
		if total < 4 || off+total > len(b) {
			return "", 0, false
		}
		return formatVarlenaPayload(b[off+4:off+total], c.TypCategory), off + total, true
	default:
		// VARATT_IS_4B_C: compressed inline — would need pglz/lz4 to read.
		return "", 0, false
	}
}

// formatVarlenaPayload renders a varlena's data bytes: string types
// (typcategory 'S') as text when valid and free of control bytes, everything
// else (bytea, geometry, …) as \x-hex.
func formatVarlenaPayload(payload []byte, typCategory string) string {
	if typCategory == "S" && utf8.Valid(payload) && !hasControlBytes(payload) {
		return string(payload)
	}
	return hexString(payload)
}

// formatBytesText renders raw bytes (a non-int/float fixed type, e.g. name or
// uuid): trailing NULs trimmed, shown as text when printable ASCII, else \x-hex.
func formatBytesText(v []byte) string {
	for len(v) > 0 && v[len(v)-1] == 0x00 {
		v = v[:len(v)-1]
	}
	if len(v) == 0 {
		return ""
	}
	if isPrintableASCII(v) {
		return string(v)
	}
	return hexString(v)
}

// parseHexBytes turns pageinspect's space-separated hex ("17 00 1d") into bytes.
// A field carrying pageinspect's "…" truncation marker — or any malformed field
// — stops parsing and reports truncated so the caller can append an ellipsis.
func parseHexBytes(s string) (b []byte, truncated bool) {
	for f := range strings.FieldsSeq(s) {
		if len(f) != 2 {
			truncated = true
			break
		}
		v, err := strconv.ParseUint(f, 16, 8)
		if err != nil {
			truncated = true
			break
		}
		b = append(b, byte(v))
	}
	return b, truncated
}

// isUnsignedType reports whether a fixed-width numeric type is unsigned (the
// oid family). Everything else fixed-width renders signed.
func isUnsignedType(typname string) bool {
	switch typname {
	case "oid", "xid", "xid8", "cid",
		"regproc", "regprocedure", "regoper", "regoperator",
		"regclass", "regtype", "regrole", "regnamespace",
		"regconfig", "regdictionary", "regcollation":
		return true
	}
	return false
}

func isPrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return len(b) > 0
}

// hasControlBytes flags ASCII control characters (which would corrupt the TUI).
// Bytes >= 0x80 are left to utf8.Valid — multibyte runes are fine.
func hasControlBytes(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

func hexString(b []byte) string {
	return "\\x" + hex.EncodeToString(b)
}

// leadingKeyValue decodes just the index's first key column from a tuple's raw
// bytes — the value the seek feature compares against. It uses the raw bytes (not
// the heap-projected Decoded) so leaf and internal pages yield the same leading
// column consistently. Reports false for an absent/empty key (the minus-infinity
// downlink) or when no column type is known.
func leadingKeyValue(t pg.IndexTuple, cols []pg.IndexKeyColumn) (string, bool) {
	if t.Data == nil {
		return "", false
	}
	parts, _, ok := decodeIndexKeyParts(*t.Data, cols)
	if !ok || len(parts) == 0 {
		return "", false
	}
	return parts[0], true
}

// keyValueLess orders two decoded key values the way the seek scan needs: as
// numbers when both parse (so "100" > "99"), otherwise byte-wise as text. This
// matches the decoder's output — ints/oids compare numerically, while text and
// ISO-formatted dates/timestamps compare lexicographically (ISO sorts correctly).
// Text uses C/byte order, which can differ from a non-C index collation, so a
// seek on a collated text key may land a few rows off.
func keyValueLess(a, b string) bool {
	if ai, aerr := strconv.ParseInt(a, 10, 64); aerr == nil {
		if bi, berr := strconv.ParseInt(b, 10, 64); berr == nil {
			return ai < bi
		}
	}
	if af, aerr := strconv.ParseFloat(a, 64); aerr == nil {
		if bf, berr := strconv.ParseFloat(b, 64); berr == nil {
			return af < bf
		}
	}
	return a < b
}
