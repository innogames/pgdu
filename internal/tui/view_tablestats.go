package tui

import (
	"strings"
)

// renderTblColumnConfig draws the htop-style column picker for the Table
// overview tool (C on levelTableStats). Same look-and-feel as
// renderActColumnConfig / renderColumnConfig.
func (m *Model) renderTblColumnConfig(_ *screen, height int) string {
	return tblSpec.renderConfig(m, &m.tblTable, tblCtx{}, height)
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
	b.WriteString("  " + mu("reset (the \"counters since\" timestamp in the status line); sizes, ages and the") + "\n")
	b.WriteString("  " + mu("buffered/dirty buffer-pool columns are point-in-time. Press ") + styleBadge.Render("C") +
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
