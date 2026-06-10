package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// statementColumns is the projected column schema for the current visibility set
// and track_planning state, derived from stmtColumnRegistry. Used on the first
// (empty) load before any rows exist; buildStatementItems returns the same
// schema once rows arrive.
func (m *Model) statementColumns(trackPlanning bool) []pg.DiagColumn {
	return diagColumnsFrom(m.visibleStmtCols(stmtCtx{trackPlanning: trackPlanning}))
}

// buildStatementItems converts window-delta QueryStats into generic-table rows
// (item.data = []pg.DiagCell) over the currently visible columns. It returns the
// items, the projected column descriptors (parallel to each item's cells), the
// summed window exec time (the time% denominator, also carried to the detail
// view), and the cells for a pinned "← Sum" footer totalling the whole table
// (nil when there are no rows).
func (m *Model) buildStatementItems(rows []pg.QueryStat, trackPlanning bool) ([]item, []stmtColDesc, float64, []pg.DiagCell) {
	var windowMs float64
	for _, q := range rows {
		windowMs += q.TotalExecTime
	}
	ctx := stmtCtx{windowMs: windowMs, trackPlanning: trackPlanning}
	descs := m.visibleStmtCols(ctx)

	items := make([]item, 0, len(rows))
	for _, q := range rows {
		items = append(items, item{
			name:        flattenQuery(q.Query),
			data:        cellsFor(descs, q, ctx),
			statQueryID: q.QueryID,
		})
	}
	if len(rows) == 0 {
		return items, descs, windowMs, nil
	}
	// Build the footer over a summed QueryStat so the ratio columns come out as
	// true pooled totals for free: mean_ms = Σtotal_ms÷Σcalls, hit% the weighted
	// ratio, blk/row Σblocks÷Σrows, and time% exactly 100 (Σtotal_ms == windowMs).
	total := cellsFor(descs, sumQueryStats(rows), ctx)
	labelStmtFooter(descs, total)
	return items, descs, windowMs, total
}

// sumQueryStats totals every additive counter across rows into one aggregate
// QueryStat (identity fields left zero). Summing all counters — not just those
// any single column reads today — keeps the footer correct as opt-in columns are
// enabled or new ones added to the registry.
func sumQueryStats(rows []pg.QueryStat) pg.QueryStat {
	var t pg.QueryStat
	for _, q := range rows {
		t.Calls += q.Calls
		t.Rows += q.Rows
		t.TotalExecTime += q.TotalExecTime
		t.Plans += q.Plans
		t.TotalPlanTime += q.TotalPlanTime
		t.SharedBlksHit += q.SharedBlksHit
		t.SharedBlksRead += q.SharedBlksRead
		t.SharedBlksDirtied += q.SharedBlksDirtied
		t.SharedBlksWritten += q.SharedBlksWritten
		t.LocalBlksHit += q.LocalBlksHit
		t.LocalBlksRead += q.LocalBlksRead
		t.LocalBlksDirtied += q.LocalBlksDirtied
		t.LocalBlksWritten += q.LocalBlksWritten
		t.TempBlksRead += q.TempBlksRead
		t.TempBlksWritten += q.TempBlksWritten
		t.SharedBlkReadTime += q.SharedBlkReadTime
		t.SharedBlkWriteTime += q.SharedBlkWriteTime
		t.LocalBlkReadTime += q.LocalBlkReadTime
		t.LocalBlkWriteTime += q.LocalBlkWriteTime
		t.TempBlkReadTime += q.TempBlkReadTime
		t.TempBlkWriteTime += q.TempBlkWriteTime
		t.WALRecords += q.WALRecords
		t.WALFPI += q.WALFPI
		t.WALBytes += q.WALBytes
	}
	return t
}

func diagNum(display string, n float64) pg.DiagCell {
	return pg.DiagCell{Display: display, Num: n, HasNum: true}
}

// flattenQuery collapses all internal whitespace runs to single spaces so a
// multi-line normalized query renders as one table row.
func flattenQuery(q string) string {
	return strings.Join(strings.Fields(q), " ")
}

