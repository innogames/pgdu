package pg

// All read-only SQL pgdu issues. Centralized so they can be audited and
// adjusted in one place.

const sqlDatabases = `
SELECT d.datname,
       pg_database_size(d.datname) AS size_bytes
FROM   pg_database d
WHERE  d.datistemplate = false
  AND  has_database_privilege(current_user, d.datname, 'CONNECT')
ORDER  BY size_bytes DESC
`

const sqlSchemas = `
SELECT n.nspname,
       COALESCE(SUM(pg_total_relation_size(c.oid)), 0)::bigint AS size_bytes,
       COUNT(c.oid) FILTER (WHERE c.relkind IN ('r','m','p'))    AS table_count
FROM   pg_namespace n
LEFT   JOIN pg_class c
       ON c.relnamespace = n.oid
      AND c.relkind IN ('r','m','p')
WHERE  n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
  AND  n.nspname NOT LIKE 'pg_temp_%'
  AND  n.nspname NOT LIKE 'pg_toast_temp_%'
GROUP  BY n.nspname
ORDER  BY size_bytes DESC
`

// toast_bytes uses pg_total_relation_size on the TOAST relation so it
// covers the toast main fork *and* its index and FSM/VM — what users mean
// by "how much does TOAST cost on disk". pg_relation_size alone reports
// only the toast main fork, which under-counts (and reads as 0 whenever
// the toast file was never written, even when the toast index has pages).
const sqlTables = `
SELECT c.oid,
       c.relname,
       pg_relation_size(c.oid)                                AS heap_bytes,
       pg_indexes_size(c.oid)                                 AS indexes_bytes,
       COALESCE(pg_total_relation_size(c.reltoastrelid), 0)   AS toast_bytes,
       pg_total_relation_size(c.oid)                          AS total_bytes,
       c.reltuples::bigint                                    AS est_rows,
       COALESCE(c.reltoastrelid, 0)::oid                      AS toast_oid,
       COALESCE((SELECT 'pg_toast.' || tc.relname
                 FROM pg_class tc
                 WHERE tc.oid = c.reltoastrelid), '')         AS toast_name
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  n.nspname = $1
  AND  c.relkind IN ('r','m','p')
ORDER  BY total_bytes DESC
`

// Per-table autovacuum/analyze counters. Joined onto the heap row of the
// parts view so users can see "is this table being kept clean?" alongside its
// size and bloat. pg_stat_all_tables also covers tables outside the default
// search_path; matviews are absent and the LEFT JOIN at the call site yields
// nil HeapStats for them.
const sqlHeapStats = `
SELECT COALESCE(n_live_tup, 0)::bigint,
       COALESCE(n_dead_tup, 0)::bigint,
       last_vacuum,
       last_autovacuum,
       last_analyze,
       last_autoanalyze
FROM   pg_stat_all_tables
WHERE  relid = $1
`

const sqlIndexes = `
SELECT i.oid,
       i.relname               AS index_name,
       pg_relation_size(i.oid) AS size_bytes,
       idx.indisprimary,
       idx.indisunique,
       am.amname               AS access_method
FROM   pg_index idx
JOIN   pg_class i ON i.oid = idx.indexrelid
JOIN   pg_am am ON am.oid = i.relam
WHERE  idx.indrelid = $1
ORDER  BY size_bytes DESC
`

// Per-column space estimate from planner statistics. Cheap: single pg_stats
// / pg_attribute lookup, no table scan. avg_width × (1 − null_frac) ×
// reltuples approximates the heap bytes occupied by each column. avg_width
// already reflects on-disk size (with TOAST compression accounted for by
// ANALYZE's sampling), so a fat bytea column correctly dominates.
// Accuracy is bounded by ANALYZE freshness.
const sqlColumns = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod)               AS type_name,
       COALESCE(s.avg_width, 0)::int                      AS avg_width,
       COALESCE(s.null_frac, 0)::float8                   AS null_frac,
       (COALESCE(s.avg_width, 0) *
        (1 - COALESCE(s.null_frac, 0)) *
        GREATEST(c.reltuples, 0))::bigint                 AS est_bytes,
       (a.attstorage IN ('e','x') AND c.reltoastrelid <> 0) AS toastable
FROM   pg_attribute a
JOIN   pg_class c     ON c.oid = a.attrelid
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN pg_stats s
       ON s.schemaname = n.nspname
      AND s.tablename  = c.relname
      AND s.attname    = a.attname
WHERE  a.attrelid = $1
  AND  a.attnum  > 0
  AND  NOT a.attisdropped
