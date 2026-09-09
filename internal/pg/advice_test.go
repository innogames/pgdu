package pg

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/sysmem"
)

const (
	gib = int64(1 << 30)
	mib = int64(1 << 20)
)

// healthyInfo is a snapshot no rule should fire on: everything known, every
// figure inside its band. Each test case mutates one aspect of a copy.
func healthyInfo() *MaintenanceInfo {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	return &MaintenanceInfo{
		SampledAt: now,
		StartTime: now.Add(-10 * 24 * time.Hour),
		MaxConns:  100,
		Host: sysmem.Info{
			Total: 32 * gib, Available: 20 * gib, Free: 2 * gib, Cached: 16 * gib,
			SwapTotal: 8 * gib, SwapFree: 8 * gib,
			HugePagesTotal: 4200, HugePagesFree: 100, HugePageSize: 2 * mib,
		},
		Settings: map[string]string{
			"huge_pages": "try", "shared_buffers": "8GB", "work_mem": "16MB",
			"effective_cache_size": "24GB", "max_wal_size": "4GB", "wal_buffers": "16MB",
			"checkpoint_completion_target": "0.9", "bgwriter_lru_maxpages": "100",
			"idle_in_transaction_session_timeout": "10min", "track_io_timing": "on",
			"log_checkpoints": "on", "pg_stat_statements.track": "top",
		},
		SettingBytes: map[string]int64{
			"shared_buffers": 8 * gib, "work_mem": 16 * mib, "maintenance_work_mem": gib,
			"autovacuum_work_mem": -1, "effective_cache_size": 24 * gib,
			"max_wal_size": 4 * gib, "wal_buffers": 16 * mib,
		},
		Tuning: MaintTuning{
			CheckpointTimeoutSecs: 1800, CheckpointCompletion: 0.9,
			AutovacCostDelayMs: 2, VacuumCostDelayMs: 0, AutovacCostLimit: -1, VacuumCostLimit: 200,
			AutovacMaxWorkers: 3, ShmemHugePages: 4200, BgwriterLRUMaxpages: 100, IdleInTxnTimeoutMs: 600_000,
		},
		Statements: ExtCapacity{Installed: true, Used: 1000, Max: 5000},
		Checkpointer: CheckpointerStat{HasData: true, Timed: 480, Requested: 10,
			WriteTimeMs: 480 * 20_000, SyncTimeMs: 480 * 100, StatsReset: now.Add(-10 * 24 * time.Hour)},
		WAL: WALStat{HasData: true, Records: 1_000_000, FPI: 100_000, Bytes: 100 * gib,
			StatsReset: now.Add(-10 * 24 * time.Hour), CurrentLSNBytes: 500 * gib},
		IO:      IOStat{HasData: true, Reads: 1_000_000, Writes: 100_000, Evictions: 50_000},
		IOSplit: IOSplitStat{HasData: true, CheckpointerWrites: 80_000, BgwriterWrites: 19_000, ClientWrites: 1_000},
		BufCache: BufCacheStat{Installed: true, HasData: true, Used: 1_000_000, Dirty: 10_000,
			UsageCounts: []BufferUsageCount{{Count: 0, Buffers: 200_000}, {Count: 1, Buffers: 200_000},
				{Count: 2, Buffers: 200_000}, {Count: 3, Buffers: 200_000}, {Count: 4, Buffers: 100_000}, {Count: 5, Buffers: 100_000}}},
		Autovac: AutovacStat{WorkersBusy: 1, OverThreshold: 3},
	}
}

func TestMaintAdviceHealthyIsSilent(t *testing.T) {
	if got := MaintAdvice(healthyInfo()); len(got) != 0 {
		t.Errorf("healthy snapshot produced advice: %+v", got)
	}
	if MaintAdvice(nil) != nil {
		t.Error("nil info must yield nil advice")
	}
}

