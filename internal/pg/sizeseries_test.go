package pg

import (
	"math"
	"testing"
	"time"
)

// seriesDir writes samples into a fresh directory and returns its path.
func seriesDir(t *testing.T, samples ...*SizeSample) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range samples {
		if _, err := SaveSizeSample(dir, s); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	return dir
}

func row(schema, name string, bytes int64) SizeRow {
	return SizeRow{Schema: schema, Name: name, RelKind: "r", RelFilenode: 1,
		HeapBytes: bytes, TotalBytes: bytes}
}

// A trace must lay its samples on a time axis, not a capture axis: two
// captures minutes apart belong in one bucket, however many files they are.
func TestSizeSeriesBucketsByTime(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("public", "t", 100)),
		// A burst: three captures inside the same hour.
		testSample("app", base.Add(10*time.Hour), row("public", "t", 200)),
		testSample("app", base.Add(10*time.Hour+time.Minute), row("public", "t", 210)),
		testSample("app", base.Add(10*time.Hour+2*time.Minute), row("public", "t", 220)),
		testSample("app", base.Add(20*time.Hour), row("public", "t", 300)),
	)

	s, err := LoadSizeSeries(dir, "db:5432", "app", 4, nil, "")
	if err != nil {
		t.Fatalf("LoadSizeSeries: %v", err)
	}
	if s == nil {
		t.Fatal("LoadSizeSeries returned no series")
	}
	if s.Samples != 5 {
		t.Errorf("Samples = %d, want 5 (the axis spans every capture)", s.Samples)
	}
	vals := s.Series("public.t")
	if len(vals) != 4 {
		t.Fatalf("series has %d cells, want 4", len(vals))
	}
	// The burst collapses to one bucket, keeping its NEWEST member (220), and
	// the two lone captures keep theirs.
	if vals[0] != 100 {
		t.Errorf("first bucket = %v, want 100", vals[0])
	}
	if vals[3] != 300 {
		t.Errorf("last bucket = %v, want 300", vals[3])
	}
	var burst float64
	for _, v := range vals[1:3] {
		if !math.IsNaN(v) {
			burst = v
		}
	}
	if burst != 220 {
		t.Errorf("burst bucket = %v, want 220 (the newest of the three)", burst)
	}
}

// A bucket nobody captured in is unknown, and must never read as size zero.
func TestSizeSeriesGapIsUnknown(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("public", "t", 100)),
		testSample("app", base.Add(100*time.Hour), row("public", "t", 200)),
	)
	s, err := LoadSizeSeries(dir, "db:5432", "app", 10, nil, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	vals := s.Series("public.t")
	gaps := 0
	for i, v := range vals {
		if math.IsNaN(v) {
			gaps++
			if !s.At[i].IsZero() {
				t.Errorf("bucket %d is NaN but At says a sample landed there", i)
			}
		}
	}
	if gaps != 8 {
		t.Errorf("got %d gap buckets, want 8 (two captures, ten cells)", gaps)
	}
}

// Absence means two different things, and the size floor is what tells them
// apart: without a floor the relation did not exist, with one it may simply
// have been hidden.
func TestSizeSeriesAbsenceDependsOnFloor(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	unfloored := testSample("app", base, row("public", "big", 1<<20))
	floored := testSample("app", base.Add(time.Hour), row("public", "big", 1<<20))
	floored.MinBytes = 4096 // a floor was in force; "small" may be under it
	last := testSample("app", base.Add(2*time.Hour),
		row("public", "big", 1<<20), row("public", "small", 10))

	dir := seriesDir(t, unfloored, floored, last)
	s, err := LoadSizeSeries(dir, "db:5432", "app", 3, nil, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	vals := s.Series("public.small")
	if len(vals) != 3 {
		t.Fatalf("series has %d cells, want 3", len(vals))
	}
	if vals[0] != 0 {
		t.Errorf("absent from an unfloored sample = %v, want 0 (it did not exist)", vals[0])
	}
	if !math.IsNaN(vals[1]) {
		t.Errorf("absent from a floored sample = %v, want unknown", vals[1])
	}
	if vals[2] != 10 {
		t.Errorf("present = %v, want 10", vals[2])
	}
}

// The end sample is passed in rather than read back, so the trace's right-hand
// end is the same figure the size column shows.
func TestSizeSeriesUsesEndSampleInMemory(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t, testSample("app", base, row("public", "t", 100)))
	live := testSample("app", base.Add(24*time.Hour), row("public", "t", 999))

	s, err := LoadSizeSeries(dir, "db:5432", "app", 4, live, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	vals := s.Series("public.t")
	if vals[len(vals)-1] != 999 {
		t.Errorf("last bucket = %v, want 999 from the in-memory end sample", vals[len(vals)-1])
	}
}

// A sample from a restored cluster describes different data under the same
// name; splicing it into the trace would invent a step.
func TestSizeSeriesSkipsIncompatible(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	other := testSample("app", base.Add(time.Hour), row("public", "t", 500))
	other.SystemID = 42 // another cluster

	dir := seriesDir(t,
		testSample("app", base, row("public", "t", 100)),
		other,
		testSample("app", base.Add(2*time.Hour), row("public", "t", 300)),
	)
	s, err := LoadSizeSeries(dir, "db:5432", "app", 4, nil, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", s.Skipped)
	}
	for _, v := range s.Series("public.t") {
		if v == 500 {
			t.Error("the other cluster's sample was spliced into the trace")
		}
	}
}

// Not enough history to plot is a normal state, not an error.
func TestSizeSeriesTooLittleHistory(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t, testSample("app", base, row("public", "t", 100)))
	s, err := LoadSizeSeries(dir, "db:5432", "app", 8, nil, "")
	if err != nil {
		t.Fatalf("LoadSizeSeries: %v", err)
	}
	if s != nil {
		t.Errorf("got a series from one sample, want nil")
	}
}

