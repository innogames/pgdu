package pg

import (
	"fmt"
	"strings"
	"time"

	"pgdu/internal/sysmem"
)

// ExtCapacity describes how full a shared-memory stats extension is.
// Used by the Maintenance dashboard to show fill-level bars with urgency
// colouring and to let the user reset the extension from inside the TUI.
type ExtCapacity struct {
	Name       string // "pg_stat_statements" or "pg_qualstats"
	Installed  bool
	Used       int64     // count(*) of currently tracked entries
	Max        int64     // the .max GUC; 0 = unknown / extension not preloaded
	StatsReset time.Time // last reset timestamp; zero = unknown
	ShmemBytes int64     // reserved shared memory (pg_shmem_allocations); 0 = unknown
	TextBytes  int64     // query-text bytes (pg_stat_statements only); 0 = n/a
}

// FillRatio is Used/Max as a fraction in [0,1]. Returns 0 when Max ≤ 0 (GUC
// unknown) so callers can treat it as "no data" and skip the bar.
func (e ExtCapacity) FillRatio() float64 {
	if e.Max <= 0 {
		return 0
	}
	return float64(e.Used) / float64(e.Max)
}

// MaintenanceInfo is the one-shot snapshot gathered for the Maintenance
// dashboard. All fields are best-effort: a missing extension or a failing
// sub-query leaves the corresponding field zero/nil rather than aborting
// the whole load.
type MaintenanceInfo struct {
	Statements ExtCapacity
	Qualstats  ExtCapacity

	// Server identity & uptime
	Version    string    // SELECT version()
	StartTime  time.Time // pg_postmaster_start_time()
	ConfLoad   time.Time // pg_conf_load_time()
	InRecovery bool      // pg_is_in_recovery()
	Database   string    // current_database(): where the per-database figures were read

	// Connection counts
	MaxConns       int
	ConnByState    map[string]int // pg_stat_activity grouped by state
	LongestXactSec float64        // max xact age in seconds (non-idle)

	// Cache health: the hit ratio and the block traffic it is computed over
	// (a ratio over a handful of blocks says nothing).
	CacheHitRatio float64 // sum(blks_hit)/(hit+read) over pg_stat_database, in percent
	CacheBlocks   int64   // sum(blks_hit + blks_read)

	// Transaction & session health (pg_stat_database aggregate, non-template DBs)
	XactCommit   int64
	XactRollback int64
	Deadlocks    int64
	Conflicts    int64
	// Per-day rates of the two counters worth grading, summed over each
	// database's own stats window (floored at one day) in SQL.
	DeadlocksPerDay float64
	TempBytesPerDay float64
	// Session counters below are PG14+; all stay zero on older clusters.
	Sessions      int64
	SessAbandoned int64   // connections dropped due to client disconnect mid-session
	SessFatal     int64   // sessions ended by a fatal server error
	SessKilled    int64   // sessions ended by pg_terminate_backend()
	ActiveTimeMs  float64 // cumulative active query time in ms across all sessions
	IdleTxTimeMs  float64 // cumulative idle-in-transaction time in ms

	// Tuple-level activity aggregated across pg_stat_user_tables for the current
	// database. Ratios (HOT %, index-usage %, dead-tuple %) are derived at render
	// time. All zero when the DB has no user tables / no activity yet.
	TupInserted   int64
	TupUpdated    int64
	TupDeleted    int64
	TupHotUpdated int64
	SeqScans      int64
	IdxScans      int64
	LiveTuples    int64
	DeadTuples    int64
	// TableStatsReset is pg_stat_database.stats_reset for the current database —
	// when the counters above (and the Table overview's) were last zeroed by
	// pg_stat_reset(). Zero when the server has never reset them.
	TableStatsReset time.Time

	// Autovacuum / wraparound
	XidAge       int64  // max(age(datfrozenxid)) over pg_database
	XidAgeDB     string // database holding that oldest datfrozenxid
	FreezeMaxAge int64  // autovacuum_freeze_max_age from settings
	// FailsafeAge is vacuum_failsafe_age: the age at which VACUUM abandons its
	// cost delay and index cleanup to catch up. Unlike FreezeMaxAge it marks
	// real danger rather than routine maintenance, which is why the wraparound
	// advice grades against it. 0 when unknown (PG < 14, where the GUC does not
	// exist) — the advice then falls back to a fixed age.
	FailsafeAge int64

	// Multixact IDs have their own 32-bit counter and freeze horizon.
	MxidAge          int64  // max(mxid_age(datminmxid)) over pg_database
	MxidAgeDB        string // database holding that oldest datminmxid
	MxidFreezeMaxAge int64  // autovacuum_multixact_freeze_max_age from settings
	MxidFailsafeAge  int64  // vacuum_multixact_failsafe_age; 0 when unknown (PG < 14)

	// Horizon is the oldest live xmin and what pins it — the leading indicator
	// for freezing falling behind.
	Horizon HorizonStat

	// Checkpointer, WAL, bgwriter and per-backend-type I/O counters, each with
	// its own HasData gate (privilege or missing view).
	Checkpointer CheckpointerStat
	WAL          WALStat
	Bgwriter     BgwriterStat
	IOSplit      IOSplitStat

	// Buffer-cache occupancy (pg_buffercache_summary + usage histogram).
	BufCache BufCacheStat

	// SLRU caches (pg_stat_slru), busiest first.
	SLRU []SLRUStat

	// Autovacuum saturation for the current database.
	Autovac AutovacStat

	// pg_wal on disk (pg_ls_waldir; needs pg_monitor).
	WALDir WALDirStat

	// Longest idle-in-transaction / longest running query.
	Sess SessionStat

	// SampledAt is when this snapshot was taken (client clock), the basis for
	// the two-sample rates the overview derives between refreshes.
	SampledAt time.Time

	// Host is the local machine's memory as read by the TUI from /proc/meminfo
	// (never by pg): only meaningful when pgdu runs on the database host, zero
	// otherwise and the host-relative rows stay hidden.
	Host sysmem.Info

	// SettingBytes holds the memory GUCs as bytes (pg_size_bytes of the human
	// value in Settings) so the advice rules can do arithmetic; autovacuum_work_mem
	// keeps its -1 sentinel. Missing key = unknown.
	SettingBytes map[string]int64

	// Tuning holds the numeric GUCs the advice rules need in base units.
	Tuning MaintTuning

	// Pending configuration changes (pg_settings)
	PendingRestart         int      // settings requiring a server restart
	PendingRestartSettings []string // names of those settings (up to ~5)
	PendingReload          int      // settings requiring a SIGHUP / pg_reload_conf()
	PendingReloadSettings  []string // names of those settings (up to ~5)

	// Lock waits: count of pg_stat_activity rows waiting on a Lock event
	LockWaits int

	// Blocked chains (pg_blocking_pids; up to 8 rows, longest wait first).
	Blocked []BlockedStat

	// Prepared transactions (2PC). An abandoned prepared xact pins the xmin
	// horizon and delays autovacuum; OldestPrepSec > 0 is always worth action.
	PreparedXacts int
	OldestPrepSec float64

	// Temp-file pressure: cumulative from pg_stat_database (work_mem signal)
	TempFiles int64
	TempBytes int64
	TempByDB  []TempDBStat // per-database breakdown (only DBs with temp_files > 0)

	// I/O statistics (pg_stat_io, all backend types summed).
	IO IOStat

	// WAL archiver status (pg_stat_archiver). ArchiveFailed > 0 is a critical
	// signal: the pg_wal directory fills up silently when archiving stalls.
	ArchiveCount          int64
	ArchiveFailed         int64
	ArchiveLastFailed     string    // WAL file name of the last failure
	ArchiveLastTime       time.Time // time of last successful archive; zero when none yet
	ArchiveLastFailedTime time.Time // time of the last failure; newer than ArchiveLastTime = stuck now

	// WAL in-flight: how much WAL has been generated since the last checkpoint.
	// When WALBytesSinceCheckpoint reaches WALMaxBytes, Postgres triggers a
	// requested checkpoint — exactly what the "N requested" counter counts up.
	WALBytesSinceCheckpoint int64     // pg_current_wal_insert_lsn() - redo_lsn
	WALMaxBytes             int64     // max_wal_size in bytes (for the fill bar)
	WALCheckpointTime       time.Time // when the last checkpoint completed

	// Replication: Replicas is filled on a primary, WalReceiver on a standby.
	Replicas    []ReplicaStat
	ReplSlots   []ReplSlotStat
	WalReceiver *WalReceiverStat

	// Curated GUCs (name → raw setting string from pg_settings)
	Settings map[string]string
}

