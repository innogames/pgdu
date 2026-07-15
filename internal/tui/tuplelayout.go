package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"pgdu/internal/pg"
)

// tupleSegKind classifies one contiguous byte run inside a heap tuple for the
// byte-layout overlay.
type tupleSegKind int

const (
	segHeaderField tupleSegKind = iota // one field of the 23 B fixed tuple header
	segNullBitmap                      // ceil(natts/8) bitmap, present iff HEAP_HASNULL
	segHeaderPad                       // padding between header/bitmap and t_hoff
	segColumn                          // one attribute's stored bytes
	segPad                             // inter-column alignment padding
	segUnaccounted                     // bytes the walk couldn't attribute
)

// tupleSeg is one segment of a tuple's byte layout. start is the byte offset
// within the tuple (0 = start of the tuple header, i.e. lp_off on the page);
// zero-byte segments (NULLs, not-stored attrs) keep their nominal start so the
// legend can still order them. name labels header fields; value carries the
// decoded content (header field values, null-bitmap bits, decoded column
// values — "" when undecodable, the renderer falls back to hex then).
type tupleSeg struct {
	kind  tupleSegKind
	attr  *pg.TupleAttr // segColumn only
	name  string        // segHeaderField only; columns take attr.Name
	start int
	bytes int
	class string
	value string
}

// tlSort is the byte-layout overlay's own sort selector. The legend isn't a
// screen item list, so it can't ride the shared sortMode machinery — this
// mirrors its UX (←/→ cycle, r reverses) over the segment slice instead.
type tlSort int

// Declaration order is the ←/→ cycle. It matches the legend header's
// left-to-right column order (bytes · offset · column) cyclically, rotated so
// the zero value stays offset — the physical default openTupleLayout arms.
const (
	tlSortOffset tlSort = iota // physical order within the tuple (default)
	tlSortColumn
	tlSortBytes
	tlSortCount // sentinel for cycling
)

func (s tlSort) label() string {
	switch s {
	case tlSortBytes:
		return "bytes"
	case tlSortColumn:
		return "column"
	default:
		return "offset"
	}
}

// defaultDesc matches the list levels' convention: sizes biggest-first,
// everything else ascending.
func (s tlSort) defaultDesc() bool { return s == tlSortBytes }

// cmp is the three-way segment comparison for this sort key — on the type
// itself so label/defaultDesc/comparison live together, like sortMode. Three-
// way rather than a less() so descending can invert the key while equal rows
// keep their physical order.
func (s tlSort) cmp(a, b tupleSeg) int {
	switch s {
	case tlSortBytes:
		return a.bytes - b.bytes
	case tlSortColumn:
		return strings.Compare(tupleSegName(a), tupleSegName(b))
	default:
		return a.start - b.start
	}
}

