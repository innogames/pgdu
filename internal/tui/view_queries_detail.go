package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// --- query detail (levelStatementDetail) ---

func (m *Model) renderStatementDetail(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	q := s.stat.detail
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
	if s.stat.windowExecMs > 0 {
		pct = q.TotalExecTime / s.stat.windowExecMs * 100
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
		{"plan time", planTimeMetric(*q, s.stat.trackPlanning, mu)},
		{"shared hit ratio", hitStr},
		{"I/O time", fmtMs(q.IOTime()) + " ms"},
		{"shared blocks", fmt.Sprintf("%s hit · %s read (miss) · %s dirtied · %s written",
			formatRows(q.SharedBlksHit), formatRows(q.SharedBlksRead),
			formatRows(q.SharedBlksDirtied), formatRows(q.SharedBlksWritten))},
		{"blocks/row", bprStr + mu("  (hit+read ÷ rows)")},
		{"temp blocks", fmt.Sprintf("%s read · %s written", formatRows(q.TempBlksRead), formatRows(q.TempBlksWritten))},
		{"WAL", fmt.Sprintf("%s · %s records · %s FPI", humanize.Bytes(q.WALBytes), formatRows(q.WALRecords), formatRows(q.WALFPI))},
	}
	// Size the label column from the static rows above so the joins value below
	// knows how much width is left to wrap into; the pass after the appends
	// widens it again should a later row ever carry a longer label.
	labelW := 0
	for _, kv := range metrics {
		if n := lipgloss.Width(kv[0]); n > labelW {
			labelW = n
		}
	}
	// Identify the statement's main table (parsed from FROM/UPDATE/INTO), the
	// same value shown in the overview's `table` column. Omitted when unparseable.
	t := pg.MainTable(q.Query)
	if t != "" {
		metrics = append(metrics, [2]string{"table", mainTableDisplay(q.Query)})
	}
	// The other relations the statement reads. Appended independently of the
	// table row because the two parsers disagree by design — FROM
	// generate_series(…) g, events e has no main table, but `events` is still
	// worth naming — and wrapped onto continuation rows with an empty label,
	// like the HOT caveat below, so a wide join list can't break the layout.
	for i, line := range joinedTablesLines(q.Query, max(m.width-6-labelW, 20)) {
		label := "joins"
		if i > 0 {
			label = ""
		}
		metrics = append(metrics, [2]string{label, line})
	}
	// HOT update ratio for the main table, fetched async into statHotStats. It's
	// cumulative (since the last stats reset), not window-scoped like the rows
	// above, so it's explicitly labelled "lifetime". Higher is better →
	// percentStyle (green high). Shown only once loaded and the table has
	// recorded updates; otherwise omitted (no row clutters a SELECT-only table).
	if hs := s.stat.hotStats; t != "" && hs != nil {
		if ratio, ok := hs.HotRatio(); ok {
			val := percentStyle(ratio).Render(fmtFloat(ratio)+"%") +
				mu(fmt.Sprintf("  (%s HOT · %s non-HOT of %s updates)",
					formatRows(hs.HotUpdates), formatRows(hs.NonHotUpdates()), formatRows(hs.Updates)))
			metrics = append(metrics, [2]string{"HOT updates", val})
			// Make clear this is a table-level counter (every update to the
			// table since the last stats reset), not scoped to this query.
			metrics = append(metrics, [2]string{"", mu("all updates to this table, since last stats reset")})
		}
	}
	// The breakdowns the rows above collapse. All read straight off the
	// window-delta QueryStat (no extrema — those are cumulative-only and
	// meaningless in a delta).
	metrics = append(metrics,
		[2]string{"I/O breakdown", fmt.Sprintf("shared %s/%s · local %s/%s · temp %s/%s ms",
			fmtMs(q.SharedBlkReadTime), fmtMs(q.SharedBlkWriteTime),
			fmtMs(q.LocalBlkReadTime), fmtMs(q.LocalBlkWriteTime),
			fmtMs(q.TempBlkReadTime), fmtMs(q.TempBlkWriteTime)) + mu("  (read/write)")},
		[2]string{"local blocks", fmt.Sprintf("%s hit · %s read (miss) · %s dirtied · %s written",
			formatRows(q.LocalBlksHit), formatRows(q.LocalBlksRead),
			formatRows(q.LocalBlksDirtied), formatRows(q.LocalBlksWritten))},
	)
	if q.Calls > 0 {
		c := float64(q.Calls)
		metrics = append(metrics, [2]string{"per call", fmt.Sprintf("%s WAL · %s shared blocks · %s plans",
			humanize.Bytes(int64(float64(q.WALBytes)/c)),
			fmtFloat(float64(q.SharedBlksHit+q.SharedBlksRead)/c), fmtFloat(float64(q.Plans)/c))})
	}
	planDetail := formatRows(q.Plans) + " plans"
	if q.Plans > 0 {
		planDetail += mu(fmt.Sprintf("  (%s ms mean)", fmtMs(q.TotalPlanTime/float64(q.Plans))))
	}
	metrics = append(metrics, [2]string{"plan detail", planDetail})
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
	for _, line := range highlightSQL(q.Query, m.width-4) {
		b.WriteString("    " + line + "\n")
	}

	explainable := pg.ExplainableQuery(q.Query)

	// --- sample call ---
	// A non-empty call is complete and built from captured values by
	// construction; only its origin varies. Without one, say what is missing
	// and how to get real values — never a guessed literal.
	b.WriteString("\n  " + styleHeader.Render(" sample call ") + "\n")
	switch {
	case !explainable:
		b.WriteString("    " + mu("not a SELECT/DML statement — no parameters to fill") + "\n")
	case s.stat.sampleCall != "":
		b.WriteString("    " + mu(sampleSourceHint(s)) + "\n")
		// Same highlighter as the query section: the literals substituted for
		// $n land in the accent the whole block used to wear, so the colour now
		// marks exactly the filled-in values. A statement without parameters is
		// already printed in full just above.
		if s.stat.sampleSource != sampleNoParams {
			for _, line := range highlightSQL(s.stat.sampleCall, m.width-4) {
				b.WriteString("    " + line + "\n")
			}
		}
	case !s.stat.sampleResolved:
		b.WriteString("    " + mu("inferring parameters…") + "\n")
	default:
		if s.stat.sampleErr != nil {
			b.WriteString("    " + mu("could not infer parameters: "+s.stat.sampleErr.Error()) + "\n")
			writeSchemaDriftHint(&b, s.stat.sampleErr)
		}
		for _, line := range noSampleLines(s) {
			b.WriteString("    " + mu(m.clipDetail(line)) + "\n")
		}
	}
	if s.stat.verbose && explainable {
		m.renderSampleParams(&b, s)
	}

	// --- explain ---
	explainHdr := " explain (generic plan) "
	switch {
	case s.stat.explainAnalyze:
		explainHdr = " explain (analyze · verbose · buffers) "
	case s.stat.sampleCall != "":
		// Captured values → a plain EXPLAIN, so the planner sees real data.
		explainHdr = " explain (real plan) "
	}
	b.WriteString("\n  " + styleHeader.Render(explainHdr) + "\n")
	switch {
	case !explainable:
		b.WriteString("    " + mu("EXPLAIN is only available for SELECT/DML statements") + "\n")
	case s.stat.explaining:
		b.WriteString("    " + mu("running EXPLAIN…") + "\n")
	case s.stat.explainErr != nil:
		b.WriteString("    " + styleErr.Render(s.stat.explainErr.Error()) + "\n")
		// The sample-call section has already explained the drift when its
		// PREPARE hit the same missing object; say it once.
		if !pg.SchemaDrift(s.stat.sampleErr) {
			writeSchemaDriftHint(&b, s.stat.explainErr)
		}
	case s.stat.explain != "":
		for _, line := range m.colorizeExplain(s.stat.explain, s.stat.explainAnalyze) {
			b.WriteString("    " + line + "\n")
		}
	default:
		b.WriteString("    " + mu("no plan available") + "\n")
	}

	// EXPLAIN ANALYZE affordance. ANALYZE executes the query for real, so it's
	// offered only for read-only SELECT shapes and only once a complete sample
	// call built from captured values is available to actually run.
	if explainable && !s.stat.explaining && pg.ReadOnlyQuery(q.Query) && s.stat.sampleCall != "" {
		b.WriteString("    " + mu("press ") + styleBadge.Render("↵") +
			mu(" to run EXPLAIN (ANALYZE, VERBOSE, BUFFERS) — ") +
			styleErr.Render("executes the query for real") + "\n")
		b.WriteString("    " + mu("press ") + styleBadge.Render("E") +
			mu(" to execute it and show the result rows — ") +
			styleErr.Render("executes the query for real") + "\n")
	}

	// Captured-values affordance: only when pg_qualstats holds constants for this
	// query, so the browser never opens on an empty list.
	if explainable && s.stat.qualSamples {
		b.WriteString("    " + mu("press ") + styleBadge.Render("p") +
			mu(" to browse the real values pg_qualstats captured for this query") + "\n")
	}

	verbHint := " to show where each parameter value came from"
	if s.stat.verbose {
		verbHint = " to hide the parameter sources"
	}
	b.WriteString("    " + mu("press ") + styleBadge.Render("v") + mu(verbHint) + "\n")
	// The u jump only resolves when the statement has a parseable main table —
	// mirror that here so we don't advertise a no-op (see the DiskUsage handler).
	if pg.MainTable(q.Query) != "" {
		b.WriteString("    " + mu("press ") + styleBadge.Render("u") +
			mu(" to jump to this table's disk usage") + "\n")
	}

	// The detail panel has no list to page through, so it scrolls as a single
	// body: s.offset is the first visible line. Long sample calls / EXPLAIN
	// output overflow short terminals otherwise, hiding the header off-screen.
	return scrollWindow(b.String(), &s.offset, height)
}

