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
	if m.FileSize <= 0 {
		t.Errorf("FileSize not filled: %+v", m)
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

// Query snapshots had no retention at all before the daily auto capture; this
// is the mirror of TestPruneKeepsNewestN.
func TestPruneSnapshotsKeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for i := range 40 {
		if _, err := SaveSnapshot(dir, &Snapshot{
			Target: "db:5432", Database: "app", CapturedAt: base.Add(-time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	}
	removed, err := PruneSnapshots(dir, "db:5432", "app", 30)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if removed != 10 {
		t.Errorf("removed %d, want 10", removed)
	}
	left, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(left) != 30 {
		t.Fatalf("kept %d, want 30", len(left))
	}
	if !left[0].CapturedAt.Equal(base) {
		t.Errorf("newest kept = %v, want %v", left[0].CapturedAt, base)
	}
}

// The snapshot pruner must leave the size samples sharing the directory alone.
func TestPruneSnapshotsScoped(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		if _, err := SaveSnapshot(dir, &Snapshot{
			Target: "db:5432", Database: "app", CapturedAt: base.Add(-time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	}
	if _, err := SaveSnapshot(dir, &Snapshot{
		Target: "other:5432", Database: "app", CapturedAt: base.Add(-9 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	for i := range 3 {
		s := &SizeSample{Target: "db:5432", Database: "app", CapturedAt: base.Add(-time.Duration(i) * time.Hour)}
		if _, err := SaveSizeSample(dir, s); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}

	removed, err := PruneSnapshots(dir, "db:5432", "app", 1)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed %d, want the 2 in-scope snapshots past keep", removed)
	}
	snaps, _ := ListSnapshots(dir)
	if len(snaps) != 2 {
		t.Errorf("want ours plus the other host's kept, got %d", len(snaps))
	}
	sizes, _ := ListSizeSamples(dir)
	if len(sizes) != 3 {
		t.Errorf("the size samples were touched: %d left, want 3", len(sizes))
	}
}

func TestSnapshotCapturedToday(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	metas := []SnapshotMeta{{Target: "db:5432", Database: "app", CapturedAt: now.Add(-time.Hour)}}
	if !SnapshotCapturedToday(metas, "db:5432", "app", now) {
		t.Error("a snapshot from an hour ago is today's")
	}
	if SnapshotCapturedToday(metas, "db:5432", "billing", now) {
		t.Error("another database's snapshot must not count")
	}
	if SnapshotCapturedToday(nil, "db:5432", "app", now) {
		t.Error("an empty listing is never today")
	}
}

// SaveSnapshot is the only writer, so it stamps the header regardless of how
// the caller built the Snapshot — CaptureSnapshot is not the only constructor.
func TestSaveSnapshotStampsVersion(t *testing.T) {
	dir := t.TempDir()
	path, err := SaveSnapshot(dir, &Snapshot{Target: "db:5432", Database: "app", CapturedAt: time.Now()})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	snap, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if snap.Version != snapshotVersion {
		t.Errorf("Version = %d, want %d", snap.Version, snapshotVersion)
	}
}
