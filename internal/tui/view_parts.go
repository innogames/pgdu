package tui

import (
	"fmt"
	"strings"
	"time"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderPartsLevel is the top-level renderer for levelParts. It either shows
// the parts list + vacuum output pane (when a vacuum is in progress or done for
// this table), or the parts list + maintenance stats panel below.
func (m *Model) renderPartsLevel(s *screen, height int) string {
	// Footer lines rendered directly beneath the last part row (with the table
	// they annotate): the Σ totals line and the size/bloat colour legend.
	// These are dropped first when the terminal is too short to fit everything.
	footer, footerH := m.partsFooter(s)

	if m.vacuumPaneVisible(s) {
		// Keep a fixed slice of the list for context (partsVacuumListRows rows
		// plus the 1-line header) and hand the rest of the area to the
		// scrollable log pane.
		listH := partsVacuumListRows + 1
		paneH := height - listH - footerH
		if paneH < vacuumPaneMin {
			// Too short for list + footer + a usable pane: drop the footer, then
			// shrink the list, keeping the pane at its minimum.
			footer, footerH = "", 0
			paneH = height - listH
			if paneH < vacuumPaneMin {
				listH = max(height-vacuumPaneMin, 1)
				paneH = max(height-listH, 1)
			}
		}
		return m.renderListWithFooter(s, listH+footerH, footer) + m.renderVacuumPane(paneH)
	}

	panel := m.renderMaintPanel(s)
	if panel != "" {
		panelLines := strings.Count(panel, "\n")
		listH := height - footerH - panelLines
		if listH < 3 {
			// Terminal too short for list + footer + panel — drop the panel.
			if height-footerH < 3 {
				return m.renderList(s, height)
			}
			return m.renderListWithFooter(s, height, footer)
		}
		return m.renderListWithFooter(s, listH+footerH, footer) + panel
	}
	if height-footerH >= 1 {
		return m.renderListWithFooter(s, height, footer)
	}
	return m.renderList(s, height)
}

// partsVacuumListRows is how many parts-list rows stay visible for context
// while a VACUUM log pane is shown; the log gets everything else.
const partsVacuumListRows = 8

// vacuumPaneMin is the smallest usable vacuum log pane (header + a few lines).
const vacuumPaneMin = 4

// partsFooter builds the totals + legend lines shown directly under the parts
// list and returns them (each newline-terminated) with their line count.
func (m *Model) partsFooter(s *screen) (string, int) {
	var b strings.Builder
	n := 0
	if totals := m.renderPartsTotals(s); totals != "" {
		b.WriteString(totals + "\n")
		n++
	}
	if legend := renderPartsLegend(s); legend != "" {
		b.WriteString(legend + "\n")
		n++
	}
	return b.String(), n
}

// renderPartsLegend is the size/bloat colour legend for the parts level. Unlike
// the generic bottom-of-screen legend, it renders directly beneath the parts
// list so it sits with the table it describes (see renderPartsLevel).
func renderPartsLegend(s *screen) string {
	if s.level != levelParts {
		return ""
	}
	sep := styleMuted.Render("  ·  ")
	// "~" and the blank row are the two things about the bloat column that are
	// not self-evident: which figures were scanned, and why some rows have
	// none (toast without pgstattuple, a non-btree index, anything past the
	// size cap or the per-part timeout).
	return "  " + swatch(styleBar) + " " + styleMuted.Render("size") + sep +
		swatch(styleBloat) + " " + styleMuted.Render("bloat") + sep +
		styleMuted.Render("~ estimated from statistics") + sep +
		styleMuted.Render("blank not measured")
}

// renderPartsTotals sums the parts sizes and wasted bytes and renders a pinned
// Σ footer beneath the parts list, with the sums aligned under their columns
// (like the buffer-tables footer). The heap/index/toast breakdown follows in
// the name area. Returns "" until parts have loaded.
func (m *Model) renderPartsTotals(s *screen) string {
	if s.level != levelParts || len(s.items) == 0 {
		return ""
	}
	var heap, idx, toast, bloat, measured int64
	n := 0
	anyBloat := false
	for _, it := range s.items {
		p, ok := it.data.(pg.Part)
		if !ok {
			continue
		}
		switch p.Kind {
		case pg.PartHeap:
			heap += p.SizeBytes
		case pg.PartIndex:
			idx += p.SizeBytes
		case pg.PartToast:
			toast += p.SizeBytes
		}
		// Bloat comes from the item fields, not the Part payload: onBloatFilled
		// patches item.bloat/hasBloat in place and leaves item.data untouched.
		if it.hasBloat {
			bloat += it.bloat
			measured += p.SizeBytes
			anyBloat = true
		}
		n++
	}
	total := heap + idx + toast
	if total == 0 {
		return ""
	}
	bloatCols := ""
	if anyBloat {
		// Over the measured parts, not the total: TOAST has no estimator, and a
		// table whose TOAST dwarfs everything else would otherwise divide real
		// waste by a size nobody measured and print 0% under a column of 30s.
		pct := 0
		if measured > 0 {
			pct = int(float64(bloat) / float64(measured) * 100)
		}
		byteStr, pctStr := "-", "-"
		if bloat > 0 {
			byteStr = humanize.Bytes(bloat)
			pctStr = fmt.Sprintf("%d%%", pct)
		}
		bloatCols = padRight(byteStr, bloatByteColW) + "  " + padRight(pctStr, bloatPctColW) + "  "
	}
	var breakdown []string
	if heap > 0 {
		breakdown = append(breakdown, "heap "+humanize.Bytes(heap))
	}
	if idx > 0 {
		breakdown = append(breakdown, "index "+humanize.Bytes(idx))
	}
	if toast > 0 {
		breakdown = append(breakdown, "toast "+humanize.Bytes(toast))
	}
	// Cursor slot + blank bar area, then the same column layout as the rows
	// (size, bloat bytes, bloat %, blank type column, drillMark slot).
	line := "  " + strings.Repeat(" ", m.barWidth(s)+2) + "  " +
		padRight(humanize.Bytes(total), 10) + "  " +
		bloatCols +
		strings.Repeat(" ", partTypeColW) + "  " + "  " +
		fmt.Sprintf("Σ %d parts", n)
	if len(breakdown) > 0 {
		line += " · " + strings.Join(breakdown, " · ")
	}
	return styleTotal.Render(line)
}

// renderVacuumBanner renders the one-line confirmation prompt for the vacuum
// action on the parts level. Returns "" when there is nothing to show.
func (m *Model) renderVacuumBanner(s *screen) string {
	if s.level != levelParts || s.parts.pendingMaint == "" {
		return ""
	}
	return confirmBanner(s.parts.pendingMaint.statement(s.table.Qualified()))
}

// renderVacuumPane renders the streaming VACUUM output pane (header + output body).
func (m *Model) renderVacuumPane(paneH int) string {
	// Pointer, not a copy: the follow-snap and follow-re-evaluation below must
	// persist to m.vacuum so tail-follow survives across renders.
	vs := &m.vacuum
	var hdr string
	mu := styleMuted.Render
	// While running the clock ticks live; once finished it freezes at the
	// completion time so "done in …" doesn't keep counting up on every render.
	elapsed := time.Since(vs.started)
	if !vs.running && !vs.finished.IsZero() {
		elapsed = vs.finished.Sub(vs.started)
	}
	elapsed = elapsed.Round(time.Second)
	qual := vs.table.Qualified()
	switch {
	case vs.running:
		hdr = "  " + m.spinner.View() + " " + styleSelected.Render(vs.op.label()) +
			mu(" "+qual) +
			mu(fmt.Sprintf("  running %s", elapsed))
	case vs.err != nil:
		hdr = "  " + styleErr.Render(vs.op.label()+" "+qual+" failed: "+shortErr(vs.err)) +
			mu(fmt.Sprintf("  (after %s) · press ", elapsed)) +
			styleBadge.Render("esc") + mu(" to dismiss")
	default:
		hdr = "  " + styleBadge.Render(vs.op.label()) +
			mu(" "+qual) +
			mu(fmt.Sprintf("  done in %s · press ", elapsed)) +
			styleBadge.Render("esc") + mu(" to dismiss")
	}

	bodyH := paneH - 1
	if bodyH < 1 {
		return hdr + "\n"
	}

	if vs.follow {
		// Snap offset to end so scrollWindow tail-follows.
		vs.offset = len(vs.buf)
	}
	body := strings.Join(vs.buf, "\n")
	rendered := scrollWindow(body, &vs.offset, bodyH)
	// Re-evaluate follow: if offset landed at the bottom, keep following.
	lines := strings.Count(body, "\n")
	if vs.offset >= lines-bodyH+1 {
		vs.follow = true
	}

	return hdr + "\n" + rendered
}

// shortErr returns a single-line error summary trimmed to ~80 runes.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	return s
}
