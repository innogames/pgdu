package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// diagForShowQuery returns the diagnostic whose SQL the show-SQL overlay should
// display for screen s: the running query on a result screen, or the highlighted
// entry on the diagnostics list (so its SQL can be grabbed to run elsewhere
// without running it here first). Returns nil when neither applies or the cursor
// is out of range.
func (s *screen) diagForShowQuery() *pg.Diagnostic {
	switch s.level {
	case levelDiagnosticResult:
		return s.diag
	case levelDiagnostics:
		vis := s.visibleIndexes()
		if s.cursor < 0 || s.cursor >= len(vis) {
			return nil
		}
		if d, ok := s.items[vis[s.cursor]].data.(pg.Diagnostic); ok {
			return &d
		}
	}
	return nil
}

// renderDiagQuery prints the SQL of the diagnostic resolved by diagForShowQuery
// so it can be selected and copied out of the terminal. It replaces the list or
// result table while open (s key on levelDiagnosticResult / levelDiagnostics);
// any key dismisses it. The query text is shown verbatim (server-side
// formatting) padded to `height` lines so the help row stays pinned to the
// bottom.
func (m *Model) renderDiagQuery(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	d := s.diagForShowQuery()
	if d == nil {
		// Guarded by the View switch, but stay defensive rather than panic.
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render(d.Title+" — SQL") + mu("  ·  press ") +
		styleBadge.Render("s") + mu(" or any key to dismiss") + "\n\n")

	used := 2 // the two lines written above
	for line := range strings.SplitSeq(strings.Trim(d.SQL, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
		used++
	}
	for i := used; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderDiagnosticInfo draws the ? reference overlay for the diagnostics tool:
// the running diagnostic's (result screen) or the highlighted entry's (list)
// purpose and how to interpret its result, from the registry's Description and
// Help fields. Same resolution as the s SQL viewer (diagForShowQuery).
func (m *Model) renderDiagnosticInfo(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	d := s.diagForShowQuery()
	if d == nil {
		// Guarded by hasInfoOverlay/View, but stay defensive rather than panic.
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}
	infoHeader(&b, d.Title)

	b.WriteString("  " + styleHeader.Render(" purpose ") + "\n")
	for _, line := range wrapIndent(d.Description, m.width) {
		b.WriteString(mu(line) + "\n")
	}

	// Help strings are authored for every registry entry, but degrade to just
	// the description if a future one ships without.
	if d.Help != "" {
		b.WriteString("\n  " + styleHeader.Render(" how to read it ") + "\n")
		for _, line := range wrapIndent(d.Help, m.width) {
			b.WriteString(mu(line) + "\n")
		}
	}

	b.WriteString("\n  " + styleHeader.Render(" keys ") + "\n")
	kb := func(k, desc string) string { return styleBadge.Render(k) + mu(" "+desc) }
	if s.level == levelDiagnosticResult {
		b.WriteString("    " + kb("s", "show SQL") + mu("  ·  ") + kb("C", "columns") + mu("  ·  ") +
			kb("←/→", "sort column") + mu("  ·  ") + kb("r", "reverse") + "\n")
		b.WriteString("    " + kb("/", "filter rows") + mu("  ·  ") + kb("e", "export csv") + "\n")
	} else {
		b.WriteString("    " + kb("enter", "run") + mu("  ·  ") + kb("s", "preview SQL") + mu("  ·  ") +
			kb("f", "cycle category") + mu("  ·  ") + kb("/", "filter") + "\n")
	}

	return padInfo(&b, height)
}

// wrapIndent word-wraps free prose to the terminal width with a four-space
// indent, first collapsing the source's own line breaks and indentation
// (Diagnostic.Help strings are written as indented raw literals). A blank line
// in the source separates paragraphs, preserved as an empty output line.
func wrapIndent(text string, termWidth int) []string {
	const indent = "    "
	width := max(termWidth-len(indent), 20)

	// Split into paragraphs of whitespace-normalised words.
	var paras [][]string
	var cur []string
	for line := range strings.SplitSeq(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			if len(cur) > 0 {
				paras = append(paras, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, words...)
	}
	if len(cur) > 0 {
		paras = append(paras, cur)
	}

	var out []string
	for pi, words := range paras {
		if pi > 0 {
			out = append(out, "")
		}
		line := words[0]
		w := displayWidth(words[0])
		for _, word := range words[1:] {
			ww := displayWidth(word)
			if w+1+ww > width {
				out = append(out, indent+line)
				line, w = word, ww
				continue
			}
			line += " " + word
			w += 1 + ww
		}
		out = append(out, indent+line)
	}
	return out
}

// renderDescribe draws the \d-style description panel for levelDescribe. It
// is a plain-text free-form view (no bars) that scrolls as a single body
// through scrollWindow (s.offset = first visible line), so wide tables whose
// column/index/FK lists overflow the terminal stay reachable. Switches on
// s.describe.Kind to render either a table (columns / indexes / constraints /
// summary) or an index (definition, method, parent, optional partial predicate).
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
				// Cyan matches the "index" hue used elsewhere. Any index
				// counts — key, expression, or partial predicate — because
				// updating such a column disqualifies HOT.
				if col.Indexed {
					line += "  " + styleBar.Render("indexed")
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

		// --- foreign keys (outgoing: this table references others) ---
		if len(d.FKOutgoing) > 0 {
			b.WriteString("\n  " + styleHeader.Render(" foreign keys ") + "\n")
			b.WriteString(renderFKRows(d.FKOutgoing, false))
		}

		// --- referenced by (incoming: others reference this table) ---
		if len(d.FKIncoming) > 0 {
			b.WriteString("\n  " + styleHeader.Render(" referenced by ") + "\n")
			b.WriteString(renderFKRows(d.FKIncoming, true))
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

	return scrollWindow(b.String(), &s.offset, height)
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

// renderFKRows renders one foreign-key section of the describe-table view (the
// "foreign keys" outgoing list or, with incoming=true, the "referenced by"
// list). incoming flips which side supplies the muted, aligned leading name —
// the constraint name for outgoing, the referencing child table for incoming —
// and the arrow orientation so both read referencer→referenced. Lines are left
// untruncated to match the columns section (truncLine rune-slices and would
// corrupt the styled spans); the describe panel already tolerates wide styled
// lines.
func renderFKRows(fks []pg.DescribeFK, incoming bool) string {
	mu := styleMuted.Render
	lead := func(fk pg.DescribeFK) string {
		if incoming {
			return fk.OtherTable // the referencing child table
		}
		return fk.Name // the constraint name
	}

	leadW := 0
	for _, fk := range fks {
		if n := lipgloss.Width(lead(fk)); n > leadW {
			leadW = n
		}
	}

	var b strings.Builder
	for _, fk := range fks {
		var body string
		if incoming {
			body = fk.OtherCols + " → " + fk.LocalCols
		} else {
			body = fk.LocalCols + " → " + fk.OtherTable + "(" + fk.OtherCols + ")"
		}
		line := "    " + mu(padRight(lead(fk), leadW)) + "  " + body
		if fk.OnDelete != "" {
			line += "  " + mu("on delete "+fk.OnDelete)
		}
		if fk.OnUpdate != "" {
			line += "  " + mu("on update "+fk.OnUpdate)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// renderDiagnosticList renders the flat list of available diagnostic queries at
// levelDiagnostics. Layout: cursor | category badge | title | muted description,
// under a one-line category-filter hint (f cycles all → index → table → …).
func (m *Model) renderDiagnosticList(s *screen, height int) string {
	var b strings.Builder

	label := "all"
	if s.diagCatFilter != "" {
		label = s.diagCatFilter
	}
	b.WriteString("  " + styleMuted.Render("category: ") + diagCatStyle(label).Render(label) +
		styleMuted.Render("  ·  ") + styleBadge.Render("f") + styleMuted.Render(" cycles") + "\n")
	height--

	catW := 0
	for _, c := range diagCategories() {
		if len(c) > catW {
			catW = len(c)
		}
	}
	nameW := 0
	for i := range s.items {
		if n := displayWidth(s.items[i].name); n > nameW {
			nameW = n
		}
	}

	vis := s.visibleIndexes()
	rowsH := height
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))

	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		selected := vi == s.cursor
		cursor := "  "
		name := padRight(it.name, nameW)
		if selected {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		cat := ""
		if d, ok := it.data.(pg.Diagnostic); ok {
			cat = d.Category
		}
		badge := diagCatStyle(cat).Render(padRight(cat, catW))
		detail := ""
		if it.detail != "" {
			detail = "  " + styleMuted.Render(it.detail)
		}
		b.WriteString(cursor + badge + "  " + name + detail + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderDiagColumnConfig draws the htop-style column picker for a diagnostic
// result (C on levelDiagnosticResult). Same look-and-feel as the three
// registry-backed pickers, but the rows come from the result's dynamic column
// set and the selection is remembered per diagnostic key.
func (m *Model) renderDiagColumnConfig(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("configure columns") + mu("  ·  ") +
		styleBadge.Render("space") + mu(" toggles · ") +
		styleBadge.Render("↑/↓") + mu(" move · ") +
		styleBadge.Render("r") + mu(" reset · ") +
		styleBadge.Render("C") + mu(" or ") + styleBadge.Render("esc") + mu(" to close") + "\n")
	title := "this diagnostic"
	if s.diag != nil {
		title = s.diag.Title
	}
	b.WriteString("  " + mu("choose which columns "+title+" shows (remembered per diagnostic)") + "\n\n")

	res := s.diagResult
	if res == nil || s.diag == nil {
		return padInfo(&b, height)
	}
	vis := m.diagVis(s.diag.Key)
	nameW := 0
	for _, c := range res.Columns {
		if n := len(c.Name); n > nameW {
			nameW = n
		}
	}
	for i, c := range res.Columns {
		box := "[ ]"
		if diagColOn(vis, c.Name) {
			box = "[x]"
		}
		cursor := "  "
		if i == m.diagColCfgCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		label := box + "  " + padRight(c.Name, nameW)
		if i == m.diagColCfgCursor {
			label = styleSelected.Render(label)
		}
		b.WriteString(cursor + label + "\n")
	}
	return padInfo(&b, height)
}

// diagColWidth is the maximum per-column display width in the result table.
// Wide values (long SQL definitions, grants) are truncated with "…" so the
// row fits in the terminal.
const diagColWidth = 36

// diagBarWidthMin/Max bound the headline bar in a diagnostic table. The cap is
// deliberately smaller than the disk browser's barWidthMax (80): diagnostic
// rows carry several numeric columns plus wide schema/table/index names, so a
// long bar would crowd the data columns off the right edge. Width the bar
// doesn't use is handed back to truncated text columns instead.
const (
	diagBarWidthMin = 12
	diagBarWidthMax = 48
)

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
	barKind := pg.DiagText
	if barCol >= 0 && barCol < nCols {
		barKind = cols[barCol].Kind
	}
	barIsPercent := barKind == pg.DiagPercent || barKind == pg.DiagPercentGraded || barKind == pg.DiagPercentBad
	barIsBytes := barKind == pg.DiagBytes

	// Per-column display widths (capped at diagColWidth), uncapped natural widths,
	// the bar column's numeric max, and the per-column cost-grade maxima. These
	// scan every row but depend only on the cell values, so they're memoized on
	// the screen and recomputed only when the data reloads (see diagMetrics).
	colWBase, naturalW, barMax, costMax := s.diagMetrics(cols, barCol)
	colW := append([]int(nil), colWBase...) // local copy: the last-column grow below mutates it

	// With no bar column, the bar's horizontal budget is unused, so let columns
	// capped at diagColWidth grow back toward their natural width into the
	// remaining terminal width — this is where wide text (index names, queries,
	// definitions, grants) would otherwise be clipped. Distribute the slack across
	// every truncated column (left to right), not just the last one, so a short
	// trailing column doesn't strand the space a wide middle column needs.
	if barCol < 0 && nCols > 0 {
		used := 2 // cursor
		for _, w := range colW {
			used += w + colGutter
		}
		remaining := m.width - used
		// Repeatedly hand a slice of the slack to whichever capped columns still
		// want more, until the space runs out or no column wants growth.
		for remaining > 0 {
			grew := false
			for i := range colW {
				if remaining <= 0 {
					break
				}
				if want := naturalW[i] - colW[i]; want > 0 {
					give := min(want, remaining)
					colW[i] += give
					remaining -= give
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	// Bar width: whatever remains after fixed columns, capped (diagBarWidthMax).
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
	// The bar column renders one extra gutter after its number cell that fixedW
	// doesn't count (a normal column ends in a single gutter; the bar column ends
	// in bar+gutter+number+gutter). Subtract it here or the line overruns m.width
	// by a gutter and the trailing column gets clipped.
	avail := m.width - fixedW - colGutter
	barW := min(max(avail, diagBarWidthMin), diagBarWidthMax)

	// Hand width the (capped) bar didn't claim back to columns still truncated
	// below their natural width — the same redistribution the no-bar path does
	// above, so wide schema/table/index names aren't clipped merely because a bar
	// column is present. The bar column itself is skipped (its number cell is
	// already wide enough for any humanized size).
	for slack := avail - barW; slack > 0; {
		grew := false
		for i := range colW {
			if i == barCol || slack <= 0 {
				continue
			}
			if want := naturalW[i] - colW[i]; want > 0 {
				give := min(want, slack)
				colW[i] += give
				slack -= give
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	// ── header ──────────────────────────────────────────────────────────────
	arrow := "↑"
	if s.sortDesc {
		arrow = "↓"
	}
	mark := func(label string, colIdx int) string {
		if colIdx == s.diagSortCol {
			return boldSeg(label + arrow)
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
						style = diagPercentBarStyle(barKind, cell.Num)
					}
					barStr = paintBar(barW, barSegment{cells: filled, style: style})
				} else {
					barStr = paintBar(barW) // empty bar for null cells
				}

				numStr := cell.Display
				if barIsBytes && cell.HasNum {
					numStr = humanize.Bytes(int64(cell.Num))
				}
				if barIsPercent && cell.HasNum {
					numStr = formatDiagPercent(cell)
				}
				// Colour the percentage next to its bar the same way the bar is
				// graded, so the digits read at a glance too.
				if barIsPercent && cell.HasNum && !selected {
					numStr = diagPercentBarStyle(barKind, cell.Num).Render(numStr)
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
			if cell.HasNum && i < nCols && isDiagPercentKind(cols[i].Kind) {
				raw = formatDiagPercent(cell)
			}
			display := truncateDiagCell(raw, colW[i])
			graded := i < nCols && cols[i].Kind == pg.DiagPercentGraded
			percentBad := i < nCols && cols[i].Kind == pg.DiagPercentBad
			costGraded := i < nCols && cols[i].Kind == pg.DiagCostGraded
			duration := i < nCols && cols[i].Kind == pg.DiagDuration
			isNumeric := cell.HasNum || (i < nCols && (cols[i].Kind == pg.DiagInt ||
				cols[i].Kind == pg.DiagFloat || cols[i].Kind == pg.DiagPercent ||
				cols[i].Kind == pg.DiagBytes || graded || percentBad || costGraded || duration))

			// Grade "higher is better" percent cells green→red so the eye can
			// triage hit ratios without reading digits. Skipped on the selected
			// row, which renders in the selection style like every other cell.
			if graded && cell.HasNum && !selected {
				display = gradedPercentStyle(cell.Num).Render(display)
			}
			// Grade "higher is worse" percent cells red→green on an absolute scale
			// (dead-tuple %, seq-scan %) using the same bands as the bloat column.
			if percentBad && cell.HasNum && !selected {
				display = bloatPercentStyle(int(cell.Num)).Render(display)
			}
			// Plain percent columns that aren't the headline bar (e.g. the
			// ins/upd/del split, HOT % when another column is the bar) get the
			// same green→red grade so every % column carries colour.
			if i < nCols && cols[i].Kind == pg.DiagPercent && i != barCol && cell.HasNum && !selected {
				display = percentStyle(cell.Num).Render(display)
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
			// Backend state: per-value colour (active green, idle-in-xact yellow,
			// aborted red, idle muted) so the eye triages the connection list.
			if i < nCols && cols[i].Kind == pg.DiagBackendState && !selected {
				if st, ok := stateStyle(cell.Display); ok {
					display = st.Render(display)
				}
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

	// Total footer: rendered directly beneath the last row, so short result
	// sets read as a closed table instead of the sum hiding at the bottom of
	// the screen. When the rows fill the viewport there's no padding below, so
	// it sits on the last content line and stays visible regardless of scroll.
	// Reuses the column layout but is never selected, barred or graded (the
	// total is each column's max — grading would paint it solid red).
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

	// Pad the data area to its budget so the help row stays pinned.
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// isDiagPercentKind reports whether k is one of the percentage column kinds
// (plain, higher-is-better graded, or higher-is-worse graded).
func isDiagPercentKind(k pg.DiagColumnKind) bool {
	return k == pg.DiagPercent || k == pg.DiagPercentGraded || k == pg.DiagPercentBad
}

// diagPercentDecimals is the fixed number of decimal places every percentage
// cell renders with, so a %-column reads with a consistent digit count down the
// column ("9.0" beside "10.5", never a bare "9") and the values line up when
// right-aligned. Server-side round() only bounds the precision; the shared float
// formatter's trailing-zero stripping is what made it ragged, so a fixed width
// is re-imposed here at render time.
const diagPercentDecimals = 1

// formatDiagPercent renders a percentage cell's numeric value to a fixed decimal
// count. A non-numeric cell (a NULL rendered as "—") keeps its placeholder.
func formatDiagPercent(cell pg.DiagCell) string {
	if !cell.HasNum {
		return cell.Display
	}
	return fmt.Sprintf("%.*f", diagPercentDecimals, cell.Num)
}

// diagPercentBarStyle picks the colour scale for a percent-typed headline bar:
// plain percents grade by fullness (percentStyle), "higher is better" columns
// green→red (gradedPercentStyle), "higher is worse" columns by the bloat bands.
func diagPercentBarStyle(kind pg.DiagColumnKind, pct float64) lipgloss.Style {
	switch kind {
	case pg.DiagPercentGraded:
		return gradedPercentStyle(pct)
	case pg.DiagPercentBad:
		return bloatPercentStyle(int(pct))
	default:
		return percentStyle(pct)
	}
}

// truncateDiagCell clips a cell value to maxW cells, appending "…" when the
// value is wider than the cap. It also guarantees the result is a single line:
// a table cell that kept embedded newlines would wrap the whole row across
// several terminal lines, overflowing the content budget and scrolling the
// header off-screen (raw query text — e.g. idle-in-xact "last_query" — is the
// usual source of these newlines).
func truncateDiagCell(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	// Fast path for the common single-line ASCII cell (numbers, identifiers,
	// already-flattened query text): display width is the byte length, so we can
	// slice directly and skip the per-rune grapheme-width scans that made this
	// the hottest frame in the profile. asciiWidth rejects any control byte, so a
	// newline never reaches here. Keep maxW-1 columns for text plus the ellipsis.
	if w, ok := asciiWidth(s); ok {
		if w <= maxW {
			return s
		}
		return s[:maxW-1] + "…"
	}
	// Slow path: non-ASCII or control bytes. Fold any whitespace run (newlines,
	// tabs, indentation) to a single space so the cell stays one line — the same
	// normalisation the top-queries table applies to its query column — then
	// measure with grapheme-aware widths.
	s = flattenQuery(s)
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
