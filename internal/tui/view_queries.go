package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

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

// planTimeMetric renders the detail-view plan-time line, distinguishing a real
// zero from "not collected" (pg_stat_statements.track_planning off).
func planTimeMetric(q pg.QueryStat, trackPlanning bool, mu func(...string) string) string {
	if !trackPlanning {
		return "—" + mu("  (track_planning off — not collected)")
	}
	return fmtMs(q.TotalPlanTime) + " ms" + mu(fmt.Sprintf("  (%s plans)", formatRows(q.Plans)))
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
			mu("over the last ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" (since "+s.statBaselineAt.Format("2006-01-02 15:04:05")+" snapshot) · live") +
			mu(fmt.Sprintf("  ·  %d queries  ·  refresh %s  ·  t cadence · C columns · R for live · Enter for detail",
				len(s.statRows), m.refreshLabel()))
	default:
		elapsed := max(s.statSampledAt.Sub(s.statBaselineAt), 0)
		line = "  " + styleHeader.Render(" queries ") + "  " +
			mu("over the last ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" (since "+s.statBaselineAt.Format("15:04:05")+")") +
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

// renderStatementsInfo is the ? overlay for the top-queries tool: it explains
// the window model (which is the subtle part — pg_stat_statements has no time
// axis) and every column.
func (m *Model) renderStatementsInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "Top queries reference")

	b.WriteString("  " + styleHeader.Render(" the window ") + "  " +
		mu("why numbers start at zero and grow") + "\n")
	b.WriteString("    " + mu("pg_stat_statements counters are cumulative since the last reset — they have no time axis.") + "\n")
	b.WriteString("    " + mu("pgdu snapshots them when you open this tool (the baseline) and shows the delta against it,") + "\n")
	b.WriteString("    " + mu("so the table is everything that ran ‘since you opened it’. "+m.refreshSentence()) + "\n")
	b.WriteString("    " + mu("press ") + styleBadge.Render("R") + mu(" to drop the baseline and restart the window. Stats are scoped to the current database.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("all sortable — ") + styleBadge.Render("←") + mu("/") + styleBadge.Render("→") +
		mu(" switch the column, ") + styleBadge.Render("r") +
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
	col("rows/call", "average rows per call (rows ÷ calls); opt-in — ‘—’ when no calls")
	col("hit", "shared blocks served from cache (shared_blks_hit)")
	col("miss", "shared blocks read from disk/OS (shared_blks_read)")
	col("hit%", "cache hit ratio: hit ÷ (hit+miss); ‘—’ when the query touched no blocks")
	col("blk/row", "shared blocks (hit+read) per row — work per result row; lower is better; ‘—’ when 0 rows")
	col("io_ms", "time in block read+write I/O (needs track_io_timing for non-zero values)")
	col("wal", "WAL bytes generated by the query")
	col("table", "the main table parsed from the statement (FROM/UPDATE/INTO) — d describes it")
	col("T", "command type: S select · SL select…for update · L advisory lock · I insert · U update · D delete · M merge · T begin/commit · P prepare")
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
		styleBadge.Render("r") + mu(" reset · ") +
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
