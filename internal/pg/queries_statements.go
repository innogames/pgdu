package pg

// sqlStatements reads the pg_stat_statements 1.11 column set for the current
// database only. The 1.11 names (shared_blk_read_time etc., renamed from the
// pre-17 blk_read_time) work on PG17/18; we avoid the 1.12-only
// parallel_workers_* columns so the same query runs on both. wal_bytes is a
// numeric in the catalog — cast to bigint so it scans into int64. Rows whose
// queryid is NULL (text unavailable / insufficient privilege) are skipped.
const sqlStatements = `
SELECT queryid, userid, dbid, query,
       calls, rows,
       total_exec_time, min_exec_time, max_exec_time, mean_exec_time, stddev_exec_time,
       plans, total_plan_time,
       shared_blks_hit, shared_blks_read, shared_blks_dirtied, shared_blks_written,
       local_blks_hit, local_blks_read, local_blks_dirtied, local_blks_written,
       temp_blks_read, temp_blks_written,
       shared_blk_read_time, shared_blk_write_time,
       local_blk_read_time, local_blk_write_time,
       temp_blk_read_time, temp_blk_write_time,
       wal_records, wal_fpi, wal_bytes::bigint
FROM   pg_stat_statements
WHERE  dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND  queryid IS NOT NULL
  -- Hide pgdu's own footprints so the tool doesn't watch itself. These also
  -- match the rare user query against the stats catalogs / a hand-run
  -- GENERIC_PLAN, which is an acceptable trade-off in this view. New pgdu
  -- queries are suppressed at the source via SET pg_stat_statements.track=none
  -- (see Client.PoolFor); this filter covers rows recorded before that and
  -- unprivileged sessions where the SET is not permitted.
  AND  query NOT LIKE '%pg_stat_statements%'
  AND  query NOT LIKE '%pg_available_extensions%'
  AND  query NOT LIKE 'EXPLAIN (GENERIC_PLAN%'
  AND  query NOT LIKE 'PREPARE pgdu_infer_params%'
`