// MaintTuning holds the numeric GUCs the advice rules do arithmetic on, parsed
// from pg_settings.setting (base units: seconds, milliseconds, pages) rather
// than the human strings in Settings. Zero means unknown.
type MaintTuning struct {
	CheckpointTimeoutSecs int64
	CheckpointCompletion  float64
	AutovacCostDelayMs    float64 // -1 = falls back to VacuumCostDelayMs
	VacuumCostDelayMs     float64
	AutovacCostLimit      int64 // -1 = falls back to VacuumCostLimit
	VacuumCostLimit       int64
	AutovacMaxWorkers     int
	ShmemHugePages        int64 // shared_memory_size_in_huge_pages; -1 when the platform has none
	BgwriterLRUMaxpages   int64
	IdleInTxnTimeoutMs    int64 // idle_in_transaction_session_timeout; 0 = disabled
	// SLRUBuffers holds the PG17+ *_buffers GUCs in blocks, keyed by GUC name;
	// 0 means the server sizes the cache from shared_buffers.
	SLRUBuffers map[string]int64
}

// EffectiveAutovacCostDelayMs resolves the -1 fallback to vacuum_cost_delay.
func (t MaintTuning) EffectiveAutovacCostDelayMs() float64 {
	if t.AutovacCostDelayMs < 0 {
		return t.VacuumCostDelayMs
	}
	return t.AutovacCostDelayMs
}

