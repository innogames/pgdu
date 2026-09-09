package tui

import (
	"strings"
	"testing"
	"time"

	"pgdu/internal/pg"
	"pgdu/internal/sysmem"
)

// overviewInfo is a plausible healthy snapshot for the render tests; each test
// mutates what it needs.
func overviewInfo() *pg.MaintenanceInfo {
	now := time.Now()
	return &pg.MaintenanceInfo{
		SampledAt:   now,
		Version:     "PostgreSQL 17.4",
		StartTime:   now.Add(-3 * 24 * time.Hour),
		MaxConns:    100,
		ConnByState: map[string]int{"active": 2, "idle": 5},
		Settings: map[string]string{
			"shared_buffers": "8GB", "work_mem": "16MB", "maintenance_work_mem": "1GB",
			"autovacuum_work_mem": "-1", "effective_cache_size": "24GB", "max_connections": "100",
			"huge_pages": "try", "autovacuum": "on", "autovacuum_max_workers": "3",
			"wal_level": "replica", "max_wal_size": "4GB", "min_wal_size": "80MB", "wal_buffers": "16MB",
			"checkpoint_timeout": "30min", "checkpoint_completion_target": "0.9",
			"track_io_timing": "on", "log_checkpoints": "on",
			"pg_stat_statements.track": "top", "pg_stat_statements.max": "5000",
			"pg_qualstats.max": "1000", "pg_qualstats.enabled": "on",
		},
		SettingBytes: map[string]int64{
			"shared_buffers": 8 << 30, "work_mem": 16 << 20, "maintenance_work_mem": 1 << 30,
			"autovacuum_work_mem": -1, "effective_cache_size": 24 << 30, "max_wal_size": 4 << 30, "wal_buffers": 16 << 20,
		},
		Tuning: pg.MaintTuning{CheckpointTimeoutSecs: 1800, CheckpointCompletion: 0.9, AutovacMaxWorkers: 3, ShmemHugePages: 4200},
		Host: sysmem.Info{Total: 32 << 30, Available: 20 << 30, Free: 2 << 30, Cached: 16 << 30,
			SwapTotal: 8 << 30, SwapFree: 8 << 30, HugePagesTotal: 4200, HugePagesFree: 10, HugePageSize: 2 << 20},
		Statements: pg.ExtCapacity{Installed: true, Used: 100, Max: 5000},
		Checkpointer: pg.CheckpointerStat{HasData: true, Timed: 140, Requested: 4, WriteTimeMs: 140 * 20_000,
			SyncTimeMs: 140 * 50, BuffersWritten: 140 * 5000, StatsReset: now.Add(-3 * 24 * time.Hour)},
		WAL: pg.WALStat{HasData: true, Records: 1_000_000, FPI: 100_000, Bytes: 30 << 30,
			StatsReset: now.Add(-3 * 24 * time.Hour), CurrentLSNBytes: 500 << 30},
		WALDir:  pg.WALDirStat{HasData: true, Bytes: 2 << 30, Files: 128},
		IO:      pg.IOStat{HasData: true, Reads: 100_000, Writes: 20_000, Hits: 5_000_000, Evictions: 90_000, Fsyncs: 300},
		IOSplit: pg.IOSplitStat{HasData: true, RelationReads: 100_000, ClientReads: 90_000, ClientReadTimeMs: 9_000, BulkReads: 1_000, CheckpointerWrites: 15_000, BgwriterWrites: 4_000, ClientWrites: 1_000},
		BufCache: pg.BufCacheStat{Installed: true, HasData: true, Used: 1_000_000, Unused: 48_576, Dirty: 20_000, UsageAvg: 2.4,
			UsageCounts: []pg.BufferUsageCount{{Count: 0, Buffers: 300_000}, {Count: 1, Buffers: 200_000}, {Count: 2, Buffers: 200_000},
				{Count: 3, Buffers: 100_000}, {Count: 4, Buffers: 100_000}, {Count: 5, Buffers: 100_000}}},
		Autovac: pg.AutovacStat{WorkersBusy: 1, OverThreshold: 2, OverTop: []string{"public.orders", "public.events"}},
		XidAge:  50_000_000, XidAgeDB: "shop", FreezeMaxAge: 200_000_000,
	}
}

// ovRow is a rendered overview row: the label padded to the label column.
func ovRow(label, value string) string {
	return label + strings.Repeat(" ", overviewLabelW-len(label)) + value
}

func overviewScreen(info *pg.MaintenanceInfo) *screen {
	s := &screen{level: levelMaintenance, tool: toolMaintenance, db: "postgres", loaded: true}
	s.maintenance.info = info
	return s
}

func TestRenderMaintenanceHealthy(t *testing.T) {
	m := &Model{width: 200}
	out := stripANSI(m.renderMaintenance(overviewScreen(overviewInfo()), 200))
	for _, want := range []string{
		" recommendations ", "none — nothing graded worse than informational",
		" buffer cache ", "occupancy", "1.0M / 1.0M buffers", "temperature", "read latency", "0.10 ms",
		"misses served from the OS page cache", "bulkread share",
		" observability ", " pg_stat_statements ", "track", "track_io_timing",
		"huge_pages", "host pool 4200", "host memory", "32.00 GB total", "swap", "25% of host RAM",
		"× 100 conns = 1.56 GB", "→ 1.00 GB (maintenance_work_mem)",
		"workers", "1 / 3 busy", "over threshold", "2 tables", "public.orders",
		"in shop", "wal rate", "since reset", "pg_wal on disk", "2.00 GB", "128 segments",
		"dirty-page writes", "checkpointer 75.0%", "avg interval", "per checkpoint",
		"counters since reset", "auto-refresh off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered overview lacks %q\n%s", want, out)
		}
	}
	// The healthy snapshot has no rate window yet (the since-reset WAL rate is
	// the only /min figure) and no qualstats settings block.
	for _, absent := range []string{"(last ", "rates over", "sample_rate", "n/a"} {
		if strings.Contains(out, absent) {
			t.Errorf("rendered overview unexpectedly contains %q", absent)
		}
	}
}

