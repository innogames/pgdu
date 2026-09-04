package pg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"pgdu/internal/cli"
)

func TestWraparoundSeverity(t *testing.T) {
	tests := []struct {
		name    string
		age, mx int64
		want    Severity
	}{
		{"quiet", 10_000_000, 200_000_000, SevOK},
		{"halfway", 170_000_000, 200_000_000, SevWarn},
		{"near freeze", 196_000_000, 200_000_000, SevCrit},
		{"unknown max", 170_000_000, 0, SevOK},
	}
	for _, tt := range tests {
		if got := wraparoundSeverity(tt.age, tt.mx); got != tt.want {
			t.Errorf("%s: wraparoundSeverity(%d, %d) = %v, want %v", tt.name, tt.age, tt.mx, got, tt.want)
		}
	}
}

// The wraparound drill-down is per-database, so the line must name the
// database holding the oldest datfrozenxid and carry it for the drill.
func TestWraparoundGradeNamesDatabase(t *testing.T) {
	info := &MaintenanceInfo{XidAge: 196_000_000, FreezeMaxAge: 200_000_000, XidAgeDB: "un1_game"}
	sev, detail, err := wraparoundGrade(info)
	if err != nil || sev != SevCrit {
		t.Fatalf("grade = %v, %v, want SevCrit", sev, err)
	}
	if !strings.HasSuffix(detail, "(in un1_game)") {
		t.Errorf("detail should name the database, got %q", detail)
	}
}

func TestMxidWraparoundGrade(t *testing.T) {
	if _, _, err := mxidWraparoundGrade(&MaintenanceInfo{MxidAge: 5}); err == nil {
		t.Errorf("missing autovacuum_multixact_freeze_max_age must degrade, not grade green")
	}
	tests := []struct {
		name string
		age  int64
		want Severity
	}{
		{"quiet", 10_000_000, SevOK},
		{"warn", 330_000_000, SevWarn},
		{"crit", 390_000_000, SevCrit},
	}
	for _, tt := range tests {
		info := &MaintenanceInfo{MxidAge: tt.age, MxidFreezeMaxAge: 400_000_000, MxidAgeDB: "un1_game"}
		sev, detail, err := mxidWraparoundGrade(info)
		if err != nil || sev != tt.want {
			t.Errorf("%s: grade = %v, %v, want %v", tt.name, sev, err, tt.want)
		}
		if !strings.Contains(detail, "datminmxid") || !strings.HasSuffix(detail, "(in un1_game)") {
			t.Errorf("%s: detail should mention datminmxid and the database, got %q", tt.name, detail)
		}
	}
}

func TestBlockedSeverity(t *testing.T) {
	tests := []struct {
		name      string
		waiting   int
		longestMs float64
		want      Severity
	}{
		{"none", 0, 0, SevOK},
		{"brief wait", 2, 5_000, SevWarn},
		{"long wait", 1, 48_000, SevCrit},
	}
	for _, tt := range tests {
		if got := blockedSeverity(tt.waiting, tt.longestMs); got != tt.want {
			t.Errorf("%s: blockedSeverity(%d, %v) = %v, want %v", tt.name, tt.waiting, tt.longestMs, got, tt.want)
		}
	}
}

func TestIdleInXactSeverity(t *testing.T) {
	if got := idleInXactSeverity(0, 0); got != SevOK {
		t.Errorf("no holders = %v, want SevOK", got)
	}
	if got := idleInXactSeverity(9, 1); got != SevOK {
		t.Errorf("young holders (1s) = %v, want SevOK", got)
	}
	if got := idleInXactSeverity(1, 120); got != SevWarn {
		t.Errorf("2m holder = %v, want SevWarn", got)
	}
	if got := idleInXactSeverity(1, 660); got != SevCrit {
		t.Errorf("11m holder = %v, want SevCrit", got)
	}
}

func TestSlotSeverity(t *testing.T) {
	if got := slotSeverity(0, 0, 0, false); got != SevOK {
		t.Errorf("healthy = %v, want SevOK", got)
	}
	if got := slotSeverity(1, 0, 0, false); got != SevWarn {
		t.Errorf("inactive = %v, want SevWarn", got)
	}
	if got := slotSeverity(1, 1, 0, false); got != SevCrit {
		t.Errorf("stale = %v, want SevCrit", got)
	}
	if got := slotSeverity(0, 0, 1, false); got != SevCrit {
		t.Errorf("lost = %v, want SevCrit", got)
	}
	if got := slotSeverity(0, 0, 0, true); got != SevCrit {
		t.Errorf("over cap = %v, want SevCrit", got)
	}
}

