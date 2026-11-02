package tui

import (
	"errors"
	"testing"
	"time"

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

// The browser hides snapshots the live stats_reset has outgrown, but the
// predicate must not: a reset since this morning's capture would otherwise make
// today look empty, and a cluster that resets regularly would capture on every
// single tool open.
func TestAutoSnapshotPredicateIgnoresResetFilter(t *testing.T) {
	now := time.Now()
	m := autoModel(t, queriesStack()...)
	msg := snapshotsListedMsg{
		metas:     []pg.SnapshotMeta{{Target: "db:5432", Database: "app", CapturedAt: now.Add(-3 * time.Hour)}},
		liveReset: now.Add(-time.Hour), // filters that snapshot out of the list
	}
	cmd := m.onSnapshotsListed(msg)
	if s := m.findLevel(levelSnapshots); s != nil && len(s.stat.snapMetas) != 0 {
		t.Fatal("setup: the browser was expected to filter the pre-reset snapshot out")
	}
	if cmd != nil {
		t.Error("today's snapshot still exists on disk; the filter must not cause a re-capture")
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
