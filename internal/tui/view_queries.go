package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"pgdu/internal/pg"
)

// statementColumns is the projected column schema for the current visibility set
// and track_planning state, derived from stmtColumnRegistry. Used on the first
// (empty) load before any rows exist; buildStatementItems returns the same
// schema once rows arrive.
func (m *Model) statementColumns(trackPlanning bool) []pg.DiagColumn {
	return diagColumnsFrom(stmtSpec.visibleCols(&m.stmtTable, stmtCtx{trackPlanning: trackPlanning}))
}

// buildStatementItems converts window-delta QueryStats into generic-table rows
// (item.data = []pg.DiagCell) over the currently visible columns. windowMs is
// the whole window's exec time — the time% denominator — passed in rather than
// summed here because rows may be the list narrowed to one group, whose shares
// must still read against the window. It returns the items, the projected
// column descriptors (parallel to each item's cells), and the cells for a
// pinned "← Sum" footer totalling the rows given (nil when there are none).
func (m *Model) buildStatementItems(rows []pg.QueryStat, windowMs float64, trackPlanning bool) ([]item, []stmtColDesc, []pg.DiagCell) {
	ctx := stmtCtx{windowMs: windowMs, trackPlanning: trackPlanning}
	descs := stmtSpec.visibleCols(&m.stmtTable, ctx)

	items := make([]item, 0, len(rows))
	for _, q := range rows {
		items = append(items, item{
			name:        flattenQuery(q.Query),
			data:        cellsFor(descs, q, ctx),
			hasChildren: true, // Enter → query detail
			statQueryID: q.QueryID,
		})
	}
	if len(rows) == 0 {
		return items, descs, nil
	}
	// Build the footer over a summed QueryStat so the ratio columns come out as
	// true pooled totals for free: mean_ms = Σtotal_ms÷Σcalls, hit% the weighted
	// ratio, blk/row Σblocks÷Σrows, and time% exactly 100 (Σtotal_ms == windowMs)
	// on the unnarrowed list.
	total := cellsFor(descs, sumQueryStats(rows), ctx)
	labelStmtFooter(descs, total)
	return items, descs, total
}

// sumQueryStats totals every additive counter across rows into one aggregate
// QueryStat (identity fields left zero); see addQueryStat for the fold.
func sumQueryStats(rows []pg.QueryStat) pg.QueryStat {
	var t pg.QueryStat
	for _, q := range rows {
		addQueryStat(&t, q)
	}
	return t
}

// windowExecMs is the summed exec time of the whole window — the time%
// denominator every view shares.
func windowExecMs(rows []pg.QueryStat) float64 {
	var ms float64
	for _, q := range rows {
		ms += q.TotalExecTime
	}
	return ms
}

func diagNum(display string, n float64) pg.DiagCell {
	return pg.DiagCell{Display: display, Num: n, HasNum: true}
}

// flattenQuery collapses all internal whitespace runs to single spaces so a
// multi-line normalized query renders as one table row.
func flattenQuery(q string) string {
	return strings.Join(strings.Fields(q), " ")
}

// mainTableDisplay is the "table" column label for a query: pg.MainTable with a
// leading public. schema stripped, since public is the default and unqualified
// reads cleaner (public.production → production). Only the display is trimmed —
// the d/u actions still resolve against the fully-qualified pg.MainTable so
// to_regclass isn't left to guess the schema from search_path.
func mainTableDisplay(query string) string {
	return strings.TrimPrefix(pg.MainTable(query), "public.")
}

