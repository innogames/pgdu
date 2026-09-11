package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

func logCand(path string, current bool) pg.LogCandidate {
	src := &memLogSource{}
	return pg.LogCandidate{
		Info:   pglog.SourceInfo{Kind: "local", Path: path, Size: 10, Current: current},
		Reason: "test",
		Open:   func() pglog.Source { return pathSource{Source: src, path: path} },
	}
}

// pathSource is memLogSource under a chosen path, so a test can tell which
// candidate a screen opened.
type pathSource struct {
	pglog.Source
	path string
}

func (p pathSource) Info() pglog.SourceInfo {
	in := p.Source.Info()
	in.Path = p.path
	return in
}

// l on the WAL overview goes through the log picker without a pick: discovery
// lands, the current server log opens narrowed to checkpoints on the timeline,
// and the picker stays underneath for Esc.
func TestWALOpenCheckpointLog(t *testing.T) {
	wal := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", sort: sortBySize, sortDesc: true, loaded: true}
	m := newTestModel(wal)
	m.width, m.height = 200, 40

	m.keys.applyContext(wal)
	if !m.keys.OpenLog.Enabled() {
		t.Fatal("l must be enabled on the WAL overview")
	}
	if !slices.ContainsFunc(m.keys.ShortHelp(), func(b key.Binding) bool { return b.Help().Desc == "→ checkpoint log" }) {
		t.Error("the footer must advertise l as the checkpoint-log link")
	}
	// The hint sits on the checkpoints header line, next to the counters it
	// explains.
	wal.wal.summary = &pg.WALSummary{InsertLSN: "0/1"}
	wal.wal.checkpoint = &pg.WALCheckpointInfo{CheckpointsTimed: 3, CheckpointsRequested: 1}
	if hdr := ansi.Strip(m.renderWALSummary(wal)); !strings.Contains(hdr, "l checkpoint log") {
		t.Errorf("header checkpoints line must carry the l hint: %q", hdr)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})

	picker := m.top()
	if picker.level != levelLogFiles || !picker.log.autoOpen || !picker.log.catOn || picker.log.cat != pglog.CatCheckpoint {
		t.Fatalf("after l: level=%v autoOpen=%v cat=%v/%v", picker.level, picker.log.autoOpen, picker.log.catOn, picker.log.cat)
	}

	m.onLogFilesLoaded(logFilesLoadedMsg{cands: []pg.LogCandidate{
		logCand("/var/log/postgresql/postgresql-17-main.log.1", false),
		logCand("/var/log/postgresql/postgresql-17-main.log", true),
		logCand("/var/log/postgresql/pgbouncer.log", false),
	}})
	logs := m.top()
	if logs.level != levelLogs {
		t.Fatalf("discovery must open the current log, top=%v", logs.level)
	}
	if got := logs.log.src.Info().Path; got != "/var/log/postgresql/postgresql-17-main.log" {
		t.Errorf("opened %q, want the current server log", got)
	}
	if !logs.log.catOn || logs.log.cat != pglog.CatCheckpoint || logs.log.view != logViewTimeline {
		t.Errorf("log screen not narrowed to checkpoints on the timeline: catOn=%v cat=%v view=%v", logs.log.catOn, logs.log.cat, logs.log.view)
	}
	if picker.log.autoOpen {
		t.Error("autoOpen must be spent once the current log opened, so a later re-discovery leaves the picker up")
	}
	n := len(m.stack)
	if n < 3 || m.stack[n-2] != picker || m.stack[n-3] != wal {
		t.Errorf("stack must read …wal, picker, log; got %d screens", n)
	}
}

// Without a current server log the picker stays up (with a notice) and a
// manual pick still opens narrowed, since that is what the user came for.
func TestWALOpenCheckpointLogNoCurrent(t *testing.T) {
	wal := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", sort: sortBySize, sortDesc: true, loaded: true}
	m := newTestModel(wal)
	m.openLogLink(logLink{cat: pglog.CatCheckpoint, catOn: true})
	picker := m.top()

	m.onLogFilesLoaded(logFilesLoadedMsg{cands: []pg.LogCandidate{
		logCand("/var/log/postgresql/postgresql-17-main.log.1", false),
		logCand("/var/log/postgresql/postgresql-17-main.log.2.gz", false),
	}})
	if m.top() != picker {
		t.Fatalf("picker must stay up without a current log, top=%v", m.top().level)
	}
	if m.notice == "" {
		t.Error("the user should be told why no file opened")
	}
	if picker.log.autoOpen {
		t.Error("autoOpen must not linger")
	}

	m.drillIn()
	logs := m.top()
	if logs.level != levelLogs || !logs.log.catOn || logs.log.cat != pglog.CatCheckpoint {
		t.Errorf("a manual pick after the cross-link must keep the checkpoint narrowing: level=%v catOn=%v", logs.level, logs.log.catOn)
	}
}

