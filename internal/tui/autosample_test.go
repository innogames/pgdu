package tui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/pg"
)

// autoModel is a model armed for auto-sampling: a real snapshot directory (the
// empty one every other test uses is the bail that keeps them inert) and the
// target the metas are keyed on.
func autoModel(t *testing.T, stack ...*screen) *Model {
	t.Helper()
	m := newTestModel(stack...)
	m.snapshotDir = t.TempDir()
	m.autoSample = true
	m.sampleRetention = pg.DefaultRetention
	return m
}

// growthStack / queriesStack are what databaseChildScreens pushes when the
// tool opens for a database: the unloaded table, then the entry browser.
func growthStack() []*screen {
	return []*screen{
		{level: levelGrowth, tool: toolGrowth, db: "app"},
		{level: levelGrowthWindow, tool: toolGrowth, db: "app", loading: true,
			growth: growthState{entry: true}},
	}
}

func queriesStack() []*screen {
	return []*screen{
		{level: levelStatements, tool: toolQueries, db: "app"},
		{level: levelSnapshots, tool: toolQueries, db: "app", loading: true,
			stat: stmtState{entry: true}},
	}
}

// Opening the growth tool on a database with no sample today captures one.
func TestAutoSampleFiresOnGrowthEntry(t *testing.T) {
	m := autoModel(t, growthStack()...)
	cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"})
	if cmd == nil {
		t.Fatal("want a capture on the first listing of the entry browser")
	}
	if !m.capturingSizes() {
		t.Error("the in-flight guard must be armed before the Cmd runs")
	}
}

// The capture re-lists to show its own sample; that must not capture again.
func TestAutoSampleNotTwiceOnRelist(t *testing.T) {
	m := autoModel(t, growthStack()...)
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd == nil {
		t.Fatal("setup: the first listing should capture")
	}
	m.setCapturingSizes(false) // the capture finished and re-listed
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd != nil {
		t.Error("the re-list must not fire a second capture")
	}
}

func TestAutoSampleSkipsWhenCapturedToday(t *testing.T) {
	today := []pg.SizeSampleMeta{{Target: "db:5432", Database: "app", CapturedAt: time.Now()}}
	yesterday := []pg.SizeSampleMeta{{Target: "db:5432", Database: "app", CapturedAt: time.Now().AddDate(0, 0, -1)}}

	m := autoModel(t, growthStack()...)
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app", metas: today}); cmd != nil {
		t.Error("a sample from today must suppress the capture")
	}

	m = autoModel(t, growthStack()...)
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app", metas: yesterday}); cmd == nil {
		t.Error("yesterday's sample does not count as today's")
	}
}

// L reopens the browser over an applied window: not a tool open, no capture.
func TestAutoSampleSkipsOnReopen(t *testing.T) {
	stack := growthStack()
	stack[1].growth.entry = false
	m := autoModel(t, stack...)
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd != nil {
		t.Error("the L reopen must not capture")
	}
}

func TestAutoSampleOffReturnsNil(t *testing.T) {
	m := autoModel(t, growthStack()...)
	m.autoSample = false
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd != nil {
		t.Error("PGDU_AUTO_SAMPLE=0 / --no-auto-sample must suppress the capture")
	}

	// The bail that keeps every other test in the package inert.
	m = autoModel(t, growthStack()...)
	m.snapshotDir = ""
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd != nil {
		t.Error("an empty snapshot dir must not capture")
	}
}

// A listing that failed is no basis for deciding anything.
func TestAutoSampleSkipsOnListingError(t *testing.T) {
	m := autoModel(t, growthStack()...)
	msg := sizeSamplesListedMsg{db: "app", err: errors.New("permission denied")}
	if cmd := m.onSizeSamplesListed(msg); cmd != nil {
		t.Error("a failed listing must not trigger a capture")
	}
}

func TestAutoSnapshotFiresOnQueriesEntry(t *testing.T) {
	m := autoModel(t, queriesStack()...)
	cmd := m.onSnapshotsListed(snapshotsListedMsg{})
	if cmd == nil {
		t.Fatal("want a capture on the first listing of the entry browser")
	}
	if !m.capturingSnapshot() {
		t.Error("the in-flight guard must be armed before the Cmd runs")
	}
}

