package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
)

// triageGlyph is the severity marker at the head of each triage line, coloured
// with the shared triage-severity palette (error / accent / ok).
func triageGlyph(sev pg.Severity) string {
	switch sev {
	case pg.SevCrit:
		return lipgloss.NewStyle().Foreground(colorError).Render("✗")
	case pg.SevWarn:
		return lipgloss.NewStyle().Foreground(colorAccent).Render("▲")
	}
	return lipgloss.NewStyle().Foreground(colorOK).Render("●")
}

// triageTargetLabel names the screen Enter drills into for a triage line, for
// the muted "↵ …" hint at the end of crit/warn rows.
func triageTargetLabel(r pg.TriageResult) string {
	switch r.Target {
	case pg.TriageTargetLockTree:
		return "lock tree"
	case pg.TriageTargetMaintenance:
		return "system overview"
	}
	if d, ok := pg.DiagnosticByKey(r.DiagKey); ok {
		return d.Title
	}
	return "diagnostic"
}

// renderTriageList renders the one-key health report: severity-sorted rows of
// glyph | check | detail | drill hint, with green checks collapsed into the
// trailing summary row (see triageItems).
func (m *Model) renderTriageList(s *screen, height int) string {
	var b strings.Builder

	crit, warn := 0, 0
	for _, r := range s.triageResults {
		switch r.Severity {
		case pg.SevCrit:
			crit++
		case pg.SevWarn:
			warn++
		}
	}
	var summary string
	if crit+warn > 0 {
		if crit > 0 {
			summary = triageGlyph(pg.SevCrit) + " " + lipgloss.NewStyle().Foreground(colorError).Render(fmt.Sprintf("%d critical", crit))
		}
		if warn > 0 {
			if summary != "" {
				summary += styleMuted.Render("  ·  ")
			}
			summary += triageGlyph(pg.SevWarn) + " " + lipgloss.NewStyle().Foreground(colorAccent).Render(fmt.Sprintf("%d warning(s)", warn))
		}
	} else {
		summary = triageGlyph(pg.SevOK) + " " + styleMuted.Render("all checks ok")
	}
	b.WriteString("  " + summary + "\n")
	height--

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
			cursor = styleSelected.Render("▶ ")
			name = styleSelected.Render(name)
		}
		glyph := triageGlyph(pg.SevOK)
		hint := ""
		if r, ok := it.data.(pg.TriageResult); ok {
			glyph = triageGlyph(r.Severity)
			hint = "  " + styleMuted.Render("↵ "+triageTargetLabel(r))
		}
		line := cursor + glyph + " " + name + "  " + styleMuted.Render(it.detail) + hint
		b.WriteString(truncateToWidth(line, m.width) + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}
