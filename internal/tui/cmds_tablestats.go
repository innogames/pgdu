package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// tableOverviewLoadedMsg delivers the per-table statistics for one schema (the
// Table overview tool, levelTableStats). Named to avoid colliding with the
// maintenance tool's single-table tableStatsLoadedMsg. statsReset dates the
// cumulative counters (zero = unknown / never reset).
type tableOverviewLoadedMsg struct {
	db         string
	schema     string
	rows       []pg.TableStat
	statsReset time.Time
	err        error
}

// loadTableOverviewCmd fetches every table's stats for db.schema in one query.
func (m *Model) loadTableOverviewCmd(db, schema string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rows, reset, err := m.client.ListTableStats(ctx, db, schema)
		return tableOverviewLoadedMsg{db: db, schema: schema, rows: rows, statsReset: reset, err: err}
	})
}
