package pg

import (
	"testing"
	"time"
)

// healthyTable is a table no rule should fire on: well under every trigger,
// analyzed, autovacuum on, relfrozenxid young.
func healthyTable() *TableMaintStats {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	return &TableMaintStats{
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

func TestTableAdvice(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*TableMaintStats)
		key   string // "" = nothing should fire
		level AdviceLevel
	}{
		{name: "healthy"},
		{
			name: "dead past twice the trigger",
			// 16.0M trigger (50 + 20% of 79.8M); 40M dead is 2.5x it.
			mut:   func(s *TableMaintStats) { s.NDead = 40_000_000 },
			key:   "table_dead_rows",
			level: AdviceWarn,
		},
		{
			name:  "dead four times the trigger",
			mut:   func(s *TableMaintStats) { s.NDead = 70_000_000 },
			key:   "table_dead_rows",
			level: AdviceCrit,
		},
		{
			// Autovacuum's own threshold was raised out of reach by a
			// reloption, so the ratio says nothing and only the share does.
			name: "dead share past the band with the trigger raised",
			mut: func(s *TableMaintStats) {
				s.NDead = 30_000_000
				s.RelOptions = []string{"autovacuum_vacuum_scale_factor=0.9"}
			},
			key:   "table_dead_rows",
			level: AdviceWarn,
		},
		{
			name:  "a small table is not graded at all",
			mut:   func(s *TableMaintStats) { s.NLive, s.NDead, s.RelTuples = 200, 180, 200 },
			level: AdviceInfo,
		},
		{
			name:  "never analyzed",
			mut:   func(s *TableMaintStats) { s.RelTuples, s.LastAnalyze, s.LastAutoanalyze = -1, nil, nil },
			key:   "table_never_analyzed",
			level: AdviceWarn,
		},
		{
			name:  "analyze is not keeping up",
			mut:   func(s *TableMaintStats) { s.NModSinceAnalyze = 20_000_000 }, // 2.5x the 8.0M trigger
			key:   "table_analyze_due",
			level: AdviceWarn,
		},
		{
			// Right at the trigger autovacuum is *about* to do its job; saying
			// so would fire on every busy table in the cluster.
			name: "analyze exactly at its trigger says nothing",
			mut:  func(s *TableMaintStats) { s.NModSinceAnalyze = 8_000_050 },
		},
		{
			name:  "autovacuum disabled on this table",
			mut:   func(s *TableMaintStats) { s.RelOptions = []string{"autovacuum_enabled=false"} },
			key:   "table_autovacuum_off",
			level: AdviceCrit,
		},
		{
			name:  "autovacuum off cluster-wide",
			mut:   func(s *TableMaintStats) { s.AvacEnabled = false },
			key:   "table_autovacuum_off",
			level: AdviceWarn,
		},
		{
			// The regression this file exists for: the panel used to paint
			// "freeze needed!" in red here, while the cluster view calls a
			// routine anti-wraparound vacuum exactly that — routine.
			name:  "an age at freeze_max_age is routine, not a warning",
			mut:   func(s *TableMaintStats) { s.FrozenXIDAge = 200_000_000 },
			key:   "table_freeze",
			level: AdviceInfo,
		},
		{
			name:  "freezing has fallen behind",
			mut:   func(s *TableMaintStats) { s.FrozenXIDAge = 441_000_000 }, // 2.2x
			key:   "table_freeze",
			level: AdviceWarn,
		},
		{
			name:  "past half the failsafe",
			mut:   func(s *TableMaintStats) { s.FrozenXIDAge = 900_000_000 },
			key:   "table_freeze",
			level: AdviceCrit,
		},
		{
			name: "a partitioned parent has no relfrozenxid worth grading",
			mut: func(s *TableMaintStats) {
				s.RelKind, s.FrozenXIDAge = "p", 1_500_000_000
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := healthyTable()
			if c.mut != nil {
				c.mut(st)
			}
			got := TableAdvice(st)
			if c.key == "" {
				if len(got) != 0 {
					t.Fatalf("expected nothing to fire, got %+v", got)
				}
				return
			}
			a := got.Find(c.key)
			if a == nil {
				t.Fatalf("%s did not fire; got %+v", c.key, got)
			}
			if a.Level != c.level {
				t.Errorf("%s level = %v, want %v (%s)", c.key, a.Level, c.level, a.Reason)
			}
			if a.Reason == "" {
				t.Errorf("%s has no reason to show next to its row", c.key)
			}
		})
	}
}

// An age past its freeze_max_age must stay visible as a factor; the old
// clamped fraction pegged at 100% and could not tell a routine cycle from a
// table nothing is freezing any more.
func TestFreezeFactorIsUnclamped(t *testing.T) {
	st := healthyTable()
	st.FrozenXIDAge = 600_000_000
	if got := st.FreezeFactor(); got < 2.9 || got > 3.1 {
		t.Errorf("FreezeFactor = %v, want ~3", got)
	}
	// The meter, by contrast, measures the way to real danger and has room left.
	if got := st.FreezeDangerFrac(); got <= 0 || got >= 1 {
		t.Errorf("FreezeDangerFrac = %v, want a fraction with room left", got)
	}
}

func TestRelOptionsNote(t *testing.T) {
	got := RelOptionsNote([]string{"autovacuum_enabled=false", "fillfactor=90"})
	if got != "fillfactor=90" {
		t.Errorf("RelOptionsNote = %q, want the autovacuum flag dropped (it is its own row)", got)
	}
}
