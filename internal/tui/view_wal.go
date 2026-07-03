package tui

import (
	"fmt"
	"strings"
	"time"

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

// Column widths for the WAL by-relation view (levelWALRelations).
const (
	walRelCombinedColW = 11
	walRelFPIColW      = 11
	walRelRecColW      = 9 // record count
	walRelBlkColW      = 9 // distinct pages touched
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
	// wal_buffers_full rides on pg_stat_wal (the same read as the lifetime
	// totals), so it surfaces independently of the privilege-gated checkpoint
	// block below.
	if sum.StatBuffersFull > 0 {
		dir += mu("  ·  ") + styleErr.Render(formatRows(sum.StatBuffersFull)+" wal_buffers stalls")
	}

	win := indent + mu(fmt.Sprintf("window: %s … %s  ·  last %s analysed",
		sum.StartLSN, sum.EndLSN, humanize.Bytes(sum.WindowBytes)))
	// Window FPI byte-share: the exact full-page-image fraction of the WAL in
	// this window (summed from the rmgr breakdown already in s.items) — the
	// headline write-amplification figure. Lifetime FPI/record (counts from
	// pg_stat_wal) is a coarser cross-check; both are labelled so the byte
	// share and the per-record ratio aren't conflated.
	if share, ok := walWindowFPIShare(s); ok {
		win += mu("  ·  fpi ") + gradeStyle(share, 20, 50).Render(fmt1(share)+"% of window")
	}
	if sum.StatRecords > 0 {
		win += mu(fmt.Sprintf("  ·  %.2f fpi/record lifetime", float64(sum.StatFPI)/float64(sum.StatRecords)))
	}

	lines := []string{pos, dir, win}

	// Checkpoint context is best-effort (pg_control_checkpoint /
	// pg_stat_checkpointer may need superuser); render only what loaded.
	if cp := s.walCheckpoint; cp != nil {
		if cp.MaxWALBytes > 0 {
			ratio := float64(cp.BytesSinceCheckpoint) / float64(cp.MaxWALBytes)
			if ratio > 1 {
				ratio = 1
			}
			style := gradeStyle(ratio*100, 50, 80)
			const barW = 16
			filled := min(int(float64(barW)*ratio), barW)
			bar := paintBar(barW, barSegment{cells: filled, style: style})
			cpLine := indent + mu("checkpoint ") + bar + "  " +
				humanize.Bytes(cp.BytesSinceCheckpoint) + mu(" / ") +
				humanize.Bytes(cp.MaxWALBytes) + mu(" max_wal_size  ") +
				style.Render(fmt1(ratio*100)+"%")
			if !cp.CheckpointTime.IsZero() {
				cpLine += mu("  ·  last ") + relativeAge(time.Since(cp.CheckpointTime))
			}
			if eta, ok := cp.NextCheckpointETA(); ok {
				if until := time.Until(eta); until >= 0 {
					cpLine += mu("  ·  next ~") + shortDuration(until)
				} else {
					cpLine += mu("  ·  ") + styleErr.Render("next checkpoint overdue")
				}
			}
			lines = append(lines, cpLine)
		}

		csLine := indent + mu("checkpoints ")
		if total := cp.CheckpointsTimed + cp.CheckpointsRequested; total > 0 {
			reqPct := float64(cp.CheckpointsRequested) / float64(total) * 100
			csLine += formatRows(cp.CheckpointsTimed) + mu(" timed / ") +
				formatRows(cp.CheckpointsRequested) + mu(" requested")
			if reqPct >= 10 {
				csLine += " " + gradeStyle(reqPct, 20, 50).Render("("+fmt1(reqPct)+"% req)")
			}
		} else {
			csLine += mu("no data")
		}
		csLine += mu(fmt.Sprintf("  ·  timeout %s · completion %s · wal_compression %s",
			settingOr(cp.Settings, "checkpoint_timeout"),
			settingOr(cp.Settings, "checkpoint_completion_target"),
			settingOr(cp.Settings, "wal_compression")))
		lines = append(lines, csLine)
	}

	return strings.Join(lines, "\n")
}

// walWindowFPIShare is the full-page-image byte share of the analysed window,
// summed from the resource-manager breakdown rows in s.items. ok is false when
// the window carries no measured WAL yet, so the caller omits the figure.
func walWindowFPIShare(s *screen) (float64, bool) {
	var fpi, combined int64
	for _, it := range s.items {
		if st, ok := it.data.(pg.WALRmgrStat); ok {
			fpi += st.FPISize
			combined += st.CombinedSize
		}
	}
	if combined <= 0 {
		return 0, false
	}
	return 100 * float64(fpi) / float64(combined), true
}

// gradeStyle colours a metric green/amber/red by two thresholds: below warn is
// healthy (colorOK), at/above bad is alarming (styleErr), in between amber
// (colorAccent). Matches the thresholds renderMaintWAL uses for the same signals.
func gradeStyle(v, warn, bad float64) lipgloss.Style {
	switch {
	case v >= bad:
		return styleErr
	case v >= warn:
		return lipgloss.NewStyle().Foreground(colorAccent)
	default:
		return lipgloss.NewStyle().Foreground(colorOK)
	}
}

// settingOr returns the GUC value for key, or "unknown" when the best-effort
// settings read didn't return it (missing privilege / old server).
func settingOr(settings map[string]string, key string) string {
	if v, ok := settings[key]; ok && v != "" {
		return v
	}
	return "unknown"
}

// shortDuration formats a future interval compactly ("45s"/"12m"/"3h"/"2d") for
// the next-checkpoint ETA — relativeAge's buckets without the "ago" suffix.
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// --- WAL overview list (levelWAL) ---

func (m *Model) renderWALList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderWALHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			st, _ := it.data.(pg.WALRmgrStat)
			return renderWALRmgrRow(it, st, maxSz, barW, selected)
		})
}

func renderWALHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("combined", sort == sortBySize, sortDesc), walColCombined) + "  " +
		padRight("record", walColRecord) + "  " +
		padRight(sortMark("fpi", sort == sortByFPI, sortDesc), walColFPI) + "  " +
		padRight(sortMark("count", sort == sortByCount, sortDesc), walColCount) + "  " +
		"  " + sortMark("resource manager", sort == sortByName, sortDesc)
	// Surface the by-relation breakdown here — it's reachable only via `w`, which
	// otherwise hides in the ? overlay / expanded help and is easy to miss.
	hint := styleMuted.Render("  ·  ") + styleBadge.Render("w") + styleMuted.Render(" by relation")
	return styleMuted.Render(line) + hint
}

func renderWALRmgrRow(it item, st pg.WALRmgrStat, maxSize int64, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderWALBar(st.RecordSize, st.FPISize, maxSize, barW)
	fpiStr := "—"
	if st.FPISize > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(st.FPISize))
	}
	childMark := "  "
	if it.hasChildren {
		childMark = styleMuted.Render("+ ")
	}
	name := highlightName(it.name, selected)
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

// walRecTypePctW sizes the share-of-combined column ("100.0%").
const walRecTypePctW = 6

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
		padRight("%", walRecTypePctW) + "  " +
		padRight("record", walColRecord) + "  " +
		padRight("fpi", walColFPI) + "  " +
		padRight("count", walColCount)

	lines := []string{title, mu(header)}

	// Share is taken over every type in the window, not just the shown subset,
	// so the percentages stay honest when the table is truncated below.
	var totalCombined int64
	for _, st := range stats {
		totalCombined += st.CombinedSize
	}

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
		pct := "—"
		if totalCombined > 0 {
			pct = fmt.Sprintf("%.1f%%", 100*float64(st.CombinedSize)/float64(totalCombined))
		}
		lines = append(lines, indent+
			padRight(name, walRecTypeNameW)+"  "+
			padRight(humanize.Bytes(st.CombinedSize), walColCombined)+"  "+
			padRight(mu(pct), walRecTypePctW)+"  "+
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
	return m.renderRowList(s, height, renderWALRecordsHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			r, _ := it.data.(pg.WALRecord)
			return renderWALRecordRow(it, r, maxSz, barW, selected)
		})
}

func renderWALRecordsHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("size", sort == sortBySize, sortDesc), walRecSizeColW) + "  " +
		padRight(sortMark("fpi", sort == sortByFPI, sortDesc), walRecFPIColW) + "  " +
		padRight("lsn", walRecLSNColW) + "  " +
		"  " + sortMark("record_type", sort == sortByName, sortDesc) + "  " + styleMuted.Render("· description")
	return styleMuted.Render(line)
}

func renderWALRecordRow(it item, r pg.WALRecord, maxSize int64, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderWALBar(int64(r.RecordLength), int64(r.FPILength), maxSize, barW)
	fpiStr := styleMuted.Render("—")
	if r.FPILength > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(int64(r.FPILength)))
	}
	childMark := styleMuted.Render("+ ")
	name := highlightName(r.RecordType, selected)
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
	return m.renderRowList(s, height, renderWALBlocksHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			blk, _ := it.data.(pg.WALBlockRef)
			return renderWALBlockRow(it, blk, maxSz, barW, selected)
		})
}

func renderWALBlocksHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("fpi", sort == sortBySize, sortDesc), walBlkFPIColW) + "  " +
		padRight("data", walBlkDataColW) + "  " +
		sortMark("block reference", sort == sortByName, sortDesc) + "  " + styleMuted.Render("· db / fpi-info")
	return styleMuted.Render(line)
}

func renderWALBlockRow(it item, blk pg.WALBlockRef, maxSize int64, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	// Bar is the FPI byte count alone — the visual cue for which block refs
	// dragged a full 8 KiB page image into the WAL stream.
	bar := renderSolidBar(int64(blk.FPILength), maxSize, barW, styleBarAlt)
	fpiStr := styleMuted.Render("—")
	if blk.FPILength > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(int64(blk.FPILength)))
	}
	name := highlightName(it.name, selected)
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

// --- WAL by-relation view (levelWALRelations) ---

// renderWALRelationsHeader is the one-line title pinned above the by-relation
// list: total WAL the window generated, its full-page-image share, and the
// drill hint. Mirrors renderWALRecTypeStats's title shape.
func (m *Model) renderWALRelationsHeader(s *screen) string {
	mu := styleMuted.Render
	var combined, fpi int64
	var unresolved int
	for _, it := range s.items {
		if st, ok := it.data.(pg.WALRelStat); ok {
			combined += st.CombinedSize()
			fpi += st.FPIBytes
			if st.RelName == "" {
				unresolved++
			}
		}
	}
	share := mu("—")
	if combined > 0 {
		pct := 100 * float64(fpi) / float64(combined)
		share = gradeStyle(pct, 20, 50).Render(fmt1(pct) + "%")
	}
	header := "  " + styleHeader.Render(" by relation ") + "  " +
		mu("WAL generated per table/index in this window  ·  ") +
		styleSelected.Render(humanize.Bytes(combined)) + mu(" total · fpi ") + share +
		mu("  ·  ") + styleBadge.Render("↵") + mu(" block refs")
	// Names resolve only for the connected database (pg_filenode_relation is
	// database-local), but WAL is cluster-wide — rows for other databases fall
	// back to "relfilenode N". Flag it so the numeric rows don't read as a bug.
	if unresolved > 0 {
		header += "\n" + strings.Repeat(" ", 8) +
			mu(fmt.Sprintf("%d shown as relfilenode N — connect to that db (e.g. -d %s) to resolve names",
				unresolved, walFirstOtherDB(s)))
	}
	return header
}

