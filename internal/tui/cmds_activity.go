package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// ── Activity message types ────────────────────────────────────────────────────

type activityLoadedMsg struct {
	db      string
	rows    []pg.ActivityRow
	summary pg.ActivitySummary
	err     error
}

type activityTickMsg struct{}

// lockTreeLoadedMsg carries a fresh blocking-chain snapshot for levelLockTree.
type lockTreeLoadedMsg struct {
	db    string
	nodes []pg.LockNode
	err   error
}

// activityHostsMsg delivers newly resolved hostnames from the background DNS
// resolver so they can be merged into the activity table without a DB round-trip.
type activityHostsMsg struct {
	hosts map[string]string // IP → hostname
}

// backendActionMsg is the result of pg_cancel_backend / pg_terminate_backend.
type backendActionMsg struct {
	action string // "cancel" | "terminate"
	pid    int32
	ok     bool
	err    error
}

// activityStatementMsg carries a QueryStat fetched by queryid so the activity
// tool can drill into the existing top-queries detail view.
type activityStatementMsg struct {
	db  string
	pid int32 // used to stale-guard (row might be gone after load)
	qs  *pg.QueryStat
	err error
}

// ── Activity commands ─────────────────────────────────────────────────────────

func (m *Model) loadActivityCmd(db string, mode pg.ActivityFilter) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rows, err := m.client.ListActivity(ctx, db, mode)
		if err != nil {
			return activityLoadedMsg{db: db, err: err}
		}
		// The summary is a cheap single-row aggregate; failing it should not
		// blank the list, so a summary error degrades to zero counts.
		summary, _ := m.client.ActivitySummary(ctx, db)
		return activityLoadedMsg{db: db, rows: rows, summary: summary}
	})
}

// loadLockTreeCmd fetches the current blocking-chain backends for the lock-tree
// view. Shares the Activity tool's 30 s query() budget.
func (m *Model) loadLockTreeCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		nodes, err := m.client.ListLockWaiters(ctx, db)
		return lockTreeLoadedMsg{db: db, nodes: nodes, err: err}
	})
}

// activityTick schedules the next Activity re-sample, or returns nil when
// auto-refresh is off (m.activityRefresh == 0). Returning nil stops the
// self-rescheduling loop; cycling refresh back on or re-entering the tool
// restarts it.
func (m *Model) activityTick() tea.Cmd {
	if m.activityRefresh <= 0 {
		return nil
	}
	return tea.Tick(m.activityRefresh, func(time.Time) tea.Msg {
		return activityTickMsg{}
	})
}

// cycleActivityRefresh steps the live-reload cadence:
// 500ms → 1s → 2s → 5s → 10s → off → 500ms.
// Sub-second intervals are useful when watching a server under active load.
func (m *Model) cycleActivityRefresh() {
	switch m.activityRefresh {
	case 500 * time.Millisecond:
		m.activityRefresh = 1 * time.Second
	case 1 * time.Second:
		m.activityRefresh = 2 * time.Second
	case 2 * time.Second:
		m.activityRefresh = 5 * time.Second
	case 5 * time.Second:
		m.activityRefresh = 10 * time.Second
	case 10 * time.Second:
		m.activityRefresh = 0
	default:
		m.activityRefresh = 500 * time.Millisecond
	}
}

// resolveActivityHostsCmd resolves each unresolved IP in ips via reverse DNS
// and delivers the results as activityHostsMsg. Run on a background goroutine
// so DNS round-trips don't block the TUI.
func (m *Model) resolveActivityHostsCmd(ips []string) tea.Cmd {
	if len(ips) == 0 {
		return nil
	}
	return func() tea.Msg {
		resolved := make(map[string]string, len(ips))
		for _, ip := range ips {
			if ip == "" {
				continue
			}
			h, _ := m.client.ResolveAddr(ip)
			resolved[ip] = h
		}
		return activityHostsMsg{hosts: resolved}
	}
}

// cancelBackendCmd sends pg_cancel_backend($pid) and reports the outcome.
func (m *Model) cancelBackendCmd(db string, pid int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		ok, err := m.client.CancelBackend(ctx, db, pid)
		return backendActionMsg{action: "cancel", pid: pid, ok: ok, err: err}
	})
}

// terminateBackendCmd sends pg_terminate_backend($pid) and reports the outcome.
func (m *Model) terminateBackendCmd(db string, pid int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		ok, err := m.client.TerminateBackend(ctx, db, pid)
		return backendActionMsg{action: "terminate", pid: pid, ok: ok, err: err}
	})
}

// loadActivityStatementCmd fetches the QueryStat for a given queryid from a
// fresh pg_stat_statements snapshot so the Activity tool can drill into the
// top-queries detail view for a running query.
func (m *Model) loadActivityStatementCmd(db string, pid int32, queryID int64, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rows, err := m.client.StatementSnapshot(ctx, db)
		if err != nil {
			// Synthesise a minimal QueryStat so the detail view still shows
			// the query text and EXPLAIN even when pg_stat_statements is absent.
			qs := &pg.QueryStat{QueryID: queryID, Query: queryText}
			return activityStatementMsg{db: db, pid: pid, qs: qs}
		}
		for i := range rows {
			if rows[i].QueryID == queryID {
				return activityStatementMsg{db: db, pid: pid, qs: &rows[i]}
			}
		}
		// Query ran but isn't in pg_stat_statements yet (too short, or extension
		// not tracking this type of query) — fall back to the minimal version.
		qs := &pg.QueryStat{QueryID: queryID, Query: queryText}
		return activityStatementMsg{db: db, pid: pid, qs: qs}
	})
}
