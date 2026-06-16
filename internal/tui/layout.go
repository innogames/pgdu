package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// barWidth picks the size-bar width for the current screen. We grow it with
// the terminal so wide windows aren't dominated by trailing whitespace, but
// cap it so very wide terminals don't turn the bar into ASCII art at the
// expense of the actual numeric columns.
func (m *Model) barWidth(s *screen) int {
	w := m.width - barReserve(s.level, s.tool)
	if w < barWidthMin {
		return barWidthMin
	}
	if w > barWidthMax {
		return barWidthMax
	}
	return w
}

// Column-layout constants shared by barReserve and the renderers. Kept in one
// place so column widths and the inter-column gutter can't drift apart.
const (
	colGutter   = 2  // whitespace between adjacent columns (also brackets reserve)
	colCursor   = 2  // "▶ " selection marker
	colBrackets = 2  // "[" and "]" around the bar
	colSize     = 12 // humanize.Bytes value + slack
	colBloat    = 14 // " (NN% bloat)  "
	colMark     = 2  // "+ " child indicator
	colName     = 28 // typical relname budget
	colDetail   = 30 // generic detail-string budget
)

// barReserve is how many non-bar cells each level needs reserved for cursor,
// numeric columns and name/detail. Each level declares its own so new tools
// with different column shapes don't all have to share one global guess.
// Tool is consulted on levels whose columns differ per tool — at the tables
// level, the page-inspector swaps the toast/index detail string for a pages
// column.
func barReserve(l level, tl tool) int {
	switch l {
	case levelBufferTables:
		// cursor + bar(brackets) + buffered + total + cached + hit + dirty + name
		return colCursor + colBrackets +
			bufColBuffered + colGutter +
			bufColTotal + colGutter +
			bufColCached + colGutter +
			bufColHit + colGutter +
			bufColDirty + colGutter +
			colName
	case levelTables:
		if tl == toolPageInspect {
			// Page-inspector tables: no bloat overlay and no toast/idx detail
			// string — instead a pages column sits next to rows.
			return colCursor + colBrackets + colSize +
				(rowsColW + colGutter) + (pagesColW + colGutter) +
				colMark + colName
		}
		// cursor + bar(brackets) + size + rows + bloat + mark + name + detail
		return colCursor + colBrackets + colSize + (rowsColW + colGutter) + colBloat + colMark + colName + colDetail
	case levelParts:
		// Parts detail strings can be long ("heap · 12k dead (5%) · vac 3h ago
		// · ana 2d ago" or "index · primary · unique · btree"), so bump the
		// detail budget so the bar shrinks earlier on narrow terminals
		// instead of pushing the detail off the right.
		const partsDetail = 50
		return colCursor + colBrackets + colSize + colBloat + colMark + colName + partsDetail
	case levelColumns, levelDatabases, levelSchemas:
		return colCursor + colBrackets + colSize + colMark + colName + colDetail
	case levelHeapPages:
		// cursor + bar(brackets) + flag + used + live/dead + dead% + page name
		return colCursor + colBrackets + heapPageFlagColW + colGutter +
			heapPageUsedColW + colGutter + heapPageLPColW + colGutter +
			heapPageDeadColW + colGutter + heapPageNameColW
	case levelHeapTuples:
		// cursor + dot + lp idx + flag word + len + xmin + xmax + ctid + slack
		const tupleReserve = 2 + 2 + 6 + 10 + 8 + 12 + 12 + 14 + 6
		return tupleReserve
	case levelTupleRow:
		// cursor + column-name col + value gutter. The renderer prints
		// name and (potentially long) value as plain text — no bar, so
		// the reserve is just the column-name budget.
		return colCursor + tupleRowNameColW + colGutter
	case levelRelations:
		// Mirrors the page-inspector tables reserve, plus a parent-name
		// budget for the muted "→ <table>" tail shown on index rows.
		return colCursor + colBrackets + colSize +
			(rowsColW + colGutter) + (pagesColW + colGutter) +
			colMark + colName + relParentColW
	case levelIndexPages:
		// cursor + bar(brackets) + type + level + used + items + free% + page name
		return colCursor + colBrackets + idxPageTypeColW + colGutter +
			idxPageLevelColW + colGutter + idxPageUsedColW + colGutter +
			idxPageItemsColW + colGutter + idxPageFreeColW + colGutter +
			idxPageNameColW
	case levelIndexTuples:
		// cursor + offset + len + nulls/vars flags + ctid + key preview
		const idxTupleReserve = 2 + 6 + 8 + 8 + 14 + 4
		return idxTupleReserve
	case levelDescribe:
		// Plain-text panel — no bar drawn, so no space needs reserving.
		return 0
	case levelWAL:
		// cursor + bar(brackets) + combined + record + fpi + count + mark + name
		return colCursor + colBrackets + walColCombined + colGutter +
			walColRecord + colGutter + walColFPI + colGutter +
			walColCount + colGutter + colMark + colName
	case levelWALRecords:
		// cursor + bar(brackets) + size + fpi + lsn + mark + name + description
		return colCursor + colBrackets + walRecSizeColW + colGutter +
			walRecFPIColW + colGutter + walRecLSNColW + colGutter +
			colMark + colName + colDetail
	case levelWALBlocks, levelWALRelBlocks:
		// cursor + bar(brackets) + fpi + data + name + detail
		return colCursor + colBrackets + walBlkFPIColW + colGutter +
			walBlkDataColW + colGutter + colName + colDetail
	case levelWALRelations:
		// cursor + bar(brackets) + combined + fpi + records + pages + mark + name
		return colCursor + colBrackets + walRelCombinedColW + colGutter +
			walRelFPIColW + colGutter + walRelRecColW + colGutter +
			walRelBlkColW + colGutter + colMark + colName
	}
	return colCursor + colBrackets + colSize + colMark + colName
}

