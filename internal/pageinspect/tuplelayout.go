// Package pageinspect decodes the raw bytes pageinspect hands back — heap tuple
// layouts, index keys, on-disk jsonb and TOAST pointers — into labelled
// segments and display strings. It is pure byte decoding over pg's row types;
// nothing here knows about the terminal.
package pageinspect

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"pgdu/internal/pg"
)

// SegKind classifies one contiguous byte run inside a heap tuple for the
// byte-layout overlay.
type SegKind int

const (
	SegHeaderField SegKind = iota // one field of the 23 B fixed tuple header
	SegNullBitmap                 // ceil(natts/8) bitmap, present iff HEAP_HASNULL
	SegHeaderPad                  // padding between header/bitmap and t_hoff
	SegColumn                     // one attribute's stored bytes
	SegPad                        // inter-column alignment padding
	SegUnaccounted                // bytes the walk couldn't attribute
)

// Seg is one segment of a tuple's byte layout. Start is the byte offset
// within the tuple (0 = start of the tuple header, i.e. lp_off on the page);
// zero-byte segments (NULLs, not-stored attrs) keep their nominal Start so a
// legend can still order them. Field labels header fields; Value carries the
// decoded content (header field values, null-bitmap bits, decoded column
// values — "" when undecodable, the renderer falls back to hex then).
type Seg struct {
	Kind  SegKind
	Attr  *pg.TupleAttr // SegColumn only
	Field string        // SegHeaderField only; columns take Attr.Name
	Start int
	Bytes int
	Class string
	Value string
}

// Name labels a segment for a legend. Structural segments get parenthesized
// names so they read apart from real columns; a dropped column's mangled
// catalog name is replaced wholesale.
func (s Seg) Name() string {
	switch s.Kind {
	case SegHeaderField:
		return s.Field
	case SegNullBitmap:
		return "(null bitmap)"
	case SegHeaderPad, SegPad:
		return "(pad)"
	case SegUnaccounted:
		return "(unaccounted)"
	}
	if s.Attr.Dropped {
		return "(dropped)"
	}
	return s.Attr.Name
}

// SegSort is the byte-layout overlay's sort selector. The legend isn't a
// screen item list, so it can't ride the TUI's shared sortMode machinery —
// this mirrors its UX (←/→ cycle, r reverses) over the segment slice instead.
type SegSort int

// Declaration order is the ←/→ cycle. It matches the legend header's
// left-to-right column order (bytes · offset · column) cyclically, rotated so
// the zero value stays offset — the physical default openTupleLayout arms.
const (
	SortOffset SegSort = iota // physical order within the tuple (default)
	SortColumn
	SortBytes
	SortCount // sentinel for cycling
)

func (s SegSort) Label() string {
	switch s {
	case SortBytes:
		return "bytes"
	case SortColumn:
		return "column"
	default:
		return "offset"
	}
}

// defaultDesc matches the list levels' convention: sizes biggest-first,
// everything else ascending.
func (s SegSort) DefaultDesc() bool { return s == SortBytes }

// cmp is the three-way segment comparison for this sort key — on the type
// itself so label/defaultDesc/comparison live together, like sortMode. Three-
// way rather than a less() so descending can invert the key while equal rows
// keep their physical order.
func (s SegSort) cmp(a, b Seg) int {
	switch s {
	case SortBytes:
		return a.Bytes - b.Bytes
	case SortColumn:
		return strings.Compare(a.Name(), b.Name())
	default:
		return a.Start - b.Start
	}
}

// SortedIdx returns the legend's display order as indexes into segs.
// The bar always stays in physical order (it's a byte map), so sorting is a
// projection, not a mutation. Ties keep physical order regardless of
// direction so a reversed sort doesn't scramble equal rows.
func SortedIdx(segs []Seg, mode SegSort, desc bool) []int {
	order := make([]int, len(segs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		c := mode.cmp(segs[order[i]], segs[order[j]])
		if desc {
			return c > 0
		}
		return c < 0
	})
	return order
}

// HeapTupleHeaderLen is SizeofHeapTupleHeader (offsetof t_bits) from
// access/htup_details.h — fixed since PG 8.3.
const HeapTupleHeaderLen = 23

// toastPointerLen is the on-disk size of a varatt_external TOAST pointer:
// 1 B va_header + 1 B va_tag + 16 B varatt_external.
const toastPointerLen = 18

