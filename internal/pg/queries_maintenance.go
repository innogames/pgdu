package pg

const (
	// sqlMaintSettings fetches a curated set of GUCs in one round trip.
	// current_setting(name, true) returns the human-readable GUC representation
	// (e.g. "128MB" for shared_buffers, "5min" for checkpoint_timeout) instead of
	// the raw numeric value that pg_settings.setting carries. The missing_ok flag
	// (true) returns NULL for extension GUCs whose library isn't loaded.
	sqlMaintSettings = `
SELECT name, COALESCE(current_setting(name, true), '')
FROM   pg_settings
WHERE  name = ANY($1)
ORDER  BY name`

	// sqlStatementsCount reads the current entry count and the .max GUC together.
	// Any user who can see pg_stat_statements can run this query.
	sqlStatementsCount = `
SELECT count(*),
       COALESCE(current_setting('pg_stat_statements.max', true)::bigint, 0)
FROM   pg_stat_statements`

	// sqlStatementsReset reads the last stats_reset from pg_stat_statements_info.
	// Requires pg_read_all_stats or superuser; callers should handle errors
	// gracefully and show unknown when unprivileged.
	sqlStatementsReset = `
SELECT COALESCE(stats_reset, '-infinity'::timestamptz)
FROM   pg_stat_statements_info`

	// sqlStatementsShmem sums the shared memory pg_stat_statements reserves
	// (control struct + entry hash). pg_shmem_allocations (PG13+) requires
	// pg_read_all_stats; failure leaves the figure unknown.
	sqlStatementsShmem = `
SELECT COALESCE(sum(allocated_size), 0)
FROM   pg_shmem_allocations
WHERE  name LIKE 'pg_stat_statements%'`

	// sqlStatementsTextBytes sums the deduplicated normalized query-text bytes.
	// This is the part of pg_stat_statements that grows with distinct statements
	// (texts live in an external file, not the fixed-size entry hash).
	sqlStatementsTextBytes = `
SELECT COALESCE(sum(octet_length(query)), 0)
FROM   pg_stat_statements`

	// sqlQualstatsShmem sums the shared memory pg_qualstats reserves.
	// pg_shmem_allocations (PG13+) requires pg_read_all_stats.
	sqlQualstatsShmem = `
SELECT COALESCE(sum(allocated_size), 0)
FROM   pg_shmem_allocations
WHERE  name LIKE '%qualstats%'`

	// sqlQualstatsCapacity reads the entry count and last reset from pg_qualstats.
	// pg_qualstats() is a set-returning function so COUNT wraps it.
	sqlQualstatsCapacity = `
SELECT count(*),
       COALESCE(current_setting('pg_qualstats.max', true)::bigint, 0)
FROM   pg_qualstats()`

	// sqlMaintServer fetches server version string and the two postmaster timestamps.
	sqlMaintServer = `
SELECT version(),
       pg_postmaster_start_time(),
       pg_conf_load_time()`

	// sqlMaintActivity counts connections by state (active/idle/idle in transaction/…)
	// and finds the longest-running transaction age in seconds.
	// NULL states (autovacuum workers) map to 'other'.
	sqlMaintActivity = `
SELECT COALESCE(state, 'other') AS state,
       count(*)                  AS cnt,
       COALESCE(max(EXTRACT(epoch FROM now() - xact_start)) FILTER (WHERE state <> 'idle'), 0) AS longest_xact_secs
FROM   pg_stat_activity
WHERE  pid <> pg_backend_pid()
GROUP  BY state`

	// sqlMaintCacheHit computes the aggregate buffer-cache hit ratio across all
	// user databases (blks_hit / (blks_hit + blks_read)).
	// Returns 0 when there have been no reads yet.
	sqlMaintCacheHit = `
SELECT CASE WHEN sum(blks_hit) + sum(blks_read) > 0
            THEN sum(blks_hit)::float8 / (sum(blks_hit) + sum(blks_read))
            ELSE 0
       END
FROM   pg_stat_database
WHERE  datname NOT IN ('template0', 'template1')`

	// sqlMaintWraparound reads the maximum transaction-ID age across all
	// non-template databases. A high age approaching autovacuum_freeze_max_age
	// (typically 200 M) means wraparound is imminent and the autovacuum "emergency
	// brake" will fire, degrading all write throughput.
	sqlMaintWraparound = `
SELECT max(age(datfrozenxid))
FROM   pg_database
WHERE  datname NOT IN ('template0', 'template1')`

	// sqlMaintCheckpointer fetches the cumulative checkpoint counters from
	// pg_stat_checkpointer (introduced in PG 15; earlier clusters get zeros
	// via error handling). A high requested/(timed+requested) ratio signals
	// max_wal_size pressure: WAL is filling up faster than the checkpoint interval.
	sqlMaintCheckpointer = `
SELECT num_timed, num_requested
FROM   pg_stat_checkpointer`

	// sqlMaintMaxConns reads max_connections once (rarely changes at runtime).
	sqlMaintMaxConns = `SELECT current_setting('max_connections')::int`

	// sqlMaintPendingConfig counts settings that need a restart or reload to take
	// effect. pending_restart signals a restart; setting != reset_val with a
	// reload-level context means a SIGHUP / pg_reload_conf() is sufficient.
	sqlMaintPendingConfig = `
SELECT
    count(*) FILTER (WHERE pending_restart)                                                AS need_restart,
    count(*) FILTER (WHERE NOT pending_restart
                       AND context IN ('sighup','backend','superuser-backend')
                       AND setting <> reset_val)                                           AS need_reload
FROM pg_settings`

	// sqlMaintPendingNames returns the names of settings that need action,
	// sorted by type (restart first) then name. Capped at 8 so the display
	// stays compact even with many changed settings.
	sqlMaintPendingNames = `
SELECT name, pending_restart
FROM   pg_settings
WHERE  pending_restart
   OR  (NOT pending_restart
        AND context IN ('sighup','backend','superuser-backend')
        AND setting <> reset_val)
ORDER  BY pending_restart DESC, name
LIMIT  8`

	// sqlMaintTempByDB lists databases with non-zero temp-file usage, ordered by
	// temp_bytes descending so the biggest offenders appear first.
	sqlMaintTempByDB = `
SELECT datname, temp_files, temp_bytes
FROM   pg_stat_database
WHERE  temp_files > 0
ORDER  BY temp_bytes DESC
LIMIT  5`

	// sqlMaintLockWaits counts currently blocked queries (wait_event_type = 'Lock').
	sqlMaintLockWaits = `
SELECT count(*)
FROM   pg_stat_activity
WHERE  wait_event_type = 'Lock'
  AND  pid <> pg_backend_pid()`

	// sqlMaintTempFiles reads aggregate temp-file usage across all user databases.
	// High temp_bytes relative to work_mem × max_connections suggests work_mem is too small.
	sqlMaintTempFiles = `
SELECT COALESCE(sum(temp_files), 0),
       COALESCE(sum(temp_bytes),  0)
FROM   pg_stat_database
WHERE  datname IS NOT NULL`

	// sqlMaintWALInFlight reads how much WAL has been generated since the last
	// checkpoint. The fill ratio (bytes_since_chkpt / max_wal_bytes) shows how
	// close the cluster is to triggering a requested (size-driven) checkpoint.
	// checkpoint_time is the wall-clock time the last checkpoint completed.
	// max_wal_size from pg_settings is in MB, so multiply by 2^20 for bytes.
	sqlMaintWALInFlight = `
SELECT (pg_current_wal_insert_lsn() - redo_lsn)::bigint                              AS bytes_since_chkpt,
       COALESCE((SELECT setting::bigint * 1048576 FROM pg_settings WHERE name = 'max_wal_size'), 0) AS max_wal_bytes,
       checkpoint_time
FROM   pg_control_checkpoint()`

	// sqlMaintWALStats reads cumulative WAL write statistics (PG 14+).
	// wal_buffers_full counts how often a backend had to wait for WAL buffer
	// space — a persistent non-zero value means wal_buffers is too small.
	sqlMaintWALStats = `SELECT wal_bytes, wal_buffers_full FROM pg_stat_wal`

	// sqlMaintBgwriter reads background-writer pressure. buffers_backend is the
	// count of buffers written directly by backends (bypassing the bgwriter/
	// checkpointer), which stalls the writing query. A high ratio vs buffers_alloc
	// signals that max_wal_size or bgwriter_lru_maxpages needs tuning.
	sqlMaintBgwriter = `
SELECT COALESCE(buffers_backend, 0),
       COALESCE(buffers_alloc,   0)
FROM   pg_stat_bgwriter`

	// sqlMaintArchiver reads WAL-archiver health. failed_count > 0 means pg_wal
	// is accumulating unarchived segments, which will eventually fill the disk.
	sqlMaintArchiver = `
SELECT archived_count,
       failed_count,
       COALESCE(last_failed_wal, ''),
       COALESCE(last_archived_time, '-infinity'::timestamptz)
FROM   pg_stat_archiver`

	// sqlAllSettings fetches all pg_settings for the Settings browser.
	// boot_val is the compiled-in default; we compare setting == boot_val to
	// flag non-default values (yellow highlight).
	sqlAllSettings = `
SELECT name,
       COALESCE(setting, ''),
       COALESCE(unit,    ''),
       COALESCE(category,''),
       COALESCE(short_desc, ''),
       context,
       pending_restart,
       (setting IS NOT DISTINCT FROM boot_val) AS is_default
FROM   pg_settings
ORDER  BY category, name`

	// sqlStatementsResetAll resets all pg_stat_statements statistics. The
	// NULL arguments reset everything (no per-user / per-db scoping).
	// Requires pg_read_all_stats or superuser.
	sqlStatementsResetAll = `SELECT pg_stat_statements_reset()`

	// sqlQualstatsResetAll resets all pg_qualstats statistics.
	// Requires superuser or pg_monitor on most versions.
	sqlQualstatsResetAll = `SELECT pg_qualstats_reset()`

	// sqlMaintRecovery detects whether this node is a standby.
	sqlMaintRecovery = `SELECT pg_is_in_recovery()`

	// sqlMaintReplication reads streaming-replication standby info from the primary.
	// The query returns no rows on a standby or when no standbys are connected.
	// ByteLag is the LSN delta between the primary's write position and the
	// replica's last confirmed replay position; NULL replay_lsn maps to 0.
	sqlMaintReplication = `
SELECT application_name,
       COALESCE(client_addr::text, ''),
       COALESCE(state, ''),
       COALESCE(sync_state, ''),
       COALESCE(EXTRACT(epoch FROM write_lag),  0)::float8,
       COALESCE(EXTRACT(epoch FROM flush_lag),  0)::float8,
       COALESCE(EXTRACT(epoch FROM replay_lag), 0)::float8,
       COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0)
FROM   pg_stat_replication
ORDER  BY sync_state DESC, application_name`

	// sqlMaintReplSlots reads replication slot health. retained_bytes is the
	// amount of WAL that cannot be recycled because of this slot; when it grows
	// large and the slot is inactive, it is a serious disk-space hazard.
	// On a standby, pg_current_wal_lsn() is still valid (it returns the replay
	// position), so retained_bytes is meaningful there too.
	sqlMaintReplSlots = `
SELECT slot_name,
       slot_type,
       active,
       COALESCE(wal_status, ''),
       COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn), 0)
FROM   pg_replication_slots
ORDER  BY active DESC, slot_name`

	// sqlMaintWalReceiver reads the standby-side WAL receiver status.
	// Returns no rows on a primary. latest_end_lsn is the last LSN reported
	// to the primary; last_msg_receipt_time tells how stale the stream is.
	sqlMaintWalReceiver = `
SELECT COALESCE(status, ''),
       COALESCE(EXTRACT(epoch FROM (now() - last_msg_receipt_time)), 0)::float8
FROM   pg_stat_wal_receiver
LIMIT  1`

	// sqlMaintTxnStats aggregates commit/rollback/deadlock/conflict totals
	// across all non-template user databases. Works on PG 9.2+.
	sqlMaintTxnStats = `
SELECT COALESCE(sum(xact_commit),   0),
       COALESCE(sum(xact_rollback), 0),
       COALESCE(sum(deadlocks),     0),
       COALESCE(sum(conflicts),     0)
FROM   pg_stat_database
WHERE  datname NOT IN ('template0', 'template1')
  AND  datname IS NOT NULL`

	// sqlMaintSessionStats reads session-lifecycle counters added in PG 14.
	// Callers must handle errors on PG ≤ 13 gracefully.
	sqlMaintSessionStats = `
SELECT COALESCE(sum(sessions),                     0),
       COALESCE(sum(sessions_abandoned),            0),
       COALESCE(sum(sessions_fatal),                0),
       COALESCE(sum(sessions_killed),               0),
       COALESCE(sum(active_time),                   0)::float8,
       COALESCE(sum(idle_in_transaction_time),      0)::float8
FROM   pg_stat_database
WHERE  datname NOT IN ('template0', 'template1')
  AND  datname IS NOT NULL`

	// sqlMaintTableActivity aggregates tuple-level write/scan counters across all
	// user tables in the current database (pg_stat_user_tables). Used for the
	// HOT-update ratio, write mix, index-usage ratio and dead-tuple ratio on the
	// Maintenance dashboard. idx_scan/seq_scan/n_dead_tup can be NULL per row on
	// never-touched relations; sum() ignores NULLs and COALESCE guards the
	// all-NULL case. All columns exist on PG 9.x+, so no version gating is needed.
	sqlMaintTableActivity = `
SELECT COALESCE(sum(n_tup_ins),     0),
       COALESCE(sum(n_tup_upd),     0),
       COALESCE(sum(n_tup_del),     0),
       COALESCE(sum(n_tup_hot_upd), 0),
       COALESCE(sum(seq_scan),      0),
       COALESCE(sum(idx_scan),      0),
       COALESCE(sum(n_live_tup),    0),
       COALESCE(sum(n_dead_tup),    0)
FROM   pg_stat_user_tables`

	// sqlMaintIO aggregates I/O counters across all backend types from
	// pg_stat_io (PG 16+). BackendFsyncs is fsyncs by client backends —
	// non-zero means the checkpointer can't keep up.
	sqlMaintIO = `
SELECT COALESCE(sum(reads),    0),
       COALESCE(sum(writes),   0),
       COALESCE(sum(extends),  0),
       COALESCE(sum(hits),     0),
       COALESCE(sum(evictions),0),
       COALESCE(sum(fsyncs),   0),
       COALESCE(sum(fsyncs) FILTER (WHERE backend_type = 'client backend'), 0)
FROM   pg_stat_io`

	// sqlMaintBlocked returns currently blocked queries, longest waits first.
	// pg_blocking_pids() returns the array of PIDs that block a given PID.
	// Capped at 8 rows to keep the dashboard compact.
	sqlMaintBlocked = `
SELECT a.pid,
       pg_blocking_pids(a.pid),
       COALESCE(EXTRACT(epoch FROM now() - a.query_start), 0)::float8,
       COALESCE(left(a.query, 80), '')
FROM   pg_stat_activity a
WHERE  cardinality(pg_blocking_pids(a.pid)) > 0
  AND  a.pid <> pg_backend_pid()
ORDER  BY 3 DESC
LIMIT  8`

	// sqlMaintPrepared counts prepared transactions and finds the oldest one.
	// Abandoned 2PC transactions pin the xmin horizon and prevent autovacuum
	// from reclaiming dead tuples across the whole cluster.
	sqlMaintPrepared = `
SELECT count(*)::int,
       COALESCE(EXTRACT(epoch FROM now() - min(prepared)), 0)::float8
FROM   pg_prepared_xacts`

	// sqlTableMaintStats fetches the full autovacuum/analysis snapshot for one
	// table (identified by OID). It joins pg_class with pg_stat_all_tables and
	// reads the cluster-wide autovacuum GUCs; per-table overrides live in
	// RelOptions and are applied by the TableMaintStats methods in Go.
	// last_seq_scan / last_idx_scan are PG16+; they are NULL on older clusters
	// (PG15 and below), which is fine — the Scan uses *time.Time.
	sqlTableMaintStats = `
SELECT
    COALESCE(s.n_live_tup, 0),
    COALESCE(s.n_dead_tup, 0),
    s.last_vacuum,
    s.last_autovacuum,
    s.last_analyze,
    s.last_autoanalyze,
    COALESCE(s.vacuum_count,    0),
    COALESCE(s.autovacuum_count,0),
    COALESCE(s.analyze_count,   0),
    COALESCE(s.autoanalyze_count,0),
    COALESCE(s.n_mod_since_analyze, 0),
    COALESCE(s.n_ins_since_vacuum,  0),
    s.last_seq_scan,
    s.last_idx_scan,
    COALESCE(s.seq_scan, 0),
    COALESCE(s.idx_scan, 0),
    c.reltuples::bigint,
    age(c.relfrozenxid)::bigint,
    c.relkind::text,
    COALESCE(c.reloptions, '{}'),
    current_setting('autovacuum')::bool,
    current_setting('autovacuum_vacuum_threshold')::bigint,
    current_setting('autovacuum_vacuum_scale_factor')::float8,
    current_setting('autovacuum_vacuum_insert_threshold')::bigint,
    current_setting('autovacuum_vacuum_insert_scale_factor')::float8,
    current_setting('autovacuum_analyze_threshold')::bigint,
    current_setting('autovacuum_analyze_scale_factor')::float8,
    current_setting('autovacuum_freeze_max_age')::bigint
FROM   pg_class c
LEFT   JOIN pg_stat_all_tables s ON s.relid = c.oid
WHERE  c.oid = $1`
)

// maintSettingsKeys is the list of GUC names fetched for the Maintenance
// dashboard. Extension GUCs (pg_stat_statements.*, pg_qualstats.*) are
// included but simply won't be returned when the library isn't preloaded —
// callers treat a missing key as "unknown / not applicable".
var maintSettingsKeys = []string{
	"shared_buffers",
	"work_mem",
	"maintenance_work_mem",
	"effective_cache_size",
	"max_connections",
	"wal_level",
	"max_wal_size",
	"min_wal_size",
	"checkpoint_timeout",
	"autovacuum",
	"autovacuum_max_workers",
	"autovacuum_naptime",
	"autovacuum_freeze_max_age",
	"pg_stat_statements.max",
	"pg_stat_statements.track",
	"pg_stat_statements.track_planning",
	"pg_qualstats.max",
	"pg_qualstats.enabled",
	"pg_qualstats.sample_rate",
	"pg_qualstats.track_constants",
}