func TestMaintAdviceRules(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*MaintenanceInfo)
		key       string
		level     AdviceLevel
		suggested string // "" = don't check
		fixHas    string // substring the Fix must contain; "" = Fix must be empty
	}{
		{"huge pages not allocated", func(i *MaintenanceInfo) { i.Host.HugePagesTotal = 0 },
			"huge_pages", AdviceWarn, "vm.nr_hugepages=4200", "sysctl -w vm.nr_hugepages=4200"},
		{"huge_pages off ignores host", func(i *MaintenanceInfo) { i.Host.HugePagesTotal = 0; i.Settings["huge_pages"] = "off" },
			"", 0, "", ""},
		{"swap in use", func(i *MaintenanceInfo) { i.Host.SwapFree = 6 * gib }, // 2 GiB of 32 GiB
			"swap", AdviceWarn, "", ""},
		{"a little swap is fine", func(i *MaintenanceInfo) { i.Host.SwapFree = 8*gib - 45*mib },
			"", 0, "", ""},
		{"shared_buffers tiny", func(i *MaintenanceInfo) { i.SettingBytes["shared_buffers"] = gib },
			"shared_buffers", AdviceInfo, "", ""},
		{"effective_cache_size low", func(i *MaintenanceInfo) { i.SettingBytes["effective_cache_size"] = 4 * gib },
			"effective_cache_size", AdviceWarn, "21GB", "ALTER SYSTEM SET effective_cache_size = '21GB'"},
		{"work_mem exposure", func(i *MaintenanceInfo) { i.SettingBytes["work_mem"] = 256 * mib },
			"work_mem", AdviceWarn, "81MB", "ALTER SYSTEM SET work_mem"},
		{"dirty buffers", func(i *MaintenanceInfo) { i.BufCache.Dirty = 300_000 },
			"buffercache_dirty", AdviceWarn, "", ""},
		{"cache tight", func(i *MaintenanceInfo) {
			i.BufCache.UsageCounts = []BufferUsageCount{{Count: 0, Buffers: 10}, {Count: 4, Buffers: 400}, {Count: 5, Buffers: 400}}
		}, "buffercache_tight", AdviceInfo, "", ""},
		{"backend fsyncs", func(i *MaintenanceInfo) { i.IO.BackendFsyncs = 7 },
			"backend_fsyncs", AdviceCrit, "", ""},
		{"backends writing", func(i *MaintenanceInfo) { i.IOSplit.ClientWrites = 30_000 },
			"bgwriter_lru_maxpages", AdviceWarn, "400", "ALTER SYSTEM SET bgwriter_lru_maxpages = '400'"},
		{"bgwriter capped sweeps", func(i *MaintenanceInfo) {
			// 12M sweeps × 100 pages = 1.2G of 2.7G cleaned → 44 %.
			i.Bgwriter = BgwriterStat{BuffersClean: 2_700_000_000, MaxwrittenClean: 12_000_000}
		}, "bgwriter_lru_maxpages", AdviceInfo, "400", "bgwriter_lru_maxpages = '400'"},
		{"bgwriter rarely capped is fine", func(i *MaintenanceInfo) {
			i.Bgwriter = BgwriterStat{BuffersClean: 2_700_000_000, MaxwrittenClean: 1_000_000}
		}, "", 0, "", ""},
		{"backends writing below floor is ignored", func(i *MaintenanceInfo) {
			i.IOSplit = IOSplitStat{HasData: true, CheckpointerWrites: 5, ClientWrites: 5}
		}, "", 0, "", ""},
		{"completion target", func(i *MaintenanceInfo) { i.Tuning.CheckpointCompletion = 0.5 },
			"checkpoint_completion_target", AdviceInfo, "0.9", "checkpoint_completion_target = '0.9'"},
		// 100 GiB over 10 days is far below what 4GB covers per 30 min, so the
		// average can't size it: WAL-driven checkpoints mean bursts → double.
		{"WAL-driven checkpoints", func(i *MaintenanceInfo) { i.Checkpointer.Requested = 480 },
			"max_wal_size", AdviceCrit, "8GB", "ALTER SYSTEM SET max_wal_size = '8GB'"},
		{"frequent checkpoints get a size", func(i *MaintenanceInfo) {
			// 10 days, 12k checkpoints → one every 72 s against a 30 min timeout.
			// 100 GiB over 10 days ≈ 121 kB/s × 1800 s × 1.9 ≈ 414 MB rounds to
			// the 1 GB floor, below the current 4GB (no suggestion) — so bump the
			// volume: 10 TiB → 12.1 MiB/s × 1800 × 1.9 = 40.5 GiB → 41 GB.
			i.Checkpointer.Timed, i.Checkpointer.Requested = 0, 12_000
			i.WAL.Bytes = 10 * 1024 * gib
		}, "max_wal_size", AdviceCrit, "41GB", "ALTER SYSTEM SET max_wal_size = '41GB'"},
		{"slow fsync", func(i *MaintenanceInfo) { i.Checkpointer.SyncTimeMs = 490 * 1500 },
			"checkpoint_sync", AdviceWarn, "", ""},
		{"wal_buffers stalls", func(i *MaintenanceInfo) { i.WAL.BuffersFull = 12 },
			"wal_buffers", AdviceWarn, "64MB", "restart required"},
		{"wal_buffers already large doubles", func(i *MaintenanceInfo) { i.WAL.BuffersFull = 12; i.SettingBytes["wal_buffers"] = 128 * mib },
			"wal_buffers", AdviceWarn, "256MB", "wal_buffers = '256MB'"},
		{"FPI share only with frequent checkpoints", func(i *MaintenanceInfo) { i.WAL.FPI = 900_000 },
			"", 0, "", ""},
		{"FPI share with frequent checkpoints", func(i *MaintenanceInfo) { i.WAL.FPI = 900_000; i.Checkpointer.Requested = 12_000 },
			"wal_fpi", AdviceInfo, "", ""},
		{"autovacuum saturated", func(i *MaintenanceInfo) { i.Autovac.WorkersBusy = 3 },
			"autovacuum_cost", AdviceWarn, "400", "autovacuum_vacuum_cost_limit = '400'"},
		{"autovacuum saturated without delay is fine", func(i *MaintenanceInfo) { i.Autovac.WorkersBusy = 3; i.Tuning.AutovacCostDelayMs = 0 },
			"", 0, "", ""},
		{"autovacuum backlog", func(i *MaintenanceInfo) {
			i.Autovac.OverThreshold = 25
			i.Autovac.OverTop = []string{"public.a", "public.b"}
		},
			"autovacuum_backlog", AdviceWarn, "", ""},
		{"idle in txn warn", func(i *MaintenanceInfo) { i.Sess.IdleXactPID = 42; i.Sess.IdleXactSecs = 90 },
			"idle_in_transaction", AdviceWarn, "", ""},
		{"idle in txn crit suggests timeout", func(i *MaintenanceInfo) {
			i.Sess.IdleXactPID = 42
			i.Sess.IdleXactSecs = 900
			i.Tuning.IdleInTxnTimeoutMs = 0
		}, "idle_in_transaction", AdviceCrit, "10min", "idle_in_transaction_session_timeout = '10min'"},
		{"idle in txn below warn", func(i *MaintenanceInfo) { i.Sess.IdleXactPID = 42; i.Sess.IdleXactSecs = 5 },
			"", 0, "", ""},
		{"pgss tracking nothing", func(i *MaintenanceInfo) { i.Settings["pg_stat_statements.track"] = "none" },
			"pg_stat_statements.track", AdviceCrit, "top", "pg_stat_statements.track = 'top'"},
		{"pgss track none but not installed", func(i *MaintenanceInfo) {
			i.Settings["pg_stat_statements.track"] = "none"
			i.Statements.Installed = false
		}, "", 0, "", ""},
		{"pgss full", func(i *MaintenanceInfo) { i.Statements.Used = 4800 },
			"pg_stat_statements.max", AdviceWarn, "10000", "restart required"},
		{"track_io_timing off", func(i *MaintenanceInfo) { i.Settings["track_io_timing"] = "off" },
			"track_io_timing", AdviceWarn, "on", "track_io_timing = 'on'"},
		{"log_checkpoints off", func(i *MaintenanceInfo) { i.Settings["log_checkpoints"] = "off" },
			"log_checkpoints", AdviceInfo, "on", "log_checkpoints = 'on'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := healthyInfo()
			c.mutate(info)
			got := MaintAdvice(info)
			if c.key == "" {
				if len(got) != 0 {
					t.Fatalf("expected no advice, got %+v", got)
				}
				return
			}
			// Most mutations trip exactly one rule; a few legitimately trip a
			// second (frequent checkpoints also size max_wal_size), so look the
			// expected key up rather than insisting on a single result.
			ap := got.Find(c.key)
			if ap == nil {
				t.Fatalf("expected advice %q, got %+v", c.key, got)
			}
			a := *ap
			if a.Level != c.level {
				t.Errorf("level = %d, want %d", a.Level, c.level)
			}
			if c.suggested != "" && a.Suggested != c.suggested {
				t.Errorf("Suggested = %q, want %q", a.Suggested, c.suggested)
			}
			if c.fixHas == "" && a.Fix != "" {
				t.Errorf("Fix = %q, want none", a.Fix)
			}
			if c.fixHas != "" && !strings.Contains(a.Fix, c.fixHas) {
				t.Errorf("Fix = %q, want it to contain %q", a.Fix, c.fixHas)
			}
			if a.Reason == "" {
				t.Error("Reason must not be empty")
			}
		})
	}
}

