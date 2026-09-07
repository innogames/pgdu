package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// walDetailKeyColW is the key column of the block-detail key/value dump.
const walDetailKeyColW = 14

// --- WAL block payload (levelWALBlockDetail) ---

func (m *Model) renderWALBlockDetail(s *screen, height int) string {
	valW := max(m.width-(colCursor+walDetailKeyColW+colGutter+colGutter), 16)
	header := styleMuted.Render("  " + padRight("field", walDetailKeyColW) + "  " + "value")
	return m.renderRowList(s, height, header,
		func(it item, selected bool) string {
			r, _ := it.data.(walDetailRow)
			if r.section {
				return selectedCursor(selected) + styleHeader.Render(" "+r.key+" ")
			}
			key := padRight(highlightName(r.key, selected), walDetailKeyColW)
			value := r.value
			if !r.styled {
				value = clipCells(value, valW)
			} else if lipgloss.Width(value) > valW {
				value = clipCells(value, valW)
			}
			return selectedCursor(selected) + key + "  " + value
		})
}

func (m *Model) renderWALBlockDetailInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "WAL block payload reference")

	b.WriteString("  " + styleHeader.Render(" this view ") + "  " +
		mu("what one WAL record physically wrote for one page — the bytes behind a block reference") + "\n")
	b.WriteString("    " + mu("Source: pg_get_wal_block_info(…, show_data => true) for the single record + block_id;") + "\n")
	b.WriteString("    " + mu("page images decoded with pageinspect (page_header / heap_page_items over the image bytes).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" what WAL contains ") + "  " +
		mu("physical changes, never SQL: no query text, no user, no column names") + "\n")
	b.WriteString("    " + padRight("change data", 14) + mu("the per-block delta — for Heap INSERT/UPDATE the new tuple's bytes (header + attributes),") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 14) + mu("for DELETE/LOCK just offsets and flags, for index records the new index entry") + "\n")
	b.WriteString("    " + padRight("page image", 14) + mu("a full 8 KiB copy of the page taken on its first change after a checkpoint (FPI);") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 14) + mu("when present it *replaces* the change data (0 B) — the tuple is inside the image instead") + "\n")
	b.WriteString("    " + padRight("main data", 14) + mu("record-level fields (xmax, flags, tuple counts) — not per block, so not shown here") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" decoded tuple ") + "  " +
		mu("column values reconstructed from raw bytes using the table's current pg_attribute layout") + "\n")
	b.WriteString("    " + mu("fixed-width and inline varlena types decode; toasted values show their chunk_id; unknown types stop the walk (…).") + "\n")
	b.WriteString("    " + mu("If the table was ALTERed since the record was written the decode can be wrong — the WAL has no schema.") + "\n")
	b.WriteString("    " + mu("UPDATEs with prefix/suffix reuse (default wal_compression of updates) omit bytes shared with the old row.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" line pointers ") + "  " +
		mu("every slot of the page image: state, length, xmin/xmax and the decoded row") + "\n")
	b.WriteString("    " + styleSelected.Render("◀") + " " + mu("marks the offset this record's description names (the tuple it inserted/updated/deleted)") + "\n")
	b.WriteString("    " + mu("the image is the page *after* the change, so the new tuple is already there.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" keys ") + "  " +
		styleBadge.Render("/") + mu(" filter rows · ") +
		styleBadge.Render("e") + mu(" export the dump as CSV · ") +
		styleBadge.Render("esc") + mu(" back to the block list") + "\n")
	return padInfo(&b, height)
}

// stripANSI removes lipgloss colour codes — for exporting styled cells as text.
func stripANSI(s string) string { return ansi.Strip(s) }
