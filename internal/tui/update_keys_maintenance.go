package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// jumpToDiagnostic pushes the diagnostic with the given key over the current
// screen, run against db ("" = the default database): the overview's r / o
// cross-links and the recommendations that point at a diagnostic. Unknown keys
// are a programming error and do nothing.
func (m *Model) jumpToDiagnostic(key, db string) tea.Cmd {
	for i := range pg.Diagnostics {
		if pg.Diagnostics[i].Key == key {
			m.stack = append(m.stack, diagnosticResultScreen(&pg.Diagnostics[i], db, false))
			return m.loadCurrent()
		}
	}
	return nil
}

// openSettings pushes the pg_settings browser over s. filter pre-seeds its
// fuzzy filter, so a GUC recommendation lands on the knob it names.
func (m *Model) openSettings(s *screen, filter string) tea.Cmd {
	next := &screen{level: levelSettings, title: "settings", tool: toolMaintenance, db: s.db, filter: filter}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// handleMaintenanceEnter acts on the highlighted action row of the system
// overview: a capacity row arms its reset confirmation (y executes), a
// recommendation opens the screen that explains it.
func (m *Model) handleMaintenanceEnter(s *screen) tea.Cmd {
	st := &s.maintenance
	rows := st.actionRows()
	if st.cursor < 0 || st.cursor >= len(rows) {
		return nil
	}
	row := rows[st.cursor]
	if row.advice == nil {
		st.pendingReset = row.reset
		return nil
	}
	return m.openAdviceTarget(s, *row.advice)
}

// openAdviceTarget pushes the screen a recommendation points at; nothing for
// advice without a target (Enter is disabled on those rows, see enterLabel).
func (m *Model) openAdviceTarget(s *screen, a pg.Advice) tea.Cmd {
	switch a.Target {
	case pg.AdviceTargetDiagnostic:
		return m.jumpToDiagnostic(a.DiagKey, a.DB)
	case pg.AdviceTargetLockTree:
		next := &screen{level: levelLockTree, title: "lock tree", tool: toolActivity, db: s.db, loading: true}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case pg.AdviceTargetActivity:
		m.stack = append(m.stack, m.toolEntryScreen(toolActivity))
		return m.loadCurrent()
	case pg.AdviceTargetSettings:
		return m.openSettings(s, a.Setting)
	}
	return nil
}
