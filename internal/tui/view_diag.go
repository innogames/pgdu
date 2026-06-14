package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderDiagQuery prints the executed SQL for the current diagnostic so it can
// be selected and copied out of the terminal. It replaces the result table
// while open (s key on levelDiagnosticResult); any key dismisses it. The query
// text is shown verbatim (server-side formatting) padded to `height` lines so
// the help row stays pinned to the bottom.
func (m *Model) renderDiagQuery(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render(s.diag.Title+" — SQL") + mu("  ·  press ") +
		styleBadge.Render("s") + mu(" or any key to dismiss") + "\n\n")

	used := 2 // the two lines written above
	for line := range strings.SplitSeq(strings.Trim(s.diag.SQL, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
		used++
	}
	for i := used; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderDescribe draws the \d-style description panel for levelDescribe. It
// is a plain-text free-form view (no bars) padded to `height` lines so the
// help row stays pinned to the bottom. Switches on s.describe.Kind to render
// either a table (columns / indexes / constraints / summary) or an index
// (definition, method, parent, optional partial predicate).
func (m *Model) renderDescribe(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	d := s.describe
	if d == nil {
		// Should not happen: the loading guard in View fires before this,
		// but defend anyway.
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}

	b.WriteString("\n")

	// truncLine clips a string to m.width so wide index defs don't wrap and
	// unpins the help row.
	truncLine := func(v string) string {
		if m.width <= 4 {
			return v
		}
		w := m.width - 4 // 4 = leading "    " indent
		if lipgloss.Width(v) <= w {
			return v
		}
		r := []rune(v)
		for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
			r = r[:len(r)-1]
		}
		return string(r) + "…"
	}

	switch d.Kind {
	case pg.DescribeTable:
		b.WriteString("  " + styleSelected.Render(d.Title) + "\n\n")

		// --- columns ---
		b.WriteString("  " + styleHeader.Render(" columns ") + "\n")
		if len(d.Columns) == 0 {
			b.WriteString("    " + mu("(none)") + "\n")
		} else {
			// Compute column-name width for alignment.
			nameW := 4
			for _, col := range d.Columns {
				if n := lipgloss.Width(col.Name); n > nameW {
					nameW = n
				}
			}
			for _, col := range d.Columns {
				line := padRight(col.Name, nameW) + "  " + col.Type
				if col.NotNull {
					line += "  " + styleBadge.Render("not null")
				}
				if col.Default != "" {
					line += "  " + mu("default "+col.Default)
				}
				b.WriteString("    " + line + "\n")
			}
		}

		// --- indexes ---
		if len(d.Indexes) > 0 {
			b.WriteString("\n  " + styleHeader.Render(" indexes ") + "\n")
			for _, idx := range d.Indexes {
				badges := ""
				if idx.IsPrimary {
					badges += " " + styleBadge.Render("primary")
				} else if idx.IsUnique {
					badges += " " + styleBadge.Render("unique")
				}
				if idx.Clustered {
					badges += " " + styleBadge.Render("clustered")
				}
				b.WriteString("    " + idx.Name + badges + "\n")
				b.WriteString("      " + mu(truncLine(idx.Def)) + "\n")
			}
		}

		// --- summary ---
		b.WriteString("\n  " + mu(fmt.Sprintf(
			"%s total · ~%s rows",
			humanize.Bytes(d.SizeBytes),
			formatRows(d.EstRows),
		)) + "\n")

		// --- cache footprint (shared_buffers occupancy of this table) ---
		b.WriteString("\n  " + styleHeader.Render(" cache footprint ") + "\n")
		b.WriteString(m.renderDescribeBufferRows(s))

	case pg.DescribeIndex:
		b.WriteString("  " + styleSelected.Render(d.Title) + "\n\n")
		b.WriteString("  " + mu("on ") + d.ParentTable + "\n")

		methodLine := "  " + mu("method ") + d.AccessMethod
		if d.IdxPrimary {
			methodLine += "  " + styleBadge.Render("primary")
		}
		if d.IdxUnique && !d.IdxPrimary {
			methodLine += "  " + styleBadge.Render("unique")
		}
		b.WriteString(methodLine + "\n")

		b.WriteString("\n  " + styleHeader.Render(" definition ") + "\n")
		b.WriteString("    " + mu(truncLine(d.IndexDef)) + "\n")

		if d.Predicate != "" {
			b.WriteString("\n  " + styleHeader.Render(" partial predicate ") + "\n")
			b.WriteString("    " + mu(truncLine(d.Predicate)) + "\n")
		}
	}

	// Pad to fill the content area so the help row stays pinned.
	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderDescribeBufferRows renders the body of the describe-table cache-footprint
// section from s.descBuf, mirroring the figures of the buffer-detail screen
// (renderBufferDetail) but without the temperature histogram. It degrades
// gracefully: a missing pg_buffercache shows an inline install affordance (the
// generic `i` key acts on the non-blocking extPrompt set in onDescribeBuffersLoaded),
// a load error shows a muted line, and the in-flight state shows a placeholder.
func (m *Model) renderDescribeBufferRows(s *screen) string {
	mu := styleMuted.Render

	// Missing pg_buffercache: offer the install inline, mirroring renderExtHint.
	if p := s.extPrompt; p != nil {
		switch {
		case s.installing:
			return "    " + mu(m.spinner.View()+" installing "+p.name+"…") + "\n"
		case p.err != nil:
			return "    " + styleErr.Render("install "+p.name+" failed: "+p.err.Error()) + "  " +
				mu("(press i to retry)") + "\n"
		case !p.installable:
			return "    " + mu(p.name+" not installed and not available on this server") + "\n"
		default:
			return "    " + mu(p.name+" not installed — press ") +
				styleBadge.Render("i") + mu(" to install") + "\n"
		}
	}
	if s.descBufErr != nil {
		return "    " + styleErr.Render(s.descBufErr.Error()) + "\n"
	}
	st := s.descBuf
	if st == nil {
		return "    " + mu("…") + "\n"
	}

	barW := bufferDetailBarWidth(m.width)
	cachedVal := "—"
	if st.TotalBytes > 0 {
		pct := float64(st.BufferedBytes) / float64(st.TotalBytes) * 100
		cachedVal = percentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct)) +
			"  " + renderSolidBar(st.BufferedBytes, st.TotalBytes, barW, percentStyle(pct))
	}
	hitVal := "—"
	if hr := st.HitRatio(); hr >= 0 {
		pct := hr * 100
		hitVal = gradedPercentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct))
	}
	rows := [][2]string{
		{"buffered", humanize.Bytes(st.BufferedBytes)},
		{"table size", humanize.Bytes(st.TotalBytes)},
		{"cached", cachedVal},
		{"hit ratio", hitVal},
		{"dirty", humanize.Bytes(st.DirtyBytes)},
		{"avg usage", fmt.Sprintf("%.1f / 5", st.UsageAvg)},
	}
	labelW := 0
	for _, kv := range rows {
		if n := len(kv[0]); n > labelW {
			labelW = n
		}
	}
	var b strings.Builder
	for _, kv := range rows {
		b.WriteString("    " + mu(padRight(kv[0], labelW)) + "  " + kv[1] + "\n")
	}
	return b.String()
}

