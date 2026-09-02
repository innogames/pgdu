package pg

import (
	"context"
	"strings"
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
		{"halfway", 110_000_000, 200_000_000, SevWarn},
		{"near freeze", 170_000_000, 200_000_000, SevCrit},
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
	info := &MaintenanceInfo{XidAge: 190_000_000, FreezeMaxAge: 200_000_000, XidAgeDB: "un1_game"}
	sev, detail, err := wraparoundGrade(info)
	if err != nil || sev != SevCrit {
		t.Fatalf("grade = %v, %v, want SevCrit", sev, err)
	}
	if !strings.HasSuffix(detail, "(in un1_game)") {
		t.Errorf("detail should name the database, got %q", detail)
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
	if got := deadlockSeverity(20); got != SevWarn {
		t.Errorf("a few = %v, want SevWarn", got)
	}
	if got := deadlockSeverity(200); got != SevCrit {
		t.Errorf("many = %v, want SevCrit", got)
	}
}

func TestTempBytesSeverity(t *testing.T) {
	if got := tempBytesSeverity(1 << 20); got != SevOK {
		t.Errorf("1MB = %v, want SevOK", got)
	}
	if got := tempBytesSeverity(20 << 30); got != SevWarn {
		t.Errorf("20GB = %v, want SevWarn", got)
	}
	if got := tempBytesSeverity(200 << 30); got != SevCrit {
		t.Errorf("200GB = %v, want SevCrit", got)
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
	if len(results) != 23 {
		t.Fatalf("Triage returned %d results, want 23", len(results))
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
