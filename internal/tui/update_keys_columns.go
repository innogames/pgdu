package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// handleColumnConfigKey drives the modal column-config overlay (C on the
// top-queries table): Up/Down/Top/Bottom move the cursor over the column
// registry, space/Enter toggle the highlighted column's visibility and rebuild
// the table from the cached window (no DB), and C/esc close it. The mandatory
// query column and columns unavailable under the current track_planning setting
// can't be toggled. Quit still quits.
func (m *Model) handleColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := stmtColumnRegistry()
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Columns), msg.Type == tea.KeyEsc:
		m.showColumnConfig = false
	case key.Matches(msg, m.keys.Up):
		if m.colCfgCursor > 0 {
			m.colCfgCursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.colCfgCursor < len(reg)-1 {
			m.colCfgCursor++
		}
	case key.Matches(msg, m.keys.Top):
		m.colCfgCursor = 0
	case key.Matches(msg, m.keys.Bottom):
		m.colCfgCursor = len(reg) - 1
	case key.Matches(msg, m.keys.Refresh), key.Matches(msg, m.keys.Enter):
		// Refresh is space — the natural htop toggle; Enter also toggles.
		if m.colCfgCursor < 0 || m.colCfgCursor >= len(reg) {
			break
		}
		d := reg[m.colCfgCursor]
		if d.mandatory {
			break
		}
		if d.available != nil && !d.available(stmtCtx{trackPlanning: s.statTrackPlanning}) {
			break // can't show a column that isn't collected (e.g. plan_ms with track_planning off)
		}
		m.ensureStmtColsInit()
		m.stmtColsVisible[d.id] = !m.stmtColEnabled(d.id, d.defaultOn)
		m.rebuildStatementItems(s)
		m.saveColPrefs(colPrefsQueries, colVisToStrings(m.stmtColsVisible))
	}
	return nil
}

// handleActColumnConfigKey drives the modal column-config overlay for the
// Activity tool (C on levelActivity). Mirrors handleColumnConfigKey.
func (m *Model) handleActColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := actColumnRegistry()
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Columns), msg.Type == tea.KeyEsc:
		m.showActColumnConfig = false
	case key.Matches(msg, m.keys.Up):
		if m.actColCfgCursor > 0 {
			m.actColCfgCursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.actColCfgCursor < len(reg)-1 {
			m.actColCfgCursor++
		}
	case key.Matches(msg, m.keys.Top):
		m.actColCfgCursor = 0
	case key.Matches(msg, m.keys.Bottom):
		m.actColCfgCursor = len(reg) - 1
	case key.Matches(msg, m.keys.Refresh), key.Matches(msg, m.keys.Enter):
		if m.actColCfgCursor < 0 || m.actColCfgCursor >= len(reg) {
			break
		}
		d := reg[m.actColCfgCursor]
		if d.mandatory {
			break
		}
		m.ensureActColsInit()
		m.actColsVisible[d.id] = !m.actColEnabled(d.id, d.defaultOn)
		if s.actRows != nil {
			m.rebuildActivityItems(s)
		}
		m.saveColPrefs(colPrefsActivity, colVisToStrings(m.actColsVisible))
	}
	return nil
}