// EffectiveAutovacCostLimit resolves the -1 fallback to vacuum_cost_limit.
func (t MaintTuning) EffectiveAutovacCostLimit() int64 {
	if t.AutovacCostLimit < 0 {
		return t.VacuumCostLimit
	}
	return t.AutovacCostLimit
}

// SessionStat is the session-hygiene view of pg_stat_activity: the worst
// offender of each kind (an open transaction that went idle, the longest
// running statement) and where connections come from. Zero PIDs mean none.
type SessionStat struct {
	IdleXactPID  int32
	IdleXactApp  string
	IdleXactSecs float64 // how long the oldest idle-in-transaction has been idle

	LongQueryPID  int32
	LongQueryApp  string
	LongQuerySecs float64
	LongQueryText string // first 60 chars
}

// BufCacheStat is the cluster-wide shared_buffers occupancy from
// pg_buffercache_summary() plus the clock-sweep usage histogram. Installed is
// the pg_buffercache extension probe; HasData is whether the summary was
// readable (it needs pg_monitor), so the two degrade independently.
type BufCacheStat struct {
	Installed bool
	HasData   bool
	Used      int64
	Unused    int64
	Dirty     int64
	Pinned    int64
	UsageAvg  float64
	// UsageCounts is the 0..5 usagecount histogram; nil when unreadable.
	UsageCounts []BufferUsageCount
}

// DirtyFrac is Dirty/Used, 0 when the cache is empty.
func (b BufCacheStat) DirtyFrac() float64 {
	if b.Used <= 0 {
		return 0
	}
	return float64(b.Dirty) / float64(b.Used)
}

