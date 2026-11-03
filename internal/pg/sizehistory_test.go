package pg

import (
	"bytes"
	"compress/gzip"
	"errors"
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

// Retention keeps a prefix of the newest-first listing and nothing else.
func TestPruneKeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	var newest time.Time
	for i := range 100 {
		at := base.Add(-time.Duration(i) * 6 * time.Hour)
		if _, err := SaveSizeSample(dir, testSample("app", at)); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
		if at.After(newest) {
			newest = at
		}
	}

	removed, err := PruneSizeSamples(dir, "db:5432", "app", 30)
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	if removed != 70 {
		t.Errorf("removed %d, want 70", removed)
	}
	left, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(left) != 30 {
		t.Fatalf("kept %d, want 30", len(left))
	}
	if !left[0].CapturedAt.Equal(newest) {
		t.Errorf("newest kept = %v, want the newest written %v", left[0].CapturedAt, newest)
	}
	// Newest-first means the survivors are the 30 most recent, contiguously.
	for i := 1; i < len(left); i++ {
		if !left[i].CapturedAt.Before(left[i-1].CapturedAt) {
			t.Fatalf("listing not newest-first at %d", i)
		}
	}
	if oldest, want := left[29].CapturedAt, base.Add(-29*6*time.Hour); !oldest.Equal(want) {
		t.Errorf("oldest kept = %v, want %v — a non-contiguous prefix survived", oldest, want)
	}
}

// keep < 1 is the "never prune" escape hatch AND what guarantees the newest
// sample survives every policy. Misreading it wipes a database's history, so
// both spellings are pinned.
func TestPruneZeroKeepsNothingDeleted(t *testing.T) {
	for _, keep := range []int{0, -1} {
		dir := t.TempDir()
		now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
		for i := range 5 {
			if _, err := SaveSizeSample(dir, testSample("app", now.Add(-time.Duration(i)*time.Hour))); err != nil {
				t.Fatalf("SaveSizeSample: %v", err)
			}
		}
		removed, err := PruneSizeSamples(dir, "db:5432", "app", keep)
		if err != nil {
			t.Fatalf("PruneSizeSamples(keep=%d): %v", keep, err)
		}
		if removed != 0 {
			t.Errorf("keep=%d removed %d, want 0", keep, removed)
		}
		if left, _ := ListSizeSamples(dir); len(left) != 5 {
			t.Errorf("keep=%d left %d files, want all 5", keep, len(left))
		}
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
	// Two of ours: under keep-N the newest in scope is always safe, so the
	// older one is what must go.
	doomed := testSample("app", old.Add(2*time.Hour))
	doomedPath, err := SaveSizeSample(dir, doomed)
	if err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	if _, err := SaveSizeSample(dir, testSample("app", old.Add(3*time.Hour))); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}

	removed, err := PruneSizeSamples(dir, "db:5432", "app", 1)
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed %d, want exactly the one in-scope sample past keep", removed)
	}
	if _, err := os.Stat(doomedPath); !os.IsNotExist(err) {
		t.Error("the in-scope sample was not deleted")
	}

	sizes, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	if len(sizes) != 3 {
		t.Errorf("want ours plus the other database's and host's kept, got %d", len(sizes))
	}
	snaps, err := ListSnapshots(dir)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("the query snapshot was touched: %d left", len(snaps))
	}
}