// renderSampleParams writes the verbose per-parameter breakdown under the sample
// call: one aligned row per $n placeholder (ordinal · predicate column · type ·
// where its value came from · the literal, "—" when nothing captured it). It is
// shown for an incomplete call too, so the missing $n are visible. For a
// pg_qualstats example the whole call is captured rather than built from $n
// (sampleParams is nil), so it points at the captured-values browser instead;
// a logged call whose client inlined every value has no bind rows either.
func (m *Model) renderSampleParams(b *strings.Builder, s *screen) {
	mu := styleMuted.Render
	if s.stat.sampleSource == sampleQualExample {
		b.WriteString("\n    " + mu("parameters") + "\n")
		b.WriteString("    " + mu("all values captured by pg_qualstats — press ") +
			styleBadge.Render("p") + mu(" to browse each predicate's real constants") + "\n")
		return
	}
	if s.stat.sampleSource == sampleLog && len(s.stat.sampleParams) == 0 {
		b.WriteString("\n    " + mu("parameters") + "\n")
		b.WriteString("    " + mu("the logged call carries every value inline — nothing was bound") + "\n")
		return
	}
	if len(s.stat.sampleParams) == 0 {
		return
	}
	b.WriteString("\n    " + mu("parameters") + "\n")
	// Pre-pad the fixed-width columns to their widest cell; the value column is
	// last so it needs no padding (and carries its own accent style).
	type row struct{ ord, col, typ, src, val string }
	rows := make([]row, len(s.stat.sampleParams))
	var ordW, colW, typW, srcW int
	for i, p := range s.stat.sampleParams {
		col := p.Column
		if col == "" {
			col = "—"
		}
		val := p.Value
		if val == "" {
			val = "—"
		}
		rows[i] = row{"$" + strconv.Itoa(p.Ordinal), col, p.Type, paramSourceLabel(p.Source), val}
		ordW = max(ordW, displayWidth(rows[i].ord))
		colW = max(colW, displayWidth(rows[i].col))
		typW = max(typW, displayWidth(rows[i].typ))
		srcW = max(srcW, displayWidth(rows[i].src))
	}
	for _, r := range rows {
		b.WriteString("      " +
			mu(padRight(r.ord, ordW)) + "  " +
			padRight(r.col, colW) + "  " +
			mu(padRight(r.typ, typW)) + "  " +
			mu(padRight(r.src, srcW)) + "  " +
			styleBarAlt.Render(r.val) + "\n")
	}
}