func TestAutoSnapshotSkipsOnReopenAndWhenOff(t *testing.T) {
	stack := queriesStack()
	stack[1].stat.entry = false
	m := autoModel(t, stack...)
	if cmd := m.onSnapshotsListed(snapshotsListedMsg{}); cmd != nil {
		t.Error("the L reopen must not capture")
	}

	m = autoModel(t, queriesStack()...)
	m.autoSample = false
	if cmd := m.onSnapshotsListed(snapshotsListedMsg{}); cmd != nil {
		t.Error("the off switch must suppress the capture")
	}
}

// A snapshot the live counters have outgrown still counts as today's capture:
// it is on disk, and the day's quota is about files written, not windows on
// offer. A reset since this morning's capture would otherwise make today look
// empty, and a cluster that resets regularly would capture on every tool open.
func TestAutoSnapshotPredicateCountsPreResetSnapshots(t *testing.T) {
	now := time.Now()
	m := autoModel(t, queriesStack()...)
	msg := snapshotsListedMsg{
		metas: []pg.SnapshotMeta{{
			Path: "/snaps/app.json.gz", Target: "db:5432", Database: "app",
			CapturedAt: now.Add(-3 * time.Hour), FileSize: 4096,
		}},
		liveReset: now.Add(-time.Hour), // a reset since that capture
	}
	cmd := m.onSnapshotsListed(msg)
	s := m.findLevel(levelSnapshots)
	if s == nil || len(s.stat.snapMetas) != 1 {
		t.Fatal("a pre-reset snapshot must stay in the browser, not vanish from it")
	}
	if !s.stat.preReset["/snaps/app.json.gz"] || s.stat.preResetBytes != 4096 {
		t.Errorf("pre-reset set = %v (%d bytes), want the one snapshot and its size",
			s.stat.preReset, s.stat.preResetBytes)
	}
	if cmd != nil {
		t.Error("today's snapshot still exists on disk; a reset must not cause a re-capture")
	}
}

// The set is scoped to the current server and database, because a snapshot of
// another one says nothing about this database's counters — and the set is
// what P deletes.
func TestPreResetSetIsScoped(t *testing.T) {
	now := time.Now()
	before := now.Add(-3 * time.Hour)
	metas := []pg.SnapshotMeta{
		{Path: "mine", Target: "db:5432", Database: "app", CapturedAt: before, FileSize: 10},
		{Path: "other-db", Target: "db:5432", Database: "other", CapturedAt: before, FileSize: 10},
		{Path: "other-host", Target: "elsewhere:5432", Database: "app", CapturedAt: before, FileSize: 10},
		{Path: "after", Target: "db:5432", Database: "app", CapturedAt: now, FileSize: 10},
	}
	got, bytes := preResetSnaps(metas, "db:5432", "app", now.Add(-time.Hour))
	if len(got) != 1 || !got["mine"] || bytes != 10 {
		t.Errorf("pre-reset set = %v (%d bytes), want only the current db's older snapshot", got, bytes)
	}
	// No reset known at all: nothing has been outgrown, so nothing is offered
	// for deletion.
	if got, bytes := preResetSnaps(metas, "db:5432", "app", time.Time{}); got != nil || bytes != 0 {
		t.Errorf("unknown reset yielded %v (%d bytes), want an empty set", got, bytes)
	}
}

