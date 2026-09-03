package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// pgbOverviewScreen is the per-instance screen: header + SHOW menu.
func (m *Model) pgbOverviewScreen(inst pg.PgBouncerInstance) *screen {
	return &screen{
		level: levelPgBouncer, title: inst.Name, tool: toolPgBouncer, db: m.client.DefaultDB(),
		pgbInst: &inst, sort: sortByName, loading: true,
	}
}

// pgbShowScreen is one SHOW table of an instance.
func (m *Model) pgbShowScreen(parent *screen, show pgbShow) *screen {
	return &screen{
		level: levelPgBouncerShow, title: show.spec().title, tool: toolPgBouncer, db: parent.db,
		pgbInst: parent.pgbInst, pgbShow: show, diagBarCol: -1, loading: true,
	}
}

func (m *Model) onPgbDiscovered(msg pgbDiscoveredMsg) tea.Cmd {
	s := m.findLevel(levelPgBouncers)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.pgbInsts = msg.insts
	s.pgbProbes = msg.probes
	s.diagCols = pgbInstanceColumns()
	s.diagBarCol = -1
	if s.diagCols != nil && s.items == nil {
		s.diagSortCol, s.sortDesc = 0, false
	}
	s.items = pgbInstanceItems(msg.insts, msg.probes)
	s.diagMetricsDirty = true
	m.applySort(s)
	if s.cursor >= len(s.items) {
		s.resetCursor()
	}
	// One reachable instance: skip the list (it stays on the stack for q/Esc).
	// An unreachable one stays on the list, whose hint line explains why.
	if len(msg.insts) == 1 && len(msg.probes) == 1 && msg.probes[0].Err == nil && m.top() == s && !s.pgbAutoDrilled {
		s.pgbAutoDrilled = true
		m.stack = append(m.stack, m.pgbOverviewScreen(msg.insts[0]))
		return m.loadCurrent()
	}
	return nil
}

func (m *Model) onPgbProbed(msg pgbProbedMsg) tea.Cmd {
	s := m.findLevel(levelPgBouncers)
	if s == nil || len(msg.probes) != len(s.pgbInsts) {
		return nil
	}
	s.pgbProbes = msg.probes
	s.items = pgbInstanceItems(s.pgbInsts, msg.probes)
	s.diagMetricsDirty = true
	m.applySort(s)
	return nil
}

func (m *Model) onPgbOverviewLoaded(msg pgbOverviewLoadedMsg) tea.Cmd {
	s := m.findLevel(levelPgBouncer)
	if s == nil || s.pgbInst == nil || s.pgbInst.Key() != msg.key {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.pgbErr = msg.err
	if msg.err == nil {
		s.pgbOverview = msg.ov
	}
	if len(s.items) == 0 {
		s.items = pgbMenuItems(*s.pgbInst)
		s.itemsRev++
	}
	return nil
}

func (m *Model) onPgbShowLoaded(msg pgbShowLoadedMsg) tea.Cmd {
	s := m.findLevel(levelPgBouncerShow)
	if s == nil || s.pgbInst == nil || s.pgbInst.Key() != msg.key || s.pgbShow != msg.show {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.pgbErr = msg.err
	if msg.err != nil {
		return nil
	}
	// rebuildDiagItems projects the retained full result to the visible column
	// subset and re-pins the sort by name, so a refresh keeps the user's column
	// and a C toggle needs no re-query.
	s.diagResult = msg.res
	m.rebuildDiagItems(s)
	return nil
}

func (m *Model) onPgbTick() tea.Cmd {
	top := m.top()
	if top.tool != toolPgBouncer ||
		(top.level != levelPgBouncers && top.level != levelPgBouncer && top.level != levelPgBouncerShow) {
		m.pgbTicking = false
		return nil
	}
	next := m.pgbTick()
	if next == nil {
		m.pgbTicking = false
		return nil
	}
	if !top.loaded {
		// A load is still in flight (or the screen was just pushed); don't stack
		// a second one behind it.
		return next
	}
	switch top.level {
	case levelPgBouncers:
		if len(top.pgbInsts) == 0 {
			return next
		}
		return tea.Batch(m.probePgBouncersCmd(top.pgbInsts), next)
	case levelPgBouncer:
		return tea.Batch(m.loadPgbOverviewCmd(*top.pgbInst), next)
	}
	return tea.Batch(m.loadPgbShowCmd(*top.pgbInst, top.pgbShow), next)
}