// walFirstOtherDB returns the database name of the first relation whose name
// didn't resolve, to seed the "-d <db>" hint. Falls back to "<db>" when even
// the db name is unknown (shared catalog / dropped, reldatabase 0).
func walFirstOtherDB(s *screen) string {
	for _, it := range s.items {
		if st, ok := it.data.(pg.WALRelStat); ok && st.RelName == "" && st.DBName != "" {
			return st.DBName
		}
	}
	return "<db>"
}

func (m *Model) renderWALRelationsList(s *screen, height int) string {
	maxSz := maxItemSize(s.items, s.visibleIndexes())
	barW := m.barWidth(s)
	return m.renderRowList(s, height, renderWALRelationsListHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			st, _ := it.data.(pg.WALRelStat)
			return renderWALRelRow(it, st, maxSz, barW, selected)
		})
}

func renderWALRelationsListHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("combined", sort == sortBySize, sortDesc), walRelCombinedColW) + "  " +
		padRight(sortMark("fpi", sort == sortByFPI, sortDesc), walRelFPIColW) + "  " +
		padRight(sortMark("records", sort == sortByCount, sortDesc), walRelRecColW) + "  " +
		padRight("pages", walRelBlkColW) + "  " +
		"  " + sortMark("relation", sort == sortByName, sortDesc)
	return styleMuted.Render(line)
}

func renderWALRelRow(it item, st pg.WALRelStat, maxSize int64, barW int, selected bool) string {
	cursor := selectedCursor(selected)
	bar := renderWALBar(st.DataBytes, st.FPIBytes, maxSize, barW)
	fpiStr := styleMuted.Render("—")
	if st.FPIBytes > 0 {
		fpiStr = styleBarAlt.Render(humanize.Bytes(st.FPIBytes))
	}
	childMark := "  "
	if it.hasChildren {
		childMark = styleMuted.Render("+ ")
	}
	name := highlightName(it.name, selected)
	var tail []string
	if st.DBName != "" {
		tail = append(tail, "db "+st.DBName)
	}
	if st.OtherForkCount > 0 {
		// Block refs that hit fsm/vm/init rather than the main fork — usually
		// free-space / visibility-map maintenance riding along with the writes.
		tail = append(tail, formatRows(st.OtherForkCount)+" non-main-fork refs")
	}
	detail := ""
	if len(tail) > 0 {
		detail = "  " + styleMuted.Render(strings.Join(tail, " · "))
	}
	return cursor + bar + "  " +
		padRight(humanize.Bytes(st.CombinedSize()), walRelCombinedColW) + "  " +
		padRight(fpiStr, walRelFPIColW) + "  " +
		padRight(styleMuted.Render(formatRows(st.RecCount)), walRelRecColW) + "  " +
		padRight(styleMuted.Render(formatRows(st.BlockCount)), walRelBlkColW) + "  " +
		childMark + name + detail
}

// --- info overlays (? key) ---

// renderWALInfo explains the WAL overview: what a resource manager is, what
// the record-vs-FPI byte split means, and why FPI matters for tuning. Sized
// to fill `height` lines so the help row stays pinned to the bottom.
func (m *Model) renderWALInfo(height int) string {
	sw := swatch
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "WAL inspector reference")

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
	b.WriteString("    " + mu("pg_stat_wal (cumulative since the last stats reset, not just the window). fpi % of") + "\n")
	b.WriteString("    " + mu("window is the exact full-page-image byte share here; fpi/record lifetime is a coarser") + "\n")
	b.WriteString("    " + mu("count-based cross-check. wal_buffers stalls (if shown) means wal_buffers is too small.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" checkpoints ") + "  " +
		mu("how close WAL is to forcing a size-driven checkpoint, and the cadence") + "\n")
	b.WriteString("    " + mu("checkpoint bar = WAL since the last checkpoint's REDO point ÷ max_wal_size (at 100% a") + "\n")
	b.WriteString("    " + mu("\"requested\" checkpoint fires). last/next = when the last one finished and the next timed") + "\n")
	b.WriteString("    " + mu("one is due (checkpoint_timeout). A high requested-% means max_wal_size is too small.") + "\n")
	b.WriteString("    " + mu("This block needs pg_control_checkpoint / pg_stat_checkpointer (superuser); it is omitted") + "\n")
	b.WriteString("    " + mu("when unavailable — the rest of the header still renders.") + "\n\n")

	b.WriteString("  " + mu("Enter drills into the individual records of the selected rmgr; ") +
		styleBadge.Render("w") + mu(" groups the window by") + "\n")
	b.WriteString("  " + mu("relation (which table/index caused the WAL); ") +
		styleBadge.Render("space") + mu(" re-reads at the current LSN.") + "\n")
	b.WriteString("  " + mu("Needs the pg_walinspect extension and a superuser / pg_read_server_files role.") + "\n")

	return padInfo(&b, height)
}

