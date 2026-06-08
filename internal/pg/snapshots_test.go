package pg

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	snap := &Snapshot{
		Version:       snapshotVersion,
		Target:        "localhost:5432",
		Database:      "app",
		CapturedAt:    time.Now().Add(-time.Hour).Truncate(time.Second),
		StatsReset:    time.Now().Add(-2 * time.Hour).Truncate(time.Second),
		TrackPlanning: true,
		Stats: []QueryStat{
			{QueryID: 1, Query: "SELECT 1", Calls: 10, TotalExecTime: 100},
			{QueryID: 2, Query: "SELECT 2", Calls: 5, TotalExecTime: 50},
		},
	}

	path, err := SaveSnapshot(dir, snap)
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("saved outside dir: %s", path)
	}

	metas, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("want 1 meta, got %d", len(metas))
	}
	m := metas[0]
	if m.Database != "app" || m.QueryCount != 2 || m.Target != "localhost:5432" {
		t.Errorf("meta mismatch: %+v", m)
	}
	if !m.CapturedAt.Equal(snap.CapturedAt) {
		t.Errorf("CapturedAt: want %v got %v", snap.CapturedAt, m.CapturedAt)
	}

	loaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if len(loaded.Stats) != 2 || loaded.Stats[0].QueryID != 1 || loaded.Stats[0].Calls != 10 {
		t.Errorf("loaded stats mismatch: %+v", loaded.Stats)
	}
	if !loaded.TrackPlanning {
		t.Errorf("TrackPlanning not preserved")
	}

	if err := DeleteSnapshot(path); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	metas, err = ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots after delete: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("want 0 metas after delete, got %d", len(metas))
	}
}

func TestListSnapshotsMissingDir(t *testing.T) {
	metas, err := ListSnapshots(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if metas != nil {
		t.Errorf("want nil metas for missing dir, got %v", metas)
	}
}

func TestBaselineMap(t *testing.T) {
	snap := &Snapshot{Stats: []QueryStat{{QueryID: 7, Calls: 3}, {QueryID: 9, Calls: 4}}}
	m := snap.BaselineMap()
	if len(m) != 2 || m[7].Calls != 3 || m[9].Calls != 4 {
		t.Errorf("BaselineMap mismatch: %+v", m)
	}
}

func TestDiffStatementsClamped(t *testing.T) {
	// Baseline counters larger than current (as after a stats reset) would yield
	// negative deltas; clamped variant must floor them at zero and drop ≤0 calls.
	base := map[int64]QueryStat{
		1: {QueryID: 1, Calls: 100, TotalExecTime: 1000, SharedBlksHit: 500},
		2: {QueryID: 2, Calls: 5, TotalExecTime: 50},
	}
	current := []QueryStat{
		{QueryID: 1, Calls: 10, TotalExecTime: 80, SharedBlksHit: 20}, // went backwards → dropped (calls ≤0)
		{QueryID: 2, Calls: 30, TotalExecTime: 40, SharedBlksHit: 7},  // calls grew, time shrank
	}
	out := DiffStatementsClamped(base, current)
	if len(out) != 1 {
		t.Fatalf("want 1 row (q2), got %d: %+v", len(out), out)
	}
	q := out[0]
	if q.QueryID != 2 {
		t.Fatalf("want q2, got %d", q.QueryID)
	}
	if q.Calls != 25 {
		t.Errorf("calls: want 25, got %d", q.Calls)
	}
	if q.TotalExecTime != 0 {
		t.Errorf("negative TotalExecTime must clamp to 0, got %v", q.TotalExecTime)
	}
	if q.SharedBlksHit != 7 {
		t.Errorf("SharedBlksHit: want 7, got %d", q.SharedBlksHit)
	}
}
