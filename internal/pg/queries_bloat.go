package pg

// Detect whether pgstattuple_approx is both present and executable by this
// user — both are needed for the cheap sampling path. to_regprocedure yields
// NULL for a function that isn't there and has_function_privilege is strict,
// so the expression collapses to NULL rather than raising: the text-signature
// form would error on a missing function before any AND could help, and SQL
// guarantees no short-circuit anyway. Resolution goes through search_path,
// which is the right test — FillBloat calls pgstattuple_approx unqualified
// too, so a copy pgdu cannot resolve here is one it could not call either.
const sqlBloatProbe = `
SELECT COALESCE(has_function_privilege(current_user,
           to_regprocedure('pgstattuple_approx(regclass)')::oid, 'EXECUTE'),
       false) AS available
`

// pgstattuple_approx returns approx_free_percent, approx_free_space,
// dead_tuple_count, dead_tuple_len, etc. We treat dead_tuple_len + approx_free_space
// as "wasted" — same definition used by pg_repack et al. It takes a heap,
// which a toast table is too.
//
// $2 bounds what the call is allowed to read. pgstattuple_approx does not scan
// the whole relation: it skips pages the visibility map marks all-visible and
// takes their free space from the FSM, so what it costs is the *not*
// all-visible part — which pg_class already records, for free. Gating on that
// rather than on the relation's size is what lets a 70 GB toast table vacuum
// has just been through be measured as cheaply as a small one, while a
// smaller but entirely dirty relation is still refused.
//
// relpages = 0 is a relation nobody has vacuumed or analyzed: there is no
// visibility map worth trusting, so it counts as dirty in full. The guard sits
// in WHERE, which is evaluated before the target list, so a refused relation
// never calls the function at all — it returns no row, which the caller reads
// as "unmeasured", exactly as it reads an error.
const sqlBloatHeapApprox = `
SELECT (SELECT (dead_tuple_len + approx_free_space)::bigint
        FROM   pgstattuple_approx(t.oid)) AS wasted_bytes
FROM   (SELECT c.oid,
               CASE WHEN c.relpages > 0
                    THEN GREATEST(c.relpages - c.relallvisible, 0)::bigint
                         * current_setting('block_size')::bigint
                    ELSE pg_relation_size(c.oid) END AS dirty_bytes
        FROM   pg_class c
        WHERE  c.oid = $1::regclass) t
WHERE  t.dirty_bytes <= $2::bigint
`

// sqlBloatIndexFull measures an index that has no cheap density to read.
// pgstatindex only understands btree; for GIN, GiST and hash this walks the
// whole index, which is why the caller keeps it under the same size cap and
// per-part timeout as the heap sample. "Wasted" is defined as it is for the
// heap — dead entries plus free space — so the two columns mean the same
// thing. BRIN and SP-GiST are not supported and raise, which the best-effort
// path turns into an unmeasured row.
const sqlBloatIndexFull = `
SELECT (dead_tuple_len + free_space)::bigint AS wasted_bytes
FROM   pgstattuple($1::regclass)
`

// sqlGinPendingPages reads a GIN index's pending list: entries inserted but not
// yet merged into the main structure. This is a metapage read, not a scan. It
// is not bloat — the pages are live and will be folded in by the next vacuum or
// autoanalyze — but a pending list that keeps growing means insertions are
// outrunning that merge, which is worth seeing next to the index's size.
const sqlGinPendingPages = `
SELECT pending_pages::bigint FROM pgstatginindex($1::regclass)
`

const sqlBloatIndex = `
SELECT (CASE
          WHEN avg_leaf_density IS NULL OR avg_leaf_density = 0 THEN 0
          ELSE ((100.0 - avg_leaf_density) / 100.0) * pg_relation_size($1::regclass)
        END)::bigint AS wasted_bytes
FROM   pgstatindex($1::regclass)
`

// sqlBloatIndexEstimate is the index sibling of sqlBloatHeapEstimate: the
// statistics estimate a btree falls back to when the page walk is too
// expensive to run (or pgstattuple is not there at all). It is the bloat_index
// diagnostic's estimator narrowed to one index — see bloatIndexEstimateHead
// (queries_diag_index.go) — so the two screens agree.
//
// $1 is the index, as a qualified name cast to regclass. No row comes back for
// an index the estimator cannot speak about — not btree, relpages 0, no
// pg_stats row for an indexed column — and reltuples < 0 (never analyzed)
// is excluded here rather than estimated from a tuple count of -1, since the
// caller renders "no number" as a blank cell and that is the honest answer.
var sqlBloatIndexEstimate = bloatIndexEstimateHead +
	"      AND indexclass.oid = $1::regclass\n" +
	bloatIndexEstimateRest[1:] + `
SELECT (CASE WHEN relpages <= expected THEN 0
             ELSE bs * (relpages - expected)::bigint END)::bigint AS wasted_bytes
FROM   index_aligned_est
WHERE  reltuples >= 0
`