func TestMaintAdviceOrderAndActionable(t *testing.T) {
	info := healthyInfo()
	info.Settings["log_checkpoints"] = "off"           // Info with Fix
	info.SettingBytes["shared_buffers"] = gib          // Info without Fix
	info.Host.SwapFree = 6 * gib                       // Warn
	info.Settings["pg_stat_statements.track"] = "none" // Crit
	got := MaintAdvice(info)
	keys := make([]string, 0, len(got))
	for _, a := range got {
		keys = append(keys, a.Key)
	}
	want := "pg_stat_statements.track,swap,log_checkpoints,shared_buffers"
	if strings.Join(keys, ",") != want {
		t.Errorf("order = %v, want %s", keys, want)
	}
	act := got.Actionable()
	if len(act) != 3 || act.Find("shared_buffers") != nil {
		t.Errorf("Actionable() = %+v, want the three with a level or a fix", act)
	}
	if got.Find("swap") == nil || got.Find("nope") != nil {
		t.Error("Find lookup broken")
	}
}

func TestRecommendedMaxWALSize(t *testing.T) {
	cases := []struct {
		rate    float64
		timeout int64
		cct     float64
		want    int64
	}{
		{0, 300, 0.9, 0},
		{1000, 0, 0.9, 0},
		{100_000, 300, 0.9, gib},        // 57 MB of need → 1 GB floor
		{1 << 20, 300, 0.9, gib},        // 598 MB → 1 GB
		{1 << 20, 1800, 0.9, 4 * gib},   // 3.34 GB → 4 GB
		{1 << 20, 1800, 0, 4 * gib},     // unknown cct assumes 0.9
		{10 << 20, 1800, 0.5, 27 * gib}, // 26.4 GB → 27 GB
	}
	for _, c := range cases {
		if got := RecommendedMaxWALSize(c.rate, c.timeout, c.cct); got != c.want {
			t.Errorf("RecommendedMaxWALSize(%v, %d, %v) = %d, want %d", c.rate, c.timeout, c.cct, got, c.want)
		}
	}
}

