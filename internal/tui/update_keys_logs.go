package tui

import (
	"slices"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// handleLogKey dispatches the log-analyzer-only bindings (m group mode, tab
// pane, w window, j jump) plus the shared t/C keys when they land on a log
// level. Esc/q walk back through the stack to the file picker. Returns handled=false when the key is not ours so handleKey
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
	}

	if key.Matches(msg, m.keys.LogJump) && logs != nil {
		var e *pglog.Entry
		switch s.level {
		case levelLogEntry:
			e = s.log.entry
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

	if s.level == levelLogGroup && key.Matches(msg, m.keys.LogParams) {
		// Cycle entries → by parameters → by $1. Leaving the table restores the
		// entry order; the table picks its own default in rebuildLogParamRows.
		s.log.params = s.log.params.next()
		if s.log.params == logParamsOff {
			s.sort, s.sortDesc = sortByLast, true
		}
		m.rebuildLogChild(s)
		s.resetCursor()
		return nil, true
	}

	if s.level != levelLogs {
		return nil, false
	}

	switch {
	case key.Matches(msg, m.keys.LogGroupMode):
		if s.log.groupBy == logGroupByCategory {
			s.log.groupBy = logGroupByNone
		} else {
			s.log.groupBy = logGroupByCategory
		}
		if s.log.view == logViewGroups {
			m.rebuildLogItems(s)
			s.skipInertRow(1)
		}
		return nil, true

	case key.Matches(msg, m.keys.LogPane):
		s.log.view = s.log.view.next(s.log.report != nil && s.log.report.PoolerStats > 0)
		if s.log.view == logViewGroups {
			s.sort, s.sortDesc = sortByCount, true
		}
		m.rebuildLogItems(s)
		s.resetCursor()
		s.skipInertRow(1)
		return m.logHostsCmd(s), true

	case key.Matches(msg, m.keys.LogWindow):
		// Widen the tail window: 32 → 64 → 128 → 256 → 512 MiB → whole → 32.
		next := logWindowSteps[0]
		for i, w := range logWindowSteps {
			if w == s.log.window && i+1 < len(logWindowSteps) {
				next = logWindowSteps[i+1]
				break
			}
		}
		s.log.window = next
		return m.loadCurrent(), true

	case key.Matches(msg, m.keys.Columns):
		if s.log.view.table() {
			logSpec(s.log.view).open(m, m.logTableFor(s.log.view))
		}
		return nil, true
	}
	return nil, false
}

// handleLogColumnConfigKey drives the C picker over the timeline columns.
// Enabling the hostname column kicks off the reverse-DNS lookups.
func (m *Model) handleLogColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	v := s.log.view
	cmd := logSpec(v).handleKey(m, m.logTableFor(v), msg, logCtx{}, func() { m.rebuildLogItems(s) })
	return tea.Batch(cmd, m.logHostsCmd(s))
}

// jumpToLogEntry pops back to the levelLogs screen, switches it to the
// timeline and puts the cursor on e, so the user sees what happened around
// that line. Any / filter is lifted so the target row is visible.
func (m *Model) jumpToLogEntry(logs *screen, e *pglog.Entry) tea.Cmd {
	for i, v := range slices.Backward(m.stack) {
		if v == logs {
			m.stack = m.stack[:i+1]
			break
		}
	}
	logs.filter = ""
	logs.filterFocused = false
	logs.log.view = logViewTimeline
	m.rebuildLogItems(logs)
	for vi, idx := range logs.visibleIndexes() {
		if t := logs.logEntryOf(logs.items[idx]); t != nil && t.Off == e.Off {
			logs.cursor = vi
			break
		}
	}
	return m.logHostsCmd(logs)
}

// logDescribeTarget resolves `d` on the log levels: the main table of the
// statement behind the entry under the cursor (a slow query or log_statement SQL, or the
// STATEMENT attached to an error), described in the entry's database when the
// prefix carries %d. Without %d the line names the user but not the database,
// so the connection database is only a first guess and the lookup sweeps every
// database (anyDB): on a host with many application databases the logged table
// is rarely in the one pgdu happens to be connected to. On the groups pane the
// group's newest sample stands in for the row.
func logDescribeTarget(s *screen) (descTarget, bool) {
	var e *pglog.Entry
	switch s.level {
	case levelLogEntry:
		e = s.log.entry
	default:
		vis := s.visibleIndexes()
		if s.cursor < 0 || s.cursor >= len(vis) {
			return descTarget{}, false
		}
		it := s.items[vis[s.cursor]]
		e = s.logEntryOf(it)
		if g, ok := it.data.(*pglog.Group); ok && e == nil && s.log.report != nil {
			// Newest sample that carries a statement (an error's STATEMENT line
			// is only logged with log_min_error_statement, so not every row has one).
			for _, idx := range slices.Backward(g.Samples) {
				if idx < len(s.log.report.Entries) {
					c := &s.log.report.Entries[idx]
					if len(c.SQL) > 0 || len(c.Statement) > 0 {
						e = c
						break
					}
				}
			}
		}
	}
	if e == nil {
		return descTarget{}, false
	}
	sql := e.SQL
	if len(sql) == 0 {
		sql = e.Statement
	}
	if len(sql) == 0 {
		return descTarget{}, false
	}
	db, anyDB := s.db, true
	if len(e.DB) > 0 {
		db, anyDB = string(e.DB), false
	}
	if name := pg.MainTable(string(sql)); name != "" {
		return descTarget{byName: true, db: db, tableName: name, anyDB: anyDB}, true
	}
	// DDL on an index (DROP/ALTER/REINDEX INDEX) has no table to point at, but
	// the index itself can be described.
	if name := pg.MainIndex(string(sql)); name != "" {
		return descTarget{indexByName: true, db: db, indexName: name, anyDB: anyDB}, true
	}
	return descTarget{}, false
}