// Nobody asked for the auto capture, so its failure says nothing. The same
// failure from S still reports in full.
func TestAutoCaptureFailureIsQuiet(t *testing.T) {
	boom := errors.New("permission denied for function pg_stat_file")

	m := autoModel(t, growthStack()...)
	m.onSizeSampleCaptured(sizeSampleCapturedMsg{mode: captureAuto, db: "app", err: boom})
	if m.notice != "" {
		t.Errorf("auto failure set a notice: %q", m.notice)
	}
	if m.capturingSizes() {
		t.Error("the in-flight guard must be cleared on failure, or S is wedged")
	}

	m = autoModel(t, growthStack()...)
	m.onSizeSampleCaptured(sizeSampleCapturedMsg{mode: captureManual, db: "app", err: boom})
	if m.notice == "" {
		t.Error("S must still report its failure")
	}

	m = autoModel(t, queriesStack()...)
	m.onSnapshotSaved(snapshotSavedMsg{auto: true, db: "app", err: boom})
	if m.notice != "" {
		t.Errorf("auto snapshot failure set a notice: %q", m.notice)
	}
	if m.capturingSnapshot() {
		t.Error("the in-flight guard must be cleared on failure")
	}

	m = autoModel(t, queriesStack()...)
	m.onSnapshotSaved(snapshotSavedMsg{db: "app", err: boom})
	if m.notice == "" {
		t.Error("S must still report its failure")
	}
}

// Two captures of the same kind must never run at once: the size one scans
// every relation and can take minutes.
func TestCaptureGuardSpansBothScreens(t *testing.T) {
	stack := growthStack()
	m := autoModel(t, stack...)
	m.setCapturingSizes(true)
	if !stack[0].growth.capturing || !stack[1].growth.capturing {
		t.Fatal("the flag must be set on both growth screens")
	}
	// A listing landing mid-capture must not start a second one.
	if cmd := m.onSizeSamplesListed(sizeSamplesListedMsg{db: "app"}); cmd != nil {
		t.Error("a capture is already running")
	}
}

// snapshotBrowser is a loaded browser over dir with a pre-reset set armed —
// the state P acts on.
func snapshotBrowser(t *testing.T, paths map[string]bool, bytes int64) (*Model, *screen) {
	t.Helper()
	st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	s := &screen{level: levelSnapshots, tool: toolQueries, db: "app", loaded: true,
		stat: stmtState{preReset: paths, preResetBytes: bytes}}
	m := newTestModel(st, s)
	m.snapshotDir = t.TempDir()
	return m, s
}

// P arms the shared two-step confirm; only y executes, and the key is inert
// when there is nothing it would remove.
func TestPurgeSnapshotsArming(t *testing.T) {
	press := func(m *Model, r rune) tea.Cmd {
		_, cmd := m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		return cmd
	}

	m, _ := snapshotBrowser(t, map[string]bool{"/snaps/a.json.gz": true}, 4096)
	press(m, 'P')
	if !m.pendingPurgeSnaps {
		t.Fatal("P must arm the confirm")
	}
	if cmd := press(m, 'n'); cmd != nil || m.pendingPurgeSnaps {
		t.Error("any other key must cancel without deleting")
	}
	press(m, 'P')
	if cmd := press(m, 'y'); cmd == nil {
		t.Error("y must execute the purge")
	}
	if m.pendingPurgeSnaps {
		t.Error("the confirm must disarm once resolved")
	}

	// Nothing invalidated, nothing to offer: the binding is disabled and the
	// key cannot arm a delete of an empty set.
	m, _ = snapshotBrowser(t, nil, 0)
	press(m, 'P')
	if m.pendingPurgeSnaps {
		t.Error("P must be inert with no pre-reset snapshots")
	}
}

// The sweep removes exactly the set and no more, and a file another user
// already took away does not strand the rest.
func TestPurgeSnapshotsDeletesTheSet(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
		return path
	}
	doomed, alsoDoomed, keep := write("old1.json.gz"), write("old2.json.gz"), write("current.json.gz")
	gone := filepath.Join(dir, "vanished.json.gz") // deleted by someone else first

	m, _ := snapshotBrowser(t, nil, 0)
	cmd := m.purgeSnapshotsCmd(map[string]bool{doomed: true, alsoDoomed: true, gone: true}, dir, "app")
	cmd() // the listing it ends with needs no server: an unreachable one is fine

	for _, path := range []string{doomed, alsoDoomed} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the purge (%v)", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("a snapshot outside the set was deleted: %v", err)
	}
}