func TestCacheHitSeverity(t *testing.T) {
	if got := cacheHitSeverity(99.3); got != SevOK {
		t.Errorf("99.3 = %v, want SevOK", got)
	}
	if got := cacheHitSeverity(93); got != SevWarn {
		t.Errorf("93 = %v, want SevWarn", got)
	}
	if got := cacheHitSeverity(85); got != SevCrit {
		t.Errorf("85 = %v, want SevCrit", got)
	}
}

func TestSlruSeverity(t *testing.T) {
	if got := slruSeverity(99, 1_000_000); got != SevOK {
		t.Errorf("high hit = %v, want SevOK", got)
	}
	if got := slruSeverity(50, 100); got != SevOK {
		t.Errorf("low traffic = %v, want SevOK", got)
	}
	if got := slruSeverity(80, 5_000); got != SevWarn {
		t.Errorf("moderate reads = %v, want SevWarn", got)
	}
	if got := slruSeverity(80, 50_000); got != SevCrit {
		t.Errorf("heavy reads = %v, want SevCrit", got)
	}
}

func TestSequenceSeverity(t *testing.T) {
	if got := sequenceSeverity(10); got != SevOK {
		t.Errorf("10%% = %v, want SevOK", got)
	}
	if got := sequenceSeverity(85); got != SevWarn {
		t.Errorf("85%% = %v, want SevWarn", got)
	}
	if got := sequenceSeverity(95); got != SevCrit {
		t.Errorf("95%% = %v, want SevCrit", got)
	}
}

func TestDeadlockSeverity(t *testing.T) {
	if got := deadlockSeverity(0); got != SevOK {
		t.Errorf("none = %v, want SevOK", got)
	}
	if got := deadlockSeverity(0.4); got != SevOK {
		t.Errorf("one every few days = %v, want SevOK", got)
	}
	if got := deadlockSeverity(2); got != SevWarn {
		t.Errorf("a couple a day = %v, want SevWarn", got)
	}
	if got := deadlockSeverity(20); got != SevCrit {
		t.Errorf("many a day = %v, want SevCrit", got)
	}
}

func TestTempBytesSeverity(t *testing.T) {
	if got := tempBytesSeverity(1 << 20); got != SevOK {
		t.Errorf("1MB/day = %v, want SevOK", got)
	}
	if got := tempBytesSeverity(20 << 30); got != SevWarn {
		t.Errorf("20GB/day = %v, want SevWarn", got)
	}
	if got := tempBytesSeverity(200 << 30); got != SevCrit {
		t.Errorf("200GB/day = %v, want SevCrit", got)
	}
}