// Column widths shared by the heap-pages header and rows. Centralised here
// so the header columns and the row body never drift.
const (
	heapPageFlagColW = 1
	heapPageUsedColW = 10
	heapPageLPColW   = 12 // "###L ##R ##D"
	heapPageDeadColW = 7
	heapPageNameColW = 16
)

// Column widths for the heap-tuples header and rows. Same rationale.
const (
	tupleFlagColW = 9
	tupleLenColW  = 6
	tupleXidColW  = 10
	tupleCtidColW = 10
)

// Column widths shared by the index-pages header and rows.
const (
	idxPageTypeColW  = 4 // "leaf" / "intr" / "root" / "del"
	idxPageLevelColW = 5 // "L 12"
	idxPageUsedColW  = 10
	idxPageItemsColW = 12 // "###L ###D"
	idxPageFreeColW  = 7
	idxPageNameColW  = 16
)

// Column widths shared by the index-tuples header and rows.
const (
	idxTupleOffColW   = 5 // "#NNNN"
	idxTupleLenColW   = 6
	idxTupleFlagsColW = 8  // "N/V"
	idxTupleCtidColW  = 14 // "(blkno,off)" with room for big blocks
)

// Parent-name column on levelRelations: muted "→ <table>" tail on index
// rows so the user can correlate an index back to its table when sort
// interleaves the list.
const relParentColW = 24

// Column width for the tuple-row column-name slot. Wide enough for most
// SQL identifiers without truncation; the value column gets all the
// remaining horizontal space.
const tupleRowNameColW = 28

// truncateToWidth clips a rendered (ANSI-styled) line to at most width
// terminal cells. It must be ANSI-aware: the input contains escape sequences
// from styled cells and coloured bars, and a naive rune-based cut can sever the
// trailing reset of the last styled segment — leaving a style "open" that then
// bleeds into the start of the following lines (the cursor highlight smearing
// across rows). ansi.Truncate never breaks an escape sequence; the appended
// reset guarantees no style survives past the cut regardless of where it lands.
func truncateToWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…") + "\x1b[0m"
}
