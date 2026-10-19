package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

// The header warns at the overview's warn threshold, not before, and names
// the eviction count when pg_stat_statements_info has one.
func TestStatementsFullNote(t *testing.T) {
	if n := statementsFullNote(pg.ExtCapacity{Used: 6000, Max: 7000}); n != "" {
		t.Errorf("86%% full must stay quiet, got %q", stripANSI(n))
	}
	if n := statementsFullNote(pg.ExtCapacity{Used: 10}); n != "" {
		t.Errorf("unknown .max must stay quiet, got %q", stripANSI(n))
	}
	n := stripANSI(statementsFullNote(pg.ExtCapacity{Used: 6958, Max: 7000, Dealloc: 12,
		StatsReset: time.Now().Add(-96 * time.Hour)}))
	for _, want := range []string{"99.4% full", "evicted 12× since reset 4d ago", "X resets"} {
		if !strings.Contains(n, want) {
			t.Errorf("note missing %q: %s", want, n)
		}
	}
}

// X arms the confirm and shows it in the header, any other key cancels, y
// issues the reset.
func TestStatementsResetConfirm(t *testing.T) {
	s := &screen{level: levelStatements, tool: toolQueries, db: "app", loaded: true}
	m := newTestModel(s)
	m.width, m.height = 160, 40
	m.keys.applyContext(s)

	m.Update(keyMsg("X"))
	if !s.stat.pendingReset {
		t.Fatal("X must arm the reset")
	}
	if hdr := stripANSI(m.renderStatementsHeader(s)); !strings.Contains(hdr, "reset pg_stat_statements in every database") {
		t.Errorf("armed reset must show the confirm banner\n%s", hdr)
	}
	m.Update(keyMsg("n"))
	if s.stat.pendingReset {
		t.Error("any other key must cancel")
	}

	m.Update(keyMsg("X"))
	if _, cmd := m.Update(keyMsg("y")); cmd == nil || s.stat.pendingReset {
		t.Errorf("y must issue the reset (cmd %v, pending %v)", cmd != nil, s.stat.pendingReset)
	}
}

// A finished reset restarts the window: the zeroed counters would diff
// negative against any baseline, the @session anchor's included.
func TestStatementsResetRestartsWindow(t *testing.T) {
	s := &screen{level: levelStatements, tool: toolQueries, db: "app", loaded: true}
	s.stat.baseline = map[int64]pg.QueryStat{1: {QueryID: 1}}
	s.stat.sessionBaseline = s.stat.baseline
	s.stat.cumulative = true
	m := newTestModel(s)

	m.onMaintResetDone(maintResetDoneMsg{which: "statements"})
	if s.stat.baseline != nil || s.stat.sessionBaseline != nil || s.stat.cumulative {
		t.Errorf("window not restarted: baseline=%v session=%v cumulative=%v",
			s.stat.baseline != nil, s.stat.sessionBaseline != nil, s.stat.cumulative)
	}
	if m.notice != "pg_stat_statements reset" {
		t.Errorf("notice = %q", m.notice)
	}
}
