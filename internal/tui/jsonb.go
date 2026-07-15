package tui

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PostgreSQL jsonb is stored on disk as a binary JsonbContainer tree (jsonb v1,
// stable since 9.4), not as text — which is why the byte-layout view showed a
// jsonb attribute as raw hex. decodeJsonb reconstructs the canonical jsonb text
// the same way jsonb's output function would: keys emitted in stored order, a
// ": " after each key and ", " between elements, numbers via the on-disk
// numeric format. Reference: src/include/utils/jsonb.h and jsonb_util.c.
//
// This only handles the fully-inline payload; compressed-inline and out-of-line
// (TOASTed) jsonb never reach here (the caller renders those as annotations).
// Any bounds or format violation returns ok=false so the caller falls back to
// hex rather than emitting a half-decoded string.

const (
	// JsonbContainer header (uint32): low 28 bits are the element/pair count,
	// the top bits are shape flags.
	jbCountMask = 0x0FFFFFFF
	jbFScalar   = 0x10000000
	jbFObject   = 0x20000000
	jbFArray    = 0x40000000

	// JEntry (uint32): low 28 bits are a length or (when JENTRY_HAS_OFF is set)
	// an end offset; the middle bits carry the value's type.
	jEntryOffLenMask = 0x0FFFFFFF
	jEntryTypeMask   = 0x70000000
	jEntryHasOff     = 0x80000000

	jEntryString    = 0x00000000
	jEntryNumeric   = 0x10000000
	jEntryBoolFalse = 0x20000000
	jEntryBoolTrue  = 0x30000000
	jEntryNull      = 0x40000000
	jEntryContainer = 0x50000000

	jbMaxDepth = 128 // guard against a malformed/cyclic-looking container header
)

func decodeJsonb(payload []byte) (string, bool) {
	var sb strings.Builder
	if !jbWriteContainer(&sb, payload, 0) {
		return "", false
	}
	return sb.String(), true
}

// jbWriteContainer renders one JsonbContainer (header + JEntry array + data) as
// jsonb text. c must start at the container header; for nested containers the
// caller has already stripped the INTALIGN padding.
func jbWriteContainer(sb *strings.Builder, c []byte, depth int) bool {
	if depth > jbMaxDepth || len(c) < 4 {
		return false
	}
	header := binary.LittleEndian.Uint32(c)
	count := int(header & jbCountMask)
	isObject := header&jbFObject != 0
	isScalar := header&jbFScalar != 0

	// Objects store 2×count JEntries (all keys, then all values); arrays and
	// scalars store one per element.
	nEntries := count
	if isObject {
		nEntries = count * 2
	}
	const entriesAt = 4
	dataStart := entriesAt + nEntries*4
	if nEntries < 0 || dataStart < entriesAt || dataStart > len(c) {
		return false
	}

	entry := func(i int) uint32 { return binary.LittleEndian.Uint32(c[entriesAt+i*4:]) }

	// offsetAt / lengthAt mirror getJsonbOffset / getJsonbLength: most JEntries
	// hold a length, but every JB_OFFSET_STRIDE-th one holds an end offset to
	// cap the walk, so a child's start is found by summing lengths back to the
	// nearest offset-bearing entry.
	offsetAt := func(i int) int {
		off := 0
		for k := i - 1; k >= 0; k-- {
			off += int(entry(k) & jEntryOffLenMask)
			if entry(k)&jEntryHasOff != 0 {
				break
			}
		}
		return off
	}
	lengthAt := func(i int) int {
		e := entry(i)
		if e&jEntryHasOff != 0 {
			return int(e&jEntryOffLenMask) - offsetAt(i)
		}
		return int(e & jEntryOffLenMask)
	}
	// childBytes returns entry i's raw data slice and the INTALIGN pad that
	// precedes numeric/container values (the JEntry length includes that pad).
	childBytes := func(i int) (data []byte, pad int, ok bool) {
		off := offsetAt(i)
		length := lengthAt(i)
		start := dataStart + off
		end := start + length
		if off < 0 || length < 0 || start < dataStart || end > len(c) {
			return nil, 0, false
		}
		return c[start:end], (-off) & 3, true
	}

	writeVal := func(i int) bool {
		switch entry(i) & jEntryTypeMask {
		case jEntryNull:
			sb.WriteString("null")
		case jEntryBoolTrue:
			sb.WriteString("true")
		case jEntryBoolFalse:
			sb.WriteString("false")
		case jEntryString:
			data, _, ok := childBytes(i)
			if !ok {
				return false
			}
			jbWriteJSONString(sb, data)
		case jEntryNumeric:
			data, pad, ok := childBytes(i)
			if !ok || pad > len(data) {
				return false
			}
			s, ok := decodeOnDiskNumeric(data[pad:])
			if !ok {
				return false
			}
			sb.WriteString(s)
		case jEntryContainer:
			data, pad, ok := childBytes(i)
			if !ok || pad > len(data) {
				return false
			}
			if !jbWriteContainer(sb, data[pad:], depth+1) {
				return false
			}
		default:
			return false
		}
		return true
	}

	switch {
	case isScalar:
		// A scalar jsonb is stored as a one-element pseudo-array; emit the value
		// bare, no brackets.
		return count == 1 && writeVal(0)
	case isObject:
		sb.WriteByte('{')
		for i := 0; i < count; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			key, _, ok := childBytes(i)
			if !ok {
				return false
			}
			jbWriteJSONString(sb, key)
			sb.WriteString(": ")
			if !writeVal(count + i) {
				return false
			}
		}
		sb.WriteByte('}')
	default: // array
		sb.WriteByte('[')
		for i := 0; i < count; i++ {
			if i > 0 {
				sb.WriteString(", ")
			}
			if !writeVal(i) {
				return false
			}
		}
		sb.WriteByte(']')
	}
	return true
}

