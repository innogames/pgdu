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
		// A real cluster always has these; the horizon rules need
		// FreezeMaxAge as their denominator. The ages themselves stay 0 so
		// nothing grades until a case sets one.
		FreezeMaxAge: 200_000_000, FailsafeAge: 1_600_000_000,
		MxidFailsafeAge: 1_600_000_000,
	}
}

func TestMaintAdviceHealthyIsSilent(t *testing.T) {
	if got := MaintAdvice(healthyInfo(), nil); len(got) != 0 {
		t.Errorf("healthy snapshot produced advice: %+v", got)
	}
	if MaintAdvice(nil, nil) != nil {
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
		{"qualstats full", func(i *MaintenanceInfo) { i.Qualstats = ExtCapacity{Installed: true, Used: 950, Max: 1000} },
			"pg_qualstats.max", AdviceWarn, "2000", "restart required"},
		{"log_lock_waits off", func(i *MaintenanceInfo) { i.Settings["log_lock_waits"] = "off" },
			"log_lock_waits", AdviceInfo, "on", "log_lock_waits = 'on'"},
		{"log_temp_files off with spills", func(i *MaintenanceInfo) { i.Settings["log_temp_files"] = "-1"; i.TempBytes = gib },
			"log_temp_files", AdviceInfo, "10MB", "log_temp_files = '10MB'"},
		{"log_temp_files off without spills is fine", func(i *MaintenanceInfo) { i.Settings["log_temp_files"] = "-1" },
			"", 0, "", ""},
		{"FPI share offers wal_compression", func(i *MaintenanceInfo) {
			i.WAL.FPI = 900_000
			i.Checkpointer.Requested = 12_000
			i.Settings["wal_compression"] = "off"
		}, "wal_fpi", AdviceInfo, "on", "wal_compression = 'on'"},

		// safety
		{"autovacuum off", func(i *MaintenanceInfo) { i.Settings["autovacuum"] = "off" },
			"autovacuum", AdviceCrit, "on", "autovacuum = 'on'"},
		{"track_counts off", func(i *MaintenanceInfo) { i.Settings["track_counts"] = "off" },
			"track_counts", AdviceCrit, "on", "track_counts = 'on'"},
		{"fsync off", func(i *MaintenanceInfo) { i.Settings["fsync"] = "off" },
			"fsync", AdviceCrit, "on", "fsync = 'on'"},
		{"full_page_writes off", func(i *MaintenanceInfo) { i.Settings["full_page_writes"] = "off" },
			"full_page_writes", AdviceCrit, "on", "full_page_writes = 'on'"},
		{"data_checksums off", func(i *MaintenanceInfo) { i.Settings["data_checksums"] = "off" },
			"data_checksums", AdviceInfo, "", ""},

		// wraparound. Reaching autovacuum_freeze_max_age *is* the trigger for
		// the routine anti-wraparound autovacuum, so the whole 0–100 % band is
		// healthy: it must never grade above Info however close it gets.
		{"xid age at 99% of freeze_max_age is routine", func(i *MaintenanceInfo) {
			i.XidAge, i.FreezeMaxAge, i.XidAgeDB = 198_000_000, 200_000_000, "shop"
		}, "wraparound", AdviceInfo, "", ""},
		{"xid age just past freeze_max_age is still routine", func(i *MaintenanceInfo) {
			i.XidAge, i.FreezeMaxAge = 240_000_000, 200_000_000
		}, "wraparound", AdviceInfo, "", ""},
		{"xid age fine", func(i *MaintenanceInfo) { i.XidAge, i.FreezeMaxAge = 50_000_000, 200_000_000 },
			"", 0, "", ""},
		{"xid age 2.5x freeze_max_age warns", func(i *MaintenanceInfo) {
			i.XidAge, i.FreezeMaxAge, i.XidAgeDB = 500_000_000, 200_000_000, "shop"
		}, "wraparound", AdviceWarn, "", ""},
		{"xid age past half the failsafe is critical", func(i *MaintenanceInfo) {
			i.XidAge, i.FreezeMaxAge, i.FailsafeAge = 1_200_000_000, 200_000_000, 1_600_000_000
		}, "wraparound", AdviceCrit, "", ""},
		{"failsafe absent falls back to a fixed age", func(i *MaintenanceInfo) {
			i.XidAge, i.FreezeMaxAge, i.FailsafeAge = 1_100_000_000, 200_000_000, 0
		}, "wraparound", AdviceCrit, "", ""},
		{"mxid age at 99% is routine", func(i *MaintenanceInfo) { i.MxidAge, i.MxidFreezeMaxAge = 396_000_000, 400_000_000 },
			"mxid_wraparound", AdviceInfo, "", ""},
		// Multixacts grade against their own failsafe; with the limit tightened
		// below the 400 M default the warn band opens up as it does for XIDs.
		{"mxid age 3x a tightened limit warns", func(i *MaintenanceInfo) {
			i.MxidAge, i.MxidFreezeMaxAge = 600_000_000, 200_000_000
		}, "mxid_wraparound", AdviceWarn, "", ""},
		{"mxid age past half its failsafe is critical", func(i *MaintenanceInfo) {
			i.MxidAge, i.MxidFreezeMaxAge = 1_000_000_000, 400_000_000
		}, "mxid_wraparound", AdviceCrit, "", ""},

		// xmin horizon: the leading indicator. Graded against
		// autovacuum_freeze_max_age because that is the budget it eats into.
		{"horizon below a quarter of freeze_max_age is fine", func(i *MaintenanceInfo) {
			i.Horizon = HorizonStat{Age: 30_000_000, Kind: horizonKindIdleXact, Holder: "pid 4711"}
		}, "", 0, "", ""},
		{"horizon past a quarter warns", func(i *MaintenanceInfo) {
			i.Horizon = HorizonStat{Age: 60_000_000, Kind: horizonKindIdleXact, Holder: "pid 4711 in shop, idle in transaction for 42m"}
		}, "xmin_horizon", AdviceWarn, "", ""},
		{"horizon past half is critical", func(i *MaintenanceInfo) {
			i.Horizon = HorizonStat{Age: 120_000_000, Kind: horizonKindSlot, Holder: "slot standby_dc2 (physical, inactive)"}
		}, "xmin_horizon", AdviceCrit, "", ""},
		{"unreadable horizon is not an all-clear", func(i *MaintenanceInfo) {
			i.Horizon = HorizonStat{Restricted: true}
		}, "xmin_horizon", AdviceWarn, "", ""},

		// operational
		{"connections warn", func(i *MaintenanceInfo) { i.ConnByState = map[string]int{"idle": 70, "active": 12} },
			"max_connections", AdviceWarn, "", ""},
		{"connections crit", func(i *MaintenanceInfo) { i.ConnByState = map[string]int{"idle": 96} },
			"max_connections", AdviceCrit, "", ""},
		{"lock waits", func(i *MaintenanceInfo) { i.LockWaits = 2; i.Blocked = []BlockedStat{{PID: 1, WaitSec: 4}} },
			"lock_waits", AdviceWarn, "", ""},
		{"lock waits long", func(i *MaintenanceInfo) { i.LockWaits = 1; i.Blocked = []BlockedStat{{PID: 1, WaitSec: 45}} },
			"lock_waits", AdviceCrit, "", ""},
		{"long xact warn", func(i *MaintenanceInfo) { i.LongestXactSec = 2000 },
			"long_xact", AdviceWarn, "", ""},
		{"long xact crit", func(i *MaintenanceInfo) { i.LongestXactSec = 4 * 3600 },
			"long_xact", AdviceCrit, "", ""},
		{"prepared xact warn", func(i *MaintenanceInfo) { i.PreparedXacts = 1; i.OldestPrepSec = 10 },
			"prepared_xacts", AdviceWarn, "", ""},
		{"prepared xact crit", func(i *MaintenanceInfo) { i.PreparedXacts = 2; i.OldestPrepSec = 900 },
			"prepared_xacts", AdviceCrit, "", ""},
		{"archiver failed historically", func(i *MaintenanceInfo) {
			i.ArchiveFailed = 3
			i.ArchiveLastFailedTime = i.SampledAt.Add(-2 * time.Hour)
			i.ArchiveLastTime = i.SampledAt.Add(-time.Minute)
		}, "wal_archiver", AdviceWarn, "", ""},
		{"archiver stuck now", func(i *MaintenanceInfo) {
			i.ArchiveFailed = 3
			i.ArchiveLastFailed = "0000000100000001000000A0"
			i.ArchiveLastFailedTime = i.SampledAt.Add(-time.Minute)
			i.ArchiveLastTime = i.SampledAt.Add(-2 * time.Hour)
		}, "wal_archiver", AdviceCrit, "", ""},
		{"archive_mode without command", func(i *MaintenanceInfo) {
			i.Settings["archive_mode"], i.Settings["archive_command"], i.Settings["archive_library"] = "on", "", ""
		}, "archive_mode", AdviceCrit, "", ""},
		{"archive_mode with hidden command is fine", func(i *MaintenanceInfo) { i.Settings["archive_mode"] = "on" },
			"", 0, "", ""},
		{"pending restart", func(i *MaintenanceInfo) { i.PendingRestart = 1; i.PendingRestartSettings = []string{"shared_buffers"} },
			"pending_restart", AdviceWarn, "", ""},
		{"pending reload", func(i *MaintenanceInfo) { i.PendingReload = 1; i.PendingReloadSettings = []string{"work_mem"} },
			"pending_reload", AdviceInfo, "", "SELECT pg_reload_conf();"},

		// counters
		{"cache hit warn", func(i *MaintenanceInfo) { i.CacheHitRatio, i.CacheBlocks = 85, 10_000_000 },
			"cache_hit", AdviceWarn, "", ""},
		{"cache hit notice", func(i *MaintenanceInfo) { i.CacheHitRatio, i.CacheBlocks = 93, 10_000_000 },
			"cache_hit", AdviceInfo, "", ""},
		{"cache hit without traffic is fine", func(i *MaintenanceInfo) { i.CacheHitRatio, i.CacheBlocks = 0, 500 },
			"", 0, "", ""},
		{"slru pressure sizes the buffers", func(i *MaintenanceInfo) {
			i.SLRU = []SLRUStat{{Name: "subtransaction", Hits: 1000, Reads: 5000}, {Name: "other", Hits: 10, Reads: 500}}
			i.Tuning.SLRUBuffers = map[string]int64{"subtransaction_buffers": 0}
		}, "slru", AdviceWarn, "16MB", "subtransaction_buffers = '16MB'"}, // auto = 1024 blocks (8 GB / 512, capped) → 8 MB, doubled
		{"slru explicit size doubles", func(i *MaintenanceInfo) {
			i.SLRU = []SLRUStat{{Name: "multixact_member", Hits: 1000, Reads: 5000}}
			i.Tuning.SLRUBuffers = map[string]int64{"multixact_member_buffers": 32}
		}, "slru", AdviceWarn, "512kB", "multixact_member_buffers = '512kB'"},
		{"slru quiet cache is fine", func(i *MaintenanceInfo) { i.SLRU = []SLRUStat{{Name: "notify", Hits: 1, Reads: 100}} },
			"", 0, "", ""},
		{"deadlocks warn", func(i *MaintenanceInfo) { i.Deadlocks, i.DeadlocksPerDay = 30, 3 },
			"deadlocks", AdviceWarn, "", ""},
		{"deadlocks crit", func(i *MaintenanceInfo) { i.Deadlocks, i.DeadlocksPerDay = 300, 30 },
			"deadlocks", AdviceCrit, "", ""},
		{"a deadlock a month is fine", func(i *MaintenanceInfo) { i.Deadlocks, i.DeadlocksPerDay = 1, 0.03 },
			"", 0, "", ""},
		{"temp spill", func(i *MaintenanceInfo) { i.TempBytes, i.TempBytesPerDay = 100*gib, float64(20*gib) },
			"temp_files", AdviceWarn, "", ""},
		{"rollback ratio", func(i *MaintenanceInfo) { i.XactCommit, i.XactRollback = 6000, 4000 },
			"rollback_ratio", AdviceWarn, "", ""},
		{"rollback ratio below volume", func(i *MaintenanceInfo) { i.XactCommit, i.XactRollback = 60, 40 },
			"", 0, "", ""},

		// replication
		{"replica lagging", func(i *MaintenanceInfo) {
			i.Replicas = []ReplicaStat{{AppName: "db2", State: "streaming", ReplayLag: 90 * time.Second}}
		}, "replication_lag", AdviceWarn, "", ""},
		{"replica far behind", func(i *MaintenanceInfo) {
			i.Replicas = []ReplicaStat{{AppName: "db2", State: "catchup", ByteLag: 2 * gib}}
		}, "replication_lag", AdviceCrit, "", ""},
		{"replica healthy", func(i *MaintenanceInfo) {
			i.Replicas = []ReplicaStat{{AppName: "db2", State: "streaming", ReplayLag: time.Second}}
		}, "", 0, "", ""},
		{"sync standby missing", func(i *MaintenanceInfo) {
			i.Settings["synchronous_standby_names"] = "db2"
			i.Replicas = []ReplicaStat{{AppName: "db2", State: "streaming", SyncState: "async"}}
		}, "synchronous_standby_names", AdviceCrit, "", ""},
		{"sync standby present", func(i *MaintenanceInfo) {
			i.Settings["synchronous_standby_names"] = "db2"
			i.Replicas = []ReplicaStat{{AppName: "db2", State: "streaming", SyncState: "sync"}}
		}, "", 0, "", ""},
		{"standby without receiver", func(i *MaintenanceInfo) { i.InRecovery = true },
			"wal_receiver", AdviceWarn, "", ""},
		{"standby receiver stale", func(i *MaintenanceInfo) {
			i.InRecovery = true
			i.WalReceiver = &WalReceiverStat{Status: "streaming", LastMsgAge: 10 * time.Minute}
		}, "wal_receiver", AdviceCrit, "", ""},
		{"standby receiver fine", func(i *MaintenanceInfo) {
			i.InRecovery = true
			i.WalReceiver = &WalReceiverStat{Status: "streaming", LastMsgAge: 5 * time.Second}
		}, "", 0, "", ""},
		{"standby conflicts want feedback", func(i *MaintenanceInfo) {
			i.InRecovery = true
			i.WalReceiver = &WalReceiverStat{Status: "streaming"}
			i.Conflicts = 12
			i.Settings["hot_standby_feedback"] = "off"
		}, "hot_standby_feedback", AdviceWarn, "on", "hot_standby_feedback = 'on'"},
		{"slot inactive", func(i *MaintenanceInfo) {
			i.ReplSlots = []ReplSlotStat{{Name: "s1", Active: false, WALStatus: "reserved", RetainedBytes: mib, SafeWALBytes: -1}}
		}, "replication_slots", AdviceWarn, "", "pg_drop_replication_slot('s1')"},
		{"slot stale", func(i *MaintenanceInfo) {
			i.ReplSlots = []ReplSlotStat{{Name: "s1", Active: false, WALStatus: "extended", RetainedBytes: gib, SafeWALBytes: -1, InactiveSecs: 7200}}
		}, "replication_slots", AdviceCrit, "", "pg_drop_replication_slot('s1')"},
		{"slot lost", func(i *MaintenanceInfo) {
			i.ReplSlots = []ReplSlotStat{{Name: "s1", Active: true, WALStatus: "lost", RetainedBytes: gib}}
		}, "replication_slots", AdviceCrit, "", ""},
		{"slot near its budget", func(i *MaintenanceInfo) {
			i.ReplSlots = []ReplSlotStat{{Name: "s1", Active: true, WALStatus: "extended", RetainedBytes: 3 * gib, SafeWALBytes: gib}}
		}, "replication_slots", AdviceCrit, "", ""},
		{"slot fine", func(i *MaintenanceInfo) {
			i.ReplSlots = []ReplSlotStat{{Name: "s1", Active: true, WALStatus: "reserved", RetainedBytes: mib, SafeWALBytes: 4 * gib}}
		}, "", 0, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info := healthyInfo()
			c.mutate(info)
			got := MaintAdvice(info, nil)
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
	got := MaintAdvice(info, nil)
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

func TestMaintAdviceTargets(t *testing.T) {
	info := healthyInfo()
	info.Settings["track_io_timing"] = "off"
	info.LockWaits = 1
	info.LongestXactSec = 2000
	// Well past a forced freeze cycle, so the finding is actionable and carries
	// its drill-down; 99 % of freeze_max_age would be routine and Info-only.
	info.XidAge, info.FreezeMaxAge, info.XidAgeDB = 500_000_000, 200_000_000, "shop"
	info.Horizon = HorizonStat{Age: 60_000_000, Kind: horizonKindIdleXact, Holder: "pid 4711 in shop"}
	info.Host.SwapFree = 6 * gib
	got := MaintAdvice(info, nil)
	want := map[string]struct {
		target AdviceTarget
		diag   string
		db     string
	}{
		"track_io_timing": {AdviceTargetSettings, "", ""},
		"lock_waits":      {AdviceTargetLockTree, "", ""},
		"long_xact":       {AdviceTargetActivity, "", ""},
		"wraparound":      {AdviceTargetDiagnostic, "wraparound_tables", "shop"},
		"xmin_horizon":    {AdviceTargetDiagnostic, "idle_in_xact_holders", ""},
		"swap":            {AdviceTargetNone, "", ""},
	}
	for key, w := range want {
		a := got.Find(key)
		if a == nil {
			t.Fatalf("expected advice %q, got %+v", key, got)
		}
		if a.Target != w.target || a.DiagKey != w.diag || a.DB != w.db {
			t.Errorf("%s: target = %v/%q/%q, want %v/%q/%q", key, a.Target, a.DiagKey, a.DB, w.target, w.diag, w.db)
		}
		if a.DiagKey != "" {
			if _, ok := DiagnosticByKey(a.DiagKey); !ok {
				t.Errorf("%s: diagnostic %q not registered", key, a.DiagKey)
			}
		}
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
	// db1 reconnected (new pid) and still pairs by name; db3 is new and has no rate.
	prev.Replicas = []ReplicaStat{
		{PID: 10, AppName: "db1", ClientAddr: "10.0.0.1", ReplayLSNBytes: 500 * gib},
		{PID: 11, AppName: "db2", ClientAddr: "10.0.0.2", ReplayLSNBytes: 500 * gib},
	}
	cur.Replicas = []ReplicaStat{
		{PID: 20, AppName: "db1", ClientAddr: "10.0.0.1", ReplayLSNBytes: 500*gib + 30*mib},
		{PID: 12, AppName: "db3", ClientAddr: "10.0.0.3", ReplayLSNBytes: 500 * gib},
	}

	r := ComputeMaintRates(prev, cur)
	if !r.OK || r.Window != 30*time.Second {
		t.Fatalf("rates = %+v, want OK over 30s", r)
	}
	if r.ReadsPerMin != 1200 || r.EvictionsPerMin != 60 || r.WALBytesPerMin != float64(120*mib) ||
		r.TempBytesPerMin != float64(6*mib) || r.CheckpointsPerMin != 2 || r.WritesPerMin != 0 {
		t.Errorf("rates = %+v", r)
	}
	if got := r.ReplicaBytesPerMin; len(got) != 1 || got["db1@10.0.0.1"] != float64(60*mib) {
		t.Errorf("replica rates = %v, want only db1 at 60 MiB/min", got)
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

// The regression this redesign exists for: a cluster that burns through
// autovacuum_freeze_max_age every few days sat permanently at a red
// "wraparound" recommendation, which sent people chasing a phantom pinned
// horizon. Everything below 2× the limit must stay out of the recommendations
// panel, and the note it does carry must say the state is routine.
func TestWraparoundRoutineFreezingIsNotActionable(t *testing.T) {
	for _, age := range []int64{100_000_000, 198_000_000, 200_000_000, 260_000_000, 390_000_000} {
		info := healthyInfo()
		info.XidAge, info.XidAgeDB = age, "powa"
		got := MaintAdvice(info, nil)
		if act := got.Actionable(); len(act) != 0 {
			t.Errorf("age %d: actionable advice %+v, want none", age, act)
		}
		a := got.Find("wraparound")
		if a == nil {
			// Only the low end is allowed to stay entirely silent.
			if age >= int64(freezeRoutineNoticeFrac*float64(info.FreezeMaxAge)) {
				t.Errorf("age %d: expected an explanatory note, got none", age)
			}
			continue
		}
		if a.Level != AdviceInfo {
			t.Errorf("age %d: level = %d, want Info", age, a.Level)
		}
		if !strings.Contains(a.Reason, "routine") {
			t.Errorf("age %d: reason %q does not call the state routine", age, a.Reason)
		}
	}
}

// Past two freeze cycles the finding must be actionable and must point at both
// causes an operator can actually do something about.
func TestWraparoundBehindNamesTheCauses(t *testing.T) {
	info := healthyInfo()
	info.XidAge, info.XidAgeDB = 500_000_000, "powa"
	a := MaintAdvice(info, nil).Find("wraparound")
	if a == nil || a.Level != AdviceWarn {
		t.Fatalf("advice = %+v, want a warning", a)
	}
	for _, want := range []string{"2.5×", "autovacuum_freeze_max_age", "relfrozenxid", "xmin_horizon", "autovacuum", "in powa"} {
		if !strings.Contains(a.Reason, want) {
			t.Errorf("reason %q missing %q", a.Reason, want)
		}
	}
}

// The critical tier is about the hard limit, not the routine trigger, so it has
// to quantify the remaining room.
func TestWraparoundCriticalQuantifiesTheHardLimit(t *testing.T) {
	info := healthyInfo()
	info.XidAge, info.XidAgeDB = 1_200_000_000, "powa"
	a := MaintAdvice(info, nil).Find("wraparound")
	if a == nil || a.Level != AdviceCrit {
		t.Fatalf("advice = %+v, want critical", a)
	}
	// 1.2e9 / 2^31 ≈ 55.9 %.
	for _, want := range []string{"56%", "2^31", "failsafe"} {
		if !strings.Contains(a.Reason, want) {
			t.Errorf("reason %q missing %q", a.Reason, want)
		}
	}
}

// A horizon finding is only useful if it says which holder to go and deal with,
// and leads to the view listing that kind of holder.
func TestHorizonNamesHolderAndTarget(t *testing.T) {
	cases := []struct {
		kind   string
		target AdviceTarget
		diag   string
	}{
		{horizonKindIdleXact, AdviceTargetDiagnostic, "idle_in_xact_holders"},
		{horizonKindSlot, AdviceTargetDiagnostic, "replication_slots"},
		{horizonKindBackend, AdviceTargetActivity, ""},
		{horizonKindPrepared, AdviceTargetNone, ""},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			info := healthyInfo()
			info.Horizon = HorizonStat{Age: 60_000_000, Kind: c.kind, Holder: "the holder"}
			a := MaintAdvice(info, nil).Find("xmin_horizon")
			if a == nil {
				t.Fatal("expected an xmin_horizon finding")
			}
			if !strings.Contains(a.Reason, "the holder") {
				t.Errorf("reason %q does not name the holder", a.Reason)
			}
			if a.Target != c.target || a.DiagKey != c.diag {
				t.Errorf("target = %v/%q, want %v/%q", a.Target, a.DiagKey, c.target, c.diag)
			}
			if c.diag != "" {
				if _, ok := DiagnosticByKey(c.diag); !ok {
					t.Errorf("diagnostic %q not registered", c.diag)
				}
			}
		})
	}
}

// A filtered pg_stat_activity must never read as an all-clear: that false green
// is what the old check trained operators to distrust.
func TestHorizonRestrictedIsReported(t *testing.T) {
	info := healthyInfo()
	info.Horizon = HorizonStat{Restricted: true}
	a := MaintAdvice(info, nil).Find("xmin_horizon")
	if a == nil || a.Level != AdviceWarn {
		t.Fatalf("advice = %+v, want a warning", a)
	}
	if !strings.Contains(a.Reason, "pg_read_all_stats") {
		t.Errorf("reason %q does not say what is missing", a.Reason)
	}

	// A visible horizon that is only a lower bound says so alongside the age.
	info.Horizon = HorizonStat{Age: 60_000_000, Kind: horizonKindIdleXact, Holder: "pid 1", Restricted: true}
	a = MaintAdvice(info, nil).Find("xmin_horizon")
	if a == nil || !strings.Contains(a.Reason, "may be hidden") {
		t.Fatalf("reason = %q, want the lower-bound caveat", a.Reason)
	}
}
