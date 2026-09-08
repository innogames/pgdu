package tui

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
)

// handleColumnConfigKey drives the C picker on the top-queries table; planning
// columns can't be shown while track_planning is off.
func (m *Model) handleColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	ctx := stmtCtx{trackPlanning: s.stat.trackPlanning}
	return stmtSpec.handleKey(m, &m.stmtTable, msg, ctx, func() { m.rebuildStatementItems(s) })
}

// handleActColumnConfigKey drives the C picker on the Activity table; the
// rebuild waits for the first snapshot.
func (m *Model) handleActColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	return actSpec.handleKey(m, &m.actTable, msg, actCtx{}, func() {
		if s.act.rows != nil {
			m.rebuildActivityItems(s)
		}
	})
}

// handleTblColumnConfigKey drives the C picker on the Table overview.
func (m *Model) handleTblColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	return tblSpec.handleKey(m, &m.tblTable, msg, tblCtx{}, func() { m.rebuildTableStatItems(s) })
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

// handleTupleColumnConfigKey drives the C picker on the heap tuple list. The
// rows are the relation's live columns (pages.tupleCols); the pick is part of
// the tuple query's projection, so every toggle reloads the page rather than
// re-projecting loaded rows. The picker stays open across the reload. An empty
// pick is fine (the list falls back to its physical columns), and nothing is
// persisted — see pageState.tuplePick.
func (m *Model) handleTupleColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	cols := s.pages.tupleCols
	if len(cols) == 0 {
		m.showTupleColumnConfig = false
		return nil
	}
	var reload tea.Cmd
	cmd := m.handleColCfgKey(msg, colCfgSpec{
		n:      len(cols),
		cursor: &m.tupleColCfgCursor,
		close:  func() { m.showTupleColumnConfig = false },
		reset: func() {
			s.pages.tuplePick = nil // nil = the default: the primary key
			reload = m.loadCurrent()
		},
		toggle: func(i int) {
			name := cols[i].Name
			pick := slices.Clone(s.pages.tuplePick)
			if pick == nil {
				pick = []string{} // an explicit pick, distinct from "default"
			}
			if j := slices.Index(pick, name); j >= 0 {
				pick = slices.Delete(pick, j, j+1)
			} else {
				pick = append(pick, name)
			}
			s.pages.tuplePick = pick
			reload = m.loadCurrent()
		},
	})
	if reload != nil {
		return reload
	}
	return cmd
}