ORDER  BY est_bytes DESC NULLS LAST, a.attnum
`

// Detect whether the pgstattuple extension is installed AND the current user
// has EXECUTE privilege on pgstattuple_approx — both are needed for the cheap
// sampling path.
const sqlBloatProbe = `
SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pgstattuple')
       AND has_function_privilege(current_user,
             'pgstattuple_approx(regclass)', 'EXECUTE') AS available
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

// Crude btree index bloat estimate based on free space % from pgstatindex
// fallback: use 10% as a placeholder when no extension is available.
const sqlBloatIndexEstimate = `
SELECT (pg_relation_size($1::regclass) * 0.10)::bigint AS wasted_bytes
`

// --- shared-buffers view ---

// sqlExtensionProbe returns two booleans: whether the named extension is
// installed in the current database, and whether it is available on the
// server (i.e. CREATE EXTENSION would resolve it). The second column lets
// the TUI offer an interactive install when the extension is reachable.
const sqlExtensionProbe = `
SELECT EXISTS(SELECT 1 FROM pg_extension          WHERE extname = $1) AS installed,
       EXISTS(SELECT 1 FROM pg_available_extensions WHERE name = $1)  AS available
`

// sqlBufferCacheSummary reports cluster-wide shared_buffers occupancy split
// into three buckets: pages owned by the database the user is browsing, pages
// owned by anything else (other databases plus shared catalogs), and free
// pages. Total = COUNT(*) × block_size is the exact configured shared_buffers
// size in bytes (rounded to block boundaries).
const sqlBufferCacheSummary = `
WITH this_db AS (SELECT oid FROM pg_database WHERE datname = current_database())
SELECT
  COUNT(*) * current_setting('block_size')::int                              AS total_bytes,
  COUNT(*) FILTER (WHERE reldatabase = (SELECT oid FROM this_db))
    * current_setting('block_size')::int                                     AS this_db_bytes,
  COUNT(*) FILTER (
    WHERE relfilenode IS NOT NULL
      AND reldatabase IS DISTINCT FROM (SELECT oid FROM this_db)
  ) * current_setting('block_size')::int                                     AS other_db_bytes
FROM pg_buffercache
`

// sqlBufferStats reports per-table shared-buffer footprint and cumulative I/O
// counters for one schema. Buffer footprint sums the heap, toast and every
// index for the table, so the "biggest cache hog" answer matches the user's
// intuition about a "table".
//
// pg_buffercache.reldatabase = 0 is the shared catalog buffer pool — included
// so system relations a user owns aren't double-counted oddly, though for
// user schemas the join via relfilenode usually filters those out.
const sqlBufferStats = `
WITH bc AS (
  SELECT relfilenode, COUNT(*) AS bufs
  FROM   pg_buffercache
  WHERE  reldatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  GROUP  BY relfilenode
),
filenodes AS (
  SELECT c.oid AS tab_oid, pg_relation_filenode(c.oid) AS fn
  FROM   pg_class c
  JOIN   pg_namespace n ON n.oid = c.relnamespace
  WHERE  n.nspname = $1 AND c.relkind IN ('r','m','p')
  UNION ALL
  SELECT c.oid, pg_relation_filenode(c.reltoastrelid)
  FROM   pg_class c
  JOIN   pg_namespace n ON n.oid = c.relnamespace
  WHERE  n.nspname = $1 AND c.relkind IN ('r','m','p') AND c.reltoastrelid <> 0
  UNION ALL
  SELECT c.oid, pg_relation_filenode(i.indexrelid)
  FROM   pg_class c
  JOIN   pg_namespace n ON n.oid = c.relnamespace
  JOIN   pg_index i ON i.indrelid = c.oid
  WHERE  n.nspname = $1 AND c.relkind IN ('r','m','p')
),
buffered AS (
  SELECT f.tab_oid, COALESCE(SUM(bc.bufs), 0)::bigint AS bufs
  FROM   filenodes f
  LEFT   JOIN bc ON bc.relfilenode = f.fn
  GROUP  BY f.tab_oid
)
SELECT c.oid,
       n.nspname,
       c.relname,
       COALESCE(b.bufs, 0) * current_setting('block_size')::int      AS buffered_bytes,
       pg_total_relation_size(c.oid)                                 AS total_bytes,
       COALESCE(s.heap_blks_hit, 0) + COALESCE(s.idx_blks_hit, 0)    AS hits,
       COALESCE(s.heap_blks_read, 0) + COALESCE(s.idx_blks_read, 0)  AS reads
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN buffered b ON b.tab_oid = c.oid
LEFT   JOIN pg_statio_user_tables s ON s.relid = c.oid
WHERE  n.nspname = $1 AND c.relkind IN ('r','m','p')
ORDER  BY buffered_bytes DESC, c.relname
`
