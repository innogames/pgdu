package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// drillTriage opens the screen that backs the selected triage line.
func (m *Model) drillTriage(s *screen, cur item) tea.Cmd {
	// Drill into the screen that backs the selected triage line. The
	// collapsed "N checks ok" summary row carries no TriageResult and is
	// inert.
	r, ok := cur.data.(pg.TriageResult)
	if !ok {
		return nil
	}
	switch r.Target {
	case pg.TriageTargetLockTree:
		m.stack = append(m.stack, &screen{
			level: levelLockTree, title: "lock tree", tool: toolActivity,
			db: s.db, loading: true})
		return m.loadCurrent()
	case pg.TriageTargetMaintenance:
		m.stack = append(m.stack, m.toolEntryScreen(toolMaintenance))
		return m.loadCurrent()
	case pg.TriageTargetActivity:
		m.stack = append(m.stack, m.toolEntryScreen(toolActivity))
		return m.loadCurrent()
	case pg.TriageTargetPgBouncer:
		m.stack = append(m.stack, m.toolEntryScreen(toolPgBouncer))
		return m.loadCurrent()
	default:
		for i := range pg.Diagnostics {
			if pg.Diagnostics[i].Key == r.DiagKey {
				m.stack = append(m.stack, diagnosticResultScreen(&pg.Diagnostics[i], r.DB, false))
				return m.loadCurrent()
			}
		}
		return nil
	}
}
