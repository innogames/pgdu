package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// Column widths shared by the WAL overview (levelWAL) header and rows.
const (
	walColCombined = 11 // "1023.99 MB"
	walColRecord   = 11
	walColFPI      = 11
	walColCount    = 9
)

// Column widths for the WAL records view (levelWALRecords).
const (
	walRecSizeColW = 10
	walRecFPIColW  = 10
	walRecLSNColW  = 18 // "FFFFFFFF/FFFFFFFF"
)

// Column widths for the WAL block-refs view (levelWALBlocks).
const (
	walBlkFPIColW  = 10
	walBlkDataColW = 10
)

// renderWALBar paints one row's WAL bytes as record-data | FPI, scaled to the
// biggest sibling's combined size so the bar reads "how much WAL did this row
// generate, and how much of it was full-page-image write amplification".
func renderWALBar(record, fpi, max int64, width int) string {
	if max <= 0 {
		max = 1
	}
	combined := record + fpi
	filled := min(int(float64(width)*float64(combined)/float64(max)), width)
	var fpiCells int
	if combined > 0 {
		fpiCells = min(int(float64(filled)*float64(fpi)/float64(combined)), filled)
	}
	rec := filled - fpiCells
	return paintBar(width,
		barSegment{cells: rec, style: styleBar},
		barSegment{cells: fpiCells, style: styleBarAlt},
	)
}

// shortLSN trims an LSN to its low half ("hi/lo" → "lo") for compact status /
// breadcrumb display; the full value still shows in the records list column.
func shortLSN(lsn string) string {
	if _, lo, ok := strings.Cut(lsn, "/"); ok {
		return lo
	}
	return lsn
}

// --- WAL overview header (levelWAL) ---

// renderWALSummary draws the multi-line header above the resource-manager
// list: the current write position, the pg_wal directory footprint, the
// cluster-lifetime pg_stat_wal counters, and the LSN window the breakdown
// below was computed over. Mirrors renderBufferSummary's "stacked context"
// shape. A header-source failure renders as a single muted error line — the
// rmgr list still works without it.
func (m *Model) renderWALSummary(s *screen) string {
	if s.walSummaryErr != nil {
		return "  " + styleMuted.Render("WAL summary: ") + styleErr.Render(s.walSummaryErr.Error())
	}
	sum := s.walSummary
	if sum == nil {
		return "  " + styleMuted.Render("WAL summary: unavailable")
	}
	mu := styleMuted.Render
	indent := strings.Repeat(" ", 8)

	pos := "  " + styleHeader.Render(" WAL ") + "  " +
		mu("insert ") + styleSelected.Render(sum.InsertLSN) +
		mu("  ·  flush ") + sum.FlushLSN +
		mu("  ·  segment ") + sum.CurrentFile +
		mu("  ·  wal_level=") + sum.WalLevel

	dir := indent + mu(fmt.Sprintf("pg_wal: %s across %d segment files  ·  lifetime: %s WAL · %s records · %s FPI",
		humanize.Bytes(sum.SegmentBytes), sum.SegmentFiles,
		humanize.Bytes(sum.StatBytes), formatRows(sum.StatRecords), formatRows(sum.StatFPI)))

	win := indent + mu(fmt.Sprintf("window: %s … %s  (last %s of WAL analysed below)",
		sum.StartLSN, sum.EndLSN, humanize.Bytes(sum.WindowBytes)))

	return strings.Join([]string{pos, dir, win}, "\n")
}

// --- WAL overview list (levelWAL) ---