// joinedTablesLines is the detail view's `joins` value: the other relations the
// statement reads (pg.JoinedTables), public. stripped per name for the same
// reason mainTableDisplay strips it and separated like the neighbouring
// multi-value metric rows, wrapped to width. The break falls between names, not
// inside the separator, so a continuation line never begins or ends on a
// dangling "·". Nil when the statement joins nothing.
func joinedTablesLines(query string, width int) []string {
	joined := pg.JoinedTables(query)
	if len(joined) == 0 {
		return nil
	}
	const sep = " · "
	var lines []string
	var cur string
	for _, t := range joined {
		name := strings.TrimPrefix(t, "public.")
		switch {
		case cur == "":
			cur = name
		case utf8.RuneCountInString(cur+sep+name) <= width:
			cur += sep + name
		default:
			lines = append(lines, cur)
			cur = name
		}
	}
	return append(lines, cur)
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
	badge := styleHeader.Render(" " + s.stat.view.label() + " ")
	if s.stat.baselineAt.IsZero() {
		return "  " + badge + "  " + mu("opening window — run some queries…")
	}
	// The window span and the refresh state are the same whichever view is up;
	// the count names what the view's rows are, and the key hints name the
	// actions that apply to it.
	var line string
	switch {
	case s.stat.endSnap != nil:
		// Frozen A→B diff between two snapshots: no live "now", so the window is the
		// fixed span between the two capture times and there's nothing to refresh.
		line = "  " + badge + "  " +
			styleSelected.Render(s.stat.baselineAt.Format("15:04:05")) + mu(" → ") +
			styleSelected.Render(s.stat.sampledAt.Format("15:04:05")) +
			mu("  ·  snapshot diff (frozen)  ·  "+m.statementsCount(s)+"  ·  "+m.statementsHints(s, "R for live"))
	case s.stat.baseSnap != nil:
		// Disk baseline, live end: the window runs from the snapshot's capture time
		// up to the latest live sample.
		elapsed := max(s.stat.sampledAt.Sub(s.stat.baselineAt), 0)
		line = "  " + badge + "  " +
			mu("over the last ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" (since "+s.stat.baselineAt.Format("2006-01-02 15:04:05")+" snapshot) · live") +
			mu(fmt.Sprintf("  ·  %s  ·  refresh %s  ·  %s", m.statementsCount(s), m.refreshLabel(),
				m.statementsHints(s, "t cadence · C columns · R for live")))
	default:
		elapsed := max(s.stat.sampledAt.Sub(s.stat.baselineAt), 0)
		line = "  " + badge + "  " +
			mu("over the last ") + styleSelected.Render(fmtDuration(elapsed)) +
			mu(" (since "+s.stat.baselineAt.Format("15:04:05")+")") +
			mu(fmt.Sprintf("  ·  %s  ·  refresh %s  ·  %s", m.statementsCount(s), m.refreshLabel(),
				m.statementsHints(s, "t cadence · C columns · R resets · S saves · L loads")))
	}
	if !s.stat.trackPlanning {
		// The planning-time column is hidden (it would always read 0); point the
		// user at the setting that turns planning-time collection on.
		line += "\n  " + mu("planning time column hidden — ") + styleBadge.Render("track_planning off") +
			mu(": ALTER SYSTEM SET pg_stat_statements.track_planning = on; SELECT pg_reload_conf();")
	}
	return line
}

// statementsCount names the view's rows for the header: the window's
// statement count on the list, "N tables of M queries" on a roll-up, and
// "table production · 61 of 3524 queries · esc widens" while the list is
// narrowed to one group.
func (m *Model) statementsCount(s *screen) string {
	all := len(s.stat.rows)
	switch {
	case s.stat.view.grouped():
		noun := s.stat.view.noun()
		if len(s.items) != 1 {
			noun += "s"
		}
		return fmt.Sprintf("%d %s of %d queries", len(s.items), noun, all)
	case s.stat.group != nil:
		return fmt.Sprintf("%s %s · %d of %d queries · esc widens",
			s.stat.group.view.noun(), s.stat.group.key, s.narrowedCount(), all)
	}
	return fmt.Sprintf("%d queries", all)
}

// statementsHints is the header's key list: the tab target first (where the
// next view leads), the window keys the caller passes for its case, and what
// Enter does on the view's rows.
func (m *Model) statementsHints(s *screen, window string) string {
	enter := "↵ for detail"
	if s.stat.view.grouped() {
		enter = "↵ narrows"
	}
	return "tab " + s.stat.view.next().label() + " · " + window + " · " + enter
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
	b.WriteString("    " + mu("pgdu shows the delta against a baseline you pick when the tool opens: ") + styleSelected.Render("session start") +
		mu(" (the default —") + "\n")
	b.WriteString("    " + mu("a fresh sample, so the table is everything that ran ‘since you opened it’), a saved snapshot, or") + "\n")
	b.WriteString("    " + mu("‘since last reset’ (the raw cumulative counters). "+m.refreshSentence()) + "\n")
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

	b.WriteString("  " + styleHeader.Render(" roll-ups ") + "  " +
		mu("press ") + styleBadge.Render("tab") + mu(" — the same window grouped by table, then by type") + "\n")
	b.WriteString("    " + mu("One row per main table (or per command type) with the same metrics pooled over its statements,") + "\n")
	b.WriteString("    " + mu("a queries count, and a bar on whichever column you sort by — the heavy tables per metric at a glance.") + "\n")
	b.WriteString("    " + styleBadge.Render("↵") + mu(" on a row narrows the list to that group's statements (the header says so), ") +
		styleBadge.Render("esc") + mu(" widens it again;") + "\n")
	b.WriteString("    " + mu("esc on a roll-up returns to the list. ") + styleBadge.Render("C") +
		mu(" picks the roll-ups' own columns; d / u work on a table row.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" describe ") + "  " +
		mu("press ") + styleBadge.Render("d") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Opens the table's \\d view — columns, indexes and constraints — so you can see, e.g.,") + "\n")
	b.WriteString("    " + mu("whether the predicate columns of a slow query are actually indexed.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" disk usage ") + "  " +
		mu("press ") + styleBadge.Render("u") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Jumps to the main table's disk-usage breakdown (heap, indexes, toast, free space) in the") + "\n")
	b.WriteString("    " + mu("size explorer — esc returns here. Nothing happens when the statement has no resolvable table.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" detail ") + "  " +
		mu("press ") + styleBadge.Render("↵") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Shows the full text, the same metrics, the other tables the statement joins to, and a") + "\n")
	b.WriteString("    " + mu("‘sample call’ with its EXPLAIN, run automatically.") + "\n")
	b.WriteString("    " + mu("For read-only SELECTs, ") + styleBadge.Render("↵") +
		mu(" runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) and ") + styleBadge.Render("E") +
		mu(" executes the query and shows the result rows — both execute the query.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" real parameters ") + "  " +
		mu("pg_qualstats or the server log — values are never guessed") + "\n")
	b.WriteString("    " + mu("pg_stat_statements normalizes constants away, and pgdu never invents them: the sample call is") + "\n")
	b.WriteString("    " + mu("built only from values that really ran — the constants ") + styleBadge.Render("pg_qualstats") +
		mu(" captured (install it in") + "\n")
	b.WriteString("    " + mu("shared_preload_libraries with pg_qualstats.track_constants=on), or the bind values of a call the") + "\n")
	b.WriteString("    " + mu("server logged with its parameters (log_min_duration_statement + log_parameter_max_length).") + "\n")
	b.WriteString("    " + mu("Until one source covers every placeholder no sample call is shown, EXPLAIN runs as GENERIC_PLAN —") + "\n")
	b.WriteString("    " + mu("the plan for the parameterized query, without values — and ") + styleBadge.Render("↵") + mu(" / ") +
		styleBadge.Render("E") + mu(" are unavailable. Press ") + styleBadge.Render("p") + "\n")
	b.WriteString("    " + mu("in the detail view to browse all captured values by frequency (the value pattern); ") + styleBadge.Render("↵") + "\n")
	b.WriteString("    " + mu("there EXPLAIN-ANALYZEs the highlighted one.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" snapshots ") + "  " +
		mu("capture the window to disk and diff it later") + "\n")
	b.WriteString("    " + mu("Press ") + styleBadge.Render("S") +
		mu(" to dump the current pg_stat_statements counters to a file (under ~/.local/state/pgdu/snapshots") + "\n")
	b.WriteString("    " + mu("by default; --snapshot-dir to change). Press ") + styleBadge.Render("L") +
		mu(" to browse saved snapshots — a timeline range picker") + "\n")
	b.WriteString("    " + mu("whose ") + styleSelected.Render("◀ start") + mu(" / ") + styleSelected.Render("◀ end") +
		mu(" markers show the applied window (session start → now by default). ") + styleBadge.Render("↵") + "\n")
	b.WriteString("    " + mu("picks an endpoint: the first pick spans ‘pick → now’ (live); with a start applied, ↵ on") + "\n")
	b.WriteString("    " + mu("another row spans the range between the two, frozen — no re-sampling — unless an endpoint") + "\n")
	b.WriteString("    " + mu("is ‘now’. ") + styleBadge.Render("D") + mu(" deletes a file.") + "\n")
	b.WriteString("    " + mu("Press ") + styleBadge.Render("R") +
		mu(" to drop a loaded snapshot and return to the live window. Snapshots invalidated by a") + "\n")
	b.WriteString("    " + mu("counter reset since their capture are left out of the list — they can't serve as a baseline.") + "\n")
	b.WriteString("    " + mu("The list also carries three virtual anchors you can pick as endpoints: ") +
		styleSelected.Render("now") + mu(" (live), ") + styleSelected.Render("session start") + "\n")
	b.WriteString("    " + mu("(the window from when you opened the tool) and ") + styleSelected.Render("since last reset") +
		mu(" (everything since the server's last reset).") + "\n")
	b.WriteString("    " + mu("The same browser greets you when the tool opens, minus ‘now’: ↵ there picks the base of the") + "\n")
	b.WriteString("    " + mu("live window (session start is preselected), Esc leaves the tool without loading the table.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" slow query log ") + "  " +
		mu("the individual executions behind these aggregates") + "\n")
	b.WriteString("    " + mu("Press ") + styleBadge.Render("l") +
		mu(" to open the log analyzer on the current server log, narrowed to its slow-query lines") + "\n")
	b.WriteString("    " + mu("(log_min_duration_statement), slowest first — each execution with its duration and the") + "\n")
	b.WriteString("    " + mu("parameters it ran with. f there widens back to every category, tab switches panes.") + "\n")

	return padInfo(&b, height)
}

// --- column config overlay (C on levelStatements) ---

// renderColumnConfig draws the htop-style column picker: one checkbox row per
// registry column, with the current cursor highlighted. Default-on and opt-in
// columns are toggled with space/Enter; the mandatory query column and the
// planning columns when track_planning is off are shown but not toggleable.
func (m *Model) renderColumnConfig(s *screen, height int) string {
	ctx := stmtCtx{trackPlanning: s.stat.trackPlanning}
	if v := s.stat.view; v.grouped() {
		return stmtGroupSpec(v).renderConfig(m, &m.stmtGroupTable, ctx, height)
	}
	return stmtSpec.renderConfig(m, &m.stmtTable, ctx, height)
}