// UsageFracs returns the share of used buffers sitting at usagecount 0–1
// (cold, evictable) and 4–5 (hot). ok is false without a histogram.
func (b BufCacheStat) UsageFracs() (cold, hot float64, ok bool) {
	var total, c, h int64
	for _, u := range b.UsageCounts {
		total += u.Buffers
		switch {
		case u.Count <= 1:
			c += u.Buffers
		case u.Count >= 4:
			h += u.Buffers
		}
	}
	if total == 0 {
		return 0, 0, false
	}
	return float64(c) / float64(total), float64(h) / float64(total), true
}

// IOSplitStat breaks pg_stat_io down by who did the I/O, restricted to
// object = 'relation' so PG18's WAL rows don't skew the shares. Client reads
// with their timing give the miss latency (read_time stays 0 when
// track_io_timing is off); bulkread reads are the ring-buffer sequential scans;
// the three write counters say who is flushing dirty buffers.
type IOSplitStat struct {
	HasData          bool
	RelationReads    int64
	ClientReads      int64
	ClientReadTimeMs float64
	BulkReads        int64
	// Vacuum* is the context = 'vacuum' share: how much of the relation I/O
	// autovacuum itself is causing.
	VacuumReads  int64
	VacuumWrites int64

	CheckpointerWrites      int64
	CheckpointerWriteTimeMs float64
	BgwriterWrites          int64
	ClientWrites            int64
	ClientWriteTimeMs       float64

	// WAL* is PG18's object = 'wal' traffic (zero on PG17): WAL writes and the
	// fsyncs commits wait for, with their timings.
	WALWrites      int64
	WALWriteTimeMs float64
	WALFsyncs      int64
	WALFsyncTimeMs float64
}

// avgMs is time/count, ok only with timed events.
func avgMs(timeMs float64, n int64) (float64, bool) {
	if n <= 0 || timeMs <= 0 {
		return 0, false
	}
	return timeMs / float64(n), true
}

// ClientReadLatencyMs is the mean client-backend read (cache miss) latency;
// ok is false when there were no timed reads.
func (s IOSplitStat) ClientReadLatencyMs() (float64, bool) {
	return avgMs(s.ClientReadTimeMs, s.ClientReads)
}

// CheckpointerWriteLatencyMs is the mean checkpointer buffer write — the
// storage's plain write speed under the checkpoint's paced load.
func (s IOSplitStat) CheckpointerWriteLatencyMs() (float64, bool) {
	return avgMs(s.CheckpointerWriteTimeMs, s.CheckpointerWrites)
}

// ClientWriteLatencyMs is the mean write a backend did itself, i.e. time a
// query spent evicting a dirty page.
func (s IOSplitStat) ClientWriteLatencyMs() (float64, bool) {
	return avgMs(s.ClientWriteTimeMs, s.ClientWrites)
}

// WALFsyncLatencyMs is the mean WAL fsync (PG18+), the floor under commit
// latency with synchronous_commit on.
func (s IOSplitStat) WALFsyncLatencyMs() (float64, bool) {
	return avgMs(s.WALFsyncTimeMs, s.WALFsyncs)
}

// VacuumFracs is autovacuum's share of relation reads and writes; ok is
// false without any relation I/O.
func (s IOSplitStat) VacuumFracs() (reads, writes float64, ok bool) {
	totalW := s.CheckpointerWrites + s.BgwriterWrites + s.ClientWrites
	if s.RelationReads <= 0 && totalW <= 0 {
		return 0, 0, false
	}
	if s.RelationReads > 0 {
		reads = float64(s.VacuumReads) / float64(s.RelationReads)
	}
	if totalW > 0 {
		writes = float64(s.VacuumWrites) / float64(totalW)
	}
	return reads, writes, true
}