// sortedTupleSegIdx returns the legend's display order as indexes into segs.
// The bar always stays in physical order (it's a byte map), so sorting is a
// projection, not a mutation. Ties keep physical order regardless of
// direction so a reversed sort doesn't scramble equal rows.
func sortedTupleSegIdx(segs []tupleSeg, mode tlSort, desc bool) []int {
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

// heapTupleHeaderLen is SizeofHeapTupleHeader (offsetof t_bits) from
// access/htup_details.h — fixed since PG 8.3.
const heapTupleHeaderLen = 23

// toastPointerLen is the on-disk size of a varatt_external TOAST pointer:
// 1 B va_header + 1 B va_tag + 16 B varatt_external.
const toastPointerLen = 18

// tupleHeaderSegs breaks the fixed 23 B HeapTupleHeaderData down field by
// field (access/htup_details.h), each with its decoded value pulled from the
// heap_page_items row we already hold — no byte parsing needed.
func tupleHeaderSegs(t pg.HeapTuple) []tupleSeg {
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
	return []tupleSeg{
		{kind: segHeaderField, name: "t_xmin", start: 0, bytes: 4, class: "inserting xid", value: xidString(t.Xmin)},
		{kind: segHeaderField, name: "t_xmax", start: 4, bytes: 4, class: "deleting/locking xid", value: xidString(t.Xmax)},
		{kind: segHeaderField, name: "t_field3", start: 8, bytes: 4, class: "cid or xvac", value: field3},
		{kind: segHeaderField, name: "t_ctid", start: 12, bytes: 6, class: "self / next version", value: ctid},
		{kind: segHeaderField, name: "t_infomask2", start: 18, bytes: 2, class: "attr count + flags", value: infomask2Text(t.Infomask2)},
		{kind: segHeaderField, name: "t_infomask", start: 20, bytes: 2, class: "flag bits", value: infomaskText(t.Infomask)},
		{kind: segHeaderField, name: "t_hoff", start: 22, bytes: 1, class: "data starts at", value: hoff},
	}
}

// infomaskText renders t_infomask as hex plus the flag names that matter for
// reading a layout. The two xmin hint bits combine to "frozen" the same way
// HEAP_XMIN_FROZEN does.
func infomaskText(im int32) string {
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

// infomask2Text renders t_infomask2: the stored attribute count in the low
// bits plus the HOT flags.
func infomask2Text(im2 int32) string {
	s := fmt.Sprintf("0x%04x · %d attrs", uint16(im2), im2&pg.HeapNattsMask2)
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

// computeTupleLayout reconstructs the byte layout of one NORMAL heap tuple
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
// as an explicit trailing segUnaccounted instead.
func computeTupleLayout(t pg.HeapTuple, attrs []pg.TupleAttr) (segs []tupleSeg, ok bool) {
	lpLen := int(t.LPLen)
	if t.Hoff == nil {
		return []tupleSeg{{kind: segUnaccounted, start: 0, bytes: lpLen, class: "unaccounted"}}, false
	}
	hoff := int(*t.Hoff)

	natts := int(t.Infomask2 & pg.HeapNattsMask2)
	nulls := 0
	for _, a := range attrs {
		if a.Stored && a.Value == nil {
			nulls++
		}
	}

	header := tupleHeaderSegs(t)
	at := heapTupleHeaderLen
	if t.Infomask&pg.HeapHasNull != 0 {
		bm := (natts + 7) / 8
		bits := ""
		if t.Bits != nil {
			bits = *t.Bits
		}
		header = append(header, tupleSeg{
			kind: segNullBitmap, start: at, bytes: bm,
			class: fmt.Sprintf("%d attrs, %d null", natts, nulls),
			value: bits,
		})
		at += bm
	}
	if pad := hoff - at; pad > 0 {
		header = append(header, tupleSeg{kind: segHeaderPad, start: at, bytes: pad, class: "align to t_hoff"})
	} else if pad < 0 {
		// bitmap ran past t_hoff — metadata is inconsistent, don't guess.
		return append(tupleHeaderSegs(t), tupleSeg{
			kind: segUnaccounted, start: heapTupleHeaderLen, bytes: lpLen - heapTupleHeaderLen, class: "unaccounted",
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
			segs = append(segs, tupleSeg{kind: segColumn, attr: a, start: hoff + off, class: "not stored (added later)"})
			continue
		case a.Value == nil:
			segs = append(segs, tupleSeg{kind: segColumn, attr: a, start: hoff + off, class: "NULL"})
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
			segs = append(segs, tupleSeg{
				kind: segPad, start: hoff + off, bytes: pad,
				class: fmt.Sprintf("align %d", alignOffset(1, a.Align)),
			})
			off += pad
		}
		segs = append(segs, tupleSeg{
			kind: segColumn, attr: a, start: hoff + off, bytes: len(a.Value),
			class: classifyAttr(*a), value: decodeAttrValue(*a),
		})
		off += len(a.Value)
	}

	switch total := hoff + off; {
	case total > lpLen:
		return append(header, tupleSeg{
			kind: segUnaccounted, start: hoff, bytes: lpLen - hoff, class: "unaccounted",
		}), false
	case total < lpLen:
		segs = append(segs, tupleSeg{
			kind: segUnaccounted, start: total, bytes: lpLen - total, class: "unaccounted",
		})
	}
	return segs, true
}
