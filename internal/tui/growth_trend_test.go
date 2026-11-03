package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

// trendHist builds a loaded history over the given per-relation traces. The
// axis has one sample per cell, so a trace's index is its bucket.
func trendHist(t *testing.T, cells int, traces map[string][]int64) *pg.SizeSeries {
	t.Helper()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	for i := range cells {
		rows := make([]pg.SizeRow, 0, len(traces))
		for key, vals := range traces {
			schema, name, _ := strings.Cut(key, ".")
			if vals[i] < 0 {
				continue // absent from this sample
			}
			rows = append(rows, pg.SizeRow{
				Schema: schema, Name: name, RelKind: "r", RelFilenode: 1,
				HeapBytes: vals[i], TotalBytes: vals[i],
			})
		}
		s := &pg.SizeSample{
			Target: "db:5432", Database: "app",
			DatabaseOID: 16384, SystemID: 1,
			CapturedAt: base.Add(time.Duration(i) * 24 * time.Hour),
			BlockSize:  8192, Rows: rows,
		}
		if _, err := pg.SaveSizeSample(dir, s); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	hist, err := pg.LoadSizeSeries(dir, "db:5432", "app", cells, nil, "")
	if err != nil || hist == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", hist, err)
	}
	return hist
}

// denseHist builds n daily samples and lays them on the real axis width, so
// n > growthTrendCells forces the uniform layout.
func denseHist(t *testing.T, n int) *pg.SizeSeries {
	t.Helper()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	for i := range n {
		if _, err := pg.SaveSizeSample(dir, &pg.SizeSample{
			Target: "db:5432", Database: "app", DatabaseOID: 16384, SystemID: 1,
			CapturedAt: base.Add(time.Duration(i) * 24 * time.Hour),
			Rows: []pg.SizeRow{{Schema: "public", Name: "a", RelKind: "r",
				TotalBytes: int64(100+i) << 20}},
		}); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}
	hist, err := pg.LoadSizeSeries(dir, "db:5432", "app", growthTrendCells, nil, "")
	if err != nil || hist == nil {
		t.Fatalf("LoadSizeSeries: %v, %v", hist, err)
	}
	return hist
}

// withTrend installs a ready history on a growth screen.
func withTrend(s *screen, hist *pg.SizeSeries) {
	s.growth.trend.state = trendReady
	s.growth.trend.hist = hist
	s.growth.trend.paths = []string{"x"}
}

