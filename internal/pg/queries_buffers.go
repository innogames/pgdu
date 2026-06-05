package pg

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
