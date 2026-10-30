package pg

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testSample(db string, at time.Time, rows ...SizeRow) *SizeSample {
	return &SizeSample{
		Target: "db:5432", Database: db,
		DatabaseOID: 16384, SystemID: 7000000000000000001,
		CapturedAt: at, StatsReset: at.Add(-24 * time.Hour),
		BlockSize: 8192,
		Rows:      rows,
		Databases: []Database{{Name: db, SizeBytes: 1 << 20}},
	}
}

func TestSizeSampleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	s := testSample("app", at,
		SizeRow{OID: 1, Schema: "public", Name: "orders", RelKind: "p", PartBound: ""},
		SizeRow{OID: 2, Schema: "public", Name: "orders_2026_10", RelKind: "r",
			RelFilenode: 99, ParentOID: 1, PartBound: "FOR VALUES FROM ('2026-10-01') TO ('2026-11-01')",
			HeapBytes: 100, IndexBytes: 20, ToastBytes: 5, TotalBytes: 125,
			EstRows: 10, NLive: 10, NIns: 12, NDel: 2, NInsSinceVac: 3},
	)

	path, err := SaveSizeSample(dir, s)
	if err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("wrote to %q, want a file in %q", path, dir)
	}
	if got := filepath.Base(path); got[len(got)-len(sizeExt):] != sizeExt {
		t.Errorf("filename %q does not end in %q", got, sizeExt)
	}
	if s.RowCount != 2 || s.Kind != sizeKind || s.Version != sizeVersion {
		t.Errorf("save did not stamp the header: %+v", s.meta(path, 0))
	}

	metas, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("want 1 sample, got %d", len(metas))
	}
	m := metas[0]
	if m.Database != "app" || m.Target != "db:5432" {
		t.Errorf("identity lost: %+v", m)
	}
	if m.RowCount != 2 {
		t.Errorf("RowCount = %d, want 2", m.RowCount)
	}
	if m.DatabaseOID != 16384 || m.SystemID != 7000000000000000001 {
		t.Errorf("identity guard lost: oid=%d sysid=%d", m.DatabaseOID, m.SystemID)
	}
	if m.TotalBytes != 125 {
		t.Errorf("TotalBytes = %d, want 125 (the picker's bar depends on it)", m.TotalBytes)
	}
	if m.BlockSize != 8192 {
		t.Errorf("BlockSize = %d, want 8192", m.BlockSize)
	}
	if m.FileSize <= 0 {
		t.Errorf("FileSize = %d, want > 0", m.FileSize)
	}
	if !m.CapturedAt.Equal(at) {
		t.Errorf("CapturedAt = %v, want %v", m.CapturedAt, at)
	}

	got, err := LoadSizeSample(path)
	if err != nil {
		t.Fatalf("LoadSizeSample: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got.Rows))
	}
	r := got.Rows[1]
	if r.Name != "orders_2026_10" || r.ParentOID != 1 || r.RelFilenode != 99 {
		t.Errorf("row identity lost: %+v", r)
	}
	if r.PartBound == "" {
		t.Error("PartBound lost — it is the single-sample time axis")
	}
	if r.TotalBytes != 125 || r.NInsSinceVac != 3 {
		t.Errorf("row counters lost: %+v", r)
	}
	if len(got.Databases) != 1 || got.Databases[0].Name != "app" {
		t.Errorf("per-database totals lost: %+v", got.Databases)
	}

	if err := DeleteSnapshot(path); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	metas, err = ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples after delete: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("want no samples after delete, got %d", len(metas))
	}
}

func TestListSizeSamplesMissingDir(t *testing.T) {
	metas, err := ListSizeSamples(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("want no error for a missing dir, got %v", err)
	}
	if metas != nil {
		t.Errorf("want nil, got %+v", metas)
	}
}

// writeRawSizeFile drops arbitrary JSON under a .sizes.gz name, for the guards
// that no legitimate writer can produce.
func writeRawSizeFile(t *testing.T, dir, stem, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(raw)); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	path := filepath.Join(dir, stem+sizeExt)
	if err := os.WriteFile(path, buf.Bytes(), 0o666); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestSizeSampleVersionGuard(t *testing.T) {
	dir := t.TempDir()
	path := writeRawSizeFile(t, dir, "app-20261030T120000Z",
		`{"Kind":"sizes","Version":99,"Database":"app","RowCount":1,"Rows":[]}`)

	metas, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("a too-new sample must be skipped, got %+v", metas)
	}
	if _, err := LoadSizeSample(path); !errors.Is(err, ErrSampleVersion) {
		t.Errorf("LoadSizeSample err = %v, want ErrSampleVersion", err)
	}
}