// fmtFloat renders a number with up to 1 decimals, trailing zeros stripped.
func fmtFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if strings.ContainsRune(s, '.') {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// fmt1 renders a number with exactly one decimal place (60 → "60.0", 98.51 →
// "98.5"). The top-queries numeric columns use it so every value shows a single
// fractional digit rather than a ragged mix of 0/1/2 places.
func fmt1(f float64) string {
	return strconv.FormatFloat(f, 'f', 1, 64)
}

// planTimeMetric renders the detail-view plan-time line, distinguishing a real
// zero from "not collected" (pg_stat_statements.track_planning off).
func planTimeMetric(q pg.QueryStat, trackPlanning bool, mu func(...string) string) string {
	if !trackPlanning {
		return "—" + mu("  (track_planning off — not collected)")
	}
	return fmtMs(q.TotalPlanTime) + " ms" + mu(fmt.Sprintf("  (%s plans)", formatRows(q.Plans)))
}

// fmtMs formats a millisecond duration compactly: sub-millisecond and small
// values keep ms; large values switch to seconds so the column stays narrow.
func fmtMs(ms float64) string {
	if ms >= 100000 {
		return fmt1(ms/1000) + "s"
	}
	return fmt1(ms)
}

// --- window-status header (levelStatements) ---

func (m *Model) renderStatementsHeader(s *screen) string {
	mu := styleMuted.Render
	if s.statBaselineAt.IsZero() {
		return "  " + styleHeader.Render(" queries ") + "  " + mu("opening window — run some queries…")
	}
	var line string
	switch {
	case s.statEndSnap != nil:
		// Frozen A→B diff between two snapshots: no live "now", so the window is the
		// fixed span between the two capture times and there's nothing to refresh.
		line = "  " + styleHeader.Render(" queries ") + "  " +
			styleSelected.Render(s.statBaselineAt.Format("15:04:05")) + mu(" → ") +
			styleSelected.Render(s.statSampledAt.Format("15:04:05")) +
			mu(fmt.Sprintf("  ·  snapshot diff (frozen)  ·  %d queries  ·  R for live · Enter for detail", len(s.statRows)))
	case s.statBaseSnap != nil:
		// Disk baseline, live end: the window runs from the snapshot's capture time
		// up to the latest live sample.
		elapsed := max(s.statSampledAt.Sub(s.statBaselineAt), 0)
		line = "  " + styleHeader.Render(" queries ") + "  " +
			mu("window ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" since "+s.statBaselineAt.Format("2006-01-02 15:04:05")+" (snapshot) · live") +
			mu(fmt.Sprintf("  ·  %d queries  ·  refresh %s  ·  t cadence · C columns · R for live · Enter for detail",
				len(s.statRows), m.refreshLabel()))
	default:
		elapsed := max(s.statSampledAt.Sub(s.statBaselineAt), 0)
		line = "  " + styleHeader.Render(" queries ") + "  " +
			mu("window ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" since "+s.statBaselineAt.Format("15:04:05")) +
			mu(fmt.Sprintf("  ·  %d queries  ·  refresh %s  ·  t cadence · C columns · R resets · S saves · L loads · Enter for detail",
				len(s.statRows), m.refreshLabel()))
	}
	if !s.statTrackPlanning {
		// The planning-time column is hidden (it would always read 0); point the
		// user at the setting that turns planning-time collection on.
		line += "\n  " + mu("planning time column hidden — ") + styleBadge.Render("track_planning off") +
			mu(": ALTER SYSTEM SET pg_stat_statements.track_planning = on; SELECT pg_reload_conf();")
	}
	return line
}

// refreshLabel describes the current auto-refresh state for the header and the
// ? overlay: the interval (e.g. "2s") or "off" when the cadence has been cycled
// off (t) or disabled by config (--queries-refresh 0).
func (m *Model) refreshLabel() string {
	if m.statRefresh <= 0 {
		return "off"
	}
	return m.statRefresh.String()
}

// refreshSentence is the ? overlay's prose description of the re-sample cadence,
// adapting to whether auto-refresh is configured on or off and noting the t
// toggle.
func (m *Model) refreshSentence() string {
	if m.statRefresh <= 0 {
		return "Auto-refresh is off; press t to cycle the cadence (2s → 60s → off)."
	}
	return "It re-samples every " + m.statRefresh.String() + " — press t to cycle the cadence (2s → 60s → off)."
}

// fmtDuration renders a window age as H:MM:SS (hours dropped when zero).
func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	mn := int(d % time.Hour / time.Minute)
	sec := int(d % time.Minute / time.Second)
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mn, sec)
	}
	return fmt.Sprintf("%d:%02d", mn, sec)
}

