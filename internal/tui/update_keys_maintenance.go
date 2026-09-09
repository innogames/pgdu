package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// jumpToDiagnostic pushes the cluster-wide diagnostic with the given key over
// the current screen (the overview's r / o cross-links). Unknown keys are a
// programming error and do nothing.
func (m *Model) jumpToDiagnostic(key string) tea.Cmd {
	for i := range pg.Diagnostics {
		if pg.Diagnostics[i].Key == key {
			m.stack = append(m.stack, diagnosticResultScreen(&pg.Diagnostics[i], "", false))
			return m.loadCurrent()
		}
	}
	return nil
}

// handleMaintenanceEnter arms the reset confirmation for the highlighted
// extension-capacity row of the maintenance overview.
func (m *Model) handleMaintenanceEnter(s *screen) tea.Cmd {
	// Arm the reset confirmation for the highlighted extension capacity row.
	switch s.maintenance.cursor {
	case 0:
		s.maintenance.pendingReset = "statements"
	case 1:
		s.maintenance.pendingReset = "qualstats"
	case 2:
		s.maintenance.pendingReset = "tablestats"
	case 3:
		s.maintenance.pendingReset = "tablestats-all"
	}
	return nil
}
