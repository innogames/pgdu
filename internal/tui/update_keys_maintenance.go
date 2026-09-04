package tui

import tea "github.com/charmbracelet/bubbletea"

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
