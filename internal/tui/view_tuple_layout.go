package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
)

// tupleSegStyle returns the bar/swatch colour for one segment. colIdx counts
// pageinspect.SegColumn segments only, so bar runs and legend swatches cycle the palette
// in lockstep no matter how many pads sit between them.
func tupleSegStyle(seg pageinspect.Seg, colIdx int) lipgloss.Style {
	switch seg.Kind {
	case pageinspect.SegHeaderField, pageinspect.SegNullBitmap:
		return styleHeapToastTag
	case pageinspect.SegHeaderPad, pageinspect.SegPad:
		return styleMuted
	case pageinspect.SegUnaccounted:
		return styleBloat
	default:
		return bufferSliceStyle(colIdx)
	}
}

// renderTupleLayoutInfo is the ? reference for the byte-layout overlay: what
// each fixed-header field means, how the null bitmap and alignment padding
// work, and how to read the varlena / TOAST classifications.
func (m *Model) renderTupleLayoutInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "Tuple layout reference")

	b.WriteString("  " + styleHeader.Render(" header (23 B) ") + "  " +
		mu("HeapTupleHeaderData — the fixed MVCC bookkeeping before any column data") + "\n")
	b.WriteString("    " + padRight("t_xmin", 14) + mu("xid of the inserting transaction — the row exists for snapshots after it commits") + "\n")
	b.WriteString("    " + padRight("t_xmax", 14) + mu("xid of the deleting/locking transaction; 0 = never deleted or locked") + "\n")
	b.WriteString("    " + padRight("t_field3", 14) + mu("command id within the inserting/deleting xact — or xvac for pre-9.0 VACUUM FULL moves") + "\n")
	b.WriteString("    " + padRight("t_ctid", 14) + mu("points at itself while current; an UPDATE stamps the successor version's (block,offset)") + "\n")
	b.WriteString("    " + padRight("t_infomask2", 14) + mu("low 11 bits: how many attrs this tuple physically stores · high bits: HOT flags") + "\n")
	b.WriteString("    " + padRight("t_infomask", 14) + mu("hint bits: xmin/xmax committed/aborted (frozen = both xmin bits), has-nulls, has-external, …") + "\n")
	b.WriteString("    " + padRight("t_hoff", 14) + mu("where column data starts — header + null bitmap, MAXALIGNed") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" null bitmap ") + "\n")
	b.WriteString("    " + mu("present only when at least one column is NULL (has-nulls flag): one bit per stored") + "\n")
	b.WriteString("    " + mu("attribute, rounded up to whole bytes — 1 = value present, 0 = NULL (no bytes at all") + "\n")
	b.WriteString("    " + mu("in the data area; that's why NULL columns show 0 B). 8 columns cost 1 byte.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" padding ") + "\n")
	b.WriteString("    " + mu("each type demands its alignment (int2 2 B · int4 4 B · int8/timestamp 8 B); bytes") + "\n")
	b.WriteString("    " + mu("are wasted before a stricter column follows a looser one — reordering columns") + "\n")
	b.WriteString("    " + mu("widest-first can shrink every row. short-header varlenas skip alignment entirely.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" variable-width (varlena) ") + "\n")
	b.WriteString("    " + padRight("1B-hdr", 14) + mu("values up to 126 B: 1 header byte + payload, packed unaligned") + "\n")
	b.WriteString("    " + padRight("4B-hdr", 14) + mu("longer inline values: 4 header bytes, aligned like the type demands") + "\n")
	b.WriteString("    " + padRight("compressed", 14) + mu("inline but pglz/lz4-compressed; the value column shows the uncompressed size") + "\n")
	b.WriteString("    " + padRight("TOAST pointer", 14) + mu("18 B stub — the value lives out-of-line in the TOAST relation under the chunk id shown; enter jumps to its page") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" other classes ") + "\n")
	b.WriteString("    " + padRight("NULL", 14) + mu("0 B — only the bitmap records it") + "\n")
	b.WriteString("    " + padRight("not stored", 14) + mu("column added after this row was written; reads synthesize the default") + "\n")
	b.WriteString("    " + padRight("(dropped)", 14) + mu("dropped column — its bytes stay in old rows until they're rewritten") + "\n")
	b.WriteString("    " + padRight("unaccounted", 14) + mu("bytes the walk couldn't attribute — shown red, never guessed at") + "\n\n")

	b.WriteString("  " + mu("bar and rows share colours; ↑/↓ highlights a segment in both · ←/→ change sort, r reverses · Σ must equal lp_len · d describes the table") + "\n")
	b.WriteString("  " + mu("enter on a column opens its whole value — wrapped instead of cut to one row, json re-indented, with the stored bytes dumped below it") + "\n")

	return padInfo(&b, height)
}

// renderTupleLayout draws the modal byte-layout overlay for the selected heap
// tuple (Enter on the heap-tuples list): a proportional per-column byte bar, a
// legend of one row per segment (offsets, type, physical classification, raw
// bytes), and a Σ reconciliation line against lp_len.
func (m *Model) renderTupleLayout(s *screen, height int) string {
	if m.showInfo {
		return scrollWindow(m.renderTupleLayoutInfo(height), &m.infoOffset, height)
	}
	if m.showTupleValue {
		return scrollWindow(m.renderTupleValue(s), &m.tupleValueOffset, height)
	}
	mu := styleMuted.Render
	var b strings.Builder

	t := s.tupleByLP(s.pages.tupleAttrsLP)

	b.WriteString("\n")
	title := "  " + styleSelected.Render("tuple layout")
	if t != nil {
		title += mu(fmt.Sprintf("  ·  lp #%04d", t.LP))
		if t.Ctid != nil {
			title += mu("  ·  ctid " + *t.Ctid)
		}
		title += mu(fmt.Sprintf("  ·  %d B", t.LPLen))
		if len(s.pages.tupleAttrs) > 0 {
			title += mu(fmt.Sprintf("  ·  stores %d of %d attrs",
				t.Infomask2&pg.HeapNattsMask2, len(s.pages.tupleAttrs)))
		}
	}
	arrow := "↑"
	if m.tupleLayoutSortDesc {
		arrow = "↓"
	}
	title += mu("  ·  sort: "+m.tupleLayoutSort.Label()+arrow) + mu("  ·  ")
	if _, _, ok := m.tupleLayoutToastUnderCursor(s); ok {
		title += styleBadge.Render("↵") + mu(" → toast pages · ")
	} else if m.tupleLayoutValueUnderCursor(s) {
		title += styleBadge.Render("↵") + mu(" → value · ")
	}
	title += styleBadge.Render("d") + mu(" describe · ") +
		styleBadge.Render("esc") + mu(" to dismiss · ") +
		styleBadge.Render("space") + mu(" reload · ") + styleBadge.Render("?") + mu(" help")
	b.WriteString(title + "\n\n")

	switch {
	case s.pages.tupleAttrsLoading:
		b.WriteString("  " + m.spinner.View() + " splitting tuple…\n")
		return padInfo(&b, height)
	case s.pages.tupleAttrsErr != nil:
		b.WriteString(styleErr.Render("  error: "+s.pages.tupleAttrsErr.Error()) + "\n")
		return padInfo(&b, height)
	case t == nil || len(s.pages.tupleAttrs) == 0:
		b.WriteString(mu("  tuple gone — the page changed since it was loaded; space to retry") + "\n")
		return padInfo(&b, height)
	}

	segs, trusted := pageinspect.Layout(*t, s.pages.tupleAttrs)
	order := pageinspect.SortedIdx(segs, m.tupleLayoutSort, m.tupleLayoutSortDesc)
	if m.tupleLayoutCursor >= len(order) {
		m.tupleLayoutCursor = len(order) - 1
	}
	// The cursor indexes the sorted legend; cursorOrig is the same segment's
	// physical index, which the bar (always physical order) highlights.
	cursorOrig := -1
	if m.tupleLayoutCursor >= 0 && m.tupleLayoutCursor < len(order) {
		cursorOrig = order[m.tupleLayoutCursor]
	}

	// Per-segment styles, assigned in physical order so bar runs and legend
	// swatches stay in lockstep no matter how the legend is sorted.
	styles := make([]lipgloss.Style, len(segs))
	colIdx := 0
	for i, sg := range segs {
		styles[i] = tupleSegStyle(sg, colIdx)
		if sg.Kind == pageinspect.SegColumn {
			colIdx++
		}
	}

	// Bar: cursor's segment renders reversed so the legend row and its byte
	// run stay visually linked.
	barW := min(max(m.width-6, barWidthMin), barWidthMax)
	byteCounts := make([]int, len(segs))
	for i, sg := range segs {
		byteCounts[i] = sg.Bytes
	}
	cells := proportionalCells(byteCounts, barW)
	barSegs := make([]barSegment, 0, len(segs))
	for i := range segs {
		st := styles[i]
		if i == cursorOrig {
			st = st.Reverse(true)
		}
		barSegs = append(barSegs, barSegment{cells: cells[i], style: st})
	}
	b.WriteString("  " + paintBar(barW, barSegs...) + "\n\n")

	// Legend column widths from the data, so short tables stay tight.
	nameW, typeW, classW := len("column"), len("type"), 0
	for _, sg := range segs {
		nameW = max(nameW, displayWidth(sg.Name()))
		classW = max(classW, len(sg.Class))
		if sg.Kind == pageinspect.SegColumn {
			typeW = max(typeW, len(sg.Attr.TypeName))
		}
	}
	nameW, typeW = min(nameW, 28), min(typeW, 24)

	atW := len("8160–8191")
	header := "      " +
		padRight(sortMark("bytes", m.tupleLayoutSort == pageinspect.SortBytes, m.tupleLayoutSortDesc), 7) +
		padRight(sortMark("offset", m.tupleLayoutSort == pageinspect.SortOffset, m.tupleLayoutSortDesc), atW+2) +
		strings.Repeat(" ", colMark) + // drillMark slot ("↵ ")
		padRight(sortMark("column", m.tupleLayoutSort == pageinspect.SortColumn, m.tupleLayoutSortDesc), nameW+2) +
		padRight("type", typeW+2) + padRight("class", classW+2) + "value"
	b.WriteString(mu(header) + "\n")

	// Scroll window over the legend rows; the cursor is kept visible.
	footer := 1
	if !trusted {
		footer++
	}
	avail := max(1, height-strings.Count(b.String(), "\n")-footer)
	offset, end := viewportRange(m.tupleLayoutCursor, m.tupleLayoutOffset, avail, len(order))
	m.tupleLayoutOffset = offset

	for rank := offset; rank < end; rank++ {
		i := order[rank]
		sg := segs[i]

		at := "—"
		if sg.Bytes > 0 {
			at = fmt.Sprintf("%d–%d", sg.Start, sg.Start+sg.Bytes-1)
		}
		name := truncateToWidth(sg.Name(), nameW)
		typ := ""
		if sg.Kind == pageinspect.SegColumn {
			typ = truncateToWidth(sg.Attr.TypeName, typeW)
		}
		// The value column gets every remaining cell: the decoded value when
		// the byte decoder managed one, otherwise a hex preview of the raw
		// bytes ("\x" + 2 hex chars per byte + a possible ellipsis).
		room := m.width - (6 + 7 + atW + 2 + colMark + nameW + 2 + typeW + 2 + classW + 2)
		val := sg.Value
		if val == "" && sg.Kind == pageinspect.SegColumn && len(sg.Attr.Value) > 0 {
			val = previewBytes(sg.Attr.Value, max(4, (room-3)/2))
		}
		val = truncateToWidth(val, max(8, room))

		cursor := selectedCursor(rank == m.tupleLayoutCursor)
		nameCell := padRight(name, nameW)
		switch {
		case rank == m.tupleLayoutCursor:
			nameCell = styleSelected.Render(nameCell)
		case sg.Kind == pageinspect.SegColumn:
			nameCell = styleColName.Render(nameCell)
		default:
			nameCell = mu(nameCell)
		}

		b.WriteString(cursor + styles[i].Render("▇") + "  " +
			fmt.Sprintf("%4d B", sg.Bytes) + "  " + padRight(at, atW) + "  " +
			drillMark(tupleSegDrills(sg)) + nameCell + "  " + padRight(typ, typeW) + "  " +
			mu(padRight(sg.Class, classW)) + "  " + val + "\n")
	}

	// Σ reconciliation: the walk must re-derive lp_len exactly; anything else
	// is surfaced, never smoothed over.
	var hdr, bitmap, pads, data, unacc int
	for _, sg := range segs {
		switch sg.Kind {
		case pageinspect.SegHeaderField:
			hdr += sg.Bytes
		case pageinspect.SegNullBitmap:
			bitmap += sg.Bytes
		case pageinspect.SegHeaderPad, pageinspect.SegPad:
			pads += sg.Bytes
		case pageinspect.SegColumn:
			data += sg.Bytes
		case pageinspect.SegUnaccounted:
			unacc += sg.Bytes
		}
	}
	parts := []string{fmt.Sprintf("%d B header", hdr)}
	if bitmap > 0 {
		parts = append(parts, fmt.Sprintf("%d B null-map", bitmap))
	}
	if pads > 0 {
		parts = append(parts, fmt.Sprintf("%d B pad", pads))
	}
	parts = append(parts, fmt.Sprintf("%d B data", data))
	sum := hdr + bitmap + pads + data + unacc
	line := "  " + styleTotal.Render(fmt.Sprintf("Σ %s = %d B", strings.Join(parts, " + "), sum)) +
		mu(fmt.Sprintf("  ·  lp_len %d B", t.LPLen))
	if unacc == 0 && trusted {
		line += " " + styleBadge.Render("✓")
	} else if unacc > 0 {
		line += styleBloat.Render(fmt.Sprintf("  ·  unaccounted %d B ✗", unacc))
	}
	b.WriteString(line + "\n")
	if !trusted {
		b.WriteString(styleBloat.Render("  ⚠ stored bytes don't match the column metadata — per-column layout suppressed") + "\n")
	}

	return padInfo(&b, height)
}

// renderTupleValue is the value pane nested in the byte-layout overlay (Enter
// on a column segment): the column's whole decoded value, wrapped to the
// terminal instead of cut to the one legend row, followed by a hex dump of the
// bytes it occupies on the page. Unscrolled — renderTupleLayout runs it through
// scrollWindow.
func (m *Model) renderTupleValue(s *screen) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")

	// The legend cursor can't move while the pane is up, so the segment only
	// changes under it when a stale reload lands — say so instead of showing
	// another column's bytes.
	sg, ok := m.tupleLayoutSegUnderCursor(s)
	if !ok || sg.Kind != pageinspect.SegColumn || sg.Attr == nil {
		b.WriteString("  " + mu("value gone — the tuple was reloaded; esc goes back") + "\n")
		return b.String()
	}

	title := "  " + styleSelected.Render("value") + mu("  ·  ") + styleColName.Render(sg.Name()) +
		mu("  ·  "+sg.Attr.TypeName+"  ·  "+sg.Class+fmt.Sprintf("  ·  %d B", sg.Bytes))
	if sg.Bytes > 0 {
		title += mu(fmt.Sprintf("  ·  bytes %d–%d", sg.Start, sg.Start+sg.Bytes-1))
	}
	title += mu("  ·  ") + styleBadge.Render("esc") + mu(" back · ") +
		styleBadge.Render("↑/↓") + mu(" scroll · ") + styleBadge.Render("?") + mu(" help")
	b.WriteString(title + "\n\n")

	// A value that only decoded to hex says nothing the dump below doesn't say
	// better (offsets, ascii), so it's shown once, not twice.
	if val := reindentJSON(sg.Value); val != "" && !strings.HasPrefix(val, `\x`) {
		b.WriteString("  " + styleHeader.Render(" decoded ") + "\n")
		for _, ln := range wrapPlain(val, max(m.width-4, 20)) {
			b.WriteString("  " + ln + "\n")
		}
		b.WriteString("\n")
	}
	raw := sg.Attr.Value
	b.WriteString("  " + styleHeader.Render(" stored bytes ") + "  " +
		mu(fmt.Sprintf("%d B at tuple offset %d", len(raw), sg.Start)) + "\n")
	for off := 0; off < len(raw); off += hexDumpWidth {
		at, dump := hexDumpRow(raw[off:min(off+hexDumpWidth, len(raw))], off)
		b.WriteString("  " + mu(at) + "  " + dump + "\n")
	}
	return b.String()
}

// reindentJSON re-indents a decoded value that happens to be a JSON document
// (jsonb's binary tree comes back as one canonical line) so a nested document
// reads as a tree. Best effort and shape-driven, not type-driven: anything
// json.Indent rejects — a hex fallback, a plain string, a TOAST annotation —
// comes back untouched.
func reindentJSON(v string) string {
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(v), "", "  "); err != nil {
		return v
	}
	return out.String()
}
