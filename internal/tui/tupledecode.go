package tui

import (
	"encoding/binary"
	"fmt"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// decodeAttrValue renders one stored, non-null attribute's raw bytes as a
// human-readable value for the byte-layout overlay, reusing the index-key
// byte decoder (formatFixed / formatVarlenaPayload — little-endian, same
// caveat as decodeIndexKey). Returns "" when the bytes can't be decoded
// meaningfully; the renderer falls back to a hex preview then.
func decodeAttrValue(a pg.TupleAttr) string {
	switch {
	case a.Len > 0:
		if len(a.Value) != int(a.Len) {
			return ""
		}
		return formatFixed(a.Value, a.TypName)
	case a.Len == -1:
		return decodeInlineVarlena(a.Value, a.TypName, a.TypCategory)
	default:
		return ""
	}
}

// decodeInlineVarlena decodes a varlena value whose bytes (header included)
// were already isolated by heap_page_item_attrs — no alignment or sizing
// against neighbours needed, unlike decodeVarlena's walk. Out-of-line and
// compressed values can't be read here, but their headers still tell a
// useful story (where the value lives, how big it really is), so those
// render as annotations instead of failing to hex.
func decodeInlineVarlena(v []byte, typName, typCategory string) string {
	// json and xml sit in typcategory 'U' although their payload is plain
	// text — promote them so they read as text instead of hex. jsonb stays
	// hex: its payload is the binary tree format, not text.
	if typName == "json" || typName == "xml" {
		typCategory = "S"
	}
	if len(v) == 0 {
		return ""
	}
	h := v[0]
	switch {
	case h == 0x01:
		return describeToastPointer(v)
	case h&0x01 == 0x01:
		// VARATT_IS_1B: total length incl the 1-byte header is h>>1.
		if total := int(h >> 1); total >= 1 && total <= len(v) {
			return varlenaPayloadValue(v[1:total], typCategory)
		}
		return ""
	case h&0x03 == 0x02:
		// VARATT_IS_4B_C: compressed inline. va_tcinfo's low 30 bits carry
		// the uncompressed payload size — worth showing even though the
		// payload needs pglz/lz4 to read.
		if len(v) < 8 {
			return ""
		}
		raw := int64(binary.LittleEndian.Uint32(v[4:8]) & 0x3FFFFFFF)
		return fmt.Sprintf("compressed · %s raw", humanize.Bytes(raw))
	default:
		// VARATT_IS_4B_U: 4-byte uncompressed header.
		if total := int((binary.LittleEndian.Uint32(v) >> 2) & 0x3FFFFFFF); total >= 4 && total <= len(v) {
			return varlenaPayloadValue(v[4:total], typCategory)
		}
		return ""
	}
}

// varlenaPayloadValue wraps formatVarlenaPayload with one overlay-specific
// rule: a genuinely empty payload renders as ” — a "" return means "could
// not decode" to the renderer, which would fall back to hex and make an empty
// string masquerade as raw header bytes.
func varlenaPayloadValue(payload []byte, typCategory string) string {
	if len(payload) == 0 {
		return "''"
	}
	return formatVarlenaPayload(payload, typCategory)
}

// describeToastPointer summarises an on-disk TOAST pointer (varattrib_1b_e,
// postgres.h): 1 B va_header + 1 B va_tag + varatt_external{va_rawsize,
// va_extinfo, va_valueid, va_toastrelid}. va_valueid is the chunk_id the
// TOAST-table drill shows, so surfacing it lets the user chase the value by
// hand; va_rawsize includes the 4-byte varlena header, hence the -4.
func describeToastPointer(v []byte) string {
	if len(v) != toastPointerLen || v[1] != 0x12 { // VARTAG_ONDISK
		return "out-of-line"
	}
	rawSize := int64(binary.LittleEndian.Uint32(v[2:6])) - 4
	extSize := int64(binary.LittleEndian.Uint32(v[6:10]) & 0x3FFFFFFF)
	valueID := binary.LittleEndian.Uint32(v[10:14])
	s := fmt.Sprintf("→ toast chunk %d · %s", valueID, humanize.Bytes(rawSize))
	if extSize < rawSize {
		s += fmt.Sprintf(" (%s compressed)", humanize.Bytes(extSize))
	}
	return s
}