func TestComputeMaintRates(t *testing.T) {
	prev := healthyInfo()
	cur := healthyInfo()
	cur.SampledAt = prev.SampledAt.Add(30 * time.Second)
	cur.IO.Reads += 600
	cur.IO.Evictions += 30
	cur.WAL.CurrentLSNBytes += 60 * mib
	cur.TempBytes += 3 * mib
	cur.Checkpointer.Requested++

	r := ComputeMaintRates(prev, cur)
	if !r.OK || r.Window != 30*time.Second {
		t.Fatalf("rates = %+v, want OK over 30s", r)
	}
	if r.ReadsPerMin != 1200 || r.EvictionsPerMin != 60 || r.WALBytesPerMin != float64(120*mib) ||
		r.TempBytesPerMin != float64(6*mib) || r.CheckpointsPerMin != 2 || r.WritesPerMin != 0 {
		t.Errorf("rates = %+v", r)
	}

	if got := ComputeMaintRates(nil, cur); got.OK {
		t.Error("nil prev must not be OK")
	}
	same := healthyInfo()
	if got := ComputeMaintRates(same, same); got.OK {
		t.Error("zero window must not be OK")
	}
	reset := healthyInfo()
	reset.SampledAt = cur.SampledAt
	reset.IO.Reads = 5 // counters went backwards: stats reset in between
	if got := ComputeMaintRates(prev, reset); got.OK || got.WALBytesPerMin != 0 {
		t.Errorf("backwards counter must invalidate the whole window, got %+v", got)
	}
}

func TestSinceResetHelpers(t *testing.T) {
	info := healthyInfo()
	// 10 days / 490 checkpoints ≈ 1763 s.
	if iv, ok := info.AvgCheckpointInterval(); !ok || iv.Round(time.Second) != 1763*time.Second {
		t.Errorf("AvgCheckpointInterval = %v %v, want ~29m23s", iv, ok)
	}
	if rate, ok := info.WALBytesPerSecSinceReset(); !ok || int64(rate) != 100*gib/(10*86400) {
		t.Errorf("WALBytesPerSecSinceReset = %v %v", rate, ok)
	}
	if ms, ok := info.AvgSyncPerCheckpointMs(); !ok || ms < 97 || ms > 99 {
		t.Errorf("AvgSyncPerCheckpointMs = %v %v", ms, ok)
	}
	if v, ok := info.EffectiveAutovacWorkMem(); !ok || v != gib {
		t.Errorf("EffectiveAutovacWorkMem = %d %v, want maintenance_work_mem", v, ok)
	}

	// Never reset falls back to postmaster start; no start → unknown.
	info.Checkpointer.StatsReset = time.Time{}
	if _, ok := info.AvgCheckpointInterval(); !ok {
		t.Error("never-reset counters should use StartTime as the window")
	}
	info.StartTime = time.Time{}
	if _, ok := info.AvgCheckpointInterval(); ok {
		t.Error("no window at all must report !ok")
	}
	info.Checkpointer.Timed, info.Checkpointer.Requested = 0, 0
	if _, ok := info.AvgSyncPerCheckpointMs(); ok {
		t.Error("zero checkpoints must report !ok")
	}
}

func TestGucBytes(t *testing.T) {
	for n, want := range map[int64]string{12 * gib: "12GB", 512 * mib: "512MB", 64 << 10: "64kB", 3*gib + mib: "3073MB", 100: "1kB"} {
		if got := gucBytes(n); got != want {
			t.Errorf("gucBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
