package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

// TestAppliedWindowPaths pins how the statements screen's window state maps
// onto snapshot-browser row paths: the sentinel anchors (@reset/@session/@now),
// real snapshots resolved back to their file path via CapturedAt, and the ""
// start of a fresh re-base that no browser row represents.
func TestAppliedWindowPaths(t *testing.T) {
	t1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	metas := []pg.SnapshotMeta{
		{Path: "/snaps/a.json.gz", CapturedAt: t1},
		{Path: "/snaps/b.json.gz", CapturedAt: t2},
	}
	snapA := &pg.Snapshot{CapturedAt: t1}
	snapB := &pg.Snapshot{CapturedAt: t2}
	sessionStart := t1.Add(-time.Hour)

	cases := []struct {
		name      string
		st        screen
		wantStart string
		wantEnd   string
	}{
		{"cumulative live", screen{stat: stmtState{cumulative: true}}, snapReset, snapNow},
		{"cumulative with frozen end", screen{stat: stmtState{cumulative: true, endSnap: snapB}},
			snapReset, "/snaps/b.json.gz"},
		{"snapshot base, live end", screen{stat: stmtState{baseSnap: snapA}}, "/snaps/a.json.gz", snapNow},
		{"frozen snapshot-to-snapshot diff", screen{stat: stmtState{baseSnap: snapA, endSnap: snapB}},
			"/snaps/a.json.gz", "/snaps/b.json.gz"},
		{"base snapshot no longer listed",
			screen{stat: stmtState{baseSnap: &pg.Snapshot{CapturedAt: t1.Add(time.Minute)}}}, "", snapNow},
		{"end snapshot no longer listed",
			screen{stat: stmtState{baseSnap: snapA, endSnap: &pg.Snapshot{CapturedAt: t2.Add(time.Minute)}}},
			"/snaps/a.json.gz", ""},
		{"session window", screen{stat: stmtState{sessionStart: sessionStart, baselineAt: sessionStart}},
			snapSession, snapNow},
		// A fresh R re-base: baseline is neither a snapshot nor the session
		// start, so no row can represent it.
		{"fresh re-base", screen{stat: stmtState{sessionStart: sessionStart, baselineAt: sessionStart.Add(time.Minute)}},
			"", snapNow},
	}
	m := &Model{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &screen{stat: stmtState{snapMetas: metas}}
			start, end := m.appliedWindowPaths(&c.st, s)
			if start != c.wantStart || end != c.wantEnd {
				t.Errorf("appliedWindowPaths = (%q, %q), want (%q, %q)",
					start, end, c.wantStart, c.wantEnd)
			}
		})
	}
}

// TestSnapTimeOrdering pins the ordering roles of the sentinel paths: @reset
// is the earliest possible start, @now the latest possible end, and real
// snapshots order by their CapturedAt.
func TestSnapTimeOrdering(t *testing.T) {
	t1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	m := &Model{}
	s := &screen{stat: stmtState{snapMetas: []pg.SnapshotMeta{{Path: "/snaps/a.json.gz", CapturedAt: t1}}}}

	reset := m.snapTime(s, snapReset)
	now := m.snapTime(s, snapNow)
	snap := m.snapTime(s, "/snaps/a.json.gz")

	if !reset.Before(snap) || !snap.Before(now) {
		t.Errorf("want @reset < snapshot < @now, got %v / %v / %v", reset, snap, now)
	}
	if got := m.snapTime(s, "/snaps/gone.json.gz"); !got.IsZero() {
		t.Errorf("unknown path should map to the zero time, got %v", got)
	}
}

// TestSnapStraddledReset pins the pairing rule that replaced hiding pre-reset
// snapshots outright: a window is refused only when a counter reset actually
// falls inside it. Two captures from the same side of a reset diff fine, which
// is the whole point of keeping them listed.
func TestSnapStraddledReset(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC) }
	// a and b predate the reset at 12:00 and both record the one before them
	// (10:00); c was captured after it and records it.
	oldReset, newReset := at(10), at(12)
	metas := []pg.SnapshotMeta{
		{Path: "a", CapturedAt: at(10).Add(time.Minute), StatsReset: oldReset},
		{Path: "b", CapturedAt: at(11), StatsReset: oldReset},
		{Path: "c", CapturedAt: at(13), StatsReset: newReset},
		{Path: "unknown", CapturedAt: at(11), StatsReset: time.Time{}},
	}
	s := &screen{stat: stmtState{snapMetas: metas, liveReset: newReset}}
	m := &Model{}

	cases := []struct {
		name       string
		start, end string
		want       bool
	}{
		{"both before the reset", "a", "b", false},
		{"both after the reset", "c", "c", false},
		{"across the reset", "b", "c", true},
		{"pre-reset against the live counters", "b", snapNow, true},
		{"post-reset against the live counters", "c", snapNow, false},
		// An unknown reset on either side stays permissive: the clamped diff is
		// a dubious window, not a broken one, and refusing would resurrect the
		// blanket hiding this replaced.
		{"unknown end reset", "a", "unknown", false},
		{"unknown start is still dated", "unknown", "c", true},
		// The synthetic starts carry their own rules and never reach here.
		{"cumulative start", snapReset, "c", false},
		{"session start", snapSession, snapNow, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reset, got := m.snapStraddledReset(s, c.start, c.end)
			if got != c.want {
				t.Fatalf("snapStraddledReset(%q→%q) = %v (%v), want %v", c.start, c.end, got, reset, c.want)
			}
		})
	}
}

// A pick whose window straddles a reset is refused with a notice naming the
// reset, and leaves the applied window alone.
func TestLoadSelectedSnapshotRefusesStraddledReset(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 7, 1, h, 0, 0, 0, time.UTC) }
	st := &screen{level: levelStatements, tool: toolQueries, db: "app"}
	s := &screen{level: levelSnapshots, tool: toolQueries, db: "app", stat: stmtState{
		snapMetas: []pg.SnapshotMeta{{Path: "old", Target: "db:5432", Database: "app", CapturedAt: at(10)}},
		liveReset: at(12),
		preReset:  map[string]bool{"old": true},
	}}
	m := newTestModel(st, s)
	if cmd := m.loadSelectedSnapshot(s, item{snapPath: "old"}); cmd != nil {
		t.Error("a window straddling a reset must not load")
	}
	if !strings.Contains(m.notice, "counters were reset") {
		t.Errorf("notice = %q, want the reset named (not the server/db refusal)", m.notice)
	}
	if st.stat.baseSnap != nil || st.stat.cumulative {
		t.Error("the applied window must be left as it was")
	}
}