// jbWriteJSONString writes a JSON string literal (matching PostgreSQL's
// escape_json): quote, control bytes as \b\f\n\r\t or \u00XX, backslash and
// double-quote escaped, everything else (including multibyte UTF-8) passed
// through. Invalid UTF-8 bytes fall back to \u00XX so the TUI can't be corrupted.
func jbWriteJSONString(sb *strings.Builder, s []byte) {
	sb.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			sb.WriteString(`\"`)
			i++
		case c == '\\':
			sb.WriteString(`\\`)
			i++
		case c == '\b':
			sb.WriteString(`\b`)
			i++
		case c == '\f':
			sb.WriteString(`\f`)
			i++
		case c == '\n':
			sb.WriteString(`\n`)
			i++
		case c == '\r':
			sb.WriteString(`\r`)
			i++
		case c == '\t':
			sb.WriteString(`\t`)
			i++
		case c < 0x20:
			fmt.Fprintf(sb, `\u%04x`, c)
			i++
		case c < 0x80:
			sb.WriteByte(c)
			i++
		default:
			if r, size := utf8.DecodeRune(s[i:]); r != utf8.RuneError || size > 1 {
				sb.Write(s[i : i+size])
				i += size
			} else {
				fmt.Fprintf(sb, `\u%04x`, c)
				i++
			}
		}
	}
	sb.WriteByte('"')
}

// decodeOnDiskNumeric renders an on-disk PostgreSQL numeric (as embedded in
// jsonb) to its textual form. b starts with the Numeric's own varlena header,
// which is stripped first; what follows is n_header (uint16) plus, for the long
// form, n_weight (int16), then base-10000 digits (NumericDigit int16). See
// src/backend/utils/adt/numeric.c.
func decodeOnDiskNumeric(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	// Strip the numeric's varlena header. In practice it is always the 4-byte
	// uncompressed form, but tolerate a 1-byte short header defensively.
	var p []byte
	switch b[0] & 0x01 {
	case 0x01:
		total := int(b[0] >> 1)
		if total < 1 || total > len(b) {
			return "", false
		}
		p = b[1:total]
	default:
		if len(b) < 4 {
			return "", false
		}
		total := int((binary.LittleEndian.Uint32(b) >> 2) & 0x3FFFFFFF)
		if total < 4 || total > len(b) {
			return "", false
		}
		p = b[4:total]
	}
	if len(p) < 2 {
		return "", false
	}

	h := binary.LittleEndian.Uint16(p)
	const (
		signMask = 0xC000
		neg      = 0x4000
		short    = 0x8000
		special  = 0xC000
	)

	if h&signMask == special {
		switch h & 0xF000 {
		case 0xC000:
			return "NaN", true
		case 0xD000:
			return "Infinity", true
		case 0xF000:
			return "-Infinity", true
		}
		return "", false
	}

	var (
		negative    bool
		dscale      int
		weight      int
		digitsStart int
	)
	if h&signMask == short {
		negative = h&0x2000 != 0
		dscale = int(h&0x1F80) >> 7
		w := int(h & 0x007F) // 7-bit signed weight
		if w >= 0x40 {
			w -= 0x80
		}
		weight = w
		digitsStart = 2
	} else {
		negative = h&signMask == neg
		dscale = int(h & 0x3FFF)
		if len(p) < 4 {
			return "", false
		}
		weight = int(int16(binary.LittleEndian.Uint16(p[2:])))
		digitsStart = 4
	}

	// Bound the rendered size: weight/dscale come from possibly-corrupt page
	// bytes, and each drives a digit loop. Real jsonb numerics stay tiny.
	if weight > 1<<14 || dscale > 1<<14 {
		return "", false
	}

	nd := (len(p) - digitsStart) / 2
	digit := func(i int) int {
		if i < 0 || i >= nd {
			return 0
		}
		return int(binary.LittleEndian.Uint16(p[digitsStart+i*2:]))
	}

	var out strings.Builder
	// Integer part: base-10000 groups 0..weight (weight < 0 means |value| < 1).
	if weight < 0 {
		out.WriteByte('0')
	} else {
		for i := 0; i <= weight; i++ {
			if i == 0 {
				out.WriteString(strconv.Itoa(digit(i)))
			} else {
				fmt.Fprintf(&out, "%04d", digit(i))
			}
		}
	}
	// Fractional part: exactly dscale decimal digits, drawn from the groups
	// after the point (starting at group weight+1) padded/trimmed to dscale.
	if dscale > 0 {
		out.WriteByte('.')
		frac := make([]byte, 0, dscale+4)
		for i := weight + 1; len(frac) < dscale; i++ {
			frac = append(frac, []byte(fmt.Sprintf("%04d", digit(i)))...)
		}
		out.Write(frac[:dscale])
	}

	s := out.String()
	if negative && s != "0" {
		s = "-" + s
	}
	return s, true
}
