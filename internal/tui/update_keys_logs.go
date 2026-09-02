package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// handleLogKey dispatches the log-analyzer-only bindings (m group mode, tab
// pane, w window, o file picker) plus the shared v/f/t/C keys when they land
// on a log level. Returns handled=false when the key is not ours so handleKey
// continues with its generic cases.
func (m *Model) handleLogKey(s *screen, msg tea.KeyMsg) (cmd tea.Cmd, handled bool) {
	switch s.level {
	case levelLogs, levelLogGroup, levelLogEntry, levelLogFiles:
	default:
		return nil, false
	}
	logs := m.findLevel(levelLogs)

	switch {
	case key.Matches(msg, m.keys.ToggleRefresh):
		if logs == nil {
			return nil, true
		}
		m.cycleLogRefresh()
		if m.logRefresh > 0 && !m.logTicking {
			if tick := m.logTick(); tick != nil {
				m.logTicking = true
				return tick, true
			}
		}
		return nil, true

	case key.Matches(msg, m.keys.LogFiles):
		// Back to (or open) the picker. When the picker is below us on the
		// stack, pop to it; otherwise push a fresh one.
		for i := len(m.stack) - 1; i >= 0; i-- {
			if m.stack[i].level == levelLogFiles {
				m.stack = m.stack[:i+1]
				return nil, true
			}
		}
		m.stack = append(m.stack, &screen{level: levelLogFiles, title: "log files", tool: toolLogs, db: m.client.DefaultDB(), loading: true})
		return m.loadCurrent(), true
	}

	if key.Matches(msg, m.keys.LogJump) && logs != nil {
		var e *pg.LogEntry
		switch s.level {
		case levelLogEntry:
			e = s.logEntry
		case levelLogGroup:
			if vis := s.visibleIndexes(); s.cursor >= 0 && s.cursor < len(vis) {
				e = s.logEntryOf(s.items[vis[s.cursor]])
			}
		}
		if e == nil {
			return nil, true
		}
		return m.jumpToLogEntry(logs, e), true
	}

	if s.level != levelLogs {
		return nil, false
	}

	switch {
	case key.Matches(msg, m.keys.Verbose):
		s.logShowSpam = !s.logShowSpam
		m.rebuildLogItems(s)
		m.skipLogHeader(s, 1)
		return nil, true

	case key.Matches(msg, m.keys.LogGroupMode):
		if s.logGroupBy == logGroupByCategory {
			s.logGroupBy = logGroupByNone
		} else {
			s.logGroupBy = logGroupByCategory
		}
		if s.logView == logViewGroups {
			m.rebuildLogItems(s)
			m.skipLogHeader(s, 1)
		}
		return nil, true

	case key.Matches(msg, m.keys.LogPane):
		if s.logView == logViewGroups {
			s.logView = logViewTimeline
		} else {
			s.logView = logViewGroups
			s.sort, s.sortDesc = sortByCount, true
		}
		m.rebuildLogItems(s)
		s.resetCursor()
		m.skipLogHeader(s, 1)
		return m.logHostsCmd(s), true

	case key.Matches(msg, m.keys.LogWindow):
		// Widen the tail window: 32 → 64 → 128 → 256 → 512 MiB → whole → 32.
		next := logWindowSteps[0]
		for i, w := range logWindowSteps {
			if w == s.logWindow && i+1 < len(logWindowSteps) {
				next = logWindowSteps[i+1]
				break
			}
		}
		s.logWindow = next
		return m.loadCurrent(), true

	case key.Matches(msg, m.keys.Columns):
		if s.logView == logViewTimeline {
			m.ensureLogColsInit()
			m.showInfo = false
			m.showLogColumnConfig = true
			m.logColCfgCursor = 0
		}
		return nil, true
	}
	return nil, false
}

// handleLogColumnConfigKey drives the C picker over the timeline columns.
// Enabling the hostname column kicks off the reverse-DNS lookups.
func (m *Model) handleLogColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := logColumnRegistry()
	cmd := m.handleColCfgKey(msg, colCfgSpec{
		n:      len(reg),
		cursor: &m.logColCfgCursor,
		close:  func() { m.showLogColumnConfig = false },
		reset: func() {
			m.logColsVisible = nil
			m.ensureLogColsInit()
			m.rebuildLogItems(s)
			m.saveColPrefs(colPrefsLogs, colVisToStrings(m.logColsVisible))
		},
		toggle: func(i int) {
			d := reg[i]
			if d.mandatory {
				return
			}
			m.ensureLogColsInit()
			m.logColsVisible[d.id] = !m.logColEnabled(d.id, d.defaultOn)
			m.rebuildLogItems(s)
			m.saveColPrefs(colPrefsLogs, colVisToStrings(m.logColsVisible))
		},
	})
	return tea.Batch(cmd, m.logHostsCmd(s))
}

// jumpToLogEntry pops back to the levelLogs screen, switches it to the
// timeline and puts the cursor on e, so the user sees what happened around
// that line. The spam filter and any / filter are lifted when they would hide
// the target row.
func (m *Model) jumpToLogEntry(logs *screen, e *pg.LogEntry) tea.Cmd {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i] == logs {
			m.stack = m.stack[:i+1]
			break
		}
	}
	if !logs.logShowSpam && e.Category.IsSpam() {
		logs.logShowSpam = true
	}
	logs.filter = ""
	logs.filterFocused = false
	logs.logView = logViewTimeline
	m.rebuildLogItems(logs)
	for vi, idx := range logs.visibleIndexes() {
		if t := logs.logEntryOf(logs.items[idx]); t != nil && t.Off == e.Off {
			logs.cursor = vi
			break
		}
	}
	return m.logHostsCmd(logs)
}