func (m *Model) renderWALList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	var b strings.Builder
	b.WriteString(renderWALHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		st, _ := it.data.(pg.WALRmgrStat)
		b.WriteString(renderWALRmgrRow(it, st, maxSz, barW, vi == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderWALHeader(sort sortMode, sortDesc bool, barW int) string {
	arrow := "↑"
	if sortDesc {
		arrow = "↓"
	}
	mark := func(label string, active bool) string {
		if active {
			return label + arrow
		}
		return label
	}
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(mark("combined", sort == sortBySize), walColCombined) + "  " +
		padRight("record", walColRecord) + "  " +
		padRight(mark("fpi", sort == sortByFPI), walColFPI) + "  " +
		padRight(mark("count", sort == sortByCount), walColCount) + "  " +
		"  " + mark("resource manager", sort == sortByName)
	return styleMuted.Render(line)
}

func renderWALRmgrRow(it item, st pg.WALRmgrStat, maxSize int64, barW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	bar := renderWALBar(st.RecordSize, st.FPISize, maxSize, barW)
	fpiStr := "—"
	if st.FPISize > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(st.FPISize))
	}
	childMark := "  "
	if it.hasChildren {
		childMark = styleMuted.Render("+ ")
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	return cursor + bar + "  " +
		padRight(humanize.Bytes(st.CombinedSize), walColCombined) + "  " +
		padRight(styleMuted.Render(humanize.Bytes(st.RecordSize)), walColRecord) + "  " +
		padRight(fpiStr, walColFPI) + "  " +
		padRight(styleMuted.Render(formatRows(st.Count)), walColCount) + "  " +
		childMark + name
}

// --- WAL records summary table (levelWALRecords header) ---

// walRecTypeMaxRows caps how many record-type rows the summary table shows so
// a rmgr with many types (Heap can emit a dozen) doesn't crowd out the record
// list below. Overflow is folded into a "+N more" line.
const walRecTypeMaxRows = 12

// walRecTypeNameW is the record_type column width — wide enough for the
// longest labels pg_walinspect emits (e.g. "INSERT+INIT", "HOT_UPDATE",
// "VACUUM_PRUNE").
const walRecTypeNameW = 22

// renderWALRecTypeStats draws the per-record-type breakdown table pinned above
// the records list: how WAL bytes split across the selected rmgr's operations
// (INSERT / HOT_UPDATE / LOCK for Heap, INSERT_LEAF / SPLIT_R / … for Btree).
// Source is pg_get_wal_stats with per_record=true, already filtered to this
// rmgr and sorted biggest-combined-first by the query.
func (m *Model) renderWALRecTypeStats(s *screen) string {
	stats := s.walRecTypeStats
	mu := styleMuted.Render
	indent := strings.Repeat(" ", 8)

	title := "  " + styleHeader.Render(" by record-type ") + "  " +
		mu("WAL written per ") + styleSelected.Render(s.walRmgr) +
		mu(fmt.Sprintf(" operation in this window  ·  %d types", len(stats)))

	header := indent +
		padRight("record_type", walRecTypeNameW) + "  " +
		padRight("combined", walColCombined) + "  " +
		padRight("record", walColRecord) + "  " +
		padRight("fpi", walColFPI) + "  " +
		padRight("count", walColCount)

	lines := []string{title, mu(header)}

	shown := stats
	extra := 0
	if len(shown) > walRecTypeMaxRows {
		extra = len(shown) - walRecTypeMaxRows
		shown = shown[:walRecTypeMaxRows]
	}
	for _, st := range shown {
		// Strip the "Rmgr/" prefix — the rmgr is already named in the title.
		name := st.Name
		if _, rt, ok := strings.Cut(name, "/"); ok {
			name = rt
		}
		fpiStr := mu("—")
		if st.FPISize > 0 {
			fpiStr = styleBarAlt.Render(humanize.Bytes(st.FPISize))
		}
		lines = append(lines, indent+
			padRight(name, walRecTypeNameW)+"  "+
			padRight(humanize.Bytes(st.CombinedSize), walColCombined)+"  "+
			padRight(mu(humanize.Bytes(st.RecordSize)), walColRecord)+"  "+
			padRight(fpiStr, walColFPI)+"  "+
			padRight(mu(formatRows(st.Count)), walColCount))
	}
	if extra > 0 {
		lines = append(lines, indent+mu(fmt.Sprintf("… +%d more record types", extra)))
	}
	return strings.Join(lines, "\n")
}

// --- WAL records list (levelWALRecords) ---

func (m *Model) renderWALRecordsList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	var b strings.Builder
	b.WriteString(renderWALRecordsHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		r, _ := it.data.(pg.WALRecord)
		b.WriteString(renderWALRecordRow(it, r, maxSz, barW, vi == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderWALRecordsHeader(sort sortMode, sortDesc bool, barW int) string {
	arrow := "↑"
	if sortDesc {
		arrow = "↓"
	}
	mark := func(label string, active bool) string {
		if active {
			return label + arrow
		}
		return label
	}
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(mark("size", sort == sortBySize), walRecSizeColW) + "  " +
		padRight(mark("fpi", sort == sortByFPI), walRecFPIColW) + "  " +
		padRight("lsn", walRecLSNColW) + "  " +
		"  " + mark("record_type", sort == sortByName) + "  " + styleMuted.Render("· description")
	return styleMuted.Render(line)
}

func renderWALRecordRow(it item, r pg.WALRecord, maxSize int64, barW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	bar := renderWALBar(int64(r.RecordLength), int64(r.FPILength), maxSize, barW)
	fpiStr := styleMuted.Render("—")
	if r.FPILength > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(int64(r.FPILength)))
	}
	childMark := styleMuted.Render("+ ")
	name := r.RecordType
	if selected {
		name = styleSelected.Render(name)
	}
	xid := ""
	if r.Xid != "" && r.Xid != "0" {
		xid = styleMuted.Render("xid "+r.Xid) + " "
	}
	detail := ""
	if r.Description != "" {
		detail = "  " + xid + styleMuted.Render(r.Description)
	} else if xid != "" {
		detail = "  " + xid
	}
	return cursor + bar + "  " +
		padRight(humanize.Bytes(r.CombinedSize()), walRecSizeColW) + "  " +
		padRight(fpiStr, walRecFPIColW) + "  " +
		padRight(styleMuted.Render(r.StartLSN), walRecLSNColW) + "  " +
		childMark + name + detail
}

// --- WAL block refs list (levelWALBlocks) ---

func (m *Model) renderWALBlocksList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	rowsH := max(height-1, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	var b strings.Builder
	b.WriteString(renderWALBlocksHeader(s.sort, s.sortDesc, barW))
	b.WriteString("\n")
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		blk, _ := it.data.(pg.WALBlockRef)
		b.WriteString(renderWALBlockRow(it, blk, maxSz, barW, vi == s.cursor))
		b.WriteString("\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func renderWALBlocksHeader(sort sortMode, sortDesc bool, barW int) string {
	arrow := "↑"
	if sortDesc {
		arrow = "↓"
	}
	mark := func(label string, active bool) string {
		if active {
			return label + arrow
		}
		return label
	}
	line := strings.Repeat(" ", 2) + strings.Repeat(" ", barW+2) + "  " +
		padRight(mark("fpi", sort == sortBySize), walBlkFPIColW) + "  " +
		padRight("data", walBlkDataColW) + "  " +
		mark("block reference", sort == sortByName) + "  " + styleMuted.Render("· db / fpi-info")
	return styleMuted.Render(line)
}

func renderWALBlockRow(it item, blk pg.WALBlockRef, maxSize int64, barW int, selected bool) string {
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	// Bar is the FPI byte count alone — the visual cue for which block refs
	// dragged a full 8 KiB page image into the WAL stream.
	bar := renderSolidBar(int64(blk.FPILength), maxSize, barW, styleBarAlt)
	fpiStr := styleMuted.Render("—")
	if blk.FPILength > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(int64(blk.FPILength)))
	}
	name := it.name
	if selected {
		name = styleSelected.Render(name)
	}
	dbLabel := fmt.Sprintf("db %d", blk.RelDatabase)
	if blk.DBName != "" {
		dbLabel = "db " + blk.DBName
	}
	tail := []string{dbLabel}
	if tid, ok := blk.HeapTID(); ok {
		tail = append(tail, "tid "+tid)
	}
	if len(blk.FPIInfo) > 0 {
		tail = append(tail, strings.Join(blk.FPIInfo, ","))
	}
	detail := "  " + styleMuted.Render(strings.Join(tail, " · "))
	return cursor + bar + "  " +
		padRight(fpiStr, walBlkFPIColW) + "  " +
		padRight(styleMuted.Render(humanize.Bytes(int64(blk.BlockDataLength))), walBlkDataColW) + "  " +
		name + detail
}

// --- info overlays (? key) ---

// renderWALInfo explains the WAL overview: what a resource manager is, what
// the record-vs-FPI byte split means, and why FPI matters for tuning. Sized
// to fill `height` lines so the help row stays pinned to the bottom.
func (m *Model) renderWALInfo(height int) string {
	sw := func(style lipgloss.Style) string { return style.Render("▇") }
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("WAL inspector reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" what WAL is ") + "  " +
		mu("the write-ahead log — Postgres's durability & replication journal") + "\n")
	b.WriteString("    " + mu("Every change is appended here first, before the data files are touched — that is what makes") + "\n")
	b.WriteString("    " + mu("crash recovery, point-in-time recovery and replication possible. On disk it is a stream of") + "\n")
	b.WriteString("    " + mu("16 MB segment files under pg_wal/; a record's byte address in that stream is its LSN (the") + "\n")
	b.WriteString("    " + mu("hi/lo number shown throughout this tool). WAL is reclaimed once it is no longer needed for") + "\n")
	b.WriteString("    " + mu("recovery or a replica/slot — runaway pg_wal growth usually means a stuck slot or archiver.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" this view ") + "  " +
		mu("write-ahead-log bytes generated in the recent window, grouped by resource manager") + "\n")
	b.WriteString("    " + mu("Each row is one rmgr — the subsystem that wrote the record (Heap, Btree, Transaction,") + "\n")
	b.WriteString("    " + mu("XLOG, Gist, SPGist, …). Source: pg_get_wal_stats over the LSN window in the header.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" the bar ") + "  " +
		mu("combined bytes this rmgr wrote, scaled to the biggest rmgr in the window") + "\n")
	b.WriteString("    " + sw(styleBar) + "  " + mu("record    the WAL record bodies themselves (the logical change being logged)") + "\n")
	b.WriteString("    " + sw(styleBarAlt) + "  " + mu("FPI       full-page images — whole 8 KiB pages copied into WAL the first time a") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 10) +
		mu("page is touched after a checkpoint (full_page_writes). The dominant source of") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 10) +
		mu("WAL write-amplification — a high FPI share often means checkpoints are too frequent.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per resource manager") + "\n")
	b.WriteString("    " + padRight("combined", 10) + mu("record + FPI bytes — the total WAL volume this rmgr produced") + "\n")
	b.WriteString("    " + padRight("record", 10) + mu("bytes spent on record data alone") + "\n")
	b.WriteString("    " + padRight("fpi", 10) + mu("bytes spent on full-page images") + "\n")
	b.WriteString("    " + padRight("count", 10) + mu("number of WAL records this rmgr emitted in the window") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" header ") + "  " +
		mu("insert/flush LSN = current write position · segment = WAL file the head sits in") + "\n")
	b.WriteString("    " + mu("pg_wal = on-disk size & file count of the WAL directory · lifetime totals are from") + "\n")
	b.WriteString("    " + mu("pg_stat_wal (cumulative since the last stats reset, not just the window).") + "\n\n")

	b.WriteString("  " + mu("Enter drills into the individual records of the selected rmgr; ") +
		styleBadge.Render("space") + mu(" re-reads at the current LSN.") + "\n")
	b.WriteString("  " + mu("Needs the pg_walinspect extension and a superuser / pg_read_server_files role.") + "\n")

	return padInfo(&b, height)
}

// renderWALRecordsInfo explains the per-record view: the columns, what an LSN
// is, and how to read the description / block_ref text.
func (m *Model) renderWALRecordsInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("WAL records reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" this view ") + "  " +
		mu("every WAL record the selected resource manager wrote in the window, oldest first") + "\n")
	b.WriteString("    " + mu("Source: pg_get_wal_records_info, filtered to this rmgr and ordered by start LSN.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per WAL record") + "\n")
	b.WriteString("    " + padRight("size", 12) + mu("record_length + fpi_length — total bytes this record occupies in the WAL") + "\n")
	b.WriteString("    " + padRight("fpi", 12) + mu("full-page-image bytes carried by this record (0 = no page image)") + "\n")
	b.WriteString("    " + padRight("lsn", 12) + mu("start LSN — the record's byte address in the log (segment-relative hi/lo offset)") + "\n")
	b.WriteString("    " + padRight("record_type", 12) + mu("the rmgr-specific operation, e.g. INSERT / HOT_UPDATE / COMMIT / CHECKPOINT_ONLINE") + "\n")
	b.WriteString("    " + padRight("xid", 12) + mu("owning transaction id; absent for non-transactional records (checkpoints, etc.)") + "\n")
	b.WriteString("    " + padRight("description", 12) + mu("pg_walinspect's human-readable decode of the record's payload") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" the bar ") + "  " +
		styleBar.Render("▇") + mu(" record bytes  ·  ") + styleBarAlt.Render("▇") +
		mu(" FPI bytes — scaled to the biggest record in this list") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" summary table ") + "  " +
		mu("the block above the list breaks this rmgr's WAL down per record type") + "\n")
	b.WriteString("    " + mu("(e.g. INSERT / HOT_UPDATE / LOCK), with combined / record / fpi bytes and a count —") + "\n")
	b.WriteString("    " + mu("the same pg_get_wal_stats source as the overview, but per_record=true.") + "\n\n")

	b.WriteString("  " + mu("Enter drills into the record's block references (which relation/page it touched).") + "\n")
	b.WriteString("  " + styleBadge.Render("s") + mu(" cycles sort (size / fpi / type); the window is fixed to the overview's LSN range.") + "\n")

	return padInfo(&b, height)
}

