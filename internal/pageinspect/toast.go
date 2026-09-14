package pageinspect

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// ToastDecoded is a reassembled TOAST value read back into something a person
// can look at. Method names the compression the chunk bytes turned out to
// carry ("pglz", "lz4") once they were inflated, "" otherwise, and RawSize the
// payload size the header declared. Payload is the value's own bytes — after
// inflating, or the stored bytes when nothing was — and Text its rendering;
// Text is "" when no rendering was possible. Unverified flags a prefix whose
// header reads as compressed but could not be checked by inflating: the stored
// bytes are shown, but may well be a compressed stream. Note explains anything
// that kept the decode short of the full value.
type ToastDecoded struct {
	Method     string
	RawSize    int64
	Payload    []byte
	Text       string
	Unverified bool
	Note       string
}

// toastMaxRawSize bounds what a compression header may claim as the inflated
// size: 1 GiB is the varlena limit, anything above is a misread header.
const toastMaxRawSize = 1 << 30

// toastDecodeMaxText caps the rendered value. The pane re-wraps and re-indents
// Text on every render, so it has to stay far below what a 1 GiB value could
// expand to — yet well above the per-row budget of maxBlobDecode, since this
// runs once for one pane, not per visible row.
const toastDecodeMaxText = 256 << 10

// DecodeToastValue turns the assembled chunk bytes of one TOAST value into a
// readable rendering. The TOAST table does not say whether a value was
// compressed before it was chunked, so the leading 4 bytes are tried as a
// va_tcinfo header (2 method bits + 30 bits of raw size) and the value counts
// as compressed only when the named decoder inflates it to exactly that size —
// an uncompressed payload whose first bytes happen to spell a plausible header
// will not also survive that check. An incomplete value (Truncated) cannot be
// inflated: its stored prefix is shown as is, flagged Unverified when the header
// looked like compression.
//
// The payload's type is guessed from the owner's toastable columns: one
// distinct type is trusted, otherwise the self-validating decoders decide —
// jsonb's tree decoder rejects anything that isn't jsonb, text needs valid
// UTF-8, and hex is the floor.
func DecodeToastValue(v pg.ToastValue) ToastDecoded {
	var d ToastDecoded
	var notes []string
	method, rawSize, plausible := toastCompressionHeader(v.Data)
	switch {
	case plausible && v.Truncated:
		// Can't inflate a prefix, can't rule the header out either.
		d.Unverified = true
		notes = append(notes, fmt.Sprintf("header reads as %s (%s raw) but only the first %s of %s were fetched — a prefix can't be inflated; shown as stored",
			method, humanize.Bytes(int64(rawSize)), humanize.Bytes(int64(len(v.Data))), humanize.Bytes(v.StoredBytes)))
	case plausible:
		var out []byte
		var ok bool
		switch method {
		case "pglz":
			out, ok = pglzDecompress(v.Data[4:], rawSize)
		case "lz4":
			out, ok = lz4BlockDecompress(v.Data[4:], rawSize)
		}
		if ok {
			d.Method, d.RawSize, d.Payload = method, int64(rawSize), out
			d.Text, d.Note = toastPayloadText(out, v.OwnerTypes)
			return d
		}
		// The header read as compressed but the stream didn't inflate to size:
		// an uncompressed value that happens to start that way (a compressed
		// one always inflates), or a corrupt stream. Show the stored bytes and
		// say so.
		notes = append(notes, fmt.Sprintf("looks %s-compressed (%s raw) but did not inflate — shown as stored", method, humanize.Bytes(int64(rawSize))))
	case v.Truncated:
		notes = append(notes, fmt.Sprintf("only the first %s of %s were fetched", humanize.Bytes(int64(len(v.Data))), humanize.Bytes(v.StoredBytes)))
	}
	d.Payload = v.Data
	var note string
	d.Text, note = toastPayloadText(v.Data, v.OwnerTypes)
	if note != "" {
		notes = append(notes, note)
	}
	d.Note = strings.Join(notes, " · ")
	return d
}

// toastCompressionHeader reads the leading bytes as a va_tcinfo (postgres.h
// varattrib_4b.va_compressed): the top two bits pick the method
// (TOAST_PGLZ_COMPRESSION_ID = 0, TOAST_LZ4_COMPRESSION_ID = 1), the rest is
// the inflated payload size. Plausibility only — the caller confirms by
// inflating: a declared size no larger than what is stored, or beyond the
// varlena limit, cannot be a real header.
func toastCompressionHeader(data []byte) (method string, rawSize int, ok bool) {
	if len(data) < 4 {
		return "", 0, false
	}
	tcinfo := binary.LittleEndian.Uint32(data)
	rawSize = int(tcinfo & 0x3FFFFFFF)
	switch tcinfo >> 30 {
	case 0:
		method = "pglz"
	case 1:
		method = "lz4"
	default:
		return "", 0, false
	}
	if rawSize <= len(data)-4 || rawSize > toastMaxRawSize {
		return "", 0, false
	}
	return method, rawSize, true
}

// toastPayloadText renders the value's own bytes through the same inline
// varlena path the byte-layout legend uses (jsonb tree → text → hex), typed
// by the owner hint when there is exactly one, and clipped to
// toastDecodeMaxText.
func toastPayloadText(payload []byte, hints []pg.ToastOwnerType) (text, note string) {
	typName, typCategory := "jsonb", "S"
	if len(hints) == 1 {
		typName, typCategory = hints[0].TypName, hints[0].TypCategory
		// Same promotion decodeInlineVarlena applies: json/xml are text in
		// category 'U' clothing.
		if typName == "json" || typName == "xml" {
			typCategory = "S"
		}
	}
	text = inlinePayloadValue(payload, typName, typCategory)
	if len(text) > toastDecodeMaxText {
		cut := toastDecodeMaxText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
		note = "decoded text cut at " + humanize.Bytes(int64(toastDecodeMaxText))
	}
	return text, note
}