// renderWALRecordsInfo explains the per-record view: the columns, what an LSN
// is, and how to read the description / block_ref text.
func (m *Model) renderWALRecordsInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "WAL records reference")

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
		swatch(styleBar) + mu(" record bytes  ·  ") + swatch(styleBarAlt) +
		mu(" FPI bytes — scaled to the biggest record in this list") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" summary table ") + "  " +
		mu("the block above the list breaks this rmgr's WAL down per record type") + "\n")
	b.WriteString("    " + mu("(e.g. INSERT / HOT_UPDATE / LOCK), with combined bytes, its % share of the rmgr's") + "\n")
	b.WriteString("    " + mu("total, the record / fpi byte split and a count —") + "\n")
	b.WriteString("    " + mu("the same pg_get_wal_stats source as the overview, but per_record=true.") + "\n\n")

	b.WriteString("  " + mu("Enter drills into the record's block references (which relation/page it touched).") + "\n")
	b.WriteString("  " + styleBadge.Render("←") + mu("/") + styleBadge.Render("→") + mu(" switch sort (size / fpi / type); the window is fixed to the overview's LSN range.") + "\n")

	return padInfo(&b, height)
}

// renderWALBlocksInfo explains the deepest view: how a record maps back to
// concrete relation blocks, and what the FPI flag tells you.
func (m *Model) renderWALBlocksInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "WAL block references reference")

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
		swatch(styleBarAlt) + mu(" full-page-image bytes for this block (empty = the record logged only the change)") + "\n")
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

// renderWALRelationsInfo explains the by-relation breakdown: how the window is
// re-aggregated per table/index, what the columns mean, and how to drill.
func (m *Model) renderWALRelationsInfo(height int) string {
	sw := swatch
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "WAL by relation reference")

	b.WriteString("  " + styleHeader.Render(" this view ") + "  " +
		mu("which table/index generated the WAL in the window — \"what caused the change\"") + "\n")
	b.WriteString("    " + mu("The same LSN window as the overview, but pg_get_wal_block_info is aggregated by the") + "\n")
	b.WriteString("    " + mu("relation each block reference touched (relfilenode → relation via pg_filenode_relation).") + "\n")
	b.WriteString("    " + mu("TOAST relations are folded into their owning table. Requires PostgreSQL 16+.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" the bar ") + "  " +
		sw(styleBar) + mu(" record bytes  ·  ") + sw(styleBarAlt) +
		mu(" FPI bytes — combined, scaled to the heaviest relation") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("one row per relation") + "\n")
	b.WriteString("    " + padRight("combined", 10) + mu("record + FPI bytes this relation contributed to the window") + "\n")
	b.WriteString("    " + padRight("fpi", 10) + mu("full-page-image bytes — the write-amplification share of this relation") + "\n")
	b.WriteString("    " + padRight("records", 10) + mu("distinct WAL records that touched the relation") + "\n")
	b.WriteString("    " + padRight("pages", 10) + mu("distinct (fork, block) pages those records modified") + "\n\n")

	b.WriteString("  " + mu("Enter drills into the relation's individual block references across the window (FPI-heaviest") + "\n")
	b.WriteString("  " + mu("first); a relation that resolves to a dropped/other-database relfilenode shows the number.") + "\n")

	return padInfo(&b, height)
}
