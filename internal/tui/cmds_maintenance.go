package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// ── Maintenance message types ─────────────────────────────────────────────────

type maintLoadedMsg struct {
	db   string
	info *pg.MaintenanceInfo
	err  error
}

type settingsLoadedMsg struct {
	db   string
	rows []pg.SettingRow
	err  error
}

type progressLoadedMsg struct {
	db   string
	rows []pg.ProgressRow
	err  error
}

type maintResetDoneMsg struct {
	which string // "statements", "qualstats", "tablestats", or "tablestats-all"
	err   error
}

type tableStatsLoadedMsg struct {
	table pg.Table
	stats *pg.TableMaintStats
	err   error
}

type vacuumStartedMsg struct {
	table  pg.Table
	lineCh <-chan string
	doneCh <-chan error
}

type vacuumLineMsg struct {
	line   string
	lineCh <-chan string
	doneCh <-chan error
}

type vacuumDoneMsg struct {
	err error
}

// ── Maintenance commands ──────────────────────────────────────────────────────

func (m *Model) loadMaintenanceCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		info, err := m.client.Maintenance(ctx, db)
		return maintLoadedMsg{db: db, info: info, err: err}
	})
}

func (m *Model) loadSettingsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rows, err := m.client.ListSettings(ctx, db)
		return settingsLoadedMsg{db: db, rows: rows, err: err}
	})
}

func (m *Model) loadProgressCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rows, err := m.client.ListProgress(ctx, db)
		return progressLoadedMsg{db: db, rows: rows, err: err}
	})
}

func (m *Model) resetStatementsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.ResetStatements(ctx, db)
		return maintResetDoneMsg{which: "statements", err: err}
	})
}

func (m *Model) resetQualstatsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.ResetQualstats(ctx, db)
		return maintResetDoneMsg{which: "qualstats", err: err}
	})
}

func (m *Model) resetTableStatsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.ResetTableStats(ctx, db)
		return maintResetDoneMsg{which: "tablestats", err: err}
	})
}

func (m *Model) resetTableStatsAllDBsCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.ResetTableStatsAllDBs(ctx)
		return maintResetDoneMsg{which: "tablestats-all", err: err}
	})
}

func (m *Model) loadTableStatsCmd(t pg.Table) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		s, err := m.client.TableMaintStats(ctx, t)
		return tableStatsLoadedMsg{table: t, stats: s, err: err}
	})
}

// vacuumTableCmd starts a streaming VACUUM on t and returns a running tea.Cmd
// that delivers vacuumLineMsg for each notice line and vacuumDoneMsg when done.
// It uses tea.ExecProcess-style sequencing via a channelled approach: the outer
// goroutine launches the vacuum and a ticker delivers lines via waitVacuumLineCmd.
func (m *Model) vacuumTableCmd(t pg.Table) tea.Cmd {
	lineCh := make(chan string, 64)
	doneCh := make(chan error, 1)
	go func() {
		err := m.client.VacuumTable(context.Background(), t, func(line string) {
			lineCh <- line
		})
		doneCh <- err
		close(lineCh)
		close(doneCh)
	}()
	return func() tea.Msg {
		return vacuumStartedMsg{table: t, lineCh: lineCh, doneCh: doneCh}
	}
}

// waitVacuumLineCmd waits for the next line from a running vacuum goroutine.
// It is rescheduled by the msg handler until the done channel closes.
func waitVacuumLineCmd(lineCh <-chan string, doneCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		select {
		case line, ok := <-lineCh:
			if ok {
				return vacuumLineMsg{line: line, lineCh: lineCh, doneCh: doneCh}
			}
			// Channel closed; drain done.
			err := <-doneCh
			return vacuumDoneMsg{err: err}
		case err := <-doneCh:
			return vacuumDoneMsg{err: err}
		}
	}
}
