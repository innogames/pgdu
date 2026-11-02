package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

var winT0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func sizeMeta(path string, at time.Time, total int64) pg.SizeSampleMeta {
	return pg.SizeSampleMeta{
		Path: path, Kind: "sizes", Version: 1,
		Target: "db:5432", Database: "app",
		DatabaseOID: 16384, SystemID: 42,
		CapturedAt: at, TotalBytes: total, RowCount: 10,
	}
}

// windowStack builds the growth table with its sample browser on top.
func windowStack(entry bool, metas []pg.SizeSampleMeta) (*Model, *screen, *screen) {
	gt := &screen{level: levelGrowth, tool: toolGrowth, db: "app"}
	pick := &screen{level: levelGrowthWindow, tool: toolGrowth, db: "app"}
	pick.growth.entry = entry
	m := newTestModel(gt, pick)
	m.onSizeSamplesListed(sizeSamplesListedMsg{
		db: "app", metas: metas,
		liveOID: 16384, liveSystemID: 42, liveTotal: 9999,
	})
	return m, gt, pick
}

// Entry mode omits the live anchor (the pick is always the window's start) and
// starts the cursor on the newest sample; the reopened browser offers it.
func TestGrowthWindowItems(t *testing.T) {
	metas := []pg.SizeSampleMeta{
		sizeMeta("/s/new.sizes.gz", winT0, 300),
		sizeMeta("/s/old.sizes.gz", winT0.Add(-48*time.Hour), 100),
	}

	_, _, entry := windowStack(true, metas)
	if len(entry.items) != 2 {
		t.Fatalf("entry mode: want 2 rows, got %d", len(entry.items))
	}
	for _, it := range entry.items {
		if it.snapPath == growthNow {
			t.Error("entry mode must not offer the live anchor")
		}
	}
	if entry.items[entry.cursor].snapPath != "/s/new.sizes.gz" {
		t.Errorf("entry cursor on %q, want the newest sample", entry.items[entry.cursor].snapPath)
	}

	_, _, reopened := windowStack(false, metas)
	if len(reopened.items) != 3 || reopened.items[0].snapPath != growthNow {
		t.Fatalf("reopened browser must lead with the live anchor, got %d rows", len(reopened.items))
	}
	if reopened.items[0].size != 9999 {
		t.Errorf("the live row is barred by %d, want the live database size", reopened.items[0].size)
	}
	// The bar is bytes, not a relation count.
	if reopened.items[1].size != 300 {
		t.Errorf("sample row barred by %d, want its TotalBytes", reopened.items[1].size)
	}
}

// Nothing is filtered out: sizes have no reset to go stale against, so even
// the oldest sample stays listable.
func TestGrowthWindowKeepsOldSamples(t *testing.T) {
	metas := []pg.SizeSampleMeta{
		sizeMeta("/s/new.sizes.gz", winT0, 300),
		sizeMeta("/s/ancient.sizes.gz", winT0.AddDate(-3, 0, 0), 10),
	}
	_, _, pick := windowStack(false, metas)
	found := false
	for _, it := range pick.items {
		if it.snapPath == "/s/ancient.sizes.gz" {
			found = true
		}
	}
	if !found {
		t.Error("a three-year-old sample was dropped; history must stay listable")
	}
}

func TestGrowthSampleCompatibility(t *testing.T) {
	_, _, pick := windowStack(false, nil)
	cases := []struct {
		name string
		meta pg.SizeSampleMeta
		ok   bool
		why  string
	}{
		{"same", sizeMeta("/s/a.sizes.gz", winT0, 1), true, ""},
		{"other server", pg.SizeSampleMeta{Target: "other:5432", Database: "app"}, false, "other server/db"},
		{"other database", pg.SizeSampleMeta{Target: "db:5432", Database: "billing"}, false, "other server/db"},
		{"other cluster", pg.SizeSampleMeta{Target: "db:5432", Database: "app", DatabaseOID: 16384, SystemID: 777}, false, "other cluster"},
		{"db recreated", pg.SizeSampleMeta{Target: "db:5432", Database: "app", DatabaseOID: 999, SystemID: 42}, false, "db recreated"},
		// pg_control_system() is revoked from PUBLIC, so an unprivileged
		// sampler writes zeros. Unknown must never read as a mismatch.
		{"unknown ids", pg.SizeSampleMeta{Target: "db:5432", Database: "app"}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, why := pick.growthCompatibleVia(c.meta, "db:5432")
			if ok != c.ok || why != c.why {
				t.Errorf("= %v/%q, want %v/%q", ok, why, c.ok, c.why)
			}
		})
	}
}

