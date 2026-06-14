package pg

import (
	"fmt"
	"strings"
	"time"
)

// ExtCapacity describes how full a shared-memory stats extension is.
// Used by the Maintenance dashboard to show fill-level bars with urgency
// colouring and to let the user reset the extension from inside the TUI.
type ExtCapacity struct {
	Name       string // "pg_stat_statements" or "pg_qualstats"
	Installed  bool
	Used       int64     // count(*) of currently tracked entries
	Max        int64     // the .max GUC; 0 = unknown / extension not preloaded
	Dealloc    int64     // pg_stat_statements_info.dealloc; -1 = n/a (qualstats)
	StatsReset time.Time // last reset timestamp; zero = unknown
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

	// Connection counts
	MaxConns       int
	ConnByState    map[string]int // pg_stat_activity grouped by state
	LongestXactSec float64        // max xact age in seconds (non-idle)

	// Cache health
	CacheHitRatio float64 // sum(blks_hit)/(hit+read) over pg_stat_database

	// Transaction & session health (pg_stat_database aggregate, non-template DBs)
	XactCommit   int64
	XactRollback int64
	Deadlocks    int64
	Conflicts    int64
	// Session counters below are PG14+; all stay zero on older clusters.
	Sessions      int64
	SessAbandoned int64   // connections dropped due to client disconnect mid-session
	SessFatal     int64   // sessions ended by a fatal server error
	SessKilled    int64   // sessions ended by pg_terminate_backend()
	ActiveTimeMs  float64 // cumulative active query time in ms across all sessions
	IdleTxTimeMs  float64 // cumulative idle-in-transaction time in ms

	// Autovacuum / wraparound
	XidAge       int64 // max(age(datfrozenxid)) over pg_database
	FreezeMaxAge int64 // autovacuum_freeze_max_age from settings

	// Checkpoint health (pg_stat_checkpointer, PG 15+)
	CheckpointsTimed int64
	CheckpointsReq   int64

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

	// Background writer pressure (pg_stat_bgwriter, fallback when IO has no data).
	// BgwBuffersBackend / BgwBuffersAlloc is the fraction of buffer allocations
	// that were served by backends writing directly (bypassing the bgwriter).
	// A high ratio (> 10–15%) suggests max_wal_size or bgwriter tuning is needed.
	BgwBuffersBackend int64
	BgwBuffersAlloc   int64

	// I/O statistics (pg_stat_io, PG 16+; HasData=false on older clusters).
	IO IOStat

	// WAL archiver status (pg_stat_archiver). ArchiveFailed > 0 is a critical
	// signal: the pg_wal directory fills up silently when archiving stalls.
	ArchiveCount      int64
	ArchiveFailed     int64
	ArchiveLastFailed string    // WAL file name of the last failure
	ArchiveLastTime   time.Time // time of last successful archive

	// WAL in-flight: how much WAL has been generated since the last checkpoint.
	// When WALBytesSinceCheckpoint reaches WALMaxBytes, Postgres triggers a
	// requested checkpoint — exactly what the "N requested" counter counts up.
	WALBytesSinceCheckpoint int64     // pg_current_wal_insert_lsn() - redo_lsn
	WALMaxBytes             int64     // max_wal_size in bytes (for the fill bar)
	WALCheckpointTime       time.Time // when the last checkpoint completed

	// WAL write statistics from pg_stat_wal (PG14+; zero on older clusters).
	// WALBuffersFull > 0 means backends stalled waiting for wal_buffers space.
	WALBytesTotal  int64
	WALBuffersFull int64

	// Replication: Replicas is filled on a primary, WalReceiver on a standby.
	Replicas    []ReplicaStat
	ReplSlots   []ReplSlotStat
	WalReceiver *WalReceiverStat

	// PgBouncer stats via a best-effort admin connection. Nil when absent.
	PgBouncer *PgBouncerInfo

	// Curated GUCs (name → raw setting string from pg_settings)
	Settings map[string]string
}

// TempDBStat holds per-database temp-file usage, used in the maintenance
// dashboard to show which database is consuming temp space.
type TempDBStat struct {
	DB    string
	Files int64
	Bytes int64
}

// SettingRow is one row from pg_settings for the settings browser.
type SettingRow struct {
	Name           string
	Setting        string // current effective value
	Unit           string // e.g. "8kB", "ms", ""
	Category       string // e.g. "Query Tuning / Planner Cost Constants"
	ShortDesc      string
	Context        string // who can change: user/superuser/sighup/postmaster
	PendingRestart bool
	IsDefault      bool // setting == boot_val (never modified)
}

// ReplicaStat holds one row from pg_stat_replication (primary-side view).
type ReplicaStat struct {
	AppName    string
	ClientAddr string
	State      string // streaming / catchup / backup / …
	SyncState  string // async / sync / quorum / …
	WriteLag   time.Duration
	FlushLag   time.Duration
	ReplayLag  time.Duration
	ByteLag    int64 // pg_wal_lsn_diff(current_wal_lsn, replay_lsn), bytes behind
}

// ReplSlotStat holds one row from pg_replication_slots.
type ReplSlotStat struct {
	Name          string
	SlotType      string // physical / logical
	Active        bool
	WALStatus     string // reserved / extended / unreserved / lost
	RetainedBytes int64  // pg_wal_lsn_diff(current_wal_lsn, restart_lsn)
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

// PgBouncerInfo holds the stats fetched from the pgbouncer admin console
// via a best-effort connection to the virtual "pgbouncer" database.
type PgBouncerInfo struct {
	Version    string
	ClActive   int
	ClWaiting  int
	SvActive   int
	SvIdle     int
	MaxWaitSec float64
	Pools      []PgbPool
}

// PgbPool is one pool row from pgbouncer SHOW POOLS.
type PgbPool struct {
	Database   string
	User       string
	Mode       string // session / transaction / statement
	ClActive   int
	ClWaiting  int
	SvActive   int
	SvIdle     int
	MaxWaitSec float64
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