// paramSourceLabel names where a sample parameter's literal came from, for the
// verbose parameter table's source column.
func paramSourceLabel(src pg.ParamSource) string {
	switch src {
	case pg.ParamQualstats:
		return "pg_qualstats"
	case pg.ParamLog:
		return "server log"
	default:
		return "not captured"
	}
}

// sampleSourceHint is the muted line over a sample call naming its origin.
func sampleSourceHint(s *screen) string {
	switch s.stat.sampleSource {
	case sampleQualExample:
		return "real values · pg_qualstats"
	case sampleQualPredicates:
		return "real values · pg_qualstats (per predicate)"
	case sampleLog:
		h := "real values · server log"
		if li := s.stat.logInfo; li != nil {
			h += " · " + filepath.Base(li.path) + " · " + li.at.Format("2006-01-02 15:04:05")
			if li.durationMs > 0 {
				h += " · " + fmtAge(li.durationMs)
			}
		}
		return h
	case sampleNoParams:
		return "no parameters — the statement is its own call"
	}
	return ""
}

// noSampleLines explains a missing sample call: values are never guessed, so it
// names the source still being tried, what pg_qualstats did cover, and what
// would provide real values.
func noSampleLines(s *screen) []string {
	if s.stat.logLookup == logLookupRunning {
		return []string{"no pg_qualstats values — searching the server log for a logged call…"}
	}
	lines := []string{"no captured parameter values — values are never guessed"}
	covered, missing := qualCoverage(s.stat.sampleParams)
	switch {
	case s.stat.qualstats && len(covered) > 0:
		lines = append(lines, "pg_qualstats covers "+strings.Join(covered, ", ")+" · missing "+strings.Join(missing, ", "))
	case s.stat.qualstats && s.stat.qualTracked > 0:
		// Tracked but never with a literal: the call binds its parameters and the
		// plan went generic, so the qual holds a Param pg_qualstats can't deparse.
		lines = append(lines, "pg_qualstats tracked "+countLabel(s.stat.qualTracked, "predicate")+
			" for this query but captured no constants — bound parameters under a generic plan leave no literal to sample")
	case s.stat.qualstats:
		lines = append(lines, "pg_qualstats has no constants for this query yet")
	case s.extPrompt != nil && s.extPrompt.name == extQualstats:
		lines = append(lines, "press i to install pg_qualstats")
	default:
		lines = append(lines, "install pg_qualstats, or log calls with their parameters (log_min_duration_statement + log_parameter_max_length)")
	}
	switch s.stat.logLookup {
	case logLookupNone:
		lines = append(lines, "no logged call with bound values in the server log — log_min_duration_statement (or log_statement) with log_parameter_max_length ≠ 0 records them")
	case logLookupNoLog:
		lines = append(lines, "no readable server log found — pass --log-file, or grant pg_read_server_files for the server's copy")
	case logLookupErr:
		lines = append(lines, "server log could not be read — "+s.stat.logErr.Error())
	case logLookupIdle, logLookupRunning, logLookupFound: // nothing to add
	}
	return lines
}

