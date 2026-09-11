package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pglog"
)

// logLink is what a screen's l key opens in the log analyzer: the current
// server log, narrowed to one category while catOn is set. It is the bridge
// from a counter to the lines behind it — the WAL header's checkpoint cadence
// to what each checkpoint cost, a top query's aggregate to its slow-log
// executions — or, from the system overview, just the log itself.
type logLink struct {
	cat   pglog.Category
	catOn bool
}

// logLinkFor names the log link of a screen, or false where l is not a log
// link (the pgbouncer levels open the instance's own logfile instead, in
// handlePgbKey).
func logLinkFor(s *screen) (logLink, bool) {
	switch s.level {
	case levelWAL:
		return logLink{cat: pglog.CatCheckpoint, catOn: true}, true
	case levelStatements, levelStatementDetail:
		return logLink{cat: pglog.CatSlowQuery, catOn: true}, true
	case levelMaintenance:
		return logLink{}, true
	}
	return logLink{}, false
}

// help is l's footer text for this link, in the overview's "→ wal" style.
func (l logLink) help() string {
	switch {
	case !l.catOn:
		return "→ logs"
	case l.cat == pglog.CatCheckpoint:
		return "→ checkpoint log"
	case l.cat == pglog.CatSlowQuery:
		return "→ slow query log"
	}
	return "→ " + l.cat.Label() + " log"
}

// handleOpenLogKey dispatches l on the levels that link into the log analyzer.
// Returns handled=false when the key is not ours.
func (m *Model) handleOpenLogKey(s *screen, msg tea.KeyMsg) (tea.Cmd, bool) {
	link, ok := logLinkFor(s)
	if !ok || !key.Matches(msg, m.keys.OpenLog) {
		return nil, false
	}
	return m.openLogLink(link), true
}

// openLogLink enters the log analyzer the way the tools menu does, but without
// a pick and (when the link narrows) already restricted to one category: an
// explicit --log-file opens directly, otherwise the picker is pushed with
// autoOpen set so discovery lands straight on the current server log. The
// picker stays underneath for Esc, and takes over when no current log is found.
func (m *Model) openLogLink(link logLink) tea.Cmd {
	next := m.toolEntryScreen(toolLogs)
	if next.level == levelLogs {
		if link.catOn {
			next.log.narrow(link.cat)
		}
	} else {
		next.log.cat, next.log.catOn, next.log.autoOpen = link.cat, link.catOn, true
	}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}