func TestFoldSizeSeries(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("s1", "a", 100), row("s1", "b", 10), row("s2", "c", 1)),
		testSample("app", base.Add(24*time.Hour), row("s1", "a", 200), row("s1", "b", 20), row("s2", "c", 2)),
	)
	s, err := LoadSizeSeries(dir, "db:5432", "app", 2, nil, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	rows := []GrowthRow{
		{Database: "app", Schema: "s1", Name: "a"},
		{Database: "app", Schema: "s1", Name: "b"},
		{Database: "app", Schema: "s2", Name: "c"},
	}

	// Leaf scope keys exactly as RollUpGrowth would, and keeps each row's own
	// trace.
	leaf := FoldSizeSeries(s, rows, GrowthByTable)
	if got := leaf[ScopeKey(rows[0], GrowthByTable)]; len(got) != 2 || got[1] != 200 {
		t.Errorf("leaf trace for a = %v, want [100 200]", got)
	}

	bySchema := FoldSizeSeries(s, rows, GrowthBySchema)
	if got := bySchema["app.s1"]; len(got) != 2 || got[0] != 110 || got[1] != 220 {
		t.Errorf("s1 fold = %v, want [110 220]", got)
	}

	byDB := FoldSizeSeries(s, rows, GrowthByDatabase)
	if got := byDB["app"]; len(got) != 2 || got[0] != 111 || got[1] != 222 {
		t.Errorf("database fold = %v, want [111 222]", got)
	}
}

// A bucket with no sample stays unknown however many members fold into it:
// that is a fact about the history, not about any row.
func TestFoldSizeSeriesKeepsBucketGaps(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("s1", "a", 100)),
		testSample("app", base.Add(100*time.Hour), row("s1", "a", 200)),
	)
	s, err := LoadSizeSeries(dir, "db:5432", "app", 6, nil, "")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	got := FoldSizeSeries(s, []GrowthRow{{Database: "app", Schema: "s1", Name: "a"}}, GrowthBySchema)["app.s1"]
	for i, v := range got {
		if s.At[i].IsZero() != math.IsNaN(v) {
			t.Errorf("bucket %d: At zero = %v but value %v", i, s.At[i].IsZero(), v)
		}
	}
}

func TestTrendPerDay(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	at := []time.Time{base, {}, base.Add(48 * time.Hour)}
	nan := math.NaN()

	// 100 bytes over two days between the two KNOWN ends.
	if got, ok := TrendPerDay([]float64{100, nan, 300}, at); !ok || got != 100 {
		t.Errorf("TrendPerDay = %v, %v; want 100, true", got, ok)
	}
	// A shrink is a negative rate, not an absent one.
	if got, ok := TrendPerDay([]float64{300, nan, 100}, at); !ok || got != -100 {
		t.Errorf("shrink = %v, %v; want -100, true", got, ok)
	}
	// One known end is no trend at all.
	if _, ok := TrendPerDay([]float64{100, nan, nan}, at); ok {
		t.Error("a single known value produced a rate")
	}
}

// The common case: a window whose end is a live capture that was never
// written. The end sample is the axis's right-hand point and must not be
// mistaken for a file to read, nor for an incompatible one to skip.
func TestSizeSeriesLiveEndAnchor(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("public", "t", 100)),
		testSample("app", base.Add(24*time.Hour), row("public", "t", 200)),
	)
	live := testSample("app", base.Add(48*time.Hour), row("public", "t", 300))

	// "@now" is what the TUI carries for a live end: no file has that path.
	s, err := LoadSizeSeries(dir, "db:5432", "app", 4, live, "@now")
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	if s.Samples != 3 {
		t.Errorf("Samples = %d, want 3 (two on disk plus the live end)", s.Samples)
	}
	if s.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", s.Skipped)
	}
	vals := s.Series("public.t")
	if vals[len(vals)-1] != 300 {
		t.Errorf("last bucket = %v, want the live end's 300", vals[len(vals)-1])
	}
}

// A window ending on a stored sample must read that sample once, from memory,
// and must not also leave a hole where its file would have been.
func TestSizeSeriesStoredEndIsNotDoubleCounted(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dir := seriesDir(t,
		testSample("app", base, row("public", "t", 100)),
		testSample("app", base.Add(24*time.Hour), row("public", "t", 200)),
	)
	metas, err := ListSizeSamples(dir)
	if err != nil {
		t.Fatalf("ListSizeSamples: %v", err)
	}
	// metas are newest first.
	endPath := metas[0].Path
	end, err := LoadSizeSample(endPath)
	if err != nil {
		t.Fatalf("LoadSizeSample: %v", err)
	}

	s, err := LoadSizeSeries(dir, "db:5432", "app", 2, end, endPath)
	if err != nil || s == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", s, err)
	}
	if s.Samples != 2 {
		t.Errorf("Samples = %d, want 2 — the end sample counted once", s.Samples)
	}
	vals := s.Series("public.t")
	for i, v := range vals {
		if math.IsNaN(v) {
			t.Errorf("bucket %d is a gap; both buckets have a sample", i)
		}
	}
	if vals[1] != 200 {
		t.Errorf("last bucket = %v, want 200", vals[1])
	}
}
