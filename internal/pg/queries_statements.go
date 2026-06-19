package pg

// sqlStatementsVersion reads the installed pg_stat_statements extension version
// (e.g. "1.10", "1.11"). This is independent of the server version: a PG17
// cluster that was pg_upgraded but never ran `ALTER EXTENSION ... UPDATE` still
// carries the older extension and its older I/O-timing column names.
const sqlStatementsVersion = `SELECT extversion FROM pg_extension WHERE extname = 'pg_stat_statements'`

// sqlStatementsDefaultVersion reads the version CREATE/ALTER EXTENSION would
// install from the on-disk extension files (independent of what's currently
// installed). Used to tell the user what an `ALTER EXTENSION ... UPDATE` would
// lift an outdated extension to. Empty when the extension isn't on the server.
const sqlStatementsDefaultVersion = `SELECT COALESCE(default_version, '') FROM pg_available_extensions WHERE name = 'pg_stat_statements'`

// sqlStatementsInfo reads the last time pg_stat_statements counters were reset
// (pg_stat_statements_info, PG14+). A snapshot persisted to disk records this so
// that a later diff can detect a reset in between — which would make the cumulative
// counters smaller than the baseline and the delta meaningless.
const sqlStatementsInfo = `SELECT stats_reset FROM pg_stat_statements_info`

// sqlSampleTableColumns resolves a relation reference (schema-qualified or bare,
// as parsed from a statement) to its schema, name and live column list, so a
// sample-value query can be built from trusted catalog identifiers. to_regclass
// returns NULL for an unknown name, yielding no row (best-effort callers treat
// that as "give up and synthesize").
const sqlSampleTableColumns = `
SELECT n.nspname, c.relname,
       coalesce(array_agg(a.attname) FILTER (WHERE a.attnum > 0 AND NOT a.attisdropped), '{}')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = c.oid
WHERE c.oid = to_regclass($1)
GROUP BY n.nspname, c.relname`

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
//
// pg_stat_statements rows are keyed by (userid, dbid, queryid); even after the
// dbid filter the same queryid appears once per role that ran it. The inner
// select is wrapped in a SUM…GROUP BY queryid so each statement is one row —
// otherwise the window baseline (keyed by queryid alone, see DiffStatements)
// would collide across roles and report near-cumulative deltas for shared
// statements like BEGIN/COMMIT/version(). userid/dbid are meaningless once
// summed (nothing downstream reads them) and emit 0.
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
	inner := sqlStatementsHead +
		"       " + shared + ",\n" +
		"       " + local + ",\n" +
		"       " + temp + ",\n" +
		sqlStatementsTail
	return sqlStatementsAggHead + inner + sqlStatementsAggTail
}

// statementsMinMajor/Minor is the oldest pg_stat_statements version whose
// columns statementsQuery can scan: 1.8 (PG13) is where total_time became
// total_exec_time and plans/total_plan_time/wal_* were added — all of which the
// query selects unconditionally. Below it the query fails with "column
// total_exec_time does not exist"; StatementSnapshot detects that up front and
// returns an *OutdatedExtensionError instead.
const (
	statementsMinMajor = 1
	statementsMinMinor = 8
)

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

// sqlStatementsAggHead / sqlStatementsAggTail wrap the per-(userid,queryid) inner
// select and collapse it to one row per queryid. The column list and order match
// StatementSnapshot's Scan exactly. Integer counters are summed and cast back to
// bigint (sum() of bigint is numeric, which won't scan into int64); timing/exec
// floats sum to double precision. mean_exec_time is recomputed as a calls-weighted
// mean (per-row means aren't additive); min/max collapse with min()/max(); stddev
// can't be combined across rows, so it's emitted as 0 (the window diff zeroes
// min/max/stddev anyway). userid/dbid are unused downstream and emit 0.
const sqlStatementsAggHead = `
SELECT queryid,
       0::oid AS userid,
       0::oid AS dbid,
       min(query) AS query,
       sum(calls)::bigint AS calls,
       sum(rows)::bigint AS rows,
       sum(total_exec_time) AS total_exec_time,
       min(min_exec_time) AS min_exec_time,
       max(max_exec_time) AS max_exec_time,
       (sum(mean_exec_time * calls) / GREATEST(sum(calls), 1))::float8 AS mean_exec_time,
       0::float8 AS stddev_exec_time,
       sum(plans)::bigint AS plans,
       sum(total_plan_time) AS total_plan_time,
       sum(shared_blks_hit)::bigint AS shared_blks_hit,
       sum(shared_blks_read)::bigint AS shared_blks_read,
       sum(shared_blks_dirtied)::bigint AS shared_blks_dirtied,
       sum(shared_blks_written)::bigint AS shared_blks_written,
       sum(local_blks_hit)::bigint AS local_blks_hit,
       sum(local_blks_read)::bigint AS local_blks_read,
       sum(local_blks_dirtied)::bigint AS local_blks_dirtied,
       sum(local_blks_written)::bigint AS local_blks_written,
       sum(temp_blks_read)::bigint AS temp_blks_read,
       sum(temp_blks_written)::bigint AS temp_blks_written,
       sum(shared_blk_read_time) AS shared_blk_read_time,
       sum(shared_blk_write_time) AS shared_blk_write_time,
       sum(local_blk_read_time) AS local_blk_read_time,
       sum(local_blk_write_time) AS local_blk_write_time,
       sum(temp_blk_read_time) AS temp_blk_read_time,
       sum(temp_blk_write_time) AS temp_blk_write_time,
       sum(wal_records)::bigint AS wal_records,
       sum(wal_fpi)::bigint AS wal_fpi,
       sum(wal_bytes)::bigint AS wal_bytes
FROM (`

const sqlStatementsAggTail = `
) ss
GROUP BY queryid`

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

// sqlTableHotStats reads the cumulative update / HOT-update counters for one
// relation (resolved by optionally schema-qualified name) from
// pg_stat_user_tables. The statement-detail view uses it to show the HOT update
// ratio of a query's main table. to_regclass resolves the name via search_path
// and yields NULL (→ no row) for an unknown or non-user relation.
const sqlTableHotStats = `
SELECT n_tup_upd, n_tup_hot_upd
FROM pg_stat_user_tables
WHERE relid = to_regclass($1)
`