// With --log-file there is no picker: the file opens directly, narrowed.
func TestWALOpenCheckpointLogExplicitFile(t *testing.T) {
	wal := &screen{level: levelWAL, title: "wal", tool: toolWAL, db: "app", sort: sortBySize, sortDesc: true, loaded: true}
	m := newTestModel(wal)
	m.logFile = "/tmp/pgdu-test-nonexistent.log"
	m.openLogLink(logLink{cat: pglog.CatCheckpoint, catOn: true})
	logs := m.top()
	if logs.level != levelLogs || !logs.log.catOn || logs.log.cat != pglog.CatCheckpoint || logs.log.view != logViewTimeline {
		t.Errorf("--log-file must open directly and narrowed: level=%v catOn=%v view=%v", logs.level, logs.log.catOn, logs.log.view)
	}
	if len(m.stack) < 2 || m.stack[len(m.stack)-2] != wal {
		t.Error("the WAL overview must stay underneath")
	}
}

// l on the top-queries table opens the slow-query lines of the current server
// log on the slow pane — the executions behind the aggregates, slowest first.
func TestTopQueriesOpenSlowLog(t *testing.T) {
	st := &screen{level: levelStatements, title: "top queries", tool: toolQueries, db: "app", loaded: true}
	m := newTestModel(st)
	m.width, m.height = 200, 40

	m.keys.applyContext(st)
	if !m.keys.OpenLog.Enabled() || m.keys.OpenLog.Help().Desc != "→ slow query log" {
		t.Fatalf("l on top queries: enabled=%v help=%q", m.keys.OpenLog.Enabled(), m.keys.OpenLog.Help().Desc)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	picker := m.top()
	if picker.level != levelLogFiles || !picker.log.autoOpen || !picker.log.catOn || picker.log.cat != pglog.CatSlowQuery {
		t.Fatalf("after l: level=%v autoOpen=%v cat=%v/%v", picker.level, picker.log.autoOpen, picker.log.catOn, picker.log.cat)
	}
	m.onLogFilesLoaded(logFilesLoadedMsg{cands: []pg.LogCandidate{
		logCand("/var/log/postgresql/postgresql-17-main.log.1", false),
		logCand("/var/log/postgresql/postgresql-17-main.log", true),
	}})
	logs := m.top()
	if logs.level != levelLogs || !logs.log.catOn || logs.log.cat != pglog.CatSlowQuery || logs.log.view != logViewSlow {
		t.Errorf("log screen must open narrowed to slow queries on the slow pane: level=%v catOn=%v cat=%v view=%v",
			logs.level, logs.log.catOn, logs.log.cat, logs.log.view)
	}

	// The detail level links the same way; unrelated levels do not.
	det := &screen{level: levelStatementDetail, tool: toolQueries, db: "app", loaded: true}
	m.keys.applyContext(det)
	if !m.keys.OpenLog.Enabled() {
		t.Error("l must be enabled on the statement detail too")
	}
	m.keys.applyContext(&screen{level: levelParts, tool: toolDisk, loaded: true})
	if m.keys.OpenLog.Enabled() {
		t.Error("l must stay off on levels without a log link")
	}
}

// l on the system overview opens the current server log as is — no category —
// and is listed in the overview's own jump-key line rather than the footer.
func TestOverviewOpenLog(t *testing.T) {
	ov := &screen{level: levelMaintenance, title: "system overview", tool: toolMaintenance, db: "app", loaded: true}
	m := newTestModel(ov)
	m.width, m.height = 200, 40

	m.keys.applyContext(ov)
	if !m.keys.OpenLog.Enabled() || m.keys.OpenLog.Help().Desc != "→ logs" {
		t.Fatalf("l on the overview: enabled=%v help=%q", m.keys.OpenLog.Enabled(), m.keys.OpenLog.Help().Desc)
	}
	if slices.ContainsFunc(m.keys.ShortHelp(), func(b key.Binding) bool { return b.Help().Key == "l" }) {
		t.Error("the overview footer must not repeat l; its header line lists the jump keys")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	picker := m.top()
	if picker.level != levelLogFiles || !picker.log.autoOpen || picker.log.catOn {
		t.Fatalf("after l: level=%v autoOpen=%v catOn=%v", picker.level, picker.log.autoOpen, picker.log.catOn)
	}
	m.onLogFilesLoaded(logFilesLoadedMsg{cands: []pg.LogCandidate{
		logCand("/var/log/postgresql/postgresql-17-main.log", true),
		logCand("/var/log/postgresql/pgbouncer.log", false),
	}})
	logs := m.top()
	if logs.level != levelLogs || logs.log.catOn || logs.log.view != logViewGroups {
		t.Errorf("the overview link must open the whole log on the default pane: level=%v catOn=%v view=%v", logs.level, logs.log.catOn, logs.log.view)
	}
	if got := logs.log.src.Info().Path; got != "/var/log/postgresql/postgresql-17-main.log" {
		t.Errorf("opened %q, want the current server log", got)
	}
}
