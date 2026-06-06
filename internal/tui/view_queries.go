package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// Column indices for the top-queries table. The order is numeric-first with the
// (wide) query text last, so — with no bar column — renderDiagResult lets the
// query column grow into the remaining terminal width.
const (
	colStmtCalls = iota
	colStmtRows
	colStmtTotalMs
	colStmtMeanMs
	colStmtPlanMs
	colStmtPctTime
	colStmtHit
	colStmtMiss
	colStmtHitPct
	colStmtIOms
	colStmtWAL
	colStmtQuery
)

// statementColumns is the fixed schema of the top-queries table, reusing the
// generic diagnostic-table column kinds so renderDiagResult and the per-column
// cycle-sort work unchanged. When track_planning is off the plan_ms column is
// dropped entirely (it would always read 0) — statementCells drops the matching
// cell so columns and cells stay parallel.
func statementColumns(trackPlanning bool) []pg.DiagColumn {
	cols := []pg.DiagColumn{
		{Name: "calls", Kind: pg.DiagInt},
		{Name: "rows", Kind: pg.DiagInt},
		{Name: "total_ms", Kind: pg.DiagFloat},
		{Name: "mean_ms", Kind: pg.DiagFloat},
		{Name: "plan_ms", Kind: pg.DiagFloat},
		{Name: "%time", Kind: pg.DiagPercent},
		{Name: "hit", Kind: pg.DiagInt},
		{Name: "miss", Kind: pg.DiagInt},
		{Name: "hit%", Kind: pg.DiagPercentGraded},
		{Name: "io_ms", Kind: pg.DiagFloat},
		{Name: "wal", Kind: pg.DiagBytes},
		{Name: "query", Kind: pg.DiagText},
	}
	if !trackPlanning {
		cols = slices.Delete(cols, colStmtPlanMs, colStmtPlanMs+1)
	}
	return cols
}

// buildStatementItems converts window-delta QueryStats into generic-table rows
// (item.data = []pg.DiagCell). It returns the items and the summed window exec
// time, which is the denominator for the %time column and is carried to the
// detail view.
func buildStatementItems(rows []pg.QueryStat, trackPlanning bool) ([]item, float64) {
	var windowMs float64
	for _, q := range rows {
		windowMs += q.TotalExecTime
	}
	items := make([]item, 0, len(rows))
	for _, q := range rows {
		items = append(items, item{
			name:        flattenQuery(q.Query),
			data:        statementCells(q, windowMs, trackPlanning),
			statQueryID: q.QueryID,
		})
	}
	return items, windowMs
}

func statementCells(q pg.QueryStat, windowMs float64, trackPlanning bool) []pg.DiagCell {
	cells := make([]pg.DiagCell, colStmtQuery+1)
	cells[colStmtCalls] = diagNum(formatRows(q.Calls), float64(q.Calls))
	cells[colStmtRows] = diagNum(formatRows(q.Rows), float64(q.Rows))
	cells[colStmtTotalMs] = diagNum(fmtMs(q.TotalExecTime), q.TotalExecTime)
	cells[colStmtMeanMs] = diagNum(fmtMs(q.MeanTime()), q.MeanTime())
	cells[colStmtPlanMs] = diagNum(fmtMs(q.TotalPlanTime), q.TotalPlanTime)

	pct := 0.0
	if windowMs > 0 {
		pct = q.TotalExecTime / windowMs * 100
	}
	cells[colStmtPctTime] = diagNum(fmt1(pct), pct)

	cells[colStmtHit] = diagNum(formatRows(q.SharedBlksHit), float64(q.SharedBlksHit))
	cells[colStmtMiss] = diagNum(formatRows(q.SharedBlksRead), float64(q.SharedBlksRead))
	if hr, ok := q.HitRatio(); ok {
		cells[colStmtHitPct] = diagNum(fmt1(hr), hr)
	} else {
		cells[colStmtHitPct] = pg.DiagCell{Display: "—"}
	}

	cells[colStmtIOms] = diagNum(fmtMs(q.IOTime()), q.IOTime())
	// DiagBytes columns are rendered via humanize.Bytes from Num; Display is a
	// fallback only.
	cells[colStmtWAL] = pg.DiagCell{Display: humanize.Bytes(q.WALBytes), Num: float64(q.WALBytes), HasNum: true}
	cells[colStmtQuery] = pg.DiagCell{Display: flattenQuery(q.Query)}
	if !trackPlanning {
		// Drop the plan_ms cell to stay parallel with statementColumns, which
		// omits the column when planning time isn't being collected.
		cells = slices.Delete(cells, colStmtPlanMs, colStmtPlanMs+1)
	}
	return cells
}

func diagNum(display string, n float64) pg.DiagCell {
	return pg.DiagCell{Display: display, Num: n, HasNum: true}
}

