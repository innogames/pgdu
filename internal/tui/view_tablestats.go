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
