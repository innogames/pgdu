package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// colCfgSpec describes one column-config overlay to handleColCfgKey: the row
// count, the cursor to move, and the close/reset/toggle actions. The behavioral
// differences between the pickers (availability gates, last-visible-column
// guard, conditional rebuilds) live inside each spec's closures.
type colCfgSpec struct {
	n      int
	cursor *int
	close  func()
	reset  func()
	toggle func(i int)
}

// handleColCfgKey drives a modal column-config overlay: Up/Down/Top/Bottom move
// the cursor over the column set, space/Enter toggle the highlighted column's
// visibility, r resets to defaults, and C/esc close it. Quit still quits.
func (m *Model) handleColCfgKey(msg tea.KeyMsg, sp colCfgSpec) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Columns), msg.Type == tea.KeyEsc:
		sp.close()
	case key.Matches(msg, m.keys.Up):
		if *sp.cursor > 0 {
			*sp.cursor--
		}
	case key.Matches(msg, m.keys.Down):
		if *sp.cursor < sp.n-1 {
			*sp.cursor++
		}
	case key.Matches(msg, m.keys.Top):
		*sp.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		*sp.cursor = sp.n - 1
	case key.Matches(msg, m.keys.ResetCols):
		sp.reset()
	case key.Matches(msg, m.keys.Refresh), key.Matches(msg, m.keys.Enter):
		// Refresh is space — the natural htop toggle; Enter also toggles.
		if i := *sp.cursor; i >= 0 && i < sp.n {
			sp.toggle(i)
		}
	}
	return nil
}

// handleColumnConfigKey drives the column-config overlay for the top-queries
// table (C on levelStatements). The mandatory query column and columns
// unavailable under the current track_planning setting can't be toggled.
func (m *Model) handleColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := stmtColumnRegistry()
	return m.handleColCfgKey(msg, colCfgSpec{
		n:      len(reg),
		cursor: &m.colCfgCursor,
		close:  func() { m.showColumnConfig = false },
		reset: func() {
			// Re-seed from the registry defaults (nil → ensure*Init rebuilds the map).
			m.stmtColsVisible = nil
			m.ensureStmtColsInit()
			m.rebuildStatementItems(s)
			m.saveColPrefs(colPrefsQueries, colVisToStrings(m.stmtColsVisible))
		},
		toggle: func(i int) {
			d := reg[i]
			if d.mandatory {
				return
			}
			if d.available != nil && !d.available(stmtCtx{trackPlanning: s.statTrackPlanning}) {
				return // can't show a column that isn't collected (e.g. plan_ms with track_planning off)
			}
			m.ensureStmtColsInit()
			m.stmtColsVisible[d.id] = !m.stmtColEnabled(d.id, d.defaultOn)
			m.rebuildStatementItems(s)
			m.saveColPrefs(colPrefsQueries, colVisToStrings(m.stmtColsVisible))
		},
	})
}

// handleActColumnConfigKey drives the column-config overlay for the Activity
// tool (C on levelActivity). Rebuilds only when a snapshot has been loaded.
func (m *Model) handleActColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := actColumnRegistry()
	return m.handleColCfgKey(msg, colCfgSpec{
		n:      len(reg),
		cursor: &m.actColCfgCursor,
		close:  func() { m.showActColumnConfig = false },
		reset: func() {
			m.actColsVisible = nil
			m.ensureActColsInit()
			if s.actRows != nil {
				m.rebuildActivityItems(s)
			}
			m.saveColPrefs(colPrefsActivity, colVisToStrings(m.actColsVisible))
		},
		toggle: func(i int) {
			d := reg[i]
			if d.mandatory {
				return
			}
			m.ensureActColsInit()
			m.actColsVisible[d.id] = !m.actColEnabled(d.id, d.defaultOn)
			if s.actRows != nil {
				m.rebuildActivityItems(s)
			}
			m.saveColPrefs(colPrefsActivity, colVisToStrings(m.actColsVisible))
		},
	})
}

// handleDiagColumnConfigKey drives the column-config overlay for a diagnostic
// result (C on levelDiagnosticResult). Unlike the registry-backed pickers it
// operates on the result's dynamic column set; visibility is kept per
// diagnostic key and by column name (see diagVis). The last visible column
// can't be hidden.
func (m *Model) handleDiagColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	res := s.diagResult
	key := s.diagVisKey()
	if res == nil || key == "" {
		m.showDiagColumnConfig = false
		return nil
	}
	cols := res.Columns
	return m.handleColCfgKey(msg, colCfgSpec{
		n:      len(cols),
		cursor: &m.diagColCfgCursor,
		close:  func() { m.showDiagColumnConfig = false },
		reset: func() {
			// Reset restores the diagnostic's defaults (which may hide columns), not
			// an all-visible view; persisting an empty map re-seeds from defaults on
			// reload.
			m.diagColsVisible[key] = defaultDiagVis(key)
			m.rebuildDiagItems(s)
			m.saveColPrefs(diagPrefsKey(key), map[string]bool{})
		},
		toggle: func(i int) {
			name := cols[i].Name
			vis := m.diagVis(key)
			if vis == nil {
				vis = make(map[string]bool, len(cols))
				for _, c := range cols {
					vis[c.Name] = true
				}
			}
			if diagColOn(vis, name) {
				visible := 0
				for _, c := range cols {
					if diagColOn(vis, c.Name) {
						visible++
					}
				}
				if visible <= 1 {
					return // keep at least one column on screen
				}
			}
			vis[name] = !diagColOn(vis, name)
			m.diagColsVisible[key] = vis
			m.rebuildDiagItems(s)
			m.saveColPrefs(diagPrefsKey(key), vis)
		},
	})
}

// handleTblColumnConfigKey drives the column-config overlay for the Table
// overview tool (C on levelTableStats).
func (m *Model) handleTblColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := tableColumnRegistry()
	return m.handleColCfgKey(msg, colCfgSpec{
		n:      len(reg),
		cursor: &m.tblColCfgCursor,
		close:  func() { m.showTblColumnConfig = false },
		reset: func() {
			m.tblColsVisible = nil
			m.ensureTblColsInit()
			m.rebuildTableStatItems(s)
			m.saveColPrefs(colPrefsTableStats, colVisToStrings(m.tblColsVisible))
		},
		toggle: func(i int) {
			d := reg[i]
			if d.mandatory {
				return
			}
			m.ensureTblColsInit()
			m.tblColsVisible[d.id] = !m.tblColEnabled(d.id, d.defaultOn)
			m.rebuildTableStatItems(s)
			m.saveColPrefs(colPrefsTableStats, colVisToStrings(m.tblColsVisible))
		},
	})
}
