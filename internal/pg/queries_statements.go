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

// sqlStatementsDBCount counts the distinct statements pg_stat_statements currently
// tracks for this database — the size a snapshot taken right now would have.
// The snapshot browser uses it to bar the live "now" / "session start" anchors
// on the same scale as the saved snapshots' query counts, so it deliberately
// skips the query-text filters of statementsQuery (a handful of rows either way).
const sqlStatementsDBCount = `
SELECT count(DISTINCT queryid)
FROM   pg_stat_statements
WHERE  dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND  queryid IS NOT NULL`

// statementsQuery builds the snapshot SQL for the current database, selecting the
// I/O-timing columns under the names the installed extension version actually
// has. The block-count columns are stable across versions, but the timing
// columns changed:
//   - < 1.10: only blk_read_time / blk_write_time (no temp- or local-block timing)
//   - 1.10:   adds temp_blk_{read,write}_time
//   - 1.11:   renames blk_*_time → shared_blk_*_time and adds local_blk_*_time
//
// 1.10 also added toplevel, which the WHERE clause filters on from that
// version up. Under pg_stat_statements.track = all the server records a
// nested statement *and* its top-level caller, whose counters already include
// it; summing both into one queryid row double-counts the time and inflates
// every share computed from it. Below 1.10 the column does not exist and the
// setting it guards against is not available either, so the filter is simply
// left out.
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
	topLevelFilter := "  AND  toplevel\n"
	if !statementsAtLeast(major, minor, 1, 10) {
		temp = "0::float8 AS temp_blk_read_time, 0::float8 AS temp_blk_write_time"
		topLevelFilter = ""
	}
	// Plain concatenation rather than fmt.Sprintf: the WHERE clause's LIKE
	// patterns contain literal % characters that Sprintf would treat as verbs.
	inner := sqlStatementsHead +
		"       " + shared + ",\n" +
		"       " + local + ",\n" +
		"       " + temp + ",\n" +
		sqlStatementsTail + topLevelFilter
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

// sqlStatementsNewer reads the counters newer servers and newer extension
// versions added, through to_jsonb — CLAUDE.md's second version tool. A key
// the row does not have yields NULL instead of failing the statement, so one
// query still runs on PG17 and PG18 alike and nothing has to be gated on a
// version. That matters more than usual here because the skew is in the
// *extension*, not the server: a PG17 cluster pg_upgraded without an ALTER
// EXTENSION still carries 1.10's column set on 18's server.
//
// NULL therefore means "this server has no such counter", which is not zero,
// which is why every one of these scans into a pointer.
//
// jit_total_time COALESCEs its parts because they arrived in two waves —
// generation/inlining/optimization/emission in 1.9, deform in 1.11 — so a
// plain sum would go NULL on 1.10 although four fifths of it is there.
// jit_functions is the gate instead: present exactly when JIT accounting is.
//
// min/max_plan_time are deliberately left out. They are extrema, so the window
// diff would zero them the way it zeroes the exec extrema, and they would read
// as "—" on every row anyone is actually looking at.
const sqlStatementsNewer = `
       (to_jsonb(s) ->> 'jit_functions')::bigint              AS jit_functions,
       (COALESCE((to_jsonb(s) ->> 'jit_generation_time')::float8, 0)
      + COALESCE((to_jsonb(s) ->> 'jit_inlining_time')::float8, 0)
      + COALESCE((to_jsonb(s) ->> 'jit_optimization_time')::float8, 0)
      + COALESCE((to_jsonb(s) ->> 'jit_emission_time')::float8, 0)
      + COALESCE((to_jsonb(s) ->> 'jit_deform_time')::float8, 0))  AS jit_total_time,
       (to_jsonb(s) ->> 'stats_since')::timestamptz           AS stats_since,
       (to_jsonb(s) ->> 'wal_buffers_full')::bigint           AS wal_buffers_full,
       (to_jsonb(s) ->> 'parallel_workers_to_launch')::bigint AS parallel_workers_to_launch,
       (to_jsonb(s) ->> 'parallel_workers_launched')::bigint  AS parallel_workers_launched,
       (to_jsonb(s) ->> 'generic_plan_calls')::bigint         AS generic_plan_calls,
       (to_jsonb(s) ->> 'custom_plan_calls')::bigint          AS custom_plan_calls
`