// dbStatsResult builds a database_stats-shaped result: one row per
// (counter, stats_age_secs) pair, with a NaN age standing for a missing value.
func dbStatsResult(col string, rows ...[2]float64) *DiagResult {
	res := &DiagResult{Columns: []DiagColumn{{Name: "database"}, {Name: col}, {Name: "stats_age_secs"}}}
	for i, r := range rows {
		row := []DiagCell{{Display: fmt.Sprintf("db%d", i)}, {Num: r[0], HasNum: true}, {Num: r[1], HasNum: true}}
		if r[1] < 0 {
			row[2] = DiagCell{Display: "∅"}
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// A long-lived cluster's absolute deadlock total must not trip the check when
// the per-day rate is small; the same total over a short window must.
func TestDeadlockGradeUsesRate(t *testing.T) {
	// 12 deadlocks over 30 days: 0.4/day, fine.
	sev, detail, _ := deadlockGrade(dbStatsResult("deadlocks", [2]float64{12, 30 * 86400}))
	if sev != SevOK {
		t.Errorf("12 over 30d = %v, want SevOK (%s)", sev, detail)
	}
	if !strings.Contains(detail, "12 deadlock(s)") || !strings.Contains(detail, "30d ago") || !strings.Contains(detail, "~0.4/day") {
		t.Errorf("detail = %q, want total, window and rate", detail)
	}
	// 12 deadlocks in 2 days: 6/day, warn.
	if sev, detail, _ = deadlockGrade(dbStatsResult("deadlocks", [2]float64{12, 2 * 86400})); sev != SevWarn {
		t.Errorf("12 over 2d = %v, want SevWarn (%s)", sev, detail)
	}
	// 12 deadlocks an hour after a reset: the window floors at a day, so 12/day.
	if sev, detail, _ = deadlockGrade(dbStatsResult("deadlocks", [2]float64{12, 3600})); sev != SevCrit {
		t.Errorf("12 over 1h = %v, want SevCrit (%s)", sev, detail)
	}
	// Per-database windows differ: rates are summed per row, not total/max window.
	res := dbStatsResult("deadlocks", [2]float64{1, 30 * 86400}, [2]float64{10, 86400})
	if sev, detail, _ = deadlockGrade(res); sev != SevCrit {
		t.Errorf("10/day in a fresh db + 1 over 30d = %v, want SevCrit (%s)", sev, detail)
	}
	// No stats_age_secs at all: never critical, but a deadlock still warns.
	res = &DiagResult{Columns: []DiagColumn{{Name: "deadlocks"}}, Rows: [][]DiagCell{{{Num: 500, HasNum: true}}}}
	if sev, detail, _ = deadlockGrade(res); sev != SevWarn || !strings.Contains(detail, "rate unknown") {
		t.Errorf("no window = %v %q, want SevWarn with rate unknown", sev, detail)
	}
	if sev, detail, _ = deadlockGrade(dbStatsResult("deadlocks", [2]float64{0, 86400})); sev != SevOK || !strings.Contains(detail, "no deadlocks") {
		t.Errorf("zero = %v %q, want SevOK", sev, detail)
	}
}

func TestTempFilesGradeUsesRate(t *testing.T) {
	// 300 GB over 100 days is 3 GB/day: fine, though the old absolute check screamed.
	sev, detail, _ := tempFilesGrade(dbStatsResult("temp_bytes", [2]float64{300 << 30, 100 * 86400}))
	if sev != SevOK {
		t.Errorf("300GB over 100d = %v, want SevOK (%s)", sev, detail)
	}
	if !strings.Contains(detail, "300.00 GB spilled") || !strings.Contains(detail, "100d ago") || !strings.Contains(detail, "3.00 GB/day") {
		t.Errorf("detail = %q, want total, window and rate", detail)
	}
	if sev, detail, _ = tempFilesGrade(dbStatsResult("temp_bytes", [2]float64{300 << 30, 2 * 86400})); sev != SevCrit {
		t.Errorf("300GB over 2d = %v, want SevCrit (%s)", sev, detail)
	}
	res := &DiagResult{Columns: []DiagColumn{{Name: "temp_bytes"}}, Rows: [][]DiagCell{{{Num: 1 << 40, HasNum: true}}}}
	if sev, detail, _ = tempFilesGrade(res); sev != SevOK || !strings.Contains(detail, "rate unknown") {
		t.Errorf("no window = %v %q, want SevOK with rate unknown", sev, detail)
	}
}

func TestArchiverSeverity(t *testing.T) {
	if got := archiverSeverity(0); got != SevOK {
		t.Errorf("no failures = %v, want SevOK", got)
	}
	if got := archiverSeverity(1); got != SevCrit {
		t.Errorf("one failure = %v, want SevCrit", got)
	}
}

func TestConnSaturationSeverity(t *testing.T) {
	if got := connSaturationSeverity(50, 100); got != SevOK {
		t.Errorf("half = %v, want SevOK", got)
	}
	if got := connSaturationSeverity(85, 100); got != SevWarn {
		t.Errorf("85%% = %v, want SevWarn", got)
	}
	if got := connSaturationSeverity(98, 100); got != SevCrit {
		t.Errorf("98%% = %v, want SevCrit", got)
	}
	if got := connSaturationSeverity(10, 0); got != SevOK {
		t.Errorf("unknown max = %v, want SevOK", got)
	}
}

func TestCheckpointSeverity(t *testing.T) {
	if got := checkpointSeverity(3, 4); got != SevOK {
		t.Errorf("too few to judge = %v, want SevOK", got)
	}
	if got := checkpointSeverity(1, 100); got != SevOK {
		t.Errorf("mostly timed = %v, want SevOK", got)
	}
	if got := checkpointSeverity(40, 100); got != SevWarn {
		t.Errorf("40%% requested = %v, want SevWarn", got)
	}
	if got := checkpointSeverity(70, 100); got != SevCrit {
		t.Errorf("70%% requested = %v, want SevCrit", got)
	}
}

func TestPreparedXactSeverity(t *testing.T) {
	if got := preparedXactSeverity(5); got != SevWarn {
		t.Errorf("young = %v, want SevWarn", got)
	}
	if got := preparedXactSeverity(600); got != SevCrit {
		t.Errorf("10m old = %v, want SevCrit", got)
	}
}

func TestRollbackSeverity(t *testing.T) {
	if got := rollbackSeverity(0.05); got != SevOK {
		t.Errorf("5%% = %v, want SevOK", got)
	}
	if got := rollbackSeverity(0.30); got != SevWarn {
		t.Errorf("30%% = %v, want SevWarn", got)
	}
	if got := rollbackSeverity(0.60); got != SevCrit {
		t.Errorf("60%% = %v, want SevCrit", got)
	}
}

func TestExtCapacitySeverity(t *testing.T) {
	if got := extCapacitySeverity(0.75); got != SevOK {
		t.Errorf("75%% = %v, want SevOK", got)
	}
	if got := extCapacitySeverity(0.90); got != SevWarn {
		t.Errorf("90%% = %v, want SevWarn", got)
	}
	if got := extCapacitySeverity(1); got != SevWarn {
		t.Errorf("full = %v, want SevWarn (no crit level)", got)
	}
}

func TestExtCapacityGrade(t *testing.T) {
	info := &MaintenanceInfo{
		Statements: ExtCapacity{Name: "pg_stat_statements", Installed: true, Used: 3765, Max: 5000},
		Qualstats:  ExtCapacity{Name: "pg_qualstats", Installed: true, Used: 952, Max: 1000},
	}
	sev, detail, err := extCapacityGrade(info)
	if err != nil || sev != SevWarn {
		t.Fatalf("grade = %v, %v, want SevWarn", sev, err)
	}
	if !strings.HasPrefix(detail, "pg_qualstats 95% full (952/1000)") {
		t.Errorf("fullest extension should lead the detail, got %q", detail)
	}
	// Installed but not preloaded (Max unknown) has nothing to grade.
	none := &MaintenanceInfo{Statements: ExtCapacity{Name: "pg_stat_statements", Installed: true, Used: 10}}
	if sev, _, _ := extCapacityGrade(none); sev != SevOK {
		t.Errorf("unknown max = %v, want SevOK", sev)
	}
}

func TestIndexBloatSeverity(t *testing.T) {
	if got := indexBloatSeverity(0, 0); got != SevOK {
		t.Errorf("none = %v, want SevOK", got)
	}
	if got := indexBloatSeverity(3, 200<<20); got != SevWarn {
		t.Errorf("200MB wasted = %v, want SevWarn", got)
	}
	if got := indexBloatSeverity(85, 4<<30); got != SevCrit {
		t.Errorf("4GB wasted = %v, want SevCrit", got)
	}
}

func TestReplicaSeverity(t *testing.T) {
	if got := replicaSeverity("streaming", 0.5, 1<<20); got != SevOK {
		t.Errorf("healthy = %v, want SevOK", got)
	}
	if got := replicaSeverity("catchup", 0, 0); got != SevWarn {
		t.Errorf("catchup = %v, want SevWarn", got)
	}
	if got := replicaSeverity("streaming", 90, 0); got != SevWarn {
		t.Errorf("90s lag = %v, want SevWarn", got)
	}
	if got := replicaSeverity("streaming", 600, 0); got != SevCrit {
		t.Errorf("10m lag = %v, want SevCrit", got)
	}
	if got := replicaSeverity("streaming", 0, 2<<30); got != SevCrit {
		t.Errorf("2GB behind, no lag reported = %v, want SevCrit", got)
	}
}

func TestWalReceiverSeverity(t *testing.T) {
	if got := walReceiverSeverity(true, "streaming", 5); got != SevOK {
		t.Errorf("streaming = %v, want SevOK", got)
	}
	if got := walReceiverSeverity(false, "", 0); got != SevWarn {
		t.Errorf("no receiver = %v, want SevWarn", got)
	}
	if got := walReceiverSeverity(true, "waiting", 5); got != SevWarn {
		t.Errorf("waiting = %v, want SevWarn", got)
	}
	if got := walReceiverSeverity(true, "streaming", 120); got != SevWarn {
		t.Errorf("2m silence = %v, want SevWarn", got)
	}
	if got := walReceiverSeverity(true, "streaming", 900); got != SevCrit {
		t.Errorf("15m silence = %v, want SevCrit", got)
	}
}

func TestLongXactSeverity(t *testing.T) {
	if got := longXactSeverity(120); got != SevOK {
		t.Errorf("2m = %v, want SevOK", got)
	}
	if got := longXactSeverity(3600); got != SevWarn {
		t.Errorf("1h = %v, want SevWarn", got)
	}
	if got := longXactSeverity(5 * 3600); got != SevCrit {
		t.Errorf("5h = %v, want SevCrit", got)
	}
}

func TestPgbouncerSeverity(t *testing.T) {
	if got := pgbouncerSeverity(0, 0); got != SevOK {
		t.Errorf("nobody waiting = %v, want SevOK", got)
	}
	if got := pgbouncerSeverity(3, 0.2); got != SevOK {
		t.Errorf("brief queueing = %v, want SevOK", got)
	}
	if got := pgbouncerSeverity(3, 2.5); got != SevWarn {
		t.Errorf("2.5s wait = %v, want SevWarn", got)
	}
	if got := pgbouncerSeverity(1, 30); got != SevCrit {
		t.Errorf("30s wait = %v, want SevCrit", got)
	}
}

func TestTriageDuration(t *testing.T) {
	tests := []struct {
		secs float64
		want string
	}{
		{48, "48s"},
		{660, "11m"},
		{7200, "2h"},
		{3 * 86400, "3d"},
	}
	for _, tt := range tests {
		if got := triageDuration(tt.secs); got != tt.want {
			t.Errorf("triageDuration(%v) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}

func TestDiagNumAndColIdx(t *testing.T) {
	res := &DiagResult{Columns: []DiagColumn{{Name: "a"}, {Name: "b"}}}
	if got := diagColIdx(res, "b"); got != 1 {
		t.Errorf("diagColIdx(b) = %d, want 1", got)
	}
	if got := diagColIdx(res, "missing"); got != -1 {
		t.Errorf("diagColIdx(missing) = %d, want -1", got)
	}
	row := []DiagCell{{Display: "x"}, {Num: 42, HasNum: true}}
	if v, ok := diagNum(row, 1); !ok || v != 42 {
		t.Errorf("diagNum(1) = %v,%v, want 42,true", v, ok)
	}
	if _, ok := diagNum(row, 0); ok {
		t.Errorf("diagNum on text cell should be false")
	}
	if _, ok := diagNum(row, -1); ok {
		t.Errorf("diagNum(-1) should be false")
	}
}

// Triage must degrade a failing check to a "could not evaluate" line instead
// of failing the report; with a cancelled context every check fails that way.
func TestTriageDegradesOnFailure(t *testing.T) {
	c := New(cli.Config{Database: "nope"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := c.Triage(ctx)
	if len(results) != 25 {
		t.Fatalf("Triage returned %d results, want 25", len(results))
	}
	for _, r := range results {
		if r.Check == "" {
			t.Errorf("result with empty Check name: %+v", r)
		}
		if r.Severity != SevWarn {
			t.Errorf("%s: severity %v, want SevWarn for a failed check", r.Check, r.Severity)
		}
		if !strings.HasPrefix(r.Detail, "could not evaluate: ") {
			t.Errorf("%s: detail %q, want could-not-evaluate", r.Check, r.Detail)
		}
	}
}

func TestTriageDiagKeysExist(t *testing.T) {
	c := New(cli.Config{Database: "nope"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range c.Triage(ctx) {
		if r.Target != TriageTargetDiagnostic {
			continue
		}
		if _, ok := DiagnosticByKey(r.DiagKey); !ok {
			t.Errorf("%s: DiagKey %q not in the Diagnostics registry", r.Check, r.DiagKey)
		}
	}
}

// TriageStream must emit exactly one result per check named by
// TriageCheckNames, and Triage must order them severity-first then in battery
// order so a streaming report keeps stable row positions.
func TestTriageStreamCoversEveryCheck(t *testing.T) {
	c := New(cli.Config{Database: "nope"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	names := c.TriageCheckNames()
	seen := make(map[string]int, len(names))
	var mu sync.Mutex
	c.TriageStream(ctx, func(r TriageResult) {
		mu.Lock()
		seen[r.Check]++
		mu.Unlock()
	})
	if len(seen) != len(names) {
		t.Fatalf("stream emitted %d distinct checks, want %d", len(seen), len(names))
	}
	for _, n := range names {
		if seen[n] != 1 {
			t.Errorf("%s: emitted %d times, want once", n, seen[n])
		}
	}

	results := c.Triage(ctx)
	for i, r := range results {
		if r.Check != names[i] {
			t.Errorf("result %d is %q, want battery order %q (all same severity)", i, r.Check, names[i])
		}
	}
}

func TestSortTriage(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	rs := []TriageResult{
		{Check: "d", Severity: SevOK},
		{Check: "c", Severity: SevCrit},
		{Check: "b", Severity: SevWarn},
		{Check: "a", Severity: SevCrit},
	}
	SortTriage(rs, names)
	got := make([]string, len(rs))
	for i, r := range rs {
		got[i] = r.Check
	}
	if want := "a c b d"; strings.Join(got, " ") != want {
		t.Errorf("SortTriage order %v, want %s", got, want)
	}
}