// headerSegs breaks the fixed 23 B HeapTupleHeaderData down field by
// field (access/htup_details.h), each with its decoded value pulled from the
// heap_page_items row we already hold — no byte parsing needed.
func headerSegs(t pg.HeapTuple) []Seg {
	ctid, field3, hoff := "—", "—", "—"
	if t.Ctid != nil {
		ctid = *t.Ctid
	}
	if t.Field3 != nil {
		field3 = strconv.Itoa(int(*t.Field3))
	}
	if t.Hoff != nil {
		hoff = strconv.Itoa(int(*t.Hoff))
	}
	return []Seg{
		{Kind: SegHeaderField, Field: "t_xmin", Start: 0, Bytes: 4, Class: "inserting xid", Value: XidString(t.Xmin)},
		{Kind: SegHeaderField, Field: "t_xmax", Start: 4, Bytes: 4, Class: "deleting/locking xid", Value: XidString(t.Xmax)},
		{Kind: SegHeaderField, Field: "t_field3", Start: 8, Bytes: 4, Class: "cid or xvac", Value: field3},
		{Kind: SegHeaderField, Field: "t_ctid", Start: 12, Bytes: 6, Class: "self / next version", Value: ctid},
		{Kind: SegHeaderField, Field: "t_infomask2", Start: 18, Bytes: 2, Class: "attr count + flags", Value: Infomask2Text(t.Infomask2)},
		{Kind: SegHeaderField, Field: "t_infomask", Start: 20, Bytes: 2, Class: "flag bits", Value: InfomaskText(t.Infomask)},
		{Kind: SegHeaderField, Field: "t_hoff", Start: 22, Bytes: 1, Class: "data starts at", Value: hoff},
	}
}

// InfomaskText renders t_infomask as hex plus the flag names that matter for
// reading a layout. The two xmin hint bits combine to "frozen" the same way
// HEAP_XMIN_FROZEN does.
func InfomaskText(im int32) string {
	var flags []string
	switch {
	case im&pg.HeapXminCommitted != 0 && im&pg.HeapXminInvalid != 0:
		flags = append(flags, "xmin-frozen")
	case im&pg.HeapXminCommitted != 0:
		flags = append(flags, "xmin-committed")
	case im&pg.HeapXminInvalid != 0:
		flags = append(flags, "xmin-aborted")
	}
	if im&pg.HeapXmaxCommitted != 0 {
		flags = append(flags, "xmax-committed")
	}
	if im&pg.HeapXmaxInvalid != 0 {
		flags = append(flags, "xmax-invalid")
	}
	if im&pg.HeapXmaxIsMulti != 0 {
		flags = append(flags, "multixact")
	}
	if im&pg.HeapUpdated != 0 {
		flags = append(flags, "updated")
	}
	if im&pg.HeapHasNull != 0 {
		flags = append(flags, "has-nulls")
	}
	if im&pg.HeapHasVarWidth != 0 {
		flags = append(flags, "has-varwidth")
	}
	if im&pg.HeapHasExternal != 0 {
		flags = append(flags, "has-external")
	}
	s := fmt.Sprintf("0x%04x", uint16(im))
	if len(flags) > 0 {
		s += " · " + strings.Join(flags, " ")
	}
	return s
}

// Infomask2Text renders t_infomask2: the stored attribute count in the low
// bits plus the HOT flags.
func Infomask2Text(im2 int32) string {
	s := fmt.Sprintf("0x%04x · %d attrs", uint16(im2), im2&pg.HeapNattsMask2)
	if im2&pg.HeapKeysUpdated2 != 0 {
		s += " · keys-updated"
	}
	if im2&pg.HeapHotUpdated2 != 0 {
		s += " · hot-updated"
	}
	if im2&pg.HeapOnlyTuple2 != 0 {
		s += " · heap-only"
	}
	return s
}

// classifyAttr names the physical shape of one stored, non-null attribute's
// bytes. The varlena cases decode the first header byte the same way
// postgres.h's VARATT_IS_* macros do (little-endian layout, the only one
// pageinspect runs on in practice).
func classifyAttr(a pg.TupleAttr) string {
	switch {
	case a.Len > 0:
		return fmt.Sprintf("fixed %d B", a.Len)
	case a.Len == -2:
		return "cstring"
	}
	if len(a.Value) == 0 {
		return "varlena"
	}
	b0 := a.Value[0]
	switch {
	case b0 == 0x01:
		if len(a.Value) == toastPointerLen {
			return "TOAST pointer"
		}
		return "external"
	case b0&0x01 == 0x01:
		return "varlena 1B-hdr"
	case b0&0x03 == 0x02:
		return "varlena (compressed)"
	default:
		return "varlena 4B-hdr"
	}
}

