package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// drillStatement opens the detail view of the highlighted query.
func (m *Model) drillStatement(s *screen, cur item) tea.Cmd {
	// Resolve the row's queryid back to its full window-delta QueryStat.
	var qs *pg.QueryStat
	for i := range s.stat.rows {
		if s.stat.rows[i].QueryID == cur.statQueryID {
			qs = &s.stat.rows[i]
			break
		}
	}
	if qs == nil {
		return nil
	}
	next := &screen{
		level: levelStatementDetail, title: "query", tool: s.tool,
		db: s.db,
		// Carry the parent's track_planning state so the plan-time line
		// matches the overview's plan_ms column; without it the detail view
		// defaults to false and wrongly reports "track_planning off".
		stat: stmtState{detail: qs, windowExecMs: s.stat.windowExecMs, trackPlanning: s.stat.trackPlanning}}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillActivityStatement opens the top-queries detail view for the highlighted backend's query.
func (m *Model) drillActivityStatement(s *screen, cur item) tea.Cmd {
	// Enter drills into the top-queries detail view for the highlighted
	// backend's query (requires pg_stat_statements; a soft hint is shown
	// when query_id is zero). The item's statQueryID reuses the same field
	// that levelStatements uses for the same purpose.
	if cur.statQueryID == 0 {
		m.notice = "no query_id — pg_stat_statements not tracking this query"
		return nil
	}
	// Need the query text to build a sample call; it lives in the ActivityRow
	// stored in screen.actRows, matched by PID embedded in the item (we use
	// the pid cell display as the lookup key).  Resolve it via actRows directly.
	if len(s.act.rows) == 0 {
		return nil
	}
	// Find the ActivityRow by QueryID (first match).
	var queryText string
	var backendPID int32
	db := s.db
	for _, r := range s.act.rows {
		if r.QueryID == cur.statQueryID {
			queryText = r.Query
			backendPID = r.PID
			if r.Database != "" {
				db = r.Database
			}
			break
		}
	}
	// Push a loading placeholder while we fetch the QueryStat snapshot.
	next := &screen{level: levelStatementDetail, title: "query", tool: s.tool, db: db, loading: true}
	m.stack = append(m.stack, next)
	return m.loadActivityStatementCmd(db, backendPID, cur.statQueryID, queryText)
}