// The pairing idiom: first pick spans pick → now; a second pick spans the
// time-ordered range between the two.
func TestGrowthWindowPairing(t *testing.T) {
	metas := []pg.SizeSampleMeta{
		sizeMeta("/s/new.sizes.gz", winT0, 300),
		sizeMeta("/s/old.sizes.gz", winT0.Add(-48*time.Hour), 100),
	}
	m, gt, pick := windowStack(false, metas)

	// No anchor yet: picking the old sample opens old → now.
	if cmd := m.pickGrowthWindow(pick, item{snapPath: "/s/old.sizes.gz"}); cmd == nil {
		t.Fatal("picking a sample produced no command")
	}
	if m.notice != "" {
		t.Errorf("unexpected notice: %q", m.notice)
	}

	// With a start applied, picking a newer row freezes the range.
	gt.growth.basePath = "/s/old.sizes.gz"
	gt.growth.endPath = growthNow
	if cmd := m.pickGrowthWindow(pick, item{snapPath: "/s/new.sizes.gz"}); cmd == nil {
		t.Fatal("picking the second endpoint produced no command")
	}

	// The live anchor alone is not a window.
	m.notice = ""
	gt.growth.basePath = ""
	if cmd := m.pickGrowthWindow(pick, item{snapPath: growthNow}); cmd != nil {
		t.Error("the live anchor alone must not open a window")
	}
	if !strings.Contains(m.notice, "start") {
		t.Errorf("notice = %q, want it to ask for a start", m.notice)
	}

	// An incompatible sample is refused with a reason.
	m.notice = ""
	pick.growth.metas = append(pick.growth.metas,
		pg.SizeSampleMeta{Path: "/s/foreign.sizes.gz", Target: "db:5432", Database: "app",
			DatabaseOID: 16384, SystemID: 777, CapturedAt: winT0})
	if cmd := m.pickGrowthWindow(pick, item{snapPath: "/s/foreign.sizes.gz"}); cmd != nil {
		t.Error("an incompatible sample must not open a window")
	}
	if !strings.Contains(m.notice, "other cluster") {
		t.Errorf("notice = %q, want it to name the mismatch", m.notice)
	}
}

// Ordering is by capture time, so a reversed pick still yields a forward
// window.
func TestGrowthWindowOrdersByTime(t *testing.T) {
	metas := []pg.SizeSampleMeta{
		sizeMeta("/s/new.sizes.gz", winT0, 300),
		sizeMeta("/s/old.sizes.gz", winT0.Add(-48*time.Hour), 100),
	}
	_, _, pick := windowStack(false, metas)
	m2 := newTestModel()
	_ = m2
	newT := pick.growth.metas[0].CapturedAt
	oldT := pick.growth.metas[1].CapturedAt
	if !oldT.Before(newT) {
		t.Fatal("test fixture is not ordered")
	}
	mm, _, p := windowStack(false, metas)
	if got := mm.growthTime(p, "/s/old.sizes.gz"); !got.Equal(oldT) {
		t.Errorf("growthTime = %v, want %v", got, oldT)
	}
	if got := mm.growthTime(p, growthNow); !got.After(newT) {
		t.Error("the live anchor must order after every stored sample")
	}
}

