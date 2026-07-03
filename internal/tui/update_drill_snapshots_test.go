package tui

import (
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
		{"cumulative live", screen{statCumulative: true}, snapReset, snapNow},
		{"cumulative with frozen end", screen{statCumulative: true, statEndSnap: snapB},
			snapReset, "/snaps/b.json.gz"},
		{"snapshot base, live end", screen{statBaseSnap: snapA}, "/snaps/a.json.gz", snapNow},
		{"frozen snapshot-to-snapshot diff", screen{statBaseSnap: snapA, statEndSnap: snapB},
			"/snaps/a.json.gz", "/snaps/b.json.gz"},
		{"base snapshot no longer listed",
			screen{statBaseSnap: &pg.Snapshot{CapturedAt: t1.Add(time.Minute)}}, "", snapNow},
		{"end snapshot no longer listed",
			screen{statBaseSnap: snapA, statEndSnap: &pg.Snapshot{CapturedAt: t2.Add(time.Minute)}},
			"/snaps/a.json.gz", ""},
		{"session window", screen{statSessionStart: sessionStart, statBaselineAt: sessionStart},
			snapSession, snapNow},
		// A fresh R re-base: baseline is neither a snapshot nor the session
		// start, so no row can represent it.
		{"fresh re-base", screen{statSessionStart: sessionStart, statBaselineAt: sessionStart.Add(time.Minute)},
			"", snapNow},
	}
	m := &Model{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &screen{statSnapMetas: metas}
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
	s := &screen{statSnapMetas: []pg.SnapshotMeta{{Path: "/snaps/a.json.gz", CapturedAt: t1}}}

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
