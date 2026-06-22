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

// sqlBufferUsageCounts is the cluster-wide clock-sweep temperature histogram:
// for each usagecount (0 = cold/evictable up to 5 = hot) the number of buffers,
// and how many are dirty / pinned. pg_buffercache_usage_counts() is a single
// cheap aggregate over the buffer pool (no full pg_buffercache projection), so
// it's far lighter than scanning every buffer ourselves. Available since
// pg_buffercache 1.4 (PostgreSQL 16) — always present on our PG 17+ floor.
const sqlBufferUsageCounts = `
SELECT usage_count, buffers, dirty, pinned
FROM   pg_buffercache_usage_counts()
ORDER  BY usage_count
`

// sqlBufferTableUsageCounts is the per-table version of the temperature
// histogram: the same usagecount → buffers/dirty/pinned breakdown, but scoped
// to one relation's filenodes (heap + toast + every index). generate_series
// fills the 0..5 buckets so callers always get six rows, even for counts with
// no buffers, which keeps the rendered bar stable.
//
// $1 is the table OID. pinning_backends > 0 marks a buffer pinned by a live
// backend right now; isdirty marks one modified in memory but not yet flushed.
const sqlBufferTableUsageCounts = `
WITH fn AS (
  SELECT pg_relation_filenode($1::oid) AS fn
  UNION
  SELECT pg_relation_filenode(reltoastrelid) FROM pg_class WHERE oid = $1::oid AND reltoastrelid <> 0
  UNION
  SELECT pg_relation_filenode(indexrelid)   FROM pg_index WHERE indrelid = $1::oid
),
b AS (
  SELECT usagecount,
         COUNT(*)                                  AS buffers,
         COUNT(*) FILTER (WHERE isdirty)           AS dirty,
         COUNT(*) FILTER (WHERE pinning_backends > 0) AS pinned
  FROM   pg_buffercache
  WHERE  relfilenode IN (SELECT fn FROM fn WHERE fn IS NOT NULL)
    AND  reldatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  GROUP  BY usagecount
)
SELECT g.usagecount,
       COALESCE(b.buffers, 0)::bigint,
       COALESCE(b.dirty, 0)::bigint,
       COALESCE(b.pinned, 0)::bigint
FROM   generate_series(0, 5) AS g(usagecount)
LEFT   JOIN b ON b.usagecount = g.usagecount
ORDER  BY g.usagecount
`

// sqlBufferStats reports per-table shared-buffer footprint and cumulative I/O
// counters for one schema. Buffer footprint sums the heap, toast and every
// index for the table, so the "biggest cache hog" answer matches the user's
// intuition about a "table". dirty_bytes and usage_avg add write-pressure and
// clock-sweep temperature per table.
//
// pg_buffercache.reldatabase = 0 is the shared catalog buffer pool — included
// so system relations a user owns aren't double-counted oddly, though for
// user schemas the join via relfilenode usually filters those out.
const sqlBufferStats = `
WITH bc AS (
  SELECT relfilenode,
         COUNT(*)                        AS bufs,
         COUNT(*) FILTER (WHERE isdirty)  AS dirty_bufs,
         SUM(usagecount)::bigint          AS usage_sum
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
  SELECT f.tab_oid,
         COALESCE(SUM(bc.bufs), 0)::bigint        AS bufs,
         COALESCE(SUM(bc.dirty_bufs), 0)::bigint  AS dirty_bufs,
         COALESCE(SUM(bc.usage_sum), 0)::bigint   AS usage_sum
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
       COALESCE(s.heap_blks_read, 0) + COALESCE(s.idx_blks_read, 0)  AS reads,
       COALESCE(b.dirty_bufs, 0) * current_setting('block_size')::int AS dirty_bytes,
       CASE WHEN COALESCE(b.bufs, 0) > 0
            THEN b.usage_sum::float8 / b.bufs
            ELSE 0 END                                               AS usage_avg
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN buffered b ON b.tab_oid = c.oid
LEFT   JOIN pg_statio_user_tables s ON s.relid = c.oid
WHERE  n.nspname = $1 AND c.relkind IN ('r','m','p')
ORDER  BY buffered_bytes DESC, c.relname
`

// sqlBufferStatByOID is sqlBufferStats scoped to a single relation by OID
// instead of a whole schema — the natural shape for the describe-table view,
// which has the OID but wants only that one table's cache footprint. The
// filenodes CTEs and final SELECT filter on c.oid = $1; the rest matches
// sqlBufferStats so the scanned column list is identical.
const sqlBufferStatByOID = `
WITH bc AS (
  SELECT relfilenode,
         COUNT(*)                        AS bufs,
         COUNT(*) FILTER (WHERE isdirty)  AS dirty_bufs,
         SUM(usagecount)::bigint          AS usage_sum
  FROM   pg_buffercache
  WHERE  reldatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  GROUP  BY relfilenode
),
filenodes AS (
  SELECT c.oid AS tab_oid, pg_relation_filenode(c.oid) AS fn
  FROM   pg_class c
  WHERE  c.oid = $1 AND c.relkind IN ('r','m','p')
  UNION ALL
  SELECT c.oid, pg_relation_filenode(c.reltoastrelid)
  FROM   pg_class c
  WHERE  c.oid = $1 AND c.relkind IN ('r','m','p') AND c.reltoastrelid <> 0
  UNION ALL
  SELECT c.oid, pg_relation_filenode(i.indexrelid)
  FROM   pg_class c
  JOIN   pg_index i ON i.indrelid = c.oid
  WHERE  c.oid = $1 AND c.relkind IN ('r','m','p')
),
buffered AS (
  SELECT f.tab_oid,
         COALESCE(SUM(bc.bufs), 0)::bigint        AS bufs,
         COALESCE(SUM(bc.dirty_bufs), 0)::bigint  AS dirty_bufs,
         COALESCE(SUM(bc.usage_sum), 0)::bigint   AS usage_sum
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
       COALESCE(s.heap_blks_read, 0) + COALESCE(s.idx_blks_read, 0)  AS reads,
       COALESCE(b.dirty_bufs, 0) * current_setting('block_size')::int AS dirty_bytes,
       CASE WHEN COALESCE(b.bufs, 0) > 0
            THEN b.usage_sum::float8 / b.bufs
            ELSE 0 END                                               AS usage_avg
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN buffered b ON b.tab_oid = c.oid
LEFT   JOIN pg_statio_user_tables s ON s.relid = c.oid
WHERE  c.oid = $1 AND c.relkind IN ('r','m','p')
`

// sqlShmemAllocations dumps the whole Postgres shared-memory segment from
// pg_shmem_allocations: every named region (the buffer pool, lock tables, SLRU
// caches, backend arrays, extension arenas, …) ordered biggest-first.
//
// Two rows have name = NULL, distinguished by off:
//   - off IS NULL → anonymous allocations (the difference between the indexed
//     allocations and freeoffset: DSA segments, dynamic shared memory, …).
//   - off IS NOT NULL → the unused tail of the segment (its size = free bytes).
//
// The view is built-in (no extension) but reading it needs pg_read_all_stats
// membership / superuser; a lesser role gets a permission error, which we let
// flow up to the TUI rather than swallow.
const sqlShmemAllocations = `
SELECT name, off, size, allocated_size
FROM   pg_shmem_allocations
ORDER  BY allocated_size DESC
`