// ClientWriteFrac is the share of relation writes done by client backends
// themselves rather than the checkpointer/bgwriter; ok is false with no writes.
func (s IOSplitStat) ClientWriteFrac() (float64, bool) {
	total := s.CheckpointerWrites + s.BgwriterWrites + s.ClientWrites
	if total <= 0 {
		return 0, false
	}
	return float64(s.ClientWrites) / float64(total), true
}

// CheckpointerStat is pg_stat_checkpointer: how many checkpoints ran, why, and
// what they cost. StatsReset is zero when the counters were never reset.
type CheckpointerStat struct {
	HasData        bool
	Timed          int64
	Requested      int64
	WriteTimeMs    float64
	SyncTimeMs     float64
	BuffersWritten int64
	StatsReset     time.Time
}

// WALStat is pg_stat_wal plus the current write position. CurrentLSNBytes is
// the LSN as a byte offset (replay position on a standby) so two samples can
// be subtracted for the live WAL rate; wal_bytes only counts what this
// server generated itself.
type WALStat struct {
	HasData         bool
	Records         int64
	FPI             int64
	Bytes           int64
	BuffersFull     int64
	StatsReset      time.Time
	CurrentLSNBytes int64
}

// FPIFrac is full-page images per WAL record; ok is false without records.
func (w WALStat) FPIFrac() (float64, bool) {
	if w.Records <= 0 {
		return 0, false
	}
	return float64(w.FPI) / float64(w.Records), true
}

// BgwriterStat is pg_stat_bgwriter: maxwritten_clean counts the LRU sweeps
// that stopped early because bgwriter_lru_maxpages was reached — the
// bgwriter wanting to do more than it is allowed to.
type BgwriterStat struct {
	BuffersClean    int64
	MaxwrittenClean int64
	BuffersAlloc    int64
}

// AutovacStat is autovacuum's backlog in the current database: how many
// workers are running right now, and how many tables already exceed their
// vacuum threshold (dead tuples past threshold + scale_factor × rows).
type AutovacStat struct {
	WorkersBusy   int
	OverThreshold int64
	OverTop       []string // up to 3 "schema.name", most dead tuples first
}

// WALDirStat is the on-disk pg_wal footprint from pg_ls_waldir(), which needs
// pg_monitor; HasData=false is rendered as "n/a".
type WALDirStat struct {
	HasData bool
	Bytes   int64
	Files   int64
}

// StatsWindow returns how long the cumulative counters behind reset have been
// accumulating: since the reset, or since postmaster start when never reset.
func (m *MaintenanceInfo) StatsWindow(reset time.Time) (time.Duration, bool) {
	since := reset
	if since.IsZero() {
		since = m.StartTime
	}
	if since.IsZero() || m.SampledAt.IsZero() {
		return 0, false
	}
	w := m.SampledAt.Sub(since)
	if w <= 0 {
		return 0, false
	}
	return w, true
}

// WALBytesPerSecSinceReset is the average WAL generation rate over the whole
// pg_stat_wal window — the stable figure to size max_wal_size from.
func (m *MaintenanceInfo) WALBytesPerSecSinceReset() (float64, bool) {
	if !m.WAL.HasData {
		return 0, false
	}
	w, ok := m.StatsWindow(m.WAL.StatsReset)
	if !ok {
		return 0, false
	}
	return float64(m.WAL.Bytes) / w.Seconds(), true
}

// AvgCheckpointInterval is the pg_stat_checkpointer window divided by the
// number of checkpoints; compared to checkpoint_timeout it tells whether WAL
// volume, not the timer, is driving checkpoints.
func (m *MaintenanceInfo) AvgCheckpointInterval() (time.Duration, bool) {
	total := m.Checkpointer.Timed + m.Checkpointer.Requested
	if !m.Checkpointer.HasData || total <= 0 {
		return 0, false
	}
	w, ok := m.StatsWindow(m.Checkpointer.StatsReset)
	if !ok {
		return 0, false
	}
	return w / time.Duration(total), true
}

