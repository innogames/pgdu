package tui

import (
	"fmt"
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
		// HOT update ratio for that table, fetched async into statHotStats. It's
		// cumulative (since the last stats reset), not window-scoped like the rows
		// above, so it's explicitly labelled "lifetime". Higher is better →
		// percentStyle (green high). Shown only once loaded and the table has
		// recorded updates; otherwise omitted (no row clutters a SELECT-only table).
		if hs := s.statHotStats; hs != nil {
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
	}
	// Verbose extras: counters/timings that the compact view collapses or omits.
	// All read straight off the window-delta QueryStat (no extrema — those are
	// cumulative-only and meaningless in a delta).
	if s.statVerbose {
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
		case s.statSampleFromQual && s.statSampleFromData:
			hint = "values from pg_qualstats + live table data"
		case s.statSampleFromQual:
			hint = "values from pg_qualstats (per predicate)"
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
	if s.statVerbose && explainable {
		m.renderSampleParams(&b, s)
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

	verbHint := " to show verbose details (parameter sources, full metrics)"
	if s.statVerbose {
		verbHint = " to hide verbose details"
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
// where its value came from · the literal). For a real pg_qualstats example the
// whole call is captured rather than built from $n (statSampleParams is nil), so
// it points at the captured-values browser instead.
func (m *Model) renderSampleParams(b *strings.Builder, s *screen) {
	mu := styleMuted.Render
	if s.statSampleReal {
		b.WriteString("\n    " + mu("parameters") + "\n")
		b.WriteString("    " + mu("all values captured by pg_qualstats — press ") +
			styleBadge.Render("p") + mu(" to browse each predicate's real constants") + "\n")
		return
	}
	if len(s.statSampleParams) == 0 {
		return
	}
	b.WriteString("\n    " + mu("parameters") + "\n")
	// Pre-pad the fixed-width columns to their widest cell; the value column is
	// last so it needs no padding (and carries its own accent style).
	type row struct{ ord, col, typ, src, val string }
	rows := make([]row, len(s.statSampleParams))
	var ordW, colW, typW, srcW int
	for i, p := range s.statSampleParams {
		col := p.Column
		if col == "" {
			col = "—"
		}
		rows[i] = row{"$" + strconv.Itoa(p.Ordinal), col, p.Type, paramSourceLabel(p.Source), p.Value}
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
	case pg.ParamLiveData:
		return "live table data"
	case pg.ParamQualstats:
		return "pg_qualstats"
	case pg.ParamExtractField:
		return "EXTRACT field"
	case pg.ParamIntervalLiteral:
		return "INTERVAL literal"
	default:
		return "synthesized"
	}
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

	return padInfo(&b, height)
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