// countLabel renders "1 predicate" / "3 predicates" for a message.
func countLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// qualCoverage splits the breakdown into the $n pg_qualstats covers and the rest.
func qualCoverage(params []pg.SampleParam) (covered, missing []string) {
	for _, p := range params {
		o := "$" + strconv.Itoa(p.Ordinal)
		if p.Source == pg.ParamQualstats {
			covered = append(covered, o)
		} else {
			missing = append(missing, o)
		}
	}
	return covered, missing
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
	if s.stat.detail != nil {
		qid = s.stat.detail.QueryID
	}
	b.WriteString("  " + styleSelected.Render(fmt.Sprintf("captured values · query %d", qid)) + "\n")
	b.WriteString("  " + mu("real predicate constants sampled by pg_qualstats — most frequent first") + "\n\n")
	used := 4 // the 4 lines written above (incl. the trailing blank)

	// Split the remaining height: when a plan is on screen it takes the lower
	// half, otherwise the list fills everything.
	explainOn := s.stat.explaining || s.stat.explain != "" || s.stat.explainErr != nil
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
		case s.stat.explaining:
			b.WriteString("    " + mu("running EXPLAIN ANALYZE…") + "\n")
		case s.stat.explainErr != nil:
			b.WriteString("    " + styleErr.Render(s.stat.explainErr.Error()) + "\n")
		default:
			for _, line := range m.colorizeExplain(s.stat.explain, true) {
				b.WriteString("    " + line + "\n")
			}
		}
	} else if s.stat.detail != nil && pg.ReadOnlyQuery(s.stat.detail.Query) &&
		(s.stat.sampleCall != "" || uniqueParams(s.stat.detail.Query) == 1) {
		b.WriteString("    " + mu("press ") + styleBadge.Render("↵") +
			mu(" to EXPLAIN (ANALYZE) the highlighted value — ") +
			styleErr.Render("executes the query for real") + "\n")
	}

	return padInfo(&b, height)
}

// clipDetail truncates one line to the usable detail width (EXPLAIN output is
// kept on single lines rather than wrapped, to preserve plan-tree indentation).
func (m *Model) clipDetail(line string) string {
	w := m.width - 4
	if w < 8 {
		return line
	}
	return clipCells(line, w)
}

// writeSchemaDriftHint follows an EXPLAIN / PREPARE error that names a missing
// column, table or function with the reason: the entry is older than the
// schema, so the failure is expected rather than a pgdu bug.
func writeSchemaDriftHint(b *strings.Builder, err error) {
	if !pg.SchemaDrift(err) {
		return
	}
	b.WriteString("    " + styleMuted.Render("the statement no longer matches the schema — this entry predates a column/table change") + "\n")
	b.WriteString("    " + styleMuted.Render("(pg_stat_statements accumulates since the last reset; the current query text has a new query id)") + "\n")
}