// AvgSyncPerCheckpointMs is the mean fsync phase of a checkpoint.
func (m *MaintenanceInfo) AvgSyncPerCheckpointMs() (float64, bool) {
	total := m.Checkpointer.Timed + m.Checkpointer.Requested
	if !m.Checkpointer.HasData || total <= 0 {
		return 0, false
	}
	return m.Checkpointer.SyncTimeMs / float64(total), true
}

// EffectiveAutovacWorkMem resolves autovacuum_work_mem's -1 default to
// maintenance_work_mem. ok is false when either GUC is unknown.
func (m *MaintenanceInfo) EffectiveAutovacWorkMem() (int64, bool) {
	v, ok := m.SettingBytes["autovacuum_work_mem"]
	if !ok {
		return 0, false
	}
	if v >= 0 {
		return v, true
	}
	mw, ok := m.SettingBytes["maintenance_work_mem"]
	return mw, ok
}

// TotalConns is the number of client connections seen by the last sample.
func (m *MaintenanceInfo) TotalConns() int {
	n := 0
	for _, c := range m.ConnByState {
		n += c
	}
	return n
}

// TempDBStat holds per-database temp-file usage, used in the maintenance
// dashboard to show which database is consuming temp space.
type TempDBStat struct {
	DB    string
	Files int64
	Bytes int64
	// StatsReset is when this database's counters were last zeroed (zero =
	// never): a lifetime byte total only means something as a rate over it.
	StatsReset time.Time
}

// SettingRow is one row from pg_settings for the settings browser.
type SettingRow struct {
	Name           string
	Setting        string // current effective value, raw (in Unit)
	Display        string // the value as current_setting() formats it: "8GB", "5min"
	Unit           string // e.g. "8kB", "ms", ""
	Category       string // e.g. "Query Tuning / Planner Cost Constants"
	ShortDesc      string
	Context        string // who can change: user/superuser/sighup/postmaster
	PendingRestart bool
	IsDefault      bool // setting == boot_val (never modified)
}

// ReplicaStat holds one row from pg_stat_replication (primary-side view).
type ReplicaStat struct {
	PID            int32 // walsender backend; matches ReplSlotStat.ActivePID
	AppName        string
	ClientAddr     string
	State          string // streaming / catchup / backup / …
	SyncState      string // async / sync / quorum / …
	WriteLag       time.Duration
	FlushLag       time.Duration
	ReplayLag      time.Duration
	ByteLag        int64 // pg_wal_lsn_diff(current_wal_lsn, replay_lsn), bytes behind
	ReplayLSNBytes int64 // replay_lsn as an absolute offset; its advance between samples is the throughput
}

// Key identifies a replica across samples. The walsender PID changes on every
// reconnect, so name and address pair samples instead; a reconnected replica
// keeps its rate, a renamed one starts over.
func (r ReplicaStat) Key() string { return r.AppName + "@" + r.ClientAddr }

// ReplSlotStat holds one row from pg_replication_slots.
type ReplSlotStat struct {
	Name          string
	SlotType      string // physical / logical
	Active        bool
	ActivePID     int32   // walsender using the slot, 0 when inactive
	WALStatus     string  // reserved / extended / unreserved / lost
	RetainedBytes int64   // pg_wal_lsn_diff(current_wal_lsn, restart_lsn)
	SafeWALBytes  int64   // headroom before max_slot_wal_keep_size invalidates the slot; -1 when unlimited
	InactiveSecs  float64 // how long the slot has had no consumer (inactive_since); 0 while active
}

// Horizon holder kinds, in the order the advice prefers to explain them: an
// idle transaction is the common, fixable case, a slot the next, a prepared
// transaction the rarest. Kind selects which existing view Enter opens.
const (
	horizonKindIdleXact = "idle transaction"
	horizonKindBackend  = "backend"
	horizonKindSlot     = "replication slot"
	horizonKindPrepared = "prepared transaction"
)

