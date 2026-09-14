package pageinspect

// pglzDecompress inflates a pglz stream (src/common/pg_lzcompress.c) whose
// uncompressed size the caller already knows from the varlena compression
// header. The format is a plain LZ77 variant: a control byte announces the
// kind of the next eight items, LSB first — a clear bit is one literal byte, a
// set bit a back-reference of two bytes (low nibble: length-3, high nibble +
// next byte: 12-bit offset) that grows by a third byte when the nibble is
// saturated. Matches may overlap their own output, hence the byte-wise copy.
//
// Reports false when the stream is malformed or does not produce exactly
// rawSize bytes — the caller uses that as the "this wasn't compressed after
// all" signal, so a partial result would only mislead.
func pglzDecompress(src []byte, rawSize int) ([]byte, bool) {
	if rawSize < 0 || rawSize > toastMaxRawSize {
		return nil, false
	}
	dst := make([]byte, 0, rawSize)
	sp := 0
	for sp < len(src) && len(dst) < rawSize {
		ctrl := src[sp]
		sp++
		for range 8 {
			if sp >= len(src) || len(dst) >= rawSize {
				break
			}
			if ctrl&1 == 0 {
				dst = append(dst, src[sp])
				sp++
			} else {
				if sp+1 >= len(src) {
					return nil, false
				}
				length := int(src[sp]&0x0f) + 3
				off := int(src[sp]&0xf0)<<4 | int(src[sp+1])
				sp += 2
				if length == 18 {
					if sp >= len(src) {
						return nil, false
					}
					length += int(src[sp])
					sp++
				}
				if off == 0 || off > len(dst) || len(dst)+length > rawSize {
					return nil, false
				}
				for range length {
					dst = append(dst, dst[len(dst)-off])
				}
			}
			ctrl >>= 1
		}
	}
	return dst, len(dst) == rawSize
}
