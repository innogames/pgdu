package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
)

const maintMB = int64(1) << 20

func maintParts() []item {
	return []item{
		{name: "heap", size: 9760 * maintMB, bloat: 209 * maintMB, hasBloat: true,
			data: pg.Part{Kind: pg.PartHeap, SizeBytes: 9760 * maintMB}},
		{name: "idx", size: 788 * maintMB, bloat: 251 * maintMB, hasBloat: true,
			data: pg.Part{Kind: pg.PartIndex, SizeBytes: 788 * maintMB}},
	}
}

func maintScreen(st *pg.TableMaintStats) *screen {
	s := &screen{level: levelParts, tool: toolDisk, items: maintParts(),
		table: pg.Table{OID: 1, Schema: "public", Name: "t"}}
	s.parts.tableStats = st
	return s
}

func healthyMaintStats() *pg.TableMaintStats {
	ago := func(d time.Duration) *time.Time { t := time.Now().Add(-d); return &t }
	return &pg.TableMaintStats{
		NLive: 79_800_000, NDead: 148_100,
		LastAutovacuum: ago(72 * time.Hour), LastAutoanalyze: ago(72 * time.Hour),
		NModSinceAnalyze: 2_800_000, NInsSinceVacuum: 1_100_000,
		RelTuples: 79_800_000, FrozenXIDAge: 2_100_000, RelKind: "r",
		AvacEnabled:         true,
		AvacVacuumThreshold: 50, AvacVacuumScale: 0.2,
		AvacInsertThreshold: 1000, AvacInsertScale: 0.2,
		AvacAnalyzeThreshold: 50, AvacAnalyzeScale: 0.1,
		FreezeMaxAge: 200_000_000, FailsafeAge: 1_600_000_000,
	}
}

func renderMaint(t *testing.T, st *pg.TableMaintStats) (string, []string) {
	t.Helper()
	s := maintScreen(st)
	m := newTestModel(s)
	m.width = 185
	out := stripANSI(m.renderMaintPanel(s))
	var lines []string
	for l := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return squashSpaces(out), lines
}

// A table with nothing wrong is a header and the reclaim row — every healthy
// metric keeps its figure in the ✓ fold instead of spending a line on it.
func TestMaintPanelHealthyFoldsToTwoLines(t *testing.T) {
	got, lines := renderMaint(t, healthyMaintStats())
	if len(lines) != 2 {
		t.Fatalf("healthy panel is %d lines, want 2:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, want := range []string{"✓ vacuum 3d", "analyze 3d", "dead 0%", "freeze 1%"} {
		if !strings.Contains(got, want) {
			t.Errorf("fold is missing %q: %s", want, got)
		}
	}
	// The things the rework removed: key hints the footer carries, and the
	// panic-in-red at a perfectly normal transaction age.
	for _, gone := range []string{"hint:", "freeze needed", "transaction age", "sequential scan"} {
		if strings.Contains(got, gone) {
			t.Errorf("panel still carries %q: %s", gone, got)
		}
	}
}

// Every finding becomes one bar row, and the metric it is about leaves the
// fold so nothing is said twice.
func TestMaintPanelSurfacesFindings(t *testing.T) {
	st := healthyMaintStats()
	st.NDead = 40_000_000
	st.NModSinceAnalyze = 30_000_000
	st.FrozenXIDAge = 441_000_000
	st.RelOptions = []string{"autovacuum_enabled=false", "fillfactor=90"}
	got, lines := renderMaint(t, st)

	for _, want := range []string{"dead ", "analyze ", "freeze ", "autovac ", "options fillfactor=90", "reclaim "} {
		if !strings.Contains(got, want) {
			t.Errorf("missing a %q row: %s", want, got)
		}
	}
	if strings.Contains(got, "dead 0%") || strings.Contains(got, "✓ dead") {
		t.Errorf("a flagged metric must leave the fold: %s", got)
	}
	if !strings.Contains(got, "✓ vacuum") {
		t.Errorf("recency never earns a row, so it must still fold: %s", got)
	}
	if !strings.Contains(got, "2.2×") {
		t.Errorf("the freeze row must show the age as a factor of freeze_max_age: %s", got)
	}
	if n := len(lines); n != 7 {
		t.Errorf("sick panel is %d lines, want 7 (header + 6 findings):\n%s", n, strings.Join(lines, "\n"))
	}
}

// An age at exactly freeze_max_age is a routine anti-wraparound vacuum, which
// the panel used to paint as an emergency.
func TestMaintPanelFreezeAtMaxAgeIsNotARow(t *testing.T) {
	st := healthyMaintStats()
	st.FrozenXIDAge = st.FreezeMaxAge
	got, lines := renderMaint(t, st)
	if len(lines) != 2 {
		t.Fatalf("a routine anti-wraparound vacuum earned a row:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(got, "freeze 100% (anti-wraparound due)") {
		t.Errorf("the fold must still say what is about to happen: %s", got)
	}
}

// A partitioned parent has no storage: no freeze figure, and no reclaim row
// without measured parts.
func TestMaintPanelPartitionedParent(t *testing.T) {
	st := healthyMaintStats()
	st.RelKind = "p"
	st.FrozenXIDAge = 1_500_000_000
	s := maintScreen(st)
	s.items = nil
	m := newTestModel(s)
	m.width = 185
	got := squashSpaces(stripANSI(m.renderMaintPanel(s)))
	if strings.Contains(got, "freeze") {
		t.Errorf("a partitioned parent has no relfrozenxid worth showing: %s", got)
	}
	if strings.Contains(got, "reclaim") {
		t.Errorf("nothing was measured, so there is nothing to reclaim: %s", got)
	}
}

// The panel's line count is what renderPartsLevel budgets the list against, so
// a narrow terminal must shrink the bar rather than wrap a row.
func TestMaintPanelFitsNarrowTerminal(t *testing.T) {
	st := healthyMaintStats()
	st.NDead = 40_000_000
	s := maintScreen(st)
	m := newTestModel(s)
	m.width = 72
	for l := range strings.SplitSeq(stripANSI(m.renderMaintPanel(s)), "\n") {
		if w := displayWidth(l); w > m.width {
			t.Errorf("line of %d cells in a %d-cell terminal: %q", w, m.width, l)
		}
	}
}

// Stats that failed to load degrade to one line and never panic.
func TestMaintPanelError(t *testing.T) {
	s := maintScreen(nil)
	s.parts.tableStatsErr = errTest("permission denied")
	m := newTestModel(s)
	m.width = 185
	got := stripANSI(m.renderMaintPanel(s))
	if !strings.Contains(got, "unavailable") || strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("error panel = %q, want a single line", got)
	}
}

// The four maintenance statements used to be advertised by a hint line inside
// the panel. That line is gone, so the footer is now their only home.
func TestPartsFooterCarriesMaintenanceKeys(t *testing.T) {
	parts := maintScreen(healthyMaintStats())
	m := newTestModel(parts)
	m.help.SetWidth(200)
	m.keys.applyContext(parts)
	got := stripANSI(m.help.View(m.keys))
	for _, want := range []string{"v VACUUM", "F VACUUM FREEZE", "a ANALYZE", "R REINDEX TABLE"} {
		if !strings.Contains(got, want) {
			t.Errorf("parts footer = %q, want %q", got, want)
		}
	}
	// v is the verbose toggle elsewhere; only the parts level relabels it.
	other := &screen{level: levelActivity, tool: toolActivity}
	m.keys.applyContext(other)
	if got := stripANSI(m.help.View(m.keys)); strings.Contains(got, "VACUUM") {
		t.Errorf("activity footer offers VACUUM: %q", got)
	}
}
