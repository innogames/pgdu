package pg

// Foundation read-only SQL: the database/schema/table/column hierarchy every
// tool drills through. Domain-specific SQL lives in the sibling queries_*.go
// files (bloat, buffers, pages, describe, diag, wal, statements); all of it is
// kept as plain const strings so the queries can be audited in one package.

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