// renderWALBlocksInfo explains the deepest view: how a record maps back to
// concrete relation blocks, and what the FPI flag tells you.
func (m *Model) renderWALBlocksInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("WAL block references reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" this view ") + "  " +
		mu("the page(s) one WAL record modified — its tie-back from the log to physical storage") + "\n")
	b.WriteString("    " + mu("Source: pg_get_wal_block_info for the single record (PostgreSQL 16+; empty on 15).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" block reference ") + "  " +
		mu("rel <relation>/<fork> blk <n> identifies the exact page touched") + "\n")
	b.WriteString("    " + padRight("relation", 12) + mu("name resolved from relfilenode via pg_filenode_relation; falls back to the raw") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 12) + mu("relfilenode when the relation is in another database or has been dropped") + "\n")
	b.WriteString("    " + padRight("tid", 12) + mu("heap tuple id (block,offset) parsed from the record description, when present") + "\n")
	b.WriteString("    " + padRight("fork", 12) +
		mu("which fork: ") + styleBadge.Render("main") + mu(" heap/index data · ") +
		styleBadge.Render("fsm") + mu(" free-space map · ") +
		styleBadge.Render("vm") + mu(" visibility map · ") +
		styleBadge.Render("init") + mu(" unlogged init") + "\n")
	b.WriteString("    " + padRight("blk", 12) + mu("block (page) number within that fork") + "\n")
	b.WriteString("    " + padRight("db", 12) + mu("reldatabase OID — 0 for shared catalogs that live outside any one database") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" the bar & fpi ") + "  " +
		styleBarAlt.Render("▇") + mu(" full-page-image bytes for this block (empty = the record logged only the change)") + "\n")
	b.WriteString("    " + padRight("data", 12) + mu("block_data_length — bytes of per-block change data (tuple, offsets, …)") + "\n")
	b.WriteString("    " + padRight("fpi-info", 12) + mu("flags on the page image, e.g. APPLY (replayed) / HAS_HOLE / COMPRESS_*") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" fpi vs data ") + "  " +
		mu("a change is logged one of three ways — which is why data is so often 0") + "\n")
	b.WriteString("    " + padRight("image", 12) +
		mu("first touch of an existing page after a checkpoint copies the whole 8 KiB page") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 12) +
		mu("into WAL (fpi set); the per-block data is then redundant, so data is 0") + "\n")
	b.WriteString("    " + padRight("new page", 12) +
		mu("a page created here (e.g. the right half of a B-tree split) is logged as data") + "\n")
	b.WriteString("    " + strings.Repeat(" ", 12) +
		mu("with no image — redo reconstructs the page from those bytes") + "\n")
	b.WriteString("    " + padRight("delta", 12) +
		mu("a small in-place edit (a link repointed, a flag set) logs just the change, no fpi") + "\n\n")

	b.WriteString("  " + mu("A record with several block refs touched several pages atomically (e.g. an index split,") + "\n")
	b.WriteString("  " + mu("or a heap update that also stamps the visibility map). This is a leaf view — no further drill.") + "\n")

	return padInfo(&b, height)
}

// padInfo pads an info overlay's builder to exactly `height` lines so the
// help row stays pinned to the bottom of the screen. Mirrors the inline
// padding loop the other render*Info helpers use.
func padInfo(b *strings.Builder, height int) string {
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