// HorizonStat is the oldest live xmin in the cluster and what holds it.
// VACUUM cannot freeze a tuple newer than this, so the horizon caps how far
// relfrozenxid can advance anywhere — it moves days before the per-database
// ages react, which makes it the leading indicator for freezing falling behind.
type HorizonStat struct {
	Age    int64  // greatest() over backends, slots and prepared xacts; 0 = nothing pins a horizon
	Kind   string // one of the horizonKind* constants; "" when Age is 0
	Holder string // the holder spelled out: "pid 4711 in shop, idle in transaction for 42m" / slot name / gid
	// Restricted is true when this session cannot see other users' rows in
	// pg_stat_activity (no pg_read_all_stats / superuser), so Age is a lower
	// bound. Reported rather than silently ignored: a filtered view that finds
	// nothing must not read as an all-clear.
	Restricted bool
}

// SLRUStat is one pg_stat_slru row: a simple-LRU cache (transaction status,
// multixacts, subtransactions, …) whose hit ratio says whether its fixed
// buffer count keeps up with the workload.
type SLRUStat struct {
	Name  string
	Hits  int64
	Reads int64
}

// HitPct is the cache hit ratio in percent; 100 with no traffic.
func (s SLRUStat) HitPct() float64 {
	total := s.Hits + s.Reads
	if total == 0 {
		return 100
	}
	return 100 * float64(s.Hits) / float64(total)
}

// BuffersGUC is the PG17+ setting sizing this cache ("subtransaction" →
// subtransaction_buffers), "" for the catch-all "other".
func (s SLRUStat) BuffersGUC() string {
	if s.Name == "" || s.Name == "other" {
		return ""
	}
	return s.Name + "_buffers"
}

// WalReceiverStat holds the standby-side view from pg_stat_wal_receiver.
type WalReceiverStat struct {
	Status     string        // stopped / starting / streaming / …
	ByteLag    int64         // latest_end_lsn - received_lsn (approximate)
	LastMsgAge time.Duration // how long since the last message from primary
}

// IOStat holds aggregate I/O counters from pg_stat_io (PG 16+).
// HasData is false on older clusters where the view doesn't exist.
type IOStat struct {
	HasData       bool
	Reads         int64
	Writes        int64
	Extends       int64 // relation extension (new blocks)
	Hits          int64 // blocks served from shared_buffers
	Evictions     int64 // blocks evicted from shared_buffers to make room
	Fsyncs        int64
	BackendFsyncs int64 // fsyncs issued by client backends (checkpointer overloaded)
}

// BlockedStat is one blocked query from pg_stat_activity.
type BlockedStat struct {
	PID       int32
	BlockedBy []int32
	WaitSec   float64
	Query     string // truncated to 80 chars
}

// TableMaintStats is the full maintenance snapshot for one table, shown as a
// panel below the parts list. All pg_stat_all_tables fields are zero/nil when
// the table is new and autovacuum hasn't run yet.
type TableMaintStats struct {
	NLive, NDead                      int64
	LastVacuum, LastAutovacuum        *time.Time
	LastAnalyze, LastAutoanalyze      *time.Time
	VacuumCount, AutovacuumCount      int64
	AnalyzeCount, AutoanalyzeCount    int64
	NModSinceAnalyze, NInsSinceVacuum int64

	// Scan activity (PG16+, always available on PG17/18+).
	LastSeqScan, LastIdxScan *time.Time
	SeqScans, IdxScans       int64

	// pg_class fields.
	RelTuples    int64    // -1 = never analyzed / VACUUM-ed
	FrozenXIDAge int64    // age(relfrozenxid); meaningless for relkind 'p'
	RelKind      string   // 'r', 'm', 'p', …
	RelOptions   []string // raw reloptions

	// Cluster GUC defaults; per-table RelOptions may override.
	AvacEnabled          bool
	AvacVacuumThreshold  int64
	AvacVacuumScale      float64
	AvacInsertThreshold  int64
	AvacInsertScale      float64
	AvacAnalyzeThreshold int64
	AvacAnalyzeScale     float64
	FreezeMaxAge         int64
}