// The column is absent until there is a history, present while one is being
// read — so the table does not jump sideways when it lands — and absent again
// when there turns out to be none.
func TestTrendColumnVisibility(t *testing.T) {
	rows := []pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}

	for _, tc := range []struct {
		name string
		set  func(*screen)
		want bool
	}{
		{"idle", func(*screen) {}, false},
		{"loading", func(s *screen) { s.growth.trend.state = trendLoading }, true},
		{"none", func(s *screen) { s.growth.trend.state = trendNone }, false},
		{"ready", func(s *screen) {
			withTrend(s, trendHist(t, 4, map[string][]int64{"public.a": {100, 120, 160, 200}}))
		}, true},
	} {
		s := growthScreen(rows, time.Hour)
		tc.set(s)
		m := newTestModel(s)
		m.rebuildGrowthItems(s)
		got := false
		for _, d := range s.growth.cols {
			if d.id == growthColTrend {
				got = true
			}
		}
		if got != tc.want {
			t.Errorf("%s: trend column present = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// While the history is being read the placeholder must already be the trace's
// full width, and must be pure ASCII — truncateDiagCell folds whitespace runs
// on the non-ASCII path, which would collapse the padding and shift the column
// as soon as the real traces arrived.
func TestTrendLoadingPlaceholderHoldsTheWidth(t *testing.T) {
	s := growthScreen([]pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}, time.Hour)
	s.growth.trend.state = trendLoading
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	cell := cellByCol(t, m, s, 0, growthColTrend)
	if n := len([]rune(cell.Display)); n != growthTrendCells {
		t.Errorf("placeholder is %d cells wide, want %d", n, growthTrendCells)
	}
	for _, r := range cell.Display {
		if r > 127 {
			t.Fatalf("placeholder %q is not ASCII, so its padding will be folded away", cell.Display)
		}
	}
	if got := truncateDiagCell(cell.Display, growthTrendCells); got != cell.Display {
		t.Errorf("truncateDiagCell changed the placeholder: %q → %q", cell.Display, got)
	}
}

// A trace's Num is the long-run rate, which is a different ranking from the
// window's per_day — that difference is the reason the column exists.
func TestTrendCellCarriesLongRunRate(t *testing.T) {
	// "slow" barely moves in the window but has doubled over the history;
	// "fast" is the other way round.
	hist := trendHist(t, 4, map[string][]int64{
		"public.slow": {100, 200, 300, 400},
		"public.fast": {1000, 1000, 1000, 1000},
	})
	rows := []pg.GrowthRow{
		gRow("public", "slow", 399, 400, pg.GrowthSteady),
		gRow("public", "fast", 900, 1000, pg.GrowthSteady),
	}
	s := growthScreen(rows, time.Hour)
	withTrend(s, hist)
	m := newTestModel(s)
	showCol(m, growthColPerDay)
	m.rebuildGrowthItems(s)

	// The table is sorted by the window's Δ, so find each row by name
	// rather than by the order it was built in.
	slow, fast := growthRowIndex(t, s, "slow"), growthRowIndex(t, s, "fast")
	slowCell := cellByCol(t, m, s, slow, growthColTrend)
	fastCell := cellByCol(t, m, s, fast, growthColTrend)
	if !slowCell.HasNum || !fastCell.HasNum {
		t.Fatal("a trace over a known span must carry its rate")
	}
	// 300 bytes over three days.
	if got := slowCell.Num; math.Abs(got-100) > 0.001 {
		t.Errorf("slow long-run rate = %v, want 100/day", got)
	}
	if got := fastCell.Num; got != 0 {
		t.Errorf("fast long-run rate = %v, want 0 (flat over the history)", got)
	}
	// The window's per_day says the opposite, which is the point: ranking by
	// the trace answers a different question from ranking by the window.
	if cellByCol(t, m, s, fast, growthColPerDay).Num <= cellByCol(t, m, s, slow, growthColPerDay).Num {
		t.Error("test setup: the window should rank fast above slow")
	}
}

// growthRowIndex finds the row whose name column is name.
func growthRowIndex(t *testing.T, s *screen, name string) int {
	t.Helper()
	idx := -1
	for i, d := range s.growth.cols {
		if d.id == growthColName {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("name column not visible")
	}
	for i, it := range s.items {
		cells, ok := it.data.([]pg.DiagCell)
		if ok && cells[idx].Display == name {
			return i
		}
	}
	t.Fatalf("row %q not found", name)
	return -1
}

// A Σ of traces would read as a real aggregate.
func TestTrendBlankInFooter(t *testing.T) {
	hist := trendHist(t, 4, map[string][]int64{"public.a": {100, 120, 160, 200}})
	s := growthScreen([]pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}, time.Hour)
	withTrend(s, hist)
	m := newTestModel(s)
	m.rebuildGrowthItems(s)

	idx := -1
	for i, d := range s.growth.cols {
		if d.id == growthColTrend {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("trend column not visible")
	}
	if got := s.diagTotalRow[idx]; got.Display != "" || got.HasNum {
		t.Errorf("footer trend cell = %+v, want blank", got)
	}
}

// A group's trace must be the sum of exactly the members its numbers come
// from, at every scope.
func TestTrendFoldsWithTheScope(t *testing.T) {
	hist := trendHist(t, 3, map[string][]int64{
		"s1.a": {10, 20, 30},
		"s1.b": {1, 2, 3},
		"s2.c": {100, 100, 100},
	})
	rows := []pg.GrowthRow{
		gRow("s1", "a", 10, 30, pg.GrowthSteady),
		gRow("s1", "b", 1, 3, pg.GrowthSteady),
		gRow("s2", "c", 100, 100, pg.GrowthSteady),
	}
	s := growthScreen(rows, 24*time.Hour)
	withTrend(s, hist)
	m := newTestModel(s)

	s.growth.view = growthBySchema
	m.rebuildGrowthItems(s)
	got := s.growth.trend.series["app.s1"]
	want := []float64{11, 22, 33}
	if len(got) != len(want) {
		t.Fatalf("s1 fold has %d cells, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("s1 fold = %v, want %v", got, want)
			break
		}
	}
	if s.growth.trend.scope != pg.GrowthBySchema {
		t.Errorf("fold scope = %v, want schema", s.growth.trend.scope)
	}

	// Tabbing back re-folds to the leaf traces rather than leaving the schema
	// sums behind under relation keys.
	s.growth.view = growthByTable
	m.rebuildGrowthItems(s)
	if got := s.growth.trend.series["app.s1.a"]; len(got) != 3 || got[2] != 30 {
		t.Errorf("leaf trace after re-fold = %v, want [10 20 30]", got)
	}
}

// Narrowing and the dropped-row filter decide which rows the table shows, and
// a group's trace must follow them — it folds over the filtered rows, not the
// whole set.
func TestTrendFoldsOverFilteredRows(t *testing.T) {
	hist := trendHist(t, 3, map[string][]int64{
		"s1.live": {10, 20, 30},
		"s1.dead": {5, 5, 5},
	})
	rows := []pg.GrowthRow{
		gRow("s1", "live", 10, 30, pg.GrowthSteady),
		gRow("s1", "dead", 5, 0, pg.GrowthGone),
	}
	s := growthScreen(rows, 24*time.Hour)
	withTrend(s, hist)
	s.growth.view = growthBySchema
	m := newTestModel(s)

	// Dropped relations are hidden by default, so they must not be in the sum.
	m.rebuildGrowthItems(s)
	if got := s.growth.trend.series["app.s1"]; got[0] != 10 {
		t.Errorf("fold with dropped rows hidden = %v, want the live row alone", got)
	}
	// v shows them, and the trace picks them up.
	s.growth.showGone = true
	m.rebuildGrowthItems(s)
	if got := s.growth.trend.series["app.s1"]; got[0] != 15 {
		t.Errorf("fold with dropped rows shown = %v, want both rows", got)
	}
}

// The history is read once. levelGrowth re-captures on every refresh tick when
// the window ends @now, and re-reading it each time would turn a cheap refresh
// into a disk sweep.
func TestTrendNotReloadedWhenSampleSetIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 2 {
		if _, err := pg.SaveSizeSample(dir, &pg.SizeSample{
			Target: "db:5432", Database: "app", DatabaseOID: 16384, SystemID: 1,
			CapturedAt: base.Add(time.Duration(i) * 24 * time.Hour),
			Rows:       []pg.SizeRow{{Schema: "public", Name: "a", TotalBytes: int64(100 * (i + 1))}},
		}); err != nil {
			t.Fatalf("SaveSizeSample: %v", err)
		}
	}

	s := growthScreen([]pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)}, time.Hour)
	m := newTestModel(s)
	m.snapshotDir = dir

	if cmd := m.growthSeriesCmd(s); cmd == nil {
		t.Fatal("first call must ask for the history")
	}
	// Pretend it landed.
	s.growth.trend.state = trendReady
	s.growth.trend.paths = growthSeriesPaths(dir, "app", "db:5432")

	if cmd := m.growthSeriesCmd(s); cmd != nil {
		t.Error("an unchanged sample set must not be re-read")
	}

	// A new capture extends the set, and that must be picked up.
	if _, err := pg.SaveSizeSample(dir, &pg.SizeSample{
		Target: "db:5432", Database: "app", DatabaseOID: 16384, SystemID: 1,
		CapturedAt: base.Add(72 * time.Hour),
		Rows:       []pg.SizeRow{{Schema: "public", Name: "a", TotalBytes: 300}},
	}); err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	if cmd := m.growthSeriesCmd(s); cmd == nil {
		t.Error("a new sample must trigger a re-read")
	}
}

