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
// as "wasted" — same definition used by pg_repack et al.
const sqlBloatHeapApprox = `
SELECT (dead_tuple_len + approx_free_space)::bigint AS wasted_bytes
FROM   pgstattuple_approx($1::regclass)
`

const sqlBloatIndex = `
SELECT (CASE
          WHEN avg_leaf_density IS NULL OR avg_leaf_density = 0 THEN 0
          ELSE ((100.0 - avg_leaf_density) / 100.0) * pg_relation_size($1::regclass)
        END)::bigint AS wasted_bytes
FROM   pgstatindex($1::regclass)
`

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
         n.nspname, c.relname
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
    / NULLIF(p.bs - 24, 0))::bigint * p.bs::bigint
)::bigint AS wasted_bytes
FROM target t, params p, stats s
`
