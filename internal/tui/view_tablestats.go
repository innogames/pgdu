package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderTblColumnConfig draws the htop-style column picker for the Table
// overview tool (C on levelTableStats). Same look-and-feel as
// renderActColumnConfig / renderColumnConfig.
func (m *Model) renderTblColumnConfig(_ *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("configure columns") + mu("  ·  ") +
		styleBadge.Render("space") + mu(" toggles · ") +
		styleBadge.Render("↑/↓") + mu(" move · ") +
		styleBadge.Render("r") + mu(" reset · ") +
		styleBadge.Render("C") + mu(" or ") + styleBadge.Render("esc") + mu(" to close") + "\n")
	b.WriteString("  " + mu("choose which columns the table overview shows") + "\n\n")

	m.ensureTblColsInit()
	reg := tableColumnRegistry()
	nameW := 0
	for _, d := range reg {
		if n := len(d.name); n > nameW {
			nameW = n
		}
	}
	for i, d := range reg {
		on := d.mandatory || m.tblColEnabled(d.id, d.defaultOn)
		box := "[ ]"
		if on {
			box = "[x]"
		}
		cursor := "  "
		if i == m.tblColCfgCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		label := box + "  " + padRight(d.name, nameW)
		var rendered string
		switch i {
		case m.tblColCfgCursor:
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

// renderTableStatsInfo is the ? reference overlay for the Table overview: a
// one-line-per-column glossary built from the same registry the C picker uses,
// so the two never drift. Toggled by ? (handleInfoKey scrolls/closes it).
func (m *Model) renderTableStatsInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	infoHeader(&b, "table overview reference")
	b.WriteString("  " + mu("One row per base / partitioned / materialized table in the schema. Write and") + "\n")
	b.WriteString("  " + mu("scan counters (ins/upd/del, seq/idx, cache) are cumulative since the last stats") + "\n")
	b.WriteString("  " + mu("reset; sizes and ages are point-in-time. Press ") + styleBadge.Render("C") +
		mu(" to choose which columns show.") + "\n\n")

	reg := tableColumnRegistry()
	nameW := 0
	for _, d := range reg {
		if n := len(d.name); n > nameW {
			nameW = n
		}
	}
	for _, d := range reg {
		b.WriteString("  " + styleSelected.Render(padRight(d.name, nameW)) + "  " + mu(d.desc) + "\n")
	}
	return padInfo(&b, height)
}
