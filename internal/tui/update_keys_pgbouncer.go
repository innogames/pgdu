package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// handlePgbKey dispatches the pgbouncer tool's own keys. Returns false when
// the key is not one of them (or the screen is not a pgbouncer level), so the
// shared handler runs as usual.
func (m *Model) handlePgbKey(s *screen, msg tea.KeyMsg) (tea.Cmd, bool) {
	if s.tool != toolPgBouncer {
		return nil, false
	}
	switch s.level {
	case levelPgBouncers, levelPgBouncer, levelPgBouncerShow:
	default:
		return nil, false
	}
	switch {
	case key.Matches(msg, m.keys.OpenLog):
		inst := m.pgbSelectedInstance(s)
		if inst == nil {
			return nil, true
		}
		if inst.Logfile == "" {
			m.notice = "no logfile configured for " + inst.Name
			return nil, true
		}
		m.stack = append(m.stack, m.logScreen(pg.OpenLocalLog(inst.Logfile)))
		return m.loadCurrent(), true
	case key.Matches(msg, m.keys.ToggleRefresh):
		m.cyclePgbRefresh()
		if m.pgbRefresh > 0 && !m.pgbTicking {
			if tick := m.pgbTick(); tick != nil {
				m.pgbTicking = true
				return tick, true
			}
		}
		return nil, true
	}
	return nil, false
}

// pgbSelectedInstance is the instance a key acts on: the highlighted row on
// the list, the screen's own instance below it.
func (m *Model) pgbSelectedInstance(s *screen) *pg.PgBouncerInstance {
	if s.level != levelPgBouncers {
		return s.pgbInst
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return nil
	}
	it := s.items[vis[s.cursor]]
	if it.pgbIdx <= 0 || it.pgbIdx > len(s.pgbInsts) {
		return nil
	}
	return &s.pgbInsts[it.pgbIdx-1]
}