func TestSizeSampleKindGuard(t *testing.T) {
	dir := t.TempDir()
	// A query snapshot's content under a size sample's name: the extension says
	// one thing and the body another, and the body wins.
	path := writeRawSizeFile(t, dir, "app-20261030T120000Z",
		`{"Version":1,"Database":"app","QueryCount":1,"Stats":[]}`)

	metas, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("a non-size file must be skipped, got %+v", metas)
	}
	if _, err := LoadSizeSample(path); err == nil {
		t.Error("LoadSizeSample accepted a file that is not a size sample")
	}
}

func TestListSizeSamplesIgnoresSnapshots(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	if _, err := SaveSizeSample(dir, testSample("app", at,
		SizeRow{Schema: "public", Name: "t", TotalBytes: 10})); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	if _, err := SaveSnapshot(dir, &Snapshot{
		Target: "db:5432", Database: "app", CapturedAt: at,
		Stats: []QueryStat{{QueryID: 1}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	sizes, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(sizes) != 1 {
		t.Errorf("want 1 size sample, got %d", len(sizes))
	}
	snaps, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("want 1 snapshot, got %d", len(snaps))
	}
}

func TestSizeSampleListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	for _, d := range []time.Duration{-2 * time.Hour, -time.Hour, 0} {
		if _, err := SaveSizeSample(dir, testSample("app", now.Add(d))); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	metas, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("want 3, got %d", len(metas))
	}
	for i := 1; i < len(metas); i++ {
		if metas[i-1].CapturedAt.Before(metas[i].CapturedAt) {
			t.Errorf("not newest-first at %d: %v then %v", i, metas[i-1].CapturedAt, metas[i].CapturedAt)
		}
	}
}

func TestFoldMinBytes(t *testing.T) {
	s := testSample("app", time.Now(),
		SizeRow{Schema: "public", Name: "big", TotalBytes: 1000},
		SizeRow{Schema: "public", Name: "small", TotalBytes: 10},
		SizeRow{Schema: "public", Name: "tiny", TotalBytes: 1},
	)
	s.fold(100)

	if len(s.Rows) != 1 || s.Rows[0].Name != "big" {
		t.Fatalf("want only the big row kept, got %+v", s.Rows)
	}
	if s.OtherCount != 2 || s.OtherBytes != 11 {
		t.Errorf("Other = %d rows / %d bytes, want 2 / 11", s.OtherCount, s.OtherBytes)
	}
	if s.MinBytes != 100 {
		t.Errorf("MinBytes = %d, want the floor recorded for the diff", s.MinBytes)
	}
}

func TestFoldZeroKeepsEverything(t *testing.T) {
	s := testSample("app", time.Now(),
		SizeRow{Schema: "public", Name: "a", TotalBytes: 5},
		SizeRow{Schema: "public", Name: "b", TotalBytes: 7},
	)
	s.fold(0)
	if len(s.Rows) != 2 || s.OtherCount != 0 || s.OtherBytes != 0 {
		t.Errorf("a zero floor must fold nothing, got %d rows / other %d", len(s.Rows), s.OtherCount)
	}
}

// The header total must span the rows the floor folded away, or the window
// picker's bar understates a sample taken with --sample-min-bytes.
func TestSaveStampsTotalAcrossFoldedRows(t *testing.T) {
	dir := t.TempDir()
	s := testSample("app", time.Now().Truncate(time.Second),
		SizeRow{Schema: "public", Name: "big", TotalBytes: 1000},
		SizeRow{Schema: "public", Name: "small", TotalBytes: 10},
		SizeRow{Schema: "public", Name: "tiny", TotalBytes: 1},
	)
	s.fold(100)
	if _, err := SaveSizeSample(dir, s); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	if s.TotalBytes != 1011 {
		t.Errorf("TotalBytes = %d, want 1011 (1000 kept + 11 folded)", s.TotalBytes)
	}
	metas, err := ListSizeSamples(dir)
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListSizeSamples: %v / %d", err, len(metas))
	}
	if metas[0].TotalBytes != 1011 || metas[0].OtherCount != 2 {
		t.Errorf("header lost the folded bytes: %+v", metas[0])
	}
}

func TestSizeSampleCompatibleWith(t *testing.T) {
	base := SizeSampleMeta{Target: "db:5432", Database: "app", DatabaseOID: 1, SystemID: 100}
	cases := []struct {
		name string
		o    SizeSampleMeta
		ok   bool
		tag  string
	}{
		{"same", base, true, ""},
		{"other server", SizeSampleMeta{Target: "db2:5432", Database: "app"}, false, "other server"},
		{"other database", SizeSampleMeta{Target: "db:5432", Database: "other"}, false, "other database"},
		{"other cluster", SizeSampleMeta{Target: "db:5432", Database: "app", DatabaseOID: 1, SystemID: 200}, false, "other cluster"},
		{"db recreated", SizeSampleMeta{Target: "db:5432", Database: "app", DatabaseOID: 2, SystemID: 100}, false, "db recreated"},
		// Unreadable identity (pg_control_system is revoked from PUBLIC) must
		// degrade to compatible, never to a mismatch.
		{"unknown ids", SizeSampleMeta{Target: "db:5432", Database: "app"}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, tag := base.CompatibleWith(c.o)
			if ok != c.ok || tag != c.tag {
				t.Errorf("= %v/%q, want %v/%q", ok, tag, c.ok, c.tag)
			}
		})
	}
}

func TestPruneRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	p := RetentionFor(365 * 24 * time.Hour)

	// Four samples a day for 400 days back, all strictly before now.
	var want, all int
	var newest time.Time
	for day := range 400 {
		for _, hour := range []int{0, 6, 12, 18} {
			at := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC).
				AddDate(0, 0, -day)
			if at.After(newest) {
				newest = at
			}
			if _, err := SaveSizeSample(dir, testSample("app", at)); err != nil {
				t.Fatalf("SaveSizeSample: %v", err)
			}
			all++
		}
	}

	removed, err := PruneSizeSamples(dir, "db:5432", "app", now, p)
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	left, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if removed+len(left) != all {
		t.Errorf("removed %d + left %d != %d written", removed, len(left), all)
	}

	// Recompute the expectation the same way a reader would reason about it:
	// everything inside 7d, one per day from 7d to 90d, one per ISO week out
	// to the horizon.
	days := map[string]int{}
	weeks := map[string]int{}
	for _, m := range left {
		age := now.Sub(m.CapturedAt)
		at := m.CapturedAt.UTC()
		y, w := at.ISOWeek()
		switch {
		case age <= p.All:
			want++
		case age <= p.Daily:
			days[at.Format("2006-01-02")]++
		case age <= p.Weekly:
			weeks[fmt.Sprintf("%04d-W%02d", y, w)]++
		default:
			t.Errorf("kept a sample past the horizon: %v (age %v)", at, age)
		}
	}
	for d, n := range days {
		if n != 1 {
			t.Errorf("day %s kept %d samples, want 1", d, n)
		}
	}
	for w, n := range weeks {
		if n != 1 {
			t.Errorf("week %s kept %d samples, want 1", w, n)
		}
	}
	// The newest capture must always survive, whatever the policy.
	if len(left) == 0 || !left[0].CapturedAt.Equal(newest) {
		t.Errorf("newest sample not kept: got %v, want %v", left[0].CapturedAt, newest)
	}
	if len(left) > 200 {
		t.Errorf("kept %d files, want the policy to bound it near ~140", len(left))
	}
}

// PruneSizeSamples runs in the shared, world-writable snapshot directory, so it
// must touch nothing but its own kind, target and database.
func TestPruneScoped(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -800)

	// A query snapshot, another database and another host, all ancient.
	if _, err := SaveSnapshot(dir, &Snapshot{
		Target: "db:5432", Database: "app", CapturedAt: old,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	other := testSample("billing", old)
	if _, err := SaveSizeSample(dir, other); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	remote := testSample("app", old.Add(time.Hour))
	remote.Target = "other-host:5432"
	if _, err := SaveSizeSample(dir, remote); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	// And one of ours that must go.
	doomed := testSample("app", old.Add(2*time.Hour))
	doomedPath, err := SaveSizeSample(dir, doomed)
	if err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}

	removed, err := PruneSizeSamples(dir, "db:5432", "app", now, RetentionFor(365*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d, want exactly the one in-scope sample", removed)
	}
	if _, err := os.Stat(doomedPath); !os.IsNotExist(err) {
		t.Error("the in-scope sample was not deleted")
	}

	sizes, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(sizes) != 2 {
		t.Errorf("want the other database's and the other host's samples kept, got %d", len(sizes))
	}
	snaps, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("the query snapshot was touched: %d left", len(snaps))
	}
}

func TestPruneKeepsEverythingWithinAll(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for h := range 20 {
		if _, err := SaveSizeSample(dir, testSample("app", now.Add(-time.Duration(h)*6*time.Hour))); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	removed, err := PruneSizeSamples(dir, "db:5432", "app", now, RetentionFor(365*24*time.Hour))
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	// 20 samples at 6h apart spans ~5 days, all inside the 7-day All tier.
	if removed != 0 {
		t.Errorf("removed %d, want 0 — everything is inside the All tier", removed)
	}
}

func TestPruneEmptyDir(t *testing.T) {
	n, err := PruneSizeSamples(filepath.Join(t.TempDir(), "nope"), "db:5432", "app", time.Now(), DefaultRetention)
	if err != nil || n != 0 {
		t.Errorf("want (0, nil) for a missing dir, got (%d, %v)", n, err)
	}
}