func TestPruneKeepsAllBelowN(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for h := range 20 {
		if _, err := SaveSizeSample(dir, testSample("app", now.Add(-time.Duration(h)*6*time.Hour))); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	removed, err := PruneSizeSamples(dir, "db:5432", "app", DefaultRetention)
	if err != nil {
		t.Fatalf("PruneSizeSamples: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d, want 0 — 20 samples is under the keep of %d", removed, DefaultRetention)
	}
}

func TestPruneEmptyDir(t *testing.T) {
	n, err := PruneSizeSamples(filepath.Join(t.TempDir(), "nope"), "db:5432", "app", DefaultRetention)
	if err != nil || n != 0 {
		t.Errorf("want (0, nil) for a missing dir, got (%d, %v)", n, err)
	}
}

// The once-a-day predicate is a UTC calendar date, not a rolling 24 hours.
func TestSizeSampleCapturedToday(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	meta := func(target, db string, at time.Time) SizeSampleMeta {
		return SizeSampleMeta{Target: target, Database: db, CapturedAt: at}
	}
	cases := []struct {
		name  string
		metas []SizeSampleMeta
		want  bool
	}{
		{"nothing at all", nil, false},
		{"just after midnight today", []SizeSampleMeta{meta("db:5432", "app", now.Add(-8*time.Hour-59*time.Minute))}, true},
		{"last thing yesterday", []SizeSampleMeta{meta("db:5432", "app", now.Add(-9*time.Hour-1*time.Minute))}, false},
		{"another host", []SizeSampleMeta{meta("other:5432", "app", now)}, false},
		{"another database", []SizeSampleMeta{meta("db:5432", "billing", now)}, false},
		{"clock skew into tomorrow", []SizeSampleMeta{meta("db:5432", "app", now.Add(24*time.Hour))}, false},
		{"one of several matches", []SizeSampleMeta{
			meta("db:5432", "billing", now),
			meta("db:5432", "app", now.Add(-time.Hour)),
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SizeSampleCapturedToday(c.metas, "db:5432", "app", now); got != c.want {
				t.Errorf("= %v, want %v", got, c.want)
			}
		})
	}
}

// The trend reads samples through a projection that decodes three fields per
// row instead of seventeen. It must see exactly what the full loader sees, or
// a trace would disagree with the window drawn beside it.
func TestReadSizeRowsProjectedMatchesFullLoad(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	s := testSample("app", at,
		SizeRow{OID: 1, Schema: "public", Name: "orders", RelKind: "r", RelFilenode: 7,
			HeapBytes: 100, IndexBytes: 20, ToastBytes: 5, TotalBytes: 125, NLive: 10},
		SizeRow{OID: 2, Schema: "billing", Name: "invoice", RelKind: "r", RelFilenode: 8,
			HeapBytes: 1, TotalBytes: 1},
	)
	s.MinBytes = 4096
	path, err := SaveSizeSample(dir, s)
	if err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}

	full, err := LoadSizeSample(path)
	if err != nil {
		t.Fatalf("LoadSizeSample: %v", err)
	}
	want := map[string]int64{}
	for _, r := range full.Rows {
		want[r.Key()] = r.TotalBytes
	}

	got := map[string]int64{}
	meta, err := readSizeRowsProjected(path, func(r sizeRowBrief) { got[r.Key()] = r.Bytes })
	if err != nil {
		t.Fatalf("readSizeRowsProjected: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("projected %d rows, full load has %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: projected %d, full load %d", k, got[k], v)
		}
	}
	// The header comes back from the same pass — the floor decides whether an
	// absent relation reads as zero or unknown, so a reader without it is no use.
	if meta.MinBytes != 4096 {
		t.Errorf("MinBytes = %d, want 4096", meta.MinBytes)
	}
	if !meta.CapturedAt.Equal(at) {
		t.Errorf("CapturedAt = %v, want %v", meta.CapturedAt, at)
	}
}

// A file this build cannot read must be refused by the projected reader
// exactly as LoadSizeSample refuses it, rather than yielding partial rows.
func TestReadSizeRowsProjectedRefusesNewerVersion(t *testing.T) {
	dir := t.TempDir()
	// Rows present and readable, but a version this build does not know: the
	// check must happen before the array is handed over, not after.
	path := writeRawSizeFile(t, dir, "app-20261030T120000Z",
		`{"Kind":"sizes","Version":99,"Database":"app","RowCount":1,`+
			`"Rows":[{"s":"public","n":"t","b":10}]}`)

	seen := 0
	if _, err := readSizeRowsProjected(path, func(sizeRowBrief) { seen++ }); !errors.Is(err, ErrSampleVersion) {
		t.Errorf("err = %v, want ErrSampleVersion", err)
	}
	if seen != 0 {
		t.Errorf("decoded %d rows from a too-new file, want 0", seen)
	}
}