// A refused window must not become the applied one, and in entry mode the
// picker must stay open rather than dropping the user onto an empty table.
func TestGrowthShortWindowRefused(t *testing.T) {
	m, gt, pick := windowStack(true, []pg.SizeSampleMeta{sizeMeta("/s/a.sizes.gz", winT0, 1)})
	depth := len(m.stack)

	m.onGrowthWindowLoaded(growthWindowLoadedMsg{db: "app", err: pg.ErrWindowTooShort})

	if gt.growth.base != nil || gt.growth.rows != nil {
		t.Error("a refused window must not be applied")
	}
	if len(m.stack) != depth {
		t.Errorf("stack depth %d, want %d — entry mode must stay on the picker", len(m.stack), depth)
	}
	if m.top() != pick {
		t.Error("entry mode left the picker after a refusal")
	}
	if !strings.Contains(m.notice, "minute") {
		t.Errorf("notice = %q, want it to explain the refusal", m.notice)
	}
}

// Each refusal gets its own explanation rather than a raw error string.
func TestGrowthWindowErrorMessages(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{pg.ErrWindowTooShort, "minute"},
		{pg.ErrSampleMismatch, "different databases"},
		{pg.ErrSampleVersion, "newer pgdu"},
	} {
		if got := growthWindowError(c.err); !strings.Contains(got, c.want) {
			t.Errorf("growthWindowError(%v) = %q, want it to mention %q", c.err, got, c.want)
		}
	}
}

// An applied window installs its rows and returns to the table.
func TestGrowthWindowApplied(t *testing.T) {
	m, gt, _ := windowStack(true, []pg.SizeSampleMeta{sizeMeta("/s/a.sizes.gz", winT0, 1)})
	res := &pg.GrowthResult{
		Window: pg.GrowthWindow{BaseAt: winT0, EndAt: winT0.Add(24 * time.Hour), Duration: 24 * time.Hour},
		Rows:   []pg.GrowthRow{gRow("public", "a", 100, 200, pg.GrowthSteady)},
	}
	m.onGrowthWindowLoaded(growthWindowLoadedMsg{
		db: "app", base: &pg.SizeSample{}, end: &pg.SizeSample{},
		basePath: "/s/a.sizes.gz", endPath: growthNow, result: res,
	})
	if gt.growth.base == nil || len(gt.growth.rows) != 1 {
		t.Fatal("the window was not applied")
	}
	if !gt.loaded || len(gt.items) != 1 {
		t.Errorf("table not rebuilt: loaded=%v items=%d", gt.loaded, len(gt.items))
	}
	if m.top() != gt {
		t.Error("a live-ended pick should return to the table")
	}
	if gt.growth.group != nil {
		t.Error("a fresh window must start without a narrowing")
	}
}

// The empty directory is the normal first run: say what to do about it, and
// keep S available right there.
func TestGrowthEmptyDirRendersCaptureHint(t *testing.T) {
	m, _, pick := windowStack(true, nil)
	m.width, m.height = 120, 30
	out := m.View().Content
	if !strings.Contains(out, "no size samples yet") {
		t.Errorf("empty picker does not explain itself:\n%s", out)
	}
	if !strings.Contains(out, "--sample") {
		t.Error("the empty state should point at the cron one-shot")
	}
	var k = defaultKeys()
	k.applyContext(pick)
	if !k.SaveSnapshot.Enabled() {
		t.Error("S must work from the picker, or the first run is a dead end")
	}
}

// D arms the shared confirm for a sample, never for the live anchor.
func TestGrowthDeleteArming(t *testing.T) {
	metas := []pg.SizeSampleMeta{sizeMeta("/s/a.sizes.gz", winT0, 1)}
	_, _, pick := windowStack(false, metas)

	pick.cursor = 0 // the live anchor
	if cur, ok := pick.currentItem(); !ok || cur.snapPath != growthNow {
		t.Fatal("cursor is not on the live anchor")
	}
	pick.cursor = 1
	cur, ok := pick.currentItem()
	if !ok || cur.snapPath != "/s/a.sizes.gz" {
		t.Fatalf("cursor is not on the sample, got %q", cur.snapPath)
	}
}