// The header has to name the axis, or the traces are a decoration rather than
// a measurement.
func TestGrowthTrendNote(t *testing.T) {
	hist := trendHist(t, 4, map[string][]int64{"public.a": {1, 2, 3, 4}})

	if got := growthTrendNote(&growthTrend{state: trendLoading}); got != "trend loading…" {
		t.Errorf("loading note = %q", got)
	}
	if got := growthTrendNote(&growthTrend{state: trendIdle}); got != "" {
		t.Errorf("idle note = %q, want empty", got)
	}
	if got := growthTrendNote(&growthTrend{state: trendNone, note: "one sample so far"}); got != "trend: one sample so far" {
		t.Errorf("none note = %q", got)
	}
	// Few samples: every one gets a cell, so the axis has no single span to
	// quote and nothing is folded away.
	got := growthTrendNote(&growthTrend{state: trendReady, hist: hist})
	for _, want := range []string{"trend", "4 samples", "gaps to scale"} {
		if !strings.Contains(got, want) {
			t.Errorf("proportional note %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, " of ") {
		t.Errorf("proportional note %q claims samples were folded away", got)
	}

	// More samples than cells: some share a cell, and the header has to own up
	// to showing fewer points than it read.
	dense := denseHist(t, growthTrendCells+10)
	got = growthTrendNote(&growthTrend{state: trendReady, hist: dense})
	if !strings.Contains(got, "per cell") {
		t.Errorf("uniform note %q should quote a span per cell", got)
	}
	if !strings.Contains(got, fmt.Sprintf("%d of %d samples", dense.Plotted, dense.Samples)) {
		t.Errorf("uniform note %q should say how many of the samples are plotted", got)
	}
	if dense.Plotted >= dense.Samples {
		t.Errorf("test setup: %d plotted of %d is not a folded axis", dense.Plotted, dense.Samples)
	}
}
