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
	s.maintenance.refreshAdvice()
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
		" schema health (postgres) ", "loading…",
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
		"~ huge_pages try → vm.nr_hugepages=4200", "sysctl -w vm.nr_hugepages=4200",
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

// replicationInfo adds two streaming replicas holding their slots and one
// abandoned slot to the healthy snapshot.
func replicationInfo() *pg.MaintenanceInfo {
	info := overviewInfo()
	info.Settings["max_slot_wal_keep_size"] = "4GB"
	info.Replicas = []pg.ReplicaStat{
		{PID: 10, AppName: "db1", ClientAddr: "10.0.0.1", State: "streaming", SyncState: "async",
			ReplayLag: 200 * time.Millisecond, ByteLag: 800 << 10, ReplayLSNBytes: 500 << 30},
		{PID: 11, AppName: "db3", ClientAddr: "10.0.0.3", State: "streaming", SyncState: "sync", ReplayLSNBytes: 500 << 30},
	}
	info.ReplSlots = []pg.ReplSlotStat{
		{Name: "slot_db1", SlotType: "physical", Active: true, ActivePID: 10, WALStatus: "reserved", RetainedBytes: 24 << 10, SafeWALBytes: 4 << 30},
		{Name: "slot_db3", SlotType: "physical", Active: true, ActivePID: 11, WALStatus: "reserved", RetainedBytes: 16 << 10, SafeWALBytes: 4 << 30},
		{Name: "slot_old", SlotType: "physical", Active: false, WALStatus: "extended", RetainedBytes: 2 << 30, SafeWALBytes: 500 << 20},
	}
	return info
}

// squashSpaces collapses column padding so table rows can be asserted as text.
func squashSpaces(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(s, "\n") {
		b.WriteString(strings.Join(strings.Fields(line), " ") + "\n")
	}
	return b.String()
}

func TestRenderMaintenanceReplication(t *testing.T) {
	m := &Model{width: 200}
	raw := stripANSI(m.renderMaintenance(overviewScreen(replicationInfo()), 300))
	out := squashSpaces(raw)
	for _, want := range []string{
		"replication & slots\n",
		"max_slot_wal_keep_size 4GB\n",
		"node address state sync lag behind slot type status retained safe\n",
		"db1 10.0.0.1 streaming async <1s 800.00 KB slot_db1 physical active reserved 24.00 KB 4.00 GB\n",
		"db3 10.0.0.3 streaming sync slot_db3 physical active reserved 16.00 KB 4.00 GB\n",
		"slot_old physical inactive extended 2.00 GB 500.00 MB\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replication section lacks %q\n%s", want, raw)
		}
	}
	// Columns line up: the slot name starts at the same offset on every table
	// row (the recommendation and its inline note name the slot too, so only
	// rows carrying the slot type count).
	var slotCol []int
	for line := range strings.SplitSeq(raw, "\n") {
		if i := strings.Index(line, " slot_"); i >= 0 && strings.Contains(line, " physical ") {
			slotCol = append(slotCol, i)
		}
	}
	if len(slotCol) != 3 || slotCol[0] != slotCol[1] || slotCol[1] != slotCol[2] {
		t.Errorf("slot column misaligned: offsets %v\n%s", slotCol, raw)
	}
	if strings.Contains(out, "rate (") {
		t.Errorf("single sample must show no throughput column\n%s", raw)
	}

	// -1 means unlimited; with an inactive slot hoarding WAL that deserves a note.
	info := replicationInfo()
	info.Settings["max_slot_wal_keep_size"] = "-1"
	out = squashSpaces(stripANSI(m.renderMaintenance(overviewScreen(info), 300)))
	if !strings.Contains(out, "max_slot_wal_keep_size unlimited an inactive slot can fill pg_wal\n") {
		t.Errorf("unlimited keep size with a 2 GB inactive slot must warn\n%s", out)
	}
	info.ReplSlots = info.ReplSlots[:2]
	out = squashSpaces(stripANSI(m.renderMaintenance(overviewScreen(info), 300)))
	if !strings.Contains(out, "max_slot_wal_keep_size unlimited\n") {
		t.Errorf("unlimited keep size without a hoarding slot must not warn\n%s", out)
	}
}

// Replica throughput shows both windows once the screen has three samples,
// and only the last one while the first sample is still the previous one.
func TestRenderMaintenanceReplicationRates(t *testing.T) {
	first := replicationInfo()
	prev := replicationInfo()
	cur := replicationInfo()
	prev.SampledAt = first.SampledAt.Add(270 * time.Second)
	cur.SampledAt = first.SampledAt.Add(300 * time.Second)
	prev.Replicas[0].ReplayLSNBytes += 240 << 20
	cur.Replicas[0].ReplayLSNBytes += 300 << 20
	cur.Replicas[1].AppName = "db4" // reconnected under a new name: no rate
	s := overviewScreen(cur)
	s.maintenance.first, s.maintenance.prev = first, prev
	m := &Model{width: 200}
	out := squashSpaces(stripANSI(m.renderMaintenance(s, 300)))
	for _, want := range []string{
		"retained safe rate (last 30s) rate (since open 5m)\n",
		"24.00 KB 4.00 GB 120.00 MB/min 60.00 MB/min\n",
		"db4 10.0.0.3 streaming sync slot_db3 physical active reserved 16.00 KB 4.00 GB\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replication table with rates lacks %q\n%s", want, out)
		}
	}

	s.maintenance.first = prev
	out = squashSpaces(stripANSI(m.renderMaintenance(s, 300)))
	if !strings.Contains(out, "safe rate (last 30s)\n") || strings.Contains(out, "since open") {
		t.Errorf("second sample must show only the last window\n%s", out)
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
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
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