const sqlStatementsTail = `       wal_records, wal_fpi, wal_bytes::bigint,
` + sqlStatementsNewer + `
FROM   pg_stat_statements s
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
// mean (per-row means aren't additive) and min/max collapse with min()/max().
//
// stddev_exec_time is the pooled standard deviation of the union of the rows,
// which *is* recoverable from each row's (calls, mean, stddev): E[x²] of a
// mixture is the calls-weighted mean of each part's (σ² + μ²), and the pooled
// variance is that minus the square of the pooled mean. GREATEST(…, 0) guards
// the float subtraction from landing a hair below zero on identical rows.
// (The window diff still zeroes it — extrema and spreads aren't subtractable —
// so it carries a value on the cumulative anchor and on statements new since
// the baseline.) userid/dbid are unused downstream and emit 0.
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
       sqrt(GREATEST(
              sum(calls * (stddev_exec_time ^ 2 + mean_exec_time ^ 2)) / GREATEST(sum(calls), 1)
            - (sum(mean_exec_time * calls) / GREATEST(sum(calls), 1)) ^ 2,
            0))::float8 AS stddev_exec_time,
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
       sum(wal_bytes)::bigint AS wal_bytes,
       -- sum() over all-NULL is NULL, which is exactly the signal wanted: the
       -- counter does not exist here, as distinct from existing and being 0.
       sum(jit_functions)::bigint AS jit_functions,
       sum(jit_total_time) AS jit_total_time,
       -- An entry's stats_since is per (userid, queryid); the newest is the one
       -- that matters, because it is the one that can postdate a baseline.
       max(stats_since) AS stats_since,
       sum(wal_buffers_full)::bigint AS wal_buffers_full,
       sum(parallel_workers_to_launch)::bigint AS parallel_workers_to_launch,
       sum(parallel_workers_launched)::bigint AS parallel_workers_launched,
       sum(generic_plan_calls)::bigint AS generic_plan_calls,
       sum(custom_plan_calls)::bigint AS custom_plan_calls
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
// qualid/uniquequalid tie the quals of one AND expression together, the latter
// per constant combination, so a sample call can reuse values that co-occurred.
const sqlQualstatsSamples = `
SELECT COALESCE(c.relname, '')  AS relation,
       COALESCE(a.attname, '')  AS column,
       COALESCE(o.oprname, '')  AS operator,
       q.constvalue,
       COALESCE(q.constant_position, 0),
       q.occurences,
       COALESCE(q.qualid::bigint, 0),
       COALESCE(q.uniquequalid::bigint, 0)
FROM   pg_qualstats q
LEFT JOIN pg_class     c ON c.oid = q.lrelid
LEFT JOIN pg_attribute a ON a.attrelid = q.lrelid AND a.attnum = q.lattnum
LEFT JOIN pg_operator  o ON o.oid = q.opno
WHERE  q.queryid = $1
  AND  q.constvalue IS NOT NULL
ORDER BY q.occurences DESC, q.constvalue
LIMIT  50
`

// sqlQualstatsQualCount counts every qual pg_qualstats tracked for a queryid,
// including the ones it captured no constant for. A non-zero count with no
// captured constants is the bind-parameter case: under a generic plan the
// qual's right-hand side is a Param, so pg_qualstats records the predicate but
// has nothing to deparse (constvalue and constant_position both NULL). That
// tells "nothing tracked yet" from "tracked, but never with a literal".
const sqlQualstatsQualCount = `SELECT count(*) FROM pg_qualstats WHERE queryid = $1`

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