// sqlFillFactorExpr reads a heap's FILLFACTOR out of reloptions, defaulting to
// the 100 that an absent option means. Every bloat estimator divides the
// per-page capacity by it, because space a FILLFACTOR deliberately reserves for
// HOT updates is not waste: a correctly tuned fillfactor=85 table read as 15%
// bloated, which is exactly the share it was told to keep free. The relation
// must be in scope as c.
const sqlFillFactorExpr = `COALESCE((SELECT o.option_value::numeric
                FROM pg_options_to_table(c.reloptions) AS o
                WHERE o.option_name = 'fillfactor'), 100)`

// Heap bloat estimation fallback. Uses pg_class.reltuples and per-column
// avg_width from pg_stats to estimate the minimum on-disk size, then reports
// the gap to the actual size. Coarse but free, and good enough to flag
// pathologically bloated tables. Returns 0 when stats are missing or the
// table is empty. Header constants follow the ioguix bloat query (BSD).
const sqlBloatHeapEstimate = `
WITH params AS (
  SELECT current_setting('block_size')::numeric AS bs, 24::numeric AS hdr, 8::numeric AS ma
),
target AS (
  SELECT c.oid, c.relpages::numeric AS pages, c.reltuples::numeric AS tuples,
         n.nspname, c.relname, ` + sqlFillFactorExpr + ` AS fillfactor
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.oid = $1
),
stats AS (
  SELECT COALESCE(SUM((1 - null_frac) * avg_width), 0)::numeric AS datawidth
  FROM pg_stats s, target t
  WHERE s.schemaname = t.nspname AND s.tablename = t.relname
)
SELECT GREATEST(0,
  (t.pages * p.bs)::bigint
  - CEIL(t.tuples * (p.hdr + s.datawidth +
      p.ma - (CASE WHEN (p.hdr + s.datawidth)::int % p.ma::int = 0
                   THEN p.ma ELSE (p.hdr + s.datawidth)::int % p.ma::int END))
    / NULLIF((p.bs - 24) * t.fillfactor / 100, 0))::bigint * p.bs::bigint
)::bigint AS wasted_bytes
FROM target t, params p, stats s
`

// sqlBloatTablesEstimate is the set-returning sibling of sqlBloatHeapEstimate:
// one estimate per table in a schema, for the bloat column on the tables list.
//
// It deliberately reuses that estimator rather than the richer ioguix one
// behind the bloat_table diagnostic. The diagnostic models the null header and
// so lands on a slightly different number; running it here would mean the
// tables list and the parts screen quoting different bloat for the same table,
// which is worse than either being a little off. It is also whole-database and
// threshold-filtered, where this screen needs one schema and every row.
//
// can_estimate is false where the inputs aren't there — a table with no
// pg_stats rows (never analyzed, or no column this role may read) and a
// partitioned parent, whose relpages is 0 because the storage is in its
// partitions. Those rows report unknown rather than a confident zero.
const sqlBloatTablesEstimate = `
WITH params AS (
  SELECT current_setting('block_size')::numeric AS bs, 24::numeric AS hdr, 8::numeric AS ma
),
widths AS (
  SELECT tablename, SUM((1 - null_frac) * avg_width)::numeric AS datawidth
  FROM   pg_stats
  WHERE  schemaname = $1
  GROUP  BY tablename
)
SELECT c.oid,
       c.relname,
       (w.datawidth IS NOT NULL AND c.relpages > 0) AS can_estimate,
       COALESCE(GREATEST(0,
         (c.relpages::numeric * p.bs)::bigint
         - CEIL(GREATEST(c.reltuples, 0) * (p.hdr + w.datawidth +
             p.ma - (CASE WHEN (p.hdr + w.datawidth)::int % p.ma::int = 0
                          THEN p.ma ELSE (p.hdr + w.datawidth)::int % p.ma::int END))
           / NULLIF((p.bs - 24) * ` + sqlFillFactorExpr + ` / 100, 0))::bigint * p.bs::bigint
       ), 0)::bigint AS wasted_bytes
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
CROSS  JOIN params p
LEFT   JOIN widths w ON w.tablename = c.relname
WHERE  n.nspname = $1
  AND  c.relkind IN ('r','m','p')
`
