package pageinspect

// lz4BlockDecompress inflates one LZ4 *block* (no frame header — Postgres
// calls LZ4_decompress_safe on the bare block, lz4.h) whose uncompressed size
// the caller knows from the varlena compression header. A block is a run of
// sequences: a token byte whose high nibble is the literal count and low
// nibble the match length (each nibble saturating at 15 and continuing in
// following bytes, 255 meaning "keep adding"), the literals, then a 16-bit
// little-endian back offset and the match, which is 4 bytes longer than the
// nibble says. The final sequence carries literals only. Matches may overlap
// their own output, hence the byte-wise copy.
//
// Reports false when the block is malformed or does not produce exactly
// rawSize bytes — the caller treats that as "not compressed after all".
func lz4BlockDecompress(src []byte, rawSize int) ([]byte, bool) {
	if rawSize < 0 || rawSize > toastMaxRawSize {
		return nil, false
	}
	dst := make([]byte, 0, rawSize)
	sp := 0
	// extend accumulates a saturated nibble's continuation bytes.
	extend := func(n int) (int, bool) {
		for {
			if sp >= len(src) {
				return 0, false
			}
			b := src[sp]
			sp++
			n += int(b)
			if b != 255 {
				return n, true
			}
		}
	}
	for sp < len(src) {
		token := src[sp]
		sp++
		lit := int(token >> 4)
		if lit == 15 {
			var ok bool
			if lit, ok = extend(lit); !ok {
				return nil, false
			}
		}
		if sp+lit > len(src) || len(dst)+lit > rawSize {
			return nil, false
		}
		dst = append(dst, src[sp:sp+lit]...)
		sp += lit
		if sp >= len(src) {
			break // last sequence: literals only
		}
		if sp+2 > len(src) {
			return nil, false
		}
		off := int(src[sp]) | int(src[sp+1])<<8
		sp += 2
		ml := int(token & 0x0f)
		if ml == 15 {
			var ok bool
			if ml, ok = extend(ml); !ok {
				return nil, false
			}
		}
		ml += 4
		if off == 0 || off > len(dst) || len(dst)+ml > rawSize {
			return nil, false
		}
		for range ml {
			dst = append(dst, dst[len(dst)-off])
		}
	}
	return dst, len(dst) == rawSize
}
