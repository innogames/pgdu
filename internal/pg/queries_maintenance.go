package pg

const (
	// sqlMaintSettings fetches a curated set of GUCs in one round trip.
	// current_setting(name, true) returns the human-readable GUC representation
	// (e.g. "128MB" for shared_buffers, "5min" for checkpoint_timeout) instead of
	// the raw numeric value that pg_settings.setting carries. The missing_ok flag
	// (true) returns NULL for extension GUCs whose library isn't loaded.
	//
	// A GUC pgdu overrides in its own session (the pool's AfterConnect sets
	// pg_stat_statements.track = none so pgdu's queries stay out of the stats)
	// must show the server-wide value the users' sessions run with, which is
	// what reset_val holds; that path only ever carries text-valued GUCs.
	sqlMaintSettings = `
SELECT name,
       COALESCE(CASE WHEN setting IS DISTINCT FROM reset_val THEN reset_val
                     ELSE current_setting(name, true) END, '')
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
	// The extension registers three named allocations — the control struct
	// (pg_qualstats) plus two dynahash headers (pg_qualstatements_hash,
	// pg_qualqueryexamples_hash) — so the pattern must match the pg_qual*
	// prefix, NOT the literal "qualstats" (the hash names lack that substring).
	// Note this still counts only the named headers: dynahash pre-allocates its
	// per-entry element blocks via ShmemAlloc as anonymous (NULL-name) rows that
	// no name filter can attribute back to qualstats.
	sqlQualstatsShmem = `
SELECT COALESCE(sum(allocated_size), 0)
FROM   pg_shmem_allocations
WHERE  name LIKE 'pg_qual%'`

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
	// NULL states (autovacuum workers) map to 'other'. The longest-xact figure
	// counts client backends only: autovacuum workers run inside a transaction
	// too, but VACUUM's snapshot is ignored by other vacuums' horizon computation,
	// so a long anti-wraparound vacuum would be a false alarm.
	sqlMaintActivity = `
SELECT COALESCE(state, 'other') AS state,
       count(*)                  AS cnt,
       COALESCE(max(EXTRACT(epoch FROM now() - xact_start))
                FILTER (WHERE state <> 'idle' AND backend_type = 'client backend'), 0) AS longest_xact_secs
FROM   pg_stat_activity
WHERE  pid <> pg_backend_pid()
GROUP  BY state`

	// sqlMaintCacheHit computes the aggregate buffer-cache hit ratio across all
	// user databases (blks_hit / (blks_hit + blks_read)), 0 with no reads yet,
	// plus the block traffic it is computed over so a ratio over a handful of
	// blocks is not graded.
	sqlMaintCacheHit = `
SELECT CASE WHEN sum(blks_hit) + sum(blks_read) > 0
            THEN sum(blks_hit)::float8 / (sum(blks_hit) + sum(blks_read))
            ELSE 0
       END,
       COALESCE(sum(blks_hit) + sum(blks_read), 0)::bigint
FROM   pg_stat_database
WHERE  datname NOT IN ('template0', 'template1')`

	// sqlMaintSLRU reads the simple-LRU cache counters (transaction status,
	// multixacts, subtransactions, …). A poor hit ratio on a busy one means its
	// *_buffers GUC (PG17+) is too small.
	sqlMaintSLRU = `
SELECT name, blks_hit, blks_read
FROM   pg_stat_slru
ORDER  BY blks_read DESC`

	// sqlMaintWraparound reads the maximum transaction-ID age across all
	// non-template databases. A high age approaching autovacuum_freeze_max_age
	// (typically 200 M) means wraparound is imminent and the autovacuum "emergency
	// brake" will fire, degrading all write throughput.
	// The database holding that oldest datfrozenxid rides along so the
	// wraparound recommendation can open the per-table freeze-age diagnostic
	// in the right place.
	sqlMaintWraparound = `
SELECT datname, age(datfrozenxid)
FROM   pg_database
WHERE  datname NOT IN ('template0', 'template1')
ORDER  BY 2 DESC
LIMIT  1`

	// sqlMaintMxidWraparound is the multixact counterpart: mxid_age(datminmxid)
	// against autovacuum_multixact_freeze_max_age drives the same emergency
	// autovacuum, and is easy to overlook because it moves independently of
	// the XID counter (row locks shared by several transactions, FK checks).
	sqlMaintMxidWraparound = `
SELECT datname, mxid_age(datminmxid)
FROM   pg_database
WHERE  datname NOT IN ('template0', 'template1')
ORDER  BY 2 DESC
LIMIT  1`

	// sqlMaintCheckpointer fetches the cumulative checkpoint counters and costs
	// from pg_stat_checkpointer (PG17+ has the write/sync times there). A high
	// requested/(timed+requested) ratio signals max_wal_size pressure: WAL is
	// filling up faster than the checkpoint interval. stats_reset (NULL = never)
	// gives the window the counters cover, for the average interval.
	sqlMaintCheckpointer = `
SELECT num_timed,
       num_requested,
       COALESCE(write_time, 0)::float8,
       COALESCE(sync_time,  0)::float8,
       COALESCE(buffers_written, 0),
       stats_reset
FROM   pg_stat_checkpointer`

	// sqlMaintSettingsRaw reads pg_settings.setting — the raw value in the GUC's
	// base unit (s, ms, 8kB pages, …) — for the handful of numeric GUCs the
	// overview does arithmetic on. Settings (sqlMaintSettings) carries the
	// human string for display; parsing "5min" back would be silly.
	sqlMaintSettingsRaw = `
SELECT name, setting
FROM   pg_settings
WHERE  name = ANY($1)`

	// sqlMaintSettingBytes converts the memory GUCs to bytes on the server side:
	// pg_size_bytes understands every unit suffix current_setting emits, and
	// passes autovacuum_work_mem's -1 sentinel through unchanged.
	sqlMaintSettingBytes = `
SELECT name, pg_size_bytes(current_setting(name))::text
FROM   pg_settings
WHERE  name = ANY($1)`

	// sqlMaintLongestIdleXact finds the client transaction that has been idle
	// the longest: it holds its snapshot (pinning vacuum's horizon) and its
	// locks while doing nothing. state_change is when it went idle.
	sqlMaintLongestIdleXact = `
SELECT pid,
       COALESCE(application_name, ''),
       EXTRACT(epoch FROM now() - state_change)::float8
FROM   pg_stat_activity
WHERE  state = 'idle in transaction'
  AND  backend_type = 'client backend'
  AND  pid <> pg_backend_pid()
ORDER  BY state_change
LIMIT  1`

	// sqlMaintLongestQuery finds the longest currently executing statement.
	sqlMaintLongestQuery = `
SELECT pid,
       COALESCE(application_name, ''),
       EXTRACT(epoch FROM now() - query_start)::float8,
       COALESCE(left(query, 60), '')
FROM   pg_stat_activity
WHERE  state = 'active'
  AND  backend_type = 'client backend'
  AND  pid <> pg_backend_pid()
ORDER  BY query_start
LIMIT  1`

	// sqlMaintBufSummary is pg_buffercache's cheap one-row aggregate over the
	// buffer pool (pg_buffercache 1.4+): occupancy, dirty and pinned counts and
	// the mean usagecount, without materialising every buffer. Needs pg_monitor.
	sqlMaintBufSummary = `
SELECT buffers_used, buffers_unused, buffers_dirty, buffers_pinned,
       COALESCE(usagecount_avg, 0)::float8
FROM   pg_buffercache_summary()`

	// sqlMaintIOSplit attributes I/O to who did it and why. Client-backend
	// reads are cache misses a query waited for, and read_time/reads their mean
	// latency (the *_time columns stay 0 with track_io_timing off). bulkread is
	// the ring-buffer context large sequential scans use; the vacuum context is
	// autovacuum's share of the traffic. The three write counters show who is
	// flushing dirty buffers: ideally the checkpointer and the bgwriter, not the
	// backends. Relation rows are filtered per column so PG18's object = 'wal'
	// rows (WAL writes and fsyncs, which PG18 moved here from pg_stat_wal) can
	// be read from the same scan; on PG17 those sums are simply zero.
	sqlMaintIOSplit = `
SELECT COALESCE(sum(reads)      FILTER (WHERE object = 'relation'), 0),
       COALESCE(sum(reads)      FILTER (WHERE object = 'relation' AND backend_type = 'client backend'), 0),
       COALESCE(sum(read_time)  FILTER (WHERE object = 'relation' AND backend_type = 'client backend'), 0)::float8,
       COALESCE(sum(reads)      FILTER (WHERE object = 'relation' AND context = 'bulkread'), 0),
       COALESCE(sum(reads)      FILTER (WHERE object = 'relation' AND context = 'vacuum'), 0),
       COALESCE(sum(writes)     FILTER (WHERE object = 'relation' AND context = 'vacuum'), 0),
       COALESCE(sum(writes)     FILTER (WHERE object = 'relation' AND backend_type = 'checkpointer'), 0),
       COALESCE(sum(write_time) FILTER (WHERE object = 'relation' AND backend_type = 'checkpointer'), 0)::float8,
       COALESCE(sum(writes)     FILTER (WHERE object = 'relation' AND backend_type = 'background writer'), 0),
       COALESCE(sum(writes)     FILTER (WHERE object = 'relation' AND backend_type = 'client backend'), 0),
       COALESCE(sum(write_time) FILTER (WHERE object = 'relation' AND backend_type = 'client backend'), 0)::float8,
       COALESCE(sum(writes)     FILTER (WHERE object = 'wal'), 0),
       COALESCE(sum(write_time) FILTER (WHERE object = 'wal'), 0)::float8,
       COALESCE(sum(fsyncs)     FILTER (WHERE object = 'wal'), 0),
       COALESCE(sum(fsync_time) FILTER (WHERE object = 'wal'), 0)::float8
FROM   pg_stat_io`

	// sqlMaintCurrentLSN is the write position as a byte offset, so two samples
	// subtract to the live WAL rate. On a standby pg_current_wal_lsn() raises,
	// so the replay position stands in.
	sqlMaintCurrentLSN = `
SELECT pg_wal_lsn_diff(CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn()
                            ELSE pg_current_wal_lsn() END, '0/0')::bigint`

	// sqlMaintWALDir sums the WAL segments on disk. pg_ls_waldir() needs
	// pg_monitor; failure leaves the row at "n/a".
	sqlMaintWALDir = `
SELECT COALESCE(sum(size), 0)::bigint, count(*)::bigint
FROM   pg_ls_waldir()`

	// sqlMaintAutovacWorkers counts the autovacuum workers running right now,
	// to compare against autovacuum_max_workers.
	sqlMaintAutovacWorkers = `
SELECT count(*)::int
FROM   pg_stat_activity
WHERE  backend_type = 'autovacuum worker'`

	// sqlMaintTablesOverThreshold counts the current database's tables whose
	// dead tuples already exceed their autovacuum trigger (threshold +
	// scale_factor × reltuples, honouring per-table overrides via the shared
	// CTE) and names the three worst. reltuples = -1 marks a never-analyzed
	// table, whose threshold would go negative, so those are left out.
	sqlMaintTablesOverThreshold = sqlVacuumRelSetCTE + `
SELECT count(*)::bigint,
       COALESCE((array_agg(schema_rel ORDER BY dead DESC))[1:3], '{}'::text[])
FROM (
    SELECT PSUT.schemaname || '.' || PSUT.relname AS schema_rel,
           PSUT.n_dead_tup                        AS dead
    FROM   pg_stat_user_tables PSUT
    JOIN   pg_class C  ON C.oid = PSUT.relid
    JOIN   rel_set RS  ON RS.oid = C.oid
    WHERE  C.reltuples >= 0
      AND  PSUT.n_dead_tup > coalesce(RS.rel_av_vac_threshold, current_setting('autovacuum_vacuum_threshold')::bigint)
                           + coalesce(RS.rel_av_vac_scale_factor, current_setting('autovacuum_vacuum_scale_factor')::numeric) * C.reltuples
) t`

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
SELECT datname, temp_files, temp_bytes, stats_reset
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

	// sqlMaintWALStats reads cumulative WAL generation from pg_stat_wal.
	// wal_buffers_full counts how often a backend had to wait for WAL buffer
	// space — a persistent non-zero value means wal_buffers is too small;
	// wal_fpi against wal_records is the full-page-image share. Only the
	// columns PG18 kept are read (it dropped the wal_write/wal_sync timings).
	sqlMaintWALStats = `
SELECT wal_records, wal_fpi, wal_bytes, wal_buffers_full, stats_reset
FROM   pg_stat_wal`

	// sqlMaintBgwriter reads the background writer's own counters. PG17 moved
	// the backend-written count to pg_stat_io (sqlMaintIOSplit); what remains
	// here is the LRU sweep: buffers_clean it wrote, maxwritten_clean the
	// number of sweeps cut short by bgwriter_lru_maxpages.
	sqlMaintBgwriter = `
SELECT COALESCE(buffers_clean, 0), COALESCE(maxwritten_clean, 0), COALESCE(buffers_alloc, 0)
FROM   pg_stat_bgwriter`

	// sqlMaintArchiver reads WAL-archiver health. A last_failed_time newer than
	// last_archived_time is an archiver stuck right now: pg_wal accumulates
	// unarchived segments until it gets through. Both times are nullable and
	// scanned as pointers (never archived / never failed).
	sqlMaintArchiver = `
SELECT archived_count,
       failed_count,
       COALESCE(last_failed_wal, ''),
       last_archived_time,
       last_failed_time
FROM   pg_stat_archiver`

	// sqlAllSettings fetches all pg_settings for the Settings browser.
	// boot_val is the compiled-in default; we compare setting == boot_val to
	// flag non-default values (yellow highlight). display is the value as
	// current_setting() formats it ("8GB", "5min") — with the same reset_val
	// detour as sqlMaintSettings for GUCs pgdu overrides in its own session.
	sqlAllSettings = `
SELECT name,
       COALESCE(setting, ''),
       COALESCE(CASE WHEN setting IS DISTINCT FROM reset_val THEN reset_val
                     ELSE current_setting(name, true) END, '') AS display,
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

	// sqlTableStatsResetAll resets the cumulative table/index/IO counters that
	// back the Table overview (pg_stat_all_tables / pg_statio_all_tables). It
	// zeroes every counter for the *current database* and bumps that database's
	// stats_reset timestamp. Requires pg_monitor (or superuser on older servers).
	sqlTableStatsResetAll = `SELECT pg_stat_reset()`

	// sqlMaintTableStatsReset reads the current database's stats_reset — when
	// pg_stat_reset() last zeroed the table/index/IO counters. NULL (server
	// never reset) maps to the zero time.
	sqlMaintTableStatsReset = `
SELECT stats_reset
FROM   pg_stat_database
WHERE  datname = current_database()`

	// sqlMaintRecovery detects whether this node is a standby.
	sqlMaintRecovery = `SELECT pg_is_in_recovery()`

	// sqlMaintReplication reads streaming-replication standby info from the primary.
	// The query returns no rows when no standbys are connected. ByteLag is the
	// LSN delta between this node's write (or, on a cascading standby, receive)
	// position and the replica's last confirmed replay position; replay_lsn is
	// also returned as an absolute byte offset so two samples give a throughput.
	// pid pairs the walsender with the slot it holds (pg_replication_slots.active_pid).
	sqlMaintReplication = `
SELECT pid,
       application_name,
       COALESCE(client_addr::text, ''),
       COALESCE(state, ''),
       COALESCE(sync_state, ''),
       COALESCE(EXTRACT(epoch FROM write_lag),  0)::float8,
       COALESCE(EXTRACT(epoch FROM flush_lag),  0)::float8,
       COALESCE(EXTRACT(epoch FROM replay_lag), 0)::float8,
       COALESCE(pg_wal_lsn_diff(` + sqlMaintHeadLSN + `, replay_lsn), 0),
       COALESCE(pg_wal_lsn_diff(replay_lsn, '0/0'), 0)
FROM   pg_stat_replication
ORDER  BY sync_state DESC, application_name`

	// sqlMaintReplSlots reads replication slot health. retained_bytes is the
	// amount of WAL that cannot be recycled because of this slot; when it grows
	// large and the slot is inactive, it is a serious disk-space hazard.
	// safe_wal_size is the headroom left before max_slot_wal_keep_size
	// invalidates the slot; NULL (no limit) maps to -1. inactive_since (PG17+)
	// says how long nobody has consumed the slot; 0 while it is active.
	sqlMaintReplSlots = `
SELECT slot_name,
       slot_type,
       active,
       COALESCE(active_pid, 0),
       COALESCE(wal_status, ''),
       COALESCE(pg_wal_lsn_diff(` + sqlMaintHeadLSN + `, restart_lsn), 0),
       COALESCE(safe_wal_size, -1),
       COALESCE(EXTRACT(epoch FROM now() - inactive_since), 0)::float8
FROM   pg_replication_slots
ORDER  BY active DESC, slot_name`

	// sqlMaintHeadLSN is the WAL position lag and retention are measured from:
	// pg_current_wal_lsn() raises an error during recovery, so a standby uses
	// what it has received instead.
	sqlMaintHeadLSN = `CASE WHEN pg_is_in_recovery() THEN pg_last_wal_receive_lsn() ELSE pg_current_wal_lsn() END`

	// sqlMaintWalReceiver reads the standby-side WAL receiver status.
	// Returns no rows on a primary. latest_end_lsn is the last LSN reported
	// to the primary; last_msg_receipt_time tells how stale the stream is.
	sqlMaintWalReceiver = `
SELECT COALESCE(status, ''),
       COALESCE(EXTRACT(epoch FROM (now() - last_msg_receipt_time)), 0)::float8
FROM   pg_stat_wal_receiver
LIMIT  1`

	// sqlMaintTxnStats aggregates commit/rollback/deadlock/conflict totals
	// across all non-template user databases, plus per-day rates of deadlocks
	// and temp bytes. Each database's stats_reset can differ, so the rate is
	// summed per row over that row's own window rather than one grand total
	// over one window; the window is floored at a day so a fresh reset does not
	// extrapolate an hour's burst into a day's rate.
	sqlMaintTxnStats = `
SELECT COALESCE(sum(xact_commit),   0),
       COALESCE(sum(xact_rollback), 0),
       COALESCE(sum(deadlocks),     0),
       COALESCE(sum(conflicts),     0),
       COALESCE(sum(deadlocks  * 86400.0 / GREATEST(EXTRACT(epoch FROM now() - COALESCE(stats_reset, pg_postmaster_start_time())), 86400)), 0)::float8,
       COALESCE(sum(temp_bytes * 86400.0 / GREATEST(EXTRACT(epoch FROM now() - COALESCE(stats_reset, pg_postmaster_start_time())), 86400)), 0)::float8
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
	"max_slot_wal_keep_size",
	"checkpoint_timeout",
	"autovacuum",
	"autovacuum_max_workers",
	"autovacuum_naptime",
	"autovacuum_freeze_max_age",
	"autovacuum_multixact_freeze_max_age",
	"autovacuum_work_mem",
	"autovacuum_vacuum_cost_delay",
	"autovacuum_vacuum_cost_limit",
	"huge_pages",
	"huge_page_size",
	"shared_memory_size_in_huge_pages",
	"wal_buffers",
	"wal_compression",
	"checkpoint_completion_target",
	"bgwriter_lru_maxpages",
	"idle_in_transaction_session_timeout",
	"track_io_timing",
	"track_wal_io_timing",
	"track_functions",
	"track_counts",
	"log_min_duration_statement",
	"log_autovacuum_min_duration",
	"log_checkpoints",
	"log_lock_waits",
	"log_temp_files",
	// Safety and replication knobs the recommendations grade. archive_command
	// and archive_library are superuser-only and simply absent for other roles.
	"fsync",
	"full_page_writes",
	"data_checksums",
	"archive_mode",
	"archive_command",
	"archive_library",
	"synchronous_standby_names",
	"hot_standby_feedback",
	"pg_stat_statements.max",
	"pg_stat_statements.track",
	"pg_stat_statements.track_planning",
	"pg_qualstats.max",
	"pg_qualstats.enabled",
	"pg_qualstats.sample_rate",
	"pg_qualstats.track_constants",
}

// maintSettingBytesKeys are the memory GUCs fetched as bytes (sqlMaintSettingBytes).
var maintSettingBytesKeys = []string{
	"shared_buffers",
	"work_mem",
	"maintenance_work_mem",
	"autovacuum_work_mem",
	"effective_cache_size",
	"wal_buffers",
	"max_wal_size",
	"min_wal_size",
	"max_slot_wal_keep_size",
}

// maintSettingsRawKeys are the numeric GUCs fetched in base units
// (sqlMaintSettingsRaw) and parsed into MaintTuning.
var maintSettingsRawKeys = []string{
	"checkpoint_timeout",
	"checkpoint_completion_target",
	"autovacuum_vacuum_cost_delay",
	"vacuum_cost_delay",
	"autovacuum_vacuum_cost_limit",
	"vacuum_cost_limit",
	"autovacuum_max_workers",
	"shared_memory_size_in_huge_pages",
	"bgwriter_lru_maxpages",
	"idle_in_transaction_session_timeout",
	"commit_timestamp_buffers",
	"multixact_member_buffers",
	"multixact_offset_buffers",
	"notify_buffers",
	"serializable_buffers",
	"subtransaction_buffers",
	"transaction_buffers",
}

// slruBufferGUCs are the PG17+ per-SLRU sizing GUCs (in blocks), parsed into
// MaintTuning.SLRUBuffers.
var slruBufferGUCs = []string{
	"commit_timestamp_buffers", "multixact_member_buffers", "multixact_offset_buffers",
	"notify_buffers", "serializable_buffers", "subtransaction_buffers", "transaction_buffers",
}