// Layout reconstructs the byte layout of one NORMAL heap tuple
// from its raw bytes plus the per-attribute split and pg_attribute metadata.
// Padding is re-derived with the same rules heap_deform_tuple uses:
// att_align_nominal for fixed-width types, att_align_pointer for varlena —
// the latter skips alignment entirely when the byte at the cursor is non-zero
// (a 1-byte varlena header starts immediately; pad bytes are always zero).
//
// ok is false when the walk overran lp_len or t_hoff is missing — the
// per-column picture can't be trusted, so the segments collapse to header +
// one unaccounted body run and the caller should render a warning. A
// *positive* residue (walk ended short of lp_len) keeps ok=true and surfaces
// as an explicit trailing SegUnaccounted instead.
func Layout(t pg.HeapTuple, attrs []pg.TupleAttr) (segs []Seg, ok bool) {
	lpLen := int(t.LPLen)
	if t.Hoff == nil {
		return []Seg{{Kind: SegUnaccounted, Start: 0, Bytes: lpLen, Class: "unaccounted"}}, false
	}
	hoff := int(*t.Hoff)

	natts := int(t.Infomask2 & pg.HeapNattsMask2)
	// A stored attribute with no bytes is exactly a cleared bit in the bitmap,
	// so the null column names come straight from the split — no need to map
	// bit positions back to columns by hand.
	var nullNames []string
	for _, a := range attrs {
		if a.Stored && a.Value == nil {
			name := a.Name
			if a.Dropped {
				name = "(dropped)"
			}
			nullNames = append(nullNames, name)
		}
	}

	header := headerSegs(t)
	at := HeapTupleHeaderLen
	if t.Infomask&pg.HeapHasNull != 0 {
		bm := (natts + 7) / 8
		bits := ""
		if t.Bits != nil {
			bits = *t.Bits
		}
		// Spell out which columns the cleared bits belong to; the raw bitmap
		// is unlabelled and matching a 0 to a column otherwise means counting
		// attribute positions off the header row.
		if len(nullNames) > 0 {
			bits += "  ·  null: " + strings.Join(nullNames, ", ")
		}
		header = append(header, Seg{
			Kind: SegNullBitmap, Start: at, Bytes: bm,
			Class: fmt.Sprintf("%d attrs, %d null", natts, len(nullNames)),
			Value: bits,
		})
		at += bm
	}
	if pad := hoff - at; pad > 0 {
		header = append(header, Seg{Kind: SegHeaderPad, Start: at, Bytes: pad, Class: "align to t_hoff"})
	} else if pad < 0 {
		// bitmap ran past t_hoff — metadata is inconsistent, don't guess.
		return append(headerSegs(t), Seg{
			Kind: SegUnaccounted, Start: HeapTupleHeaderLen, Bytes: lpLen - HeapTupleHeaderLen, Class: "unaccounted",
		}), false
	}

	segs = header
	off := 0 // cursor within t.Data (tuple offset hoff+off)
	for i := range attrs {
		a := &attrs[i]
		switch {
		case a.Value == nil && a.Dropped:
			// A dropped column that holds no bytes in this tuple is pure
			// noise — hide it. Dropped columns still occupying bytes stay:
			// their bytes are part of the layout.
			continue
		case !a.Stored:
			segs = append(segs, Seg{Kind: SegColumn, Attr: a, Start: hoff + off, Class: "not stored (added later)"})
			continue
		case a.Value == nil:
			segs = append(segs, Seg{Kind: SegColumn, Attr: a, Start: hoff + off, Class: "NULL"})
			continue
		}

		pad := alignOffset(off, a.Align) - off
		// att_align_pointer: a varlena whose next byte is non-zero starts
		// unaligned with a 1-byte header.
		if a.Len == -1 && off < len(t.Data) && t.Data[off] != 0 {
			pad = 0
		}
		if pad > 0 {
			// alignOffset(1, x) rounds 1 up to the boundary, i.e. the
			// boundary itself — reused for the label so the mapping isn't
			// spelled twice.
			segs = append(segs, Seg{
				Kind: SegPad, Start: hoff + off, Bytes: pad,
				Class: fmt.Sprintf("align %d", alignOffset(1, a.Align)),
			})
			off += pad
		}
		segs = append(segs, Seg{
			Kind: SegColumn, Attr: a, Start: hoff + off, Bytes: len(a.Value),
			Class: classifyAttr(*a), Value: DecodeAttrValue(*a),
		})
		off += len(a.Value)
	}

	switch total := hoff + off; {
	case total > lpLen:
		return append(header, Seg{
			Kind: SegUnaccounted, Start: hoff, Bytes: lpLen - hoff, Class: "unaccounted",
		}), false
	case total < lpLen:
		segs = append(segs, Seg{
			Kind: SegUnaccounted, Start: total, Bytes: lpLen - total, Class: "unaccounted",
		})
	}
	return segs, true
}

// XidString renders a nullable xid, "—" when absent.
func XidString(x *uint32) string {
	if x == nil {
		return "—"
	}
	return strconv.FormatUint(uint64(*x), 10)
}
