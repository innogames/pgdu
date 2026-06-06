package pg

// sqlStatementsVersion reads the installed pg_stat_statements extension version
// (e.g. "1.10", "1.11"). This is independent of the server version: a PG17
// cluster that was pg_upgraded but never ran `ALTER EXTENSION ... UPDATE` still
// carries the older extension and its older I/O-timing column names.
const sqlStatementsVersion = `SELECT extversion FROM pg_extension WHERE extname = 'pg_stat_statements'`

// statementsQuery builds the snapshot SQL for the current database, selecting the
// I/O-timing columns under the names the installed extension version actually
// has. The block-count columns are stable across versions, but the timing
// columns changed:
//   - < 1.10: only blk_read_time / blk_write_time (no temp- or local-block timing)
//   - 1.10:   adds temp_blk_{read,write}_time
//   - 1.11:   renames blk_*_time → shared_blk_*_time and adds local_blk_*_time
//
// We always alias to the 1.11 names so StatementSnapshot's Scan order is fixed;
// columns missing in older versions are selected as 0 (typed double precision so
// they scan into float64). We avoid the 1.12-only parallel_workers_* columns so
// the same query runs on PG17/18 and newer. wal_bytes is a numeric in the
// catalog — cast to bigint so it scans into int64. Rows whose queryid is NULL
// (text unavailable / insufficient privilege) are skipped.
func statementsQuery(major, minor int) string {
	shared := "shared_blk_read_time, shared_blk_write_time"
	local := "local_blk_read_time, local_blk_write_time"
	temp := "temp_blk_read_time, temp_blk_write_time"
	if !statementsAtLeast(major, minor, 1, 11) {
		shared = "blk_read_time AS shared_blk_read_time, blk_write_time AS shared_blk_write_time"
		local = "0::float8 AS local_blk_read_time, 0::float8 AS local_blk_write_time"
	}
	if !statementsAtLeast(major, minor, 1, 10) {
		temp = "0::float8 AS temp_blk_read_time, 0::float8 AS temp_blk_write_time"
	}
	// Plain concatenation rather than fmt.Sprintf: the WHERE clause's LIKE
	// patterns contain literal % characters that Sprintf would treat as verbs.
	return sqlStatementsHead +
		"       " + shared + ",\n" +
		"       " + local + ",\n" +
		"       " + temp + ",\n" +
		sqlStatementsTail
}

// statementsAtLeast reports whether extension version major.minor is >= want.
func statementsAtLeast(major, minor, wantMajor, wantMinor int) bool {
	if major != wantMajor {
		return major > wantMajor
	}
	return minor >= wantMinor
}

const sqlStatementsHead = `
SELECT queryid, userid, dbid, query,
       calls, rows,
       total_exec_time, min_exec_time, max_exec_time, mean_exec_time, stddev_exec_time,
       plans, total_plan_time,
       shared_blks_hit, shared_blks_read, shared_blks_dirtied, shared_blks_written,
       local_blks_hit, local_blks_read, local_blks_dirtied, local_blks_written,
       temp_blks_read, temp_blks_written,
`

const sqlStatementsTail = `       wal_records, wal_fpi, wal_bytes::bigint
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
  AND  query NOT LIKE '%pg_qualstats%'
  AND  query NOT LIKE 'EXPLAIN (GENERIC_PLAN%'
  AND  query NOT LIKE 'EXPLAIN (VERBOSE, FORMAT TEXT)%'
  AND  query NOT LIKE 'PREPARE pgdu_infer_params%'
`

// sqlQualstatsExample reconstructs one real example query (with real literal
// constants) for a queryid, using the constants pg_qualstats captured. Returns
// NULL when nothing has been sampled for that queryid yet.
const sqlQualstatsExample = `SELECT pg_qualstats_example_query($1)`

// sqlQualstatsSamples lists the real predicate constants pg_qualstats captured
// for a queryid, most-frequent first. With pg_qualstats.track_constants on,
// each distinct value is its own row; constvalue is a cast-carrying literal
// (e.g. 'line 1'::text). The LEFT JOINs resolve the left-hand column/operator
// for display and are NULL-safe for quals whose left side isn't a plain column.
// We read the pg_qualstats view (one row per captured qual occurrence).
const sqlQualstatsSamples = `
SELECT COALESCE(c.relname, '')  AS relation,
       COALESCE(a.attname, '')  AS column,
       COALESCE(o.oprname, '')  AS operator,
       q.constvalue,
       COALESCE(q.constant_position, 0),
       q.occurences
FROM   pg_qualstats q
LEFT JOIN pg_class     c ON c.oid = q.lrelid
LEFT JOIN pg_attribute a ON a.attrelid = q.lrelid AND a.attnum = q.lattnum
LEFT JOIN pg_operator  o ON o.oid = q.opno
WHERE  q.queryid = $1
  AND  q.constvalue IS NOT NULL
ORDER BY q.occurences DESC, q.constvalue
LIMIT  50
`