// ParseRelOptions converts a raw reloptions string slice (from pg_class) into a
// map of option name → value. The GUC name is the key without the "autovacuum_"
// prefix where applicable, e.g. "autovacuum_vacuum_threshold" → key "autovacuum_vacuum_threshold".
func ParseRelOptions(opts []string) map[string]string {
	m := make(map[string]string, len(opts))
	for _, o := range opts {
		if i := strings.IndexByte(o, '='); i > 0 {
			m[o[:i]] = o[i+1:]
		}
	}
	return m
}

func optFloat(opts map[string]string, key string, fallback float64) float64 {
	if v, ok := opts[key]; ok {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return fallback
}

func optInt64(opts map[string]string, key string, fallback int64) int64 {
	if v, ok := opts[key]; ok {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func optBool(opts map[string]string, key string, fallback bool) bool {
	if v, ok := opts[key]; ok {
		switch strings.ToLower(v) {
		case "true", "on", "1":
			return true
		case "false", "off", "0":
			return false
		}
	}
	return fallback
}

// VacuumTriggerAt returns the effective autovacuum_vacuum_threshold (dead-row
// count) for this table, accounting for per-table storage parameters. ok is
// false when the stats are too sparse to compute a meaningful threshold.
func (s *TableMaintStats) VacuumTriggerAt() (trig int64, ok bool) {
	if s.RelTuples < 0 {
		return 0, false
	}
	opts := ParseRelOptions(s.RelOptions)
	thresh := optInt64(opts, "autovacuum_vacuum_threshold", s.AvacVacuumThreshold)
	scale := optFloat(opts, "autovacuum_vacuum_scale_factor", s.AvacVacuumScale)
	return thresh + int64(float64(s.RelTuples)*scale), true
}

// AnalyzeTriggerAt returns the effective autovacuum_analyze_threshold (modified
// row count) for this table. ok is false when stats are too sparse.
func (s *TableMaintStats) AnalyzeTriggerAt() (trig int64, ok bool) {
	if s.RelTuples < 0 {
		return 0, false
	}
	opts := ParseRelOptions(s.RelOptions)
	thresh := optInt64(opts, "autovacuum_analyze_threshold", s.AvacAnalyzeThreshold)
	scale := optFloat(opts, "autovacuum_analyze_scale_factor", s.AvacAnalyzeScale)
	return thresh + int64(float64(s.RelTuples)*scale), true
}

// InsertTriggerAt returns the effective autovacuum_vacuum_insert_threshold
// (inserted-row count) for this table. ok is false when stats are too sparse.
func (s *TableMaintStats) InsertTriggerAt() (trig int64, ok bool) {
	if s.RelTuples < 0 {
		return 0, false
	}
	opts := ParseRelOptions(s.RelOptions)
	thresh := optInt64(opts, "autovacuum_vacuum_insert_threshold", s.AvacInsertThreshold)
	scale := optFloat(opts, "autovacuum_vacuum_insert_scale_factor", s.AvacInsertScale)
	return thresh + int64(float64(s.RelTuples)*scale), true
}

// AutovacuumEnabled returns false when autovacuum is disabled globally or
// overridden to false via this table's storage parameters.
func (s *TableMaintStats) AutovacuumEnabled() bool {
	opts := ParseRelOptions(s.RelOptions)
	return optBool(opts, "autovacuum_enabled", s.AvacEnabled)
}

// FreezeFrac returns FrozenXIDAge / FreezeMaxAge as a fraction in [0,1], or 0
// when either value is unknown.
func (s *TableMaintStats) FreezeFrac() float64 {
	if s.FreezeMaxAge <= 0 {
		return 0
	}
	f := float64(s.FrozenXIDAge) / float64(s.FreezeMaxAge)
	if f > 1 {
		return 1
	}
	return f
}