func TestRenderMaintenanceDegrades(t *testing.T) {
	info := overviewInfo()
	info.BufCache = pg.BufCacheStat{}        // extension missing
	info.WALDir = pg.WALDirStat{}            // no pg_monitor
	info.Host = sysmem.Info{}                // remote host
	info.Settings["track_io_timing"] = "off" // no read timings
	info.IOSplit.ClientReadTimeMs = 0
	m := &Model{width: 200}
	out := stripANSI(m.renderMaintenance(overviewScreen(info), 200))
	for _, want := range []string{
		ovRow("pg_buffercache", "not installed — needed for cache analysis"),
		ovRow("pg_wal on disk", "n/a (needs pg_monitor)"),
		"n/a — track_io_timing is off",
		"~ track_io_timing off → on",
		"ALTER SYSTEM SET track_io_timing = 'on'; SELECT pg_reload_conf();",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("degraded overview lacks %q\n%s", want, out)
		}
	}
	for _, absent := range []string{"host memory", "host pool", "of host RAM", "swap"} {
		if strings.Contains(out, absent) {
			t.Errorf("host rows must hide without host data, found %q", absent)
		}
	}

	info.BufCache = pg.BufCacheStat{Installed: true} // installed but summary unreadable
	out = stripANSI(m.renderMaintenance(overviewScreen(info), 200))
	if !strings.Contains(out, ovRow("occupancy", "n/a (needs pg_monitor)")) {
		t.Errorf("installed-but-unreadable buffercache should say n/a\n%s", out)
	}
}

// The extension settings blocks follow the extension probe, not the GUC's
// presence: a preloaded library without CREATE EXTENSION shows nothing.
func TestRenderMaintenanceExtensionBlocksFollowInstall(t *testing.T) {
	info := overviewInfo()
	info.Statements.Installed = false
	m := &Model{width: 200}
	out := stripANSI(m.renderMaintenance(overviewScreen(info), 200))
	if strings.Contains(out, " pg_stat_statements \n") || strings.Contains(out, "track_planning") {
		t.Errorf("pg_stat_statements settings block rendered while not installed\n%s", out)
	}
	info.Qualstats.Installed = true
	out = stripANSI(m.renderMaintenance(overviewScreen(info), 200))
	if !strings.Contains(out, " pg_qualstats ") || !strings.Contains(out, "sample_rate") {
		t.Errorf("pg_qualstats block missing when installed\n%s", out)
	}
}

func TestRenderMaintenanceRatesAndAdvice(t *testing.T) {
	prev := overviewInfo()
	cur := overviewInfo()
	cur.SampledAt = prev.SampledAt.Add(30 * time.Second)
	cur.IO.Reads += 3000
	cur.WAL.CurrentLSNBytes += 60 << 20
	cur.Checkpointer.Requested = 200 // 200/340 requested → crit
	cur.Host.HugePagesTotal = 0      // huge_pages=try silently off
	s := overviewScreen(cur)
	s.maintenance.prev = prev
	m := &Model{width: 200, maintRefresh: 10 * time.Second}
	out := stripANSI(m.renderMaintenance(s, 200))
	for _, want := range []string{
		"rates over the last 30s", "6.0k/min", "(last 30s)", "120.00 MB/min",
		"! huge_pages try → vm.nr_hugepages=4200", "sysctl -w vm.nr_hugepages=4200",
		"! max_wal_size 4GB", "of checkpoints WAL-driven",
		"auto-refresh 10s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overview with rates lacks %q\n%s", want, out)
		}
	}
	// The inline note on the max_wal_size row and the panel line say the same thing.
	if strings.Count(out, "of checkpoints WAL-driven") != 2 {
		t.Errorf("expected the max_wal_size reason inline and in the panel\n%s", out)
	}
}

func TestRenderMaintenanceNarrow(t *testing.T) {
	m := &Model{width: 120}
	out := stripANSI(m.renderMaintenance(overviewScreen(overviewInfo()), 400))
	if strings.Contains(out, "│") {
		t.Error("narrow layout must not paint the column rule")
	}
	for _, want := range []string{" server ", " memory & resources ", " buffer cache ", " recommendations "} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow overview lacks %q", want)
		}
	}
}

func TestRenderColumnsWidth(t *testing.T) {
	out := renderColumns("left line\nsecond", "right", 12, 8)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		plain := stripANSI(line)
		if !strings.HasPrefix(plain[12:], "│ ") {
			t.Errorf("column rule misplaced in %q", plain)
		}
	}
}

func TestCycleMaintRefresh(t *testing.T) {
	m := &Model{}
	seen := make([]time.Duration, 0, 5)
	for range 5 {
		m.cycleMaintRefresh()
		seen = append(seen, m.maintRefresh)
	}
	want := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second, 0, 10 * time.Second}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("cadence cycle = %v, want %v", seen, want)
		}
	}
	if m.maintTick() == nil {
		t.Error("tick must be armed while a cadence is set")
	}
	m.maintRefresh = 0
	if m.maintTick() != nil {
		t.Error("tick must be nil when off")
	}
}
