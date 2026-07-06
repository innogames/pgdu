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
	w := m.width - barReserve(s)
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
func barReserve(s *screen) int {
	l, tl := s.level, s.tool
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
	case levelShmem:
		// cursor + bar(brackets) + size + share% + group + name
		return colCursor + colBrackets +
			shmemColSize + colGutter +
			shmemColPct + colGutter +
			shmemColGroup + colGutter +
			colName
	case levelTables:
		if tl == toolPageInspect {
			// Page-inspector tables: no bloat overlay and no toast/idx detail
			// string — instead a pages column sits next to rows.
			return colCursor + colBrackets + colSize +
				(rowsColW + colGutter) + (pagesColW + colGutter) +
				colMark + colName
		}
		// cursor + bar(brackets) + size + heap + idx + rows + bloat + mark + name
		return colCursor + colBrackets + colSize +
			(breakdownColW + colGutter) + (breakdownColW + colGutter) +
			(rowsColW + colGutter) + colBloat + colMark + colName
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
		// cursor + bar(brackets) + flag + used + live + R + dead + dead% + name
		return colCursor + colBrackets + heapPageFlagColW + colGutter +
			heapPageUsedColW + colGutter +
			heapPageLiveColW + colGutter + heapPageRedirColW + colGutter +
			heapPageDeadLPColW + colGutter +
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
			(relTypeColW + colGutter) +
			colMark + colName + relParentColW
	case levelIndexPages:
		base := colCursor + colBrackets
		switch s.index.AccessMethod {
		case "gist":
			// type + used + items + free% + page name (no tree level)
			return base + idxPageTypeColW + colGutter + idxPageUsedColW + colGutter +
				idxPageItemsColW + colGutter + idxPageFreeColW + colGutter + idxPageNameColW
		case "brin":
			// type(meta/revmap/regular) + used + free% + page name
			return base + brinPageTypeColW + colGutter + idxPageUsedColW + colGutter +
				idxPageFreeColW + colGutter + idxPageNameColW
		case "gin":
			// type(flags tag) + maxoff(items) + used + free% + page name
			return base + ginPageTypeColW + colGutter + idxPageItemsColW + colGutter +
				idxPageUsedColW + colGutter + idxPageFreeColW + colGutter + idxPageNameColW
		default:
			// btree: cursor + bar(brackets) + flag + type + level + used + avg +
			// items + free% + links + name
			return base + idxPageFlagColW + colGutter +
				idxPageTypeColW + colGutter +
				idxPageLevelColW + colGutter + idxPageUsedColW + colGutter +
				idxPageAvgColW + colGutter +
				idxPageItemsColW + colGutter + idxPageFreeColW + colGutter +
				idxPageLinksColW + colGutter +
				idxPageNameColW
		}
	case levelIndexTuples:
		// cursor + offset + len + nulls/vars flags + ctid + key preview
		const idxTupleReserve = 2 + 6 + 8 + 8 + 14 + 4
		return idxTupleReserve
	case levelDescribe, levelTriage:
		// Plain-text panels — no bar drawn, so no space needs reserving.
		return 0
	case levelWaitProfile:
		// cursor + share% + sparkline + class name + gloss text
		return colCursor + waitPctColW + colGutter + waitSparkColW + colGutter +
			colName + colDetail
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
	case levelProgress:
		// cursor + bar(brackets) + command + relation + phase + done/total +
		// pct + age + eta + user
		return colCursor + colBrackets + progColCmd + colGutter +
			colName + progColPhase +
			progColDoneTotal + colGutter +
			progColPct + colGutter + progColAge + progColEta + progColUser
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
	// live / R / dead line-pointer counts, each its own sortable column.
	heapPageLiveColW   = 5 // up to ~291 tuples/page + sort arrow
	heapPageRedirColW  = 4
	heapPageDeadLPColW = 5
	heapPageDeadColW   = 7
	heapPageNameColW   = 16
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
	idxPageFlagColW  = 1 // single priority glyph (incomplete-split / half-dead / garbage)
	idxPageTypeColW  = 4 // "leaf" / "intr" / "root" / "del"
	idxPageLevelColW = 5 // "L 12"
	idxPageUsedColW  = 10
	idxPageAvgColW   = 5  // avg_item_size in bytes ("2700")
	idxPageItemsColW = 12 // "###L ###D"
	idxPageFreeColW  = 7
	idxPageLinksColW = 13 // "prev↔next" sibling links ("999999↔999999")
	idxPageNameColW  = 16

	// Per-AM page-type column widths (GiST reuses idxPageTypeColW).
	brinPageTypeColW = 7 // "regular" / "revmap" / "meta"
	ginPageTypeColW  = 9 // "data-leaf" / "entry" / "data" / "meta"
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

// Type column on levelRelations: holds the kind tag ("heap"/"toast"/"btree"/
// "gist"/"brin"/"gin") — 5 chars plus a sort-arrow allowance.
const relTypeColW = 6

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