// renderDiagnosticList renders the flat list of available diagnostic queries at
// levelDiagnostics. Layout: cursor | [category] | title | muted description.
func (m *Model) renderDiagnosticList(s *screen, height int) string {
	vis := s.visibleIndexes()
	rowsH := height
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	var b strings.Builder
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		selected := vi == s.cursor
		cursor := "  "
		name := it.name
		if selected {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		detail := ""
		if it.detail != "" {
			detail = "  " + styleMuted.Render(it.detail)
		}
		b.WriteString(cursor + name + detail + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// diagColWidth is the maximum per-column display width in the result table.
// Wide values (long SQL definitions, grants) are truncated with "…" so the
// row fits in the terminal.
const diagColWidth = 36

// diagMetrics returns the per-column render metrics renderDiagResult needs:
// the capped column widths (before the width-dependent last-column grow), the
// uncapped natural widths, the bar column's numeric max, and the per-column max
// for DiagCostGraded columns ("lower is better" cells graded relative to the
// worst value in their own column; HasNum=false cells never inflate it).
//
// All four scan every row (O(rows×cols), with a lipgloss.Width call per cell)
// but depend only on the cell values — not the cursor, sort order or terminal
// width — so the result is memoized on the screen and recomputed only when the
// data reloads (item-load sites set diagMetricsDirty) or the column count drifts
// from the cache (a defensive guard against e.g. a track_planning toggle).
func (s *screen) diagMetrics(cols []pg.DiagColumn, barCol int) (colW, naturalW []int, barMax float64, costMax []float64) {
	nCols := len(cols)
	if !s.diagMetricsDirty && len(s.diagColWBase) == nCols {
		return s.diagColWBase, s.diagNaturalW, s.diagBarMax, s.diagCostMax
	}

	colW = make([]int, nCols)
	naturalW = make([]int, nCols)
	costMax = make([]float64, nCols)
	for i, c := range cols {
		colW[i] = displayWidth(c.Name)
		naturalW[i] = colW[i]
	}
	for _, it := range s.items {
		row, ok := it.data.([]pg.DiagCell)
		if !ok {
			continue
		}
		for i := 0; i < nCols && i < len(row); i++ {
			cell := row[i]
			display := cell.Display
			if cell.HasNum && cols[i].Kind == pg.DiagBytes {
				display = humanize.Bytes(int64(cell.Num))
			}
			w := displayWidth(display)
			if w > naturalW[i] {
				naturalW[i] = w
			}
			if w > colW[i] {
				colW[i] = w
			}
			if colW[i] > diagColWidth {
				colW[i] = diagColWidth
			}
			if cell.HasNum {
				if barCol == i && cell.Num > barMax {
					barMax = cell.Num
				}
				if cols[i].Kind == pg.DiagCostGraded && cell.Num > costMax[i] {
					costMax[i] = cell.Num
				}
			}
		}
	}

	// Widen columns to fit the pinned total footer too, so its summed values
	// (the largest in the table) render compact-but-whole instead of truncated
	// with "…". Width only: the total must not inflate barMax/costMax, or it
	// would re-scale every data cell's bar/grade against the grand total.
	for i := 0; i < nCols && i < len(s.diagTotalRow); i++ {
		cell := s.diagTotalRow[i]
		display := cell.Display
		if cell.HasNum && cols[i].Kind == pg.DiagBytes {
			display = humanize.Bytes(int64(cell.Num))
		}
		w := displayWidth(display)
		if w > naturalW[i] {
			naturalW[i] = w
		}
		if w > colW[i] {
			colW[i] = min(w, diagColWidth)
		}
	}

	s.diagColWBase, s.diagNaturalW, s.diagBarMax, s.diagCostMax = colW, naturalW, barMax, costMax
	s.diagMetricsDirty = false
	return colW, naturalW, barMax, costMax
}

// renderDiagResult renders the result table for a selected diagnostic query.
// It computes per-column widths, renders a header with the active sort marked
// by an arrow, and optionally renders a bar for the headline column.
func (m *Model) renderDiagResult(s *screen, height int) string {
	var b strings.Builder

	if s.diagCols == nil || !s.loaded {
		// Still loading (shouldn't normally reach here — View guards it).
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}

	if len(s.items) == 0 {
		b.WriteString("  " + styleMuted.Render("(no rows)") + "\n")
		for i := 1; i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	cols := s.diagCols
	nCols := len(cols)
	barCol := s.diagBarCol

	// Determine bar column type up front — needed in the colW computation below.
	barIsPercent := barCol >= 0 && barCol < nCols && cols[barCol].Kind == pg.DiagPercent
	barIsBytes := barCol >= 0 && barCol < nCols && cols[barCol].Kind == pg.DiagBytes

	// Per-column display widths (capped at diagColWidth), uncapped natural widths,
	// the bar column's numeric max, and the per-column cost-grade maxima. These
	// scan every row but depend only on the cell values, so they're memoized on
	// the screen and recomputed only when the data reloads (see diagMetrics).
	colWBase, naturalW, barMax, costMax := s.diagMetrics(cols, barCol)
	colW := append([]int(nil), colWBase...) // local copy: the last-column grow below mutates it

	// With no bar column, the bar's horizontal budget is unused, so let the last
	// column grow past diagColWidth into the remaining terminal width — this is
	// where wide text (queries, definitions, grants) would otherwise be clipped.
	if barCol < 0 && nCols > 0 {
		used := 2 // cursor
		for _, w := range colW {
			used += w + colGutter
		}
		last := nCols - 1
		if remaining := m.width - used; remaining > 0 {
			colW[last] = min(naturalW[last], colW[last]+remaining)
		}
	}

	// Bar width: whatever remains after fixed columns (capped).
	// Reserve: 2 (cursor) + sum(colW + 2 gutter) for all cols + 2 (bar brackets) for bar col.
	// The bar col contributes both barW+brackets and colW[barCol]+gutter, but we
	// solve for barW so we subtract colW[barCol]+gutter separately.
	fixedW := 2 // cursor
	for i, w := range colW {
		fixedW += w + colGutter
		if i == barCol {
			fixedW += colBrackets // additional [  ] around the bar itself
		}
	}
	barW := min(max(m.width-fixedW, barWidthMin), barWidthMax)

	// ── header ──────────────────────────────────────────────────────────────
	arrow := "↑"
	if s.sortDesc {
		arrow = "↓"
	}
	mark := func(label string, colIdx int) string {
		if colIdx == s.diagSortCol {
			return label + arrow
		}
		return label
	}

	var hdr strings.Builder
	hdr.WriteString(strings.Repeat(" ", 2)) // cursor placeholder
	for i, c := range cols {
		if i == barCol {
			// Bar area: [barW chars] + gutter + number column (colW[i]).
			hdr.WriteString(strings.Repeat(" ", barW+colBrackets+colGutter))
			hdr.WriteString(padRight(mark(c.Name, i), colW[i]))
			hdr.WriteString(strings.Repeat(" ", colGutter))
			continue
		}
		hdr.WriteString(padRight(mark(c.Name, i), colW[i]))
		hdr.WriteString(strings.Repeat(" ", colGutter))
	}
	b.WriteString(styleMuted.Render(hdr.String()) + "\n")

	// ── rows ────────────────────────────────────────────────────────────────
	vis := s.visibleIndexes()
	// header consumes one line; a pinned total footer (when present) one more.
	reserve := 1
	if s.diagTotalRow != nil {
		reserve = 2
	}
	rowsH := max(height-reserve, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		row, ok := it.data.([]pg.DiagCell)
		selected := vi == s.cursor

		cursor := "  "
		if selected {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}

		var line strings.Builder
		line.WriteString(cursor)

		if !ok {
			line.WriteString("\n")
			b.WriteString(line.String())
			continue
		}

		for i := range nCols {
			var cell pg.DiagCell
			if i < len(row) {
				cell = row[i]
			}

			if i == barCol {
				// Render bar + number.
				var barStr string
				if cell.HasNum {
					scaleMax := barMax
					if barIsPercent {
						scaleMax = 100
					}
					if scaleMax <= 0 {
						scaleMax = 1
					}
					filled := max(min(int(float64(barW)*cell.Num/scaleMax), barW), 0)
					style := styleBar
					if barIsPercent {
						style = percentStyle(cell.Num)
					}
					barStr = paintBar(barW, barSegment{cells: filled, style: style})
				} else {
					barStr = paintBar(barW) // empty bar for null cells
				}

				numStr := cell.Display
				if barIsBytes && cell.HasNum {
					numStr = humanize.Bytes(int64(cell.Num))
				}
				if selected {
					numStr = styleSelected.Render(numStr)
				}
				line.WriteString(barStr)
				line.WriteString(strings.Repeat(" ", colGutter))
				line.WriteString(padRight(numStr, colW[i]))
				line.WriteString(strings.Repeat(" ", colGutter))
				continue
			}

			raw := cell.Display
			if cell.HasNum && i < nCols && cols[i].Kind == pg.DiagBytes {
				raw = humanize.Bytes(int64(cell.Num))
			}
			display := truncateDiagCell(raw, colW[i])
			graded := i < nCols && cols[i].Kind == pg.DiagPercentGraded
			costGraded := i < nCols && cols[i].Kind == pg.DiagCostGraded
			duration := i < nCols && cols[i].Kind == pg.DiagDuration
			isNumeric := cell.HasNum || (i < nCols && (cols[i].Kind == pg.DiagInt ||
				cols[i].Kind == pg.DiagFloat || cols[i].Kind == pg.DiagPercent ||
				cols[i].Kind == pg.DiagBytes || graded || costGraded || duration))

			// Grade "higher is better" percent cells green→red so the eye can
			// triage hit ratios without reading digits. Skipped on the selected
			// row, which renders in the selection style like every other cell.
			if graded && cell.HasNum && !selected {
				display = gradedPercentStyle(cell.Num).Render(display)
			}
			// Grade "lower is better" cost cells relative to their column max: 0
			// green, worst-in-window red. Same selected-row suppression.
			if costGraded && cell.HasNum && !selected {
				display = costStyleRelative(cell.Num, costMax[i]).Render(display)
			}
			// Colour elapsed-time cells by absolute magnitude band (ms→green,
			// s→yellow, min→red) so the unit itself reads at a glance.
			if duration && cell.HasNum && !selected {
				display = durationStyle(cell.Num).Render(display)
			}
			// Command-type tag: green for read-only S, red for writing/locking ones.
			if i < nCols && cols[i].Kind == pg.DiagCmdType && !selected {
				display = cmdTypeStyle(cell.Display).Render(display)
			}

			var rendered string
			if isNumeric {
				rendered = padLeft(display, colW[i])
			} else {
				rendered = padRight(display, colW[i])
			}
			if selected {
				rendered = styleSelected.Render(rendered)
			}
			line.WriteString(rendered)
			line.WriteString(strings.Repeat(" ", colGutter))
		}

		// Truncate line to terminal width so wide result tables don't wrap.
		lineStr := line.String()
		if m.width > 4 && lipgloss.Width(lineStr) > m.width {
			lineStr = truncateToWidth(lineStr, m.width)
		}
		b.WriteString(lineStr + "\n")
	}

	// Pad the data area to its budget so the footer (and help) stay pinned.
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}

	// Pinned total footer: always the last content line, so it stays visible
	// regardless of scroll. Reuses the column layout but is never selected,
	// barred or graded (the total is each column's max — grading would paint it
	// solid red).
	if s.diagTotalRow != nil {
		var line strings.Builder
		line.WriteString("  ") // cursor placeholder, no ▶
		for i := range nCols {
			var cell pg.DiagCell
			if i < len(s.diagTotalRow) {
				cell = s.diagTotalRow[i]
			}
			if i == barCol {
				// No bar for the total; keep the bar area blank so columns align.
				line.WriteString(strings.Repeat(" ", barW+colBrackets+colGutter))
				line.WriteString(padRight(cell.Display, colW[i]))
				line.WriteString(strings.Repeat(" ", colGutter))
				continue
			}
			raw := cell.Display
			if cell.HasNum && cols[i].Kind == pg.DiagBytes {
				raw = humanize.Bytes(int64(cell.Num))
			}
			display := truncateDiagCell(raw, colW[i])
			isNumeric := cell.HasNum || cols[i].Kind == pg.DiagInt ||
				cols[i].Kind == pg.DiagFloat || cols[i].Kind == pg.DiagPercent ||
				cols[i].Kind == pg.DiagBytes || cols[i].Kind == pg.DiagPercentGraded ||
				cols[i].Kind == pg.DiagCostGraded
			if isNumeric {
				line.WriteString(padLeft(display, colW[i]))
			} else {
				line.WriteString(padRight(display, colW[i]))
			}
			line.WriteString(strings.Repeat(" ", colGutter))
		}
		lineStr := styleTotal.Render(line.String())
		if m.width > 4 && lipgloss.Width(lineStr) > m.width {
			lineStr = truncateToWidth(lineStr, m.width)
		}
		b.WriteString(lineStr + "\n")
	}
	return b.String()
}

// truncateDiagCell clips a cell value to maxW cells, appending "…" when the
// value is wider than the cap.
func truncateDiagCell(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	// Fast path for the common ASCII cell (query text, numbers, identifiers):
	// display width is the byte length, so we can slice directly and skip the
	// per-rune grapheme-width scans that made this the hottest frame in the
	// profile. Keep maxW-1 columns for text plus one for the ellipsis.
	if w, ok := asciiWidth(s); ok {
		if w <= maxW {
			return s
		}
		return s[:maxW-1] + "…"
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > maxW {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// padLeft right-aligns s in a field of width n (like padRight but for numbers).
func padLeft(s string, n int) string {
	w := displayWidth(s)
	if w >= n {
		return s
	}
	return strings.Repeat(" ", n-w) + s
}