// renderStatementsInfo is the ? overlay for the top-queries tool: it explains
// the window model (which is the subtle part — pg_stat_statements has no time
// axis) and every column.
func (m *Model) renderStatementsInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Top queries reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" the window ") + "  " +
		mu("why numbers start at zero and grow") + "\n")
	b.WriteString("    " + mu("pg_stat_statements counters are cumulative since the last reset — they have no time axis.") + "\n")
	b.WriteString("    " + mu("pgdu snapshots them when you open this tool (the baseline) and shows the delta against it,") + "\n")
	b.WriteString("    " + mu("so the table is everything that ran ‘since you opened it’. "+m.refreshSentence()) + "\n")
	b.WriteString("    " + mu("press ") + styleBadge.Render("R") + mu(" to drop the baseline and restart the window. Stats are scoped to the current database.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("all sortable — ") + styleBadge.Render("s") + mu(" cycles the column, ") + styleBadge.Render("r") +
		mu(" reverses, ") + styleBadge.Render("C") + mu(" chooses which columns show (and opt-in metrics)") + "\n")
	col := func(name, desc string) {
		b.WriteString("    " + padRight(name, 9) + mu(desc) + "\n")
	}
	col("total_ms", "total execution time in the window (the default sort — your hottest queries)")
	col("time%", "share of the window's total execution time spent in this query")
	col("mean_ms", "average execution time per call (total_ms ÷ calls)")
	col("mean_plan_ms", "average planning time per plan — only shown when track_planning is on (hidden otherwise)")
	col("calls", "times the query was executed in the window")
	col("rows", "rows returned / affected across those calls")
	col("hit", "shared blocks served from cache (shared_blks_hit)")
	col("miss", "shared blocks read from disk/OS (shared_blks_read)")
	col("hit%", "cache hit ratio: hit ÷ (hit+miss); ‘—’ when the query touched no blocks")
	col("blk/row", "shared blocks (hit+read) per row — work per result row; lower is better; ‘—’ when 0 rows")
	col("io_ms", "time in block read+write I/O (needs track_io_timing for non-zero values)")
	col("wal", "WAL bytes generated by the query")
	col("table", "the main table parsed from the statement (FROM/UPDATE/INTO) — d describes it")
	col("T", "command type: S select · SL select…for update · L advisory lock · I insert · U update · D delete · M merge · T begin/commit")
	col("query", "the normalized statement text ($1, $2 … in place of constants)")
	b.WriteString("\n")

	b.WriteString("  " + styleHeader.Render(" cost colours ") + "  " +
		mu("lower is better — 0 is ideal") + "\n")
	b.WriteString("    " + mu("total_ms, mean_ms, mean_plan_ms, miss, io_ms, wal and blk/row are tinted ") +
		costStyleRelative(0, 1).Render("green") + mu(" only at 0, ") +
		costStyleRelative(1, 10).Render("sage") + mu(" for any low nonzero, ") +
		costStyleRelative(5, 10).Render("yellow") + mu(" in the middle, ") +
		costStyleRelative(10, 10).Render("red") + mu(" at the worst row in the window.") + "\n")
	b.WriteString("    " + mu("The grade is relative to the largest value visible in each column, so colours re-scale as the") + "\n")
	b.WriteString("    " + mu("window changes; an all-zero column stays green. The detail view's blk/row uses fixed thresholds instead.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" describe ") + "  " +
		mu("press ") + styleBadge.Render("d") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Opens the table's \\d view — columns, indexes and constraints — so you can see, e.g.,") + "\n")
	b.WriteString("    " + mu("whether the predicate columns of a slow query are actually indexed.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" disk usage ") + "  " +
		mu("press ") + styleBadge.Render("u") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Jumps to the main table's disk-usage breakdown (heap, indexes, toast, free space) in the") + "\n")
	b.WriteString("    " + mu("size explorer — esc returns here. Nothing happens when the statement has no resolvable table.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" detail ") + "  " +
		mu("press ") + styleBadge.Render("Enter") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Shows the full text, the same metrics, a ‘sample call’ and its EXPLAIN, run automatically.") + "\n")
	b.WriteString("    " + mu("For read-only SELECTs, ") + styleBadge.Render("Enter") +
		mu(" runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) and ") + styleBadge.Render("E") +
		mu(" executes the query and shows the result rows — both execute the query.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" real parameters ") + "  " +
		mu("via pg_qualstats — optional") + "\n")
	b.WriteString("    " + mu("pg_stat_statements normalizes away constants, so by default the sample call uses ") + "\n")
	b.WriteString("    " + mu("synthesized literals (1, 'sample', …) and EXPLAIN runs as GENERIC_PLAN — the plan for the") + "\n")
	b.WriteString("    " + mu("parameterized query, without real values. Install ") + styleBadge.Render("pg_qualstats") +
		mu(" (in shared_preload_libraries, with") + "\n")
	b.WriteString("    " + mu("pg_qualstats.track_constants=on) and pgdu uses the real values it captured: the sample call") + "\n")
	b.WriteString("    " + mu("becomes a real example and EXPLAIN sees real data. Press ") + styleBadge.Render("p") +
		mu(" in the detail view to browse all") + "\n")
	b.WriteString("    " + mu("captured values by frequency (the value pattern); ") + styleBadge.Render("Enter") +
		mu(" there EXPLAIN-ANALYZEs the highlighted one.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" snapshots ") + "  " +
		mu("capture the window to disk and diff it later") + "\n")
	b.WriteString("    " + mu("Press ") + styleBadge.Render("S") +
		mu(" to dump the current pg_stat_statements counters to a file (under ~/.local/state/pgdu/snapshots") + "\n")
	b.WriteString("    " + mu("by default; --snapshot-dir to change). Press ") + styleBadge.Render("L") +
		mu(" to browse saved snapshots — a timeline range picker") + "\n")
	b.WriteString("    " + mu("whose ") + styleSelected.Render("◀ start") + mu(" / ") + styleSelected.Render("◀ end") +
		mu(" markers show the applied window (session start → now by default). ") + styleBadge.Render("Enter") + "\n")
	b.WriteString("    " + mu("picks an endpoint: the first pick spans ‘pick → now’ (live); with a start applied, Enter on") + "\n")
	b.WriteString("    " + mu("another row spans the range between the two, frozen — no re-sampling — unless an endpoint") + "\n")
	b.WriteString("    " + mu("is ‘now’. ") + styleBadge.Render("D") + mu(" deletes a file.") + "\n")
	b.WriteString("    " + mu("Press ") + styleBadge.Render("R") +
		mu(" to drop a loaded snapshot and return to the live window. Snapshots invalidated by a") + "\n")
	b.WriteString("    " + mu("counter reset since their capture are left out of the list — they can't serve as a baseline.") + "\n")
	b.WriteString("    " + mu("The list also carries three virtual anchors you can pick as endpoints: ") +
		styleSelected.Render("now") + mu(" (live), ") + styleSelected.Render("session start") + "\n")
	b.WriteString("    " + mu("(the window from when you opened the tool) and ") + styleSelected.Render("since last reset") +
		mu(" (everything since the server's last reset).") + "\n")

	return padInfo(&b, height)
}

// --- column config overlay (C on levelStatements) ---

// renderColumnConfig draws the htop-style column picker: one checkbox row per
// registry column, with the current cursor highlighted. Default-on and opt-in
// columns are toggled with space/Enter; the mandatory query column and the
// planning columns when track_planning is off are shown but not toggleable.
func (m *Model) renderColumnConfig(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("configure columns") + mu("  ·  ") +
		styleBadge.Render("space") + mu(" toggles · ") + styleBadge.Render("↑/↓") + mu(" move · ") +
		styleBadge.Render("C") + mu(" or ") + styleBadge.Render("esc") + mu(" to close") + "\n")
	b.WriteString("  " + mu("choose which columns the top-queries table shows — opt-in metrics are off by default") + "\n\n")

	m.ensureStmtColsInit()
	ctx := stmtCtx{trackPlanning: s.statTrackPlanning}
	reg := stmtColumnRegistry()
	nameW := 0
	for _, d := range reg {
		if n := len(d.name); n > nameW {
			nameW = n
		}
	}
	for i, d := range reg {
		unavailable := d.available != nil && !d.available(ctx)
		on := d.mandatory || m.stmtColEnabled(d.id, d.defaultOn)

		box := "[ ]"
		switch {
		case unavailable:
			box = "[·]"
		case on:
			box = "[x]"
		}

		cursor := "  "
		if i == m.colCfgCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}

		label := box + "  " + padRight(d.name, nameW)
		var rendered string
		switch {
		case unavailable:
			rendered = mu(label+"  "+d.desc) + "  " + styleBadge.Render("track_planning off")
		case i == m.colCfgCursor:
			rendered = styleSelected.Render(label) + "  " + mu(d.desc)
		default:
			rendered = label + "  " + mu(d.desc)
		}
		if d.mandatory {
			rendered += mu("  (always shown)")
		}
		b.WriteString(cursor + rendered + "\n")
	}

	return padInfo(&b, height)
}

// --- query detail (levelStatementDetail) ---

func (m *Model) renderStatementDetail(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	q := s.statDetail
	if q == nil {
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render(fmt.Sprintf("query %d", q.QueryID)) + "\n\n")

	// --- window metrics ---
	pct := 0.0
	if s.statWindowExecMs > 0 {
		pct = q.TotalExecTime / s.statWindowExecMs * 100
	}
	hitStr := "—"
	if hr, ok := q.HitRatio(); ok {
		hitStr = gradedPercentStyle(hr).Render(fmtFloat(hr) + "%")
	}
	bprStr := "—"
	if bpr, ok := q.BlocksPerRow(); ok {
		bprStr = blkPerRowStyle(bpr).Render(fmt1(bpr))
	}
	b.WriteString("  " + styleHeader.Render(" window metrics ") + "\n")
	metrics := [][2]string{
		{"calls", formatRows(q.Calls)},
		{"rows", formatRows(q.Rows) + mu(fmt.Sprintf("  (%s/call)", fmtFloat(q.RowsPerCall())))},
		{"total time", fmtMs(q.TotalExecTime) + " ms" + mu(fmt.Sprintf("  (%s%% of window)", fmtFloat(pct)))},
		{"mean time", fmtMs(q.MeanTime()) + " ms"},
		{"plan time", planTimeMetric(*q, s.statTrackPlanning, mu)},
		{"shared hit ratio", hitStr},
		{"I/O time", fmtMs(q.IOTime()) + " ms"},
		{"shared blocks", fmt.Sprintf("%s hit · %s read (miss) · %s dirtied · %s written",
			formatRows(q.SharedBlksHit), formatRows(q.SharedBlksRead),
			formatRows(q.SharedBlksDirtied), formatRows(q.SharedBlksWritten))},
		{"blocks/row", bprStr + mu("  (hit+read ÷ rows)")},
		{"temp blocks", fmt.Sprintf("%s read · %s written", formatRows(q.TempBlksRead), formatRows(q.TempBlksWritten))},
		{"WAL", fmt.Sprintf("%s · %s records · %s FPI", humanize.Bytes(q.WALBytes), formatRows(q.WALRecords), formatRows(q.WALFPI))},
	}
	// Identify the statement's main table (parsed from FROM/UPDATE/INTO), the
	// same value shown in the overview's `table` column. Omitted when unparseable.
	if t := pg.MainTable(q.Query); t != "" {
		metrics = append(metrics, [2]string{"table", t})
	}
	labelW := 0
	for _, kv := range metrics {
		if n := lipgloss.Width(kv[0]); n > labelW {
			labelW = n
		}
	}
	for _, kv := range metrics {
		b.WriteString("    " + mu(padRight(kv[0], labelW)) + "  " + kv[1] + "\n")
	}

	// --- query text ---
	b.WriteString("\n  " + styleHeader.Render(" query ") + "\n")
	for _, line := range m.wrapDetail(flattenQuery(q.Query)) {
		b.WriteString("    " + line + "\n")
	}

	explainable := pg.ExplainableQuery(q.Query)

	// --- sample call ---
	b.WriteString("\n  " + styleHeader.Render(" sample call ") + "\n")
	// Once the sample source is resolved, name it: real captured values vs
	// synthesized literals, and how to get real ones when pg_qualstats is absent.
	if explainable && (s.statSampleCall != "" || s.statSampleErr != nil) {
		var hint string
		switch {
		case s.statSampleReal:
			hint = "real values · pg_qualstats"
		case s.statSampleFromData:
			hint = "values sampled from live table data"
		case s.statQualstats:
			hint = "synthesized — pg_qualstats has no sample for this query yet"
		case s.extPrompt != nil && s.extPrompt.name == extQualstats:
			hint = "synthesized — press i to install pg_qualstats for real values"
		default:
			hint = "synthesized — install pg_qualstats (shared_preload_libraries + track_constants) for real values"
		}
		b.WriteString("    " + mu(hint) + "\n")
	}
	switch {
	case !explainable:
		b.WriteString("    " + mu("not a SELECT/DML statement — no parameters to fill") + "\n")
	case s.statSampleErr != nil:
		b.WriteString("    " + mu("could not infer parameters: "+s.statSampleErr.Error()) + "\n")
	case s.statSampleCall != "":
		for _, line := range m.wrapDetail(flattenQuery(s.statSampleCall)) {
			b.WriteString("    " + styleBarAlt.Render(line) + "\n")
		}
	default:
		b.WriteString("    " + mu("inferring parameters…") + "\n")
	}

	// --- explain ---
	explainHdr := " explain (generic plan) "
	switch {
	case s.statExplainAnalyze:
		explainHdr = " explain (analyze · verbose · buffers) "
	case s.statSampleReal:
		// Real captured values → a plain EXPLAIN, so the planner sees real data.
		explainHdr = " explain (real plan) "
	}
	b.WriteString("\n  " + styleHeader.Render(explainHdr) + "\n")
	switch {
	case !explainable:
		b.WriteString("    " + mu("EXPLAIN is only available for SELECT/DML statements") + "\n")
	case s.statExplaining:
		b.WriteString("    " + mu("running EXPLAIN…") + "\n")
	case s.statExplainErr != nil:
		b.WriteString("    " + styleErr.Render(s.statExplainErr.Error()) + "\n")
	case s.statExplain != "":
		for _, line := range m.colorizeExplain(s.statExplain, s.statExplainAnalyze) {
			b.WriteString("    " + line + "\n")
		}
	default:
		b.WriteString("    " + mu("no plan available") + "\n")
	}

	// EXPLAIN ANALYZE affordance. ANALYZE executes the query for real, so it's
	// offered only for read-only SELECT shapes and only once a sample call (with
	// synthesized literals filling the $n) is available to actually run.
	if explainable && !s.statExplaining && pg.ReadOnlyQuery(q.Query) && s.statSampleCall != "" {
		b.WriteString("    " + mu("press ") + styleBadge.Render("Enter") +
			mu(" to run EXPLAIN (ANALYZE, VERBOSE, BUFFERS) — ") +
			styleErr.Render("executes the query for real") + "\n")
		b.WriteString("    " + mu("press ") + styleBadge.Render("E") +
			mu(" to execute it and show the result rows — ") +
			styleErr.Render("executes the query for real") + "\n")
	}

	// Captured-values affordance: only when pg_qualstats is present, since that's
	// the only source of real per-value data to browse.
	if explainable && s.statQualstats {
		b.WriteString("    " + mu("press ") + styleBadge.Render("p") +
			mu(" to browse the real values pg_qualstats captured for this query") + "\n")
	}

	// The detail panel has no list to page through, so it scrolls as a single
	// body: s.offset is the first visible line. Long sample calls / EXPLAIN
	// output overflow short terminals otherwise, hiding the header off-screen.
	return scrollWindow(b.String(), &s.offset, height)
}

// scrollWindow renders a height-line slice of body starting at *offset, clamping
// *offset to the last full screen (writing the clamp back so the key handler can
// over-scroll and let the view settle it) and padding short content to height so
// the help row stays pinned.
func scrollWindow(body string, offset *int, height int) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	*offset = max(0, min(*offset, len(lines)-height))
	end := min(*offset+height, len(lines))
	var b strings.Builder
	for _, ln := range lines[*offset:end] {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	for i := end - *offset; i < height; i++ {
		b.WriteByte('\n')
	}
	return b.String()
}

// --- captured values (levelStatementSamples) ---

// renderStatementSamples lists the real predicate constants pg_qualstats
// captured for the query, most-frequent first, with the occurrence count drawn
// as a bar so the value distribution (the "pattern") reads at a glance. When an
// EXPLAIN ANALYZE has been run for the highlighted value (Enter), its plan is
// shown below the list, reusing the detail view's heat-coloured rendering.
func (m *Model) renderStatementSamples(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	qid := int64(0)
	if s.statDetail != nil {
		qid = s.statDetail.QueryID
	}
	b.WriteString("  " + styleSelected.Render(fmt.Sprintf("captured values · query %d", qid)) + "\n")
	b.WriteString("  " + mu("real predicate constants sampled by pg_qualstats — most frequent first") + "\n\n")
	used := 4 // the 4 lines written above (incl. the trailing blank)

	// Split the remaining height: when a plan is on screen it takes the lower
	// half, otherwise the list fills everything.
	explainOn := s.statExplaining || s.statExplain != "" || s.statExplainErr != nil
	listH := height - used
	if explainOn {
		listH = (height - used) / 2
	}
	if listH < 1 {
		listH = 1
	}

	vis := s.visibleIndexes()
	var maxOcc int64
	for _, vi := range vis {
		if sz := s.items[vi].size; sz > maxOcc {
			maxOcc = sz
		}
	}
	barW := m.barWidth(s)
	s.offset, _ = viewportRange(s.cursor, s.offset, listH, len(vis))
	end := min(s.offset+listH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vi]
		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		cells := 0
		if maxOcc > 0 {
			cells = int(float64(it.size) / float64(maxOcc) * float64(barW))
		}
		bar := paintBar(barW, barSegment{cells: cells, style: styleBar})
		count := padRight(formatRows(it.size)+"×", 8)
		b.WriteString(cursor + bar + "  " + mu(count) + "  " + name + "\n")
	}
	for i := end - s.offset; i < listH; i++ {
		b.WriteString("\n")
	}

	if explainOn {
		b.WriteString("\n  " + styleHeader.Render(" explain (analyze · verbose · buffers) ") + "\n")
		switch {
		case s.statExplaining:
			b.WriteString("    " + mu("running EXPLAIN ANALYZE…") + "\n")
		case s.statExplainErr != nil:
			b.WriteString("    " + styleErr.Render(s.statExplainErr.Error()) + "\n")
		default:
			for _, line := range m.colorizeExplain(s.statExplain, true) {
				b.WriteString("    " + line + "\n")
			}
		}
	} else if s.statDetail != nil && pg.ReadOnlyQuery(s.statDetail.Query) {
		b.WriteString("    " + mu("press ") + styleBadge.Render("Enter") +
			mu(" to EXPLAIN (ANALYZE) the highlighted value — ") +
			styleErr.Render("executes the query for real") + "\n")
	}

	// Pad to fill the content area so the help row stays pinned.
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// --- snapshots browser (levelSnapshots) ---

// snapshotLabel is the row text for a snapshot, also the fuzzy-filter key. It
// leads with the capture time (newest-first list) and the database it covers.
func snapshotLabel(meta pg.SnapshotMeta) string {
	return meta.CapturedAt.Local().Format("2006-01-02 15:04:05") + " · " + meta.Database
}

// renderStatementSnapshots lists the on-disk snapshots as a timeline range
// picker. The query count is drawn as a bar so the relative size of each capture
// reads at a glance; each row shows its age and database. The applied window's
// endpoints carry ◀ start / ◀ end markers and the header sums the window up
// (live with its refresh cadence, or frozen). Enter picks an endpoint: the first
// pick spans pick → now (live); with a start already applied, picking another
// row spans the time-ordered range between the two — frozen unless an endpoint
// is "now". Snapshots from another server/database are flagged, not loadable.
func (m *Model) renderStatementSnapshots(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	st := m.findLevel(levelStatements)
	curDB := ""
	if st != nil {
		curDB = st.db
	}

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("query snapshots") + mu("  ·  "+m.snapshotDir) + "\n")
	b.WriteString("  " + m.renderSnapshotWindowSummary(st) + "\n")
	b.WriteString("  " + mu("Enter picks an endpoint (older=start, newer=end) · ") +
		styleBadge.Render("D") + mu(" delete · ") + styleBadge.Render("esc") + mu(" back") + "\n\n")
	used := 5

	if m.pendingDeleteSnap != "" {
		b.WriteString("  " + styleErr.Render("delete this snapshot? ") + mu("press ") +
			styleBadge.Render("y") + mu(" to confirm, any other key cancels") + "\n")
		used++
	}

	if len(s.items) == 0 {
		b.WriteString("  " + mu("no snapshots yet — press ") + styleBadge.Render("S") +
			mu(" in the queries view to save one") + "\n")
		for i := strings.Count(b.String(), "\n"); i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	listH := max(height-used, 1)
	// The applied window's endpoints, for the ◀ start / ◀ end row markers.
	startPath, endPath := "", ""
	if st != nil {
		startPath, endPath = m.appliedWindowPaths(st, s)
	}
	vis := s.visibleIndexes()
	var maxCount int64
	for _, vi := range vis {
		if sz := s.items[vi].size; sz > maxCount {
			maxCount = sz
		}
	}
	barW := m.barWidth(s)
	s.offset, _ = viewportRange(s.cursor, s.offset, listH, len(vis))
	end := min(s.offset+listH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vi]
		anchor := it.snapPath == snapNow || it.snapPath == snapReset || it.snapPath == snapSession
		meta, _ := metaByPath(s.statSnapMetas, it.snapPath)
		// Anchors carry no server/db identity — they always apply to the current
		// database, so they're never flagged incompatible.
		compatible := anchor || (meta.Target == m.target && meta.Database == curDB)

		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		cells := 0
		if maxCount > 0 {
			cells = int(float64(it.size) / float64(maxCount) * float64(barW))
		}
		bar := paintBar(barW, barSegment{cells: cells, style: styleBar})
		count := padRight(formatRows(it.size)+"q", 7)
		age := padRight(m.snapshotAge(s, it.snapPath, meta.CapturedAt), 9)

		var tags []string
		switch it.snapPath {
		case startPath:
			tags = append(tags, styleSelected.Render("◀ start"))
		case endPath:
			tags = append(tags, styleSelected.Render("◀ end"))
		}
		if !compatible {
			tags = append(tags, styleErr.Render("other server/db"))
		}
		// Only real snapshots have a backing file to show; anchors are virtual.
		row := cursor + bar + "  " + mu(count) + "  " + mu(age) + "  " + name
		if !anchor {
			row += "  " + mu(filepath.Base(it.snapPath))
		}
		if len(tags) > 0 {
			row += "  " + strings.Join(tags, " ")
		}
		b.WriteString(row + "\n")
	}
	for i := end - s.offset; i < listH; i++ {
		b.WriteString("\n")
	}

	// Pad to fill the content area so the help row stays pinned.
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// snapshotAge renders the age column for a browser row. The synthetic anchors
// have no meta CapturedAt, so their reference time is derived: "now" is live,
// "session start" dates from the recorded session baseline, and "since last
// reset" from the live pg_stat_statements stats_reset (unknown until sampled).
func (m *Model) snapshotAge(s *screen, path string, capturedAt time.Time) string {
	switch path {
	case snapNow:
		return "now"
	case snapSession:
		if st := m.findLevel(levelStatements); st != nil && !st.statSessionStart.IsZero() {
			return relativeAge(time.Since(st.statSessionStart))
		}
		return "—"
	case snapReset:
		if s.statLiveReset.IsZero() {
			return "—"
		}
		return relativeAge(time.Since(s.statLiveReset))
	default:
		return relativeAge(time.Since(capturedAt))
	}
}

// renderSnapshotWindowSummary is the browser header's one-line description of
// the applied window: "window: <start> → <end> · live · refresh 2s" (or
// "· frozen" when the end is a snapshot, where nothing re-samples).
func (m *Model) renderSnapshotWindowSummary(st *screen) string {
	mu := styleMuted.Render
	if st == nil {
		return mu("window: —")
	}
	start := "since " + st.statBaselineAt.Format("15:04:05") // a fresh R re-base
	switch {
	case st.statCumulative:
		start = "since last reset"
	case st.statBaseSnap != nil:
		start = st.statBaseSnap.CapturedAt.Local().Format("2006-01-02 15:04:05")
	case !st.statSessionStart.IsZero() && st.statBaselineAt.Equal(st.statSessionStart):
		start = "session start"
	}
	end, mode := "now", "live · refresh "+m.refreshLabel()
	if st.statEndSnap != nil {
		end = st.statEndSnap.CapturedAt.Local().Format("2006-01-02 15:04:05")
		mode = "frozen"
	}
	return mu("window: ") + styleSelected.Render(start) + mu(" → ") +
		styleSelected.Render(end) + mu("  ·  "+mode)
}

// wrapDetail hard-wraps text to the detail panel's usable width (terminal minus
// the 4-column indent), so long query/sample text doesn't clip the help row.
func (m *Model) wrapDetail(text string) []string {
	w := max(m.width-4, 8)
	var out []string
	r := []rune(text)
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	out = append(out, string(r))
	return out
}

// clipDetail truncates one line to the usable detail width (EXPLAIN output is
// kept on single lines rather than wrapped, to preserve plan-tree indentation).
func (m *Model) clipDetail(line string) string {
	w := m.width - 4
	if w < 8 || lipgloss.Width(line) <= w {
		return line
	}
	r := []rune(line)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
