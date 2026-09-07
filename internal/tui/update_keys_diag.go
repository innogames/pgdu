package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// diagDescribeTarget resolves a diagnostic row's table or index column for `d`.
func diagDescribeTarget(s *screen) (descTarget, bool) {
	curItem := s.currentItem
	switch s.level {
	case levelDiagnosticResult:
		// Generic diagnostic rows carry no pg.Table — resolve the relation by
		// the name in the row (server-side, like the top-queries view). Only
		// the table-shaped diagnostics expose a name column; the rest return
		// false here and `d` is a no-op.
		it, ok := curItem()
		if !ok || it.diagRow <= 0 || s.diagResult == nil || it.diagRow > len(s.diagResult.Rows) {
			return descTarget{}, false
		}
		// Read the full, unprojected row so a schema/table column hidden via
		// the C picker still qualifies the name.
		cols, cells := s.diagResult.Columns, s.diagResult.Rows[it.diagRow-1]
		// In all-databases mode s.db is the connection's default database, not
		// the row's; the relation only exists in the database the row came from.
		db := s.db
		if s.diagAllDBs {
			if rowDB, ok := pg.DiagRowGetter(cols, cells)("database"); ok {
				db = rowDB
			}
		}
		// Prefer a table column; fall back to an index column (index-only
		// diagnostics: unused/duplicate/redundant/index-I/O), resolved via
		// ResolveIndex into a DescribeIndex panel.
		if name, ok := diagDescribeName(cols, cells); ok {
			return descTarget{byName: true, db: db, tableName: name}, true
		}
		if name, ok := diagDescribeIndexName(cols, cells); ok {
			return descTarget{indexByName: true, db: db, indexName: name}, true
		}
		return descTarget{}, false
	}
	return descTarget{}, false
}

// handleDiagEnter opens the suggested-fix overlay for the highlighted diagnostic row.
func (m *Model) handleDiagEnter(s *screen) tea.Cmd {
	// Diagnostic rows don't drill; Enter opens the suggested-fix overlay
	// for diagnostics that define one, already armed: the script is on
	// screen, so `y` runs it straight away and any other key dismisses
	// (one Enter fewer than the reindex/vacuum flow, which has no preview
	// step). A fresh run state per open: the previous row's output must
	// not show under a different script.
	if fix, db, ok := m.diagFixForCursor(s); ok {
		s.diagFix = &diagFixRun{sql: fix, db: db, pending: true}
		m.showDiagFix = true
	} else if s.diag != nil && s.diag.Fix != nil {
		m.notice = "no suggested fix for this row"
	}
	return nil
}