// flattenQuery collapses all internal whitespace runs to single spaces so a
// multi-line normalized query renders as one table row.
func flattenQuery(q string) string {
	return strings.Join(strings.Fields(q), " ")
}

// fmtFloat renders a number with up to 2 decimals, trailing zeros stripped.
func fmtFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
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
	elapsed := max(s.statSampledAt.Sub(s.statBaselineAt), 0)
	line := "  " + styleHeader.Render(" queries ") + "  " +
		mu("window ") + styleSelected.Render(fmtDuration(elapsed)) +
		mu(" since "+s.statBaselineAt.Format("15:04:05")) +
		mu(fmt.Sprintf("  ·  %d queries  ·  refresh %s  ·  R resets · Enter for detail",
			len(s.statRows), statementsRefreshInterval))
	if !s.statTrackPlanning {
		// The planning-time column is hidden (it would always read 0); point the
		// user at the setting that turns planning-time collection on.
		line += "\n  " + mu("planning time column hidden — ") + styleBadge.Render("track_planning off") +
			mu(": ALTER SYSTEM SET pg_stat_statements.track_planning = on; SELECT pg_reload_conf();")
	}
	return line
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
	b.WriteString("    " + mu("so the table is everything that ran ‘since you opened it’. It re-samples every "+statementsRefreshInterval.String()+";") + "\n")
	b.WriteString("    " + mu("press ") + styleBadge.Render("R") + mu(" to drop the baseline and restart the window. Stats are scoped to the current database.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + "  " +
		mu("all sortable — ") + styleBadge.Render("s") + mu(" cycles the column, ") + styleBadge.Render("r") + mu(" reverses") + "\n")
	col := func(name, desc string) {
		b.WriteString("    " + padRight(name, 9) + mu(desc) + "\n")
	}
	col("calls", "times the query was executed in the window")
	col("rows", "rows returned / affected across those calls")
	col("total_ms", "total execution time in the window (the default sort — your hottest queries)")
	col("mean_ms", "average execution time per call (total_ms ÷ calls)")
	col("plan_ms", "total planning time — only shown when track_planning is on (hidden otherwise)")
	col("%time", "share of the window's total execution time spent in this query")
	col("hit", "shared blocks served from cache (shared_blks_hit)")
	col("miss", "shared blocks read from disk/OS (shared_blks_read)")
	col("hit%", "cache hit ratio: hit ÷ (hit+miss); ‘—’ when the query touched no blocks")
	col("io_ms", "time in block read+write I/O (needs track_io_timing for non-zero values)")
	col("wal", "WAL bytes generated by the query")
	col("query", "the normalized statement text ($1, $2 … in place of constants)")
	b.WriteString("\n")

	b.WriteString("  " + styleHeader.Render(" detail ") + "  " +
		mu("press ") + styleBadge.Render("Enter") + mu(" on a row") + "\n")
	b.WriteString("    " + mu("Shows the full text, the same metrics, a ‘sample call’ and its EXPLAIN, run automatically (") +
		styleBadge.Render("x") + mu(" re-runs it).") + "\n")
	b.WriteString("    " + mu("For read-only SELECTs, ") + styleBadge.Render("Enter") +
		mu(" runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) — this executes the query.") + "\n\n")

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
		mu(" there EXPLAIN-ANALYZEs the highlighted one.") + "\n")

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
		{"temp blocks", fmt.Sprintf("%s read · %s written", formatRows(q.TempBlksRead), formatRows(q.TempBlksWritten))},
		{"WAL", fmt.Sprintf("%s · %s records · %s FPI", humanize.Bytes(q.WALBytes), formatRows(q.WALRecords), formatRows(q.WALFPI))},
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
		b.WriteString("    " + mu("press ") + styleBadge.Render("x") + mu(" to EXPLAIN this query") + "\n")
	}

	// EXPLAIN ANALYZE affordance. ANALYZE executes the query for real, so it's
	// offered only for read-only SELECT shapes and only once a sample call (with
	// synthesized literals filling the $n) is available to actually run.
	if explainable && !s.statExplaining && pg.ReadOnlyQuery(q.Query) && s.statSampleCall != "" {
		b.WriteString("    " + mu("press ") + styleBadge.Render("Enter") +
			mu(" to run EXPLAIN (ANALYZE, VERBOSE, BUFFERS) — ") +
			styleErr.Render("executes the query for real") + "\n")
	}

	// Captured-values affordance: only when pg_qualstats is present, since that's
	// the only source of real per-value data to browse.
	if explainable && s.statQualstats {
		b.WriteString("    " + mu("press ") + styleBadge.Render("p") +
			mu(" to browse the real values pg_qualstats captured for this query") + "\n")
	}

	// Pad to fill the content area so the help row stays pinned.
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
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
