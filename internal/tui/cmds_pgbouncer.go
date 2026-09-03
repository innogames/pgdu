package tui

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

type pgbDiscoveredMsg struct {
	insts  []pg.PgBouncerInstance
	probes []pg.PgBouncerProbe
}

// pgbProbedMsg refreshes the instance list's health cells without re-running
// discovery (the instance set only changes on a restart; the user can space).
type pgbProbedMsg struct {
	probes []pg.PgBouncerProbe
}

type pgbOverviewLoadedMsg struct {
	key string
	ov  *pg.PgBouncerOverview
	err error
}

type pgbShowLoadedMsg struct {
	key  string
	show pgbShow
	res  *pg.DiagResult
	err  error
}

type pgbTickMsg struct{}

// probePgBouncers runs the cheap per-instance probe concurrently; one wedged
// console must not hold up the rest of the list.
func (m *Model) probePgBouncers(ctx context.Context, insts []pg.PgBouncerInstance) []pg.PgBouncerProbe {
	probes := make([]pg.PgBouncerProbe, len(insts))
	var wg sync.WaitGroup
	for i := range insts {
		wg.Go(func() { probes[i] = m.client.PgBouncerProbe(ctx, insts[i]) })
	}
	wg.Wait()
	return probes
}

func (m *Model) discoverPgBouncersCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		insts := m.client.DiscoverPgBouncers(ctx)
		return pgbDiscoveredMsg{insts: insts, probes: m.probePgBouncers(ctx, insts)}
	})
}

func (m *Model) probePgBouncersCmd(insts []pg.PgBouncerInstance) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		return pgbProbedMsg{probes: m.probePgBouncers(ctx, insts)}
	})
}

func (m *Model) loadPgbOverviewCmd(inst pg.PgBouncerInstance) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		ov, err := m.client.PgBouncerOverview(ctx, inst)
		return pgbOverviewLoadedMsg{key: inst.Key(), ov: ov, err: err}
	})
}

func (m *Model) loadPgbShowCmd(inst pg.PgBouncerInstance, show pgbShow) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		spec := show.spec()
		res, err := m.client.PgBouncerShow(ctx, inst, spec.what)
		if err == nil {
			applyPgbKinds(res, spec)
			addPgbHostnames(res, m.client.ResolveAddr)
		}
		return pgbShowLoadedMsg{key: inst.Key(), show: show, res: res, err: err}
	})
}

// pgbTick schedules the next console re-read, or nil when auto-refresh is
// off — returning nil ends the self-rescheduling loop.
func (m *Model) pgbTick() tea.Cmd {
	if m.pgbRefresh <= 0 {
		return nil
	}
	return tea.Tick(m.pgbRefresh, func(time.Time) tea.Msg { return pgbTickMsg{} })
}

// armPgbTick appends the tick to cmds unless one is already running.
func (m *Model) armPgbTick(cmds []tea.Cmd) []tea.Cmd {
	if !m.pgbTicking {
		if tick := m.pgbTick(); tick != nil {
			m.pgbTicking = true
			cmds = append(cmds, tick)
		}
	}
	return cmds
}

// cyclePgbRefresh steps the cadence: 1s → 2s → 5s → 10s → off → 1s. Console
// reads are cheap for pgbouncer, but SHOW CLIENTS on a busy pooler returns
// tens of thousands of rows, so "off" is a first-class stop.
func (m *Model) cyclePgbRefresh() {
	switch m.pgbRefresh {
	case 1 * time.Second:
		m.pgbRefresh = 2 * time.Second
	case 2 * time.Second:
		m.pgbRefresh = 5 * time.Second
	case 5 * time.Second:
		m.pgbRefresh = 10 * time.Second
	case 10 * time.Second:
		m.pgbRefresh = 0
	default:
		m.pgbRefresh = 1 * time.Second
	}
}
