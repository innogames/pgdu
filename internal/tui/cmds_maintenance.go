package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
	"pgdu/internal/sysmem"
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

// fixLineMsg / fixDoneMsg carry the streamed output and completion of a
// suggested-fix run (RunFix) on a diagnostic result screen; the channels ride
// along so the handler can re-arm the wait.
type fixLineMsg struct {
	line   string
	lineCh <-chan string
	doneCh <-chan error
}

type fixDoneMsg struct {
	err error
}

// maintTickMsg re-samples the system overview (see maintTick).
type maintTickMsg struct{}

// ── Maintenance commands ──────────────────────────────────────────────────────

// maintTick schedules the next overview re-sample, or nil when auto-refresh is
// off (m.maintRefresh == 0). Same self-rescheduling shape as activityTick.
func (m *Model) maintTick() tea.Cmd {
	if m.maintRefresh <= 0 {
		return nil
	}
	return tea.Tick(m.maintRefresh, func(time.Time) tea.Msg { return maintTickMsg{} })
}

// armMaintTick appends the overview refresh tick to cmds unless one is already
// running.
func (m *Model) armMaintTick(cmds []tea.Cmd) []tea.Cmd {
	if !m.maintTicking {
		if tick := m.maintTick(); tick != nil {
			m.maintTicking = true
			cmds = append(cmds, tick)
		}
	}
	return cmds
}

// cycleMaintRefresh steps the overview cadence: off → 10s → 30s → 60s → off.
// Nothing faster: one sample is some thirty catalog round trips, and rates
// over a few seconds of an otherwise quiet server are noise.
func (m *Model) cycleMaintRefresh() {
	switch m.maintRefresh {
	case 0:
		m.maintRefresh = 10 * time.Second
	case 10 * time.Second:
		m.maintRefresh = 30 * time.Second
	case 30 * time.Second:
		m.maintRefresh = 60 * time.Second
	default:
		m.maintRefresh = 0
	}
}

// loadMaintenanceCmd loads the overview snapshot and, like the shared-buffers
// summary, annotates it with the local host's memory: pg never touches /proc,
// and the figures only mean something when pgdu runs on the database host
// (zero fields keep the host-relative rows hidden otherwise).
func (m *Model) loadMaintenanceCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		info, err := m.client.Maintenance(ctx, db)
		if err == nil && info != nil {
			info.Host = sysmem.Read()
		}
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

// resetCmd wraps one stats-reset client call into the shared
// maintResetDoneMsg{which} completion the maintenance handler dispatches on.
func resetCmd(which string, fn func(context.Context) error) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		return maintResetDoneMsg{which: which, err: fn(ctx)}
	})
}

func (m *Model) resetStatementsCmd(db string) tea.Cmd {
	return resetCmd("statements", func(ctx context.Context) error {
		return m.client.ResetStatements(ctx, db)
	})
}

func (m *Model) resetQualstatsCmd(db string) tea.Cmd {
	return resetCmd("qualstats", func(ctx context.Context) error {
		return m.client.ResetQualstats(ctx, db)
	})
}

func (m *Model) resetTableStatsCmd(db string) tea.Cmd {
	return resetCmd("tablestats", func(ctx context.Context) error {
		return m.client.ResetTableStats(ctx, db)
	})
}

func (m *Model) resetTableStatsAllDBsCmd() tea.Cmd {
	return resetCmd("tablestats-all", m.client.ResetTableStatsAllDBs)
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
	lineCh, doneCh := startStream(func(onLine func(string)) error {
		return m.client.VacuumTable(context.Background(), t, onLine)
	})
	return func() tea.Msg {
		return vacuumStartedMsg{table: t, lineCh: lineCh, doneCh: doneCh}
	}
}

// waitVacuumLineCmd waits for the next line from a running vacuum goroutine.
// It is rescheduled by the msg handler until the done channel closes.
func waitVacuumLineCmd(lineCh <-chan string, doneCh <-chan error) tea.Cmd {
	return waitStreamCmd(lineCh, doneCh,
		func(line string) tea.Msg { return vacuumLineMsg{line: line, lineCh: lineCh, doneCh: doneCh} },
		func(err error) tea.Msg { return vacuumDoneMsg{err: err} })
}

// runDiagFixCmd executes a suggested-fix script in db (RunFix) and delivers
// fixLineMsg per streamed line, then fixDoneMsg. The caller marks the run as
// started before issuing it, so there's no separate started message. No
// client-side timeout — like REINDEX and VACUUM, a fix legitimately runs for
// minutes on a big table.
func (m *Model) runDiagFixCmd(db, script string) tea.Cmd {
	lineCh, doneCh := startStream(func(onLine func(string)) error {
		return m.client.RunFix(context.Background(), db, script, onLine)
	})
	return waitFixLineCmd(lineCh, doneCh)
}

func waitFixLineCmd(lineCh <-chan string, doneCh <-chan error) tea.Cmd {
	return waitStreamCmd(lineCh, doneCh,
		func(line string) tea.Msg { return fixLineMsg{line: line, lineCh: lineCh, doneCh: doneCh} },
		func(err error) tea.Msg { return fixDoneMsg{err: err} })
}

// startStream launches run in its own goroutine and returns the channels its
// output lines and final error arrive on. The line channel is buffered so the
// server-side receive loop (pgx's OnNotice) rarely blocks on the UI; both
// channels close once run returns.
func startStream(run func(onLine func(string)) error) (<-chan string, <-chan error) {
	lineCh := make(chan string, 64)
	doneCh := make(chan error, 1)
	go func() {
		err := run(func(line string) { lineCh <- line })
		doneCh <- err
		close(lineCh)
		close(doneCh)
	}()
	return lineCh, doneCh
}

// waitStreamCmd waits for the next line (or the completion) of a startStream
// run and wraps it in the caller's message type. The msg handler reschedules
// it after every line until the done channel yields.
func waitStreamCmd(lineCh <-chan string, doneCh <-chan error, onLine func(string) tea.Msg, onDone func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		select {
		case line, ok := <-lineCh:
			if ok {
				return onLine(line)
			}
			// Channel closed; drain done.
			return onDone(<-doneCh)
		case err := <-doneCh:
			return onDone(err)
		}
	}
}
