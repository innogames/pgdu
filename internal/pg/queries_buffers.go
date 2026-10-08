package pg

import "fmt"

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
// to one relation's filenodes. generate_series fills the 0..5 buckets so
// callers always get six rows, even for counts with no buffers, which keeps
// the rendered bar stable.
//
// $1 is the relation OID. $2 = true takes the whole table: its heap, TOAST heap
// and the indexes of both. $2 = false takes $1 alone (a split-view row), except
// that a TOAST heap brings its own index, as the split view's toast row counts
// the pair. pinning_backends > 0 marks a buffer pinned by a live backend right
// now; isdirty marks one modified in memory but not yet flushed.
const sqlBufferTableUsageCounts = `
WITH base AS (
  SELECT $1::oid AS oid
  UNION ALL
  SELECT reltoastrelid FROM pg_class WHERE oid = $1::oid AND reltoastrelid <> 0 AND $2::bool
),
fn AS (
  SELECT pg_relation_filenode(oid) AS fn FROM base
  UNION ALL
  SELECT pg_relation_filenode(i.indexrelid)
  FROM   pg_index i
  JOIN   base ON base.oid = i.indrelid
  WHERE  $2::bool OR (SELECT relkind FROM pg_class WHERE oid = $1::oid) = 't'
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

// sqlPageBuffers lists the shared buffers holding one window of a relation's
// main fork: block number → usagecount/isdirty, feeding the page inspector's
// per-page temperature column. pg_relation_filenode resolves rewritten and
// mapped relations; reldatabase 0 covers shared catalogs. One full buffer-pool
// scan per window — the same cost class as the aggregate queries above.
const sqlPageBuffers = `
SELECT relblocknumber::bigint, usagecount::int, isdirty
FROM   pg_buffercache
WHERE  relfilenode = pg_relation_filenode($1::oid::regclass)
  AND  reldatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  AND  relforknumber = 0
  AND  relblocknumber >= $2::int AND relblocknumber < $2::int + $3::int
`

// sqlBufferRelStats reports the shared-buffer footprint and cumulative I/O
// counters of every storage relation of the tables in one schema: one row for
// the heap, one for the TOAST heap (its index included, as
// pg_total_relation_size counts it) and one per index. The buffers list shows
// these rows as its split view and sums them per table (RollUpBufferStats) for
// the default one, so both views come from the same snapshot. dirty_bytes and
// usage_avg add write-pressure and clock-sweep temperature per relation.
//
// total_bytes is each relation's size over all forks; the heap's is
// pg_table_size minus the TOAST pair, so the three kinds sum to
// pg_total_relation_size(table) exactly (a relation dropped mid-query sizes
// as NULL, read as 0). hits/reads take the matching
// pg_statio counters: heap_blks for the heap, toast_blks + tidx_blks for the
// TOAST pair, the index's own idx_blks from pg_statio_user_indexes.
//
// pg_buffercache.reldatabase = 0 is the shared catalog buffer pool — included
// so system relations a user owns aren't double-counted oddly, though for
// user schemas the join via relfilenode usually filters those out.
//
// The filenode set is computed first and pg_buffercache is filtered by it
// BEFORE the GROUP BY: pg_buffercache_pages() always walks (and materialises)
// the whole pool — that part is inherent and, on a large shared_buffers, spills
// a tuplestore to a temp file — but aggregating every buffer by relfilenode
// only to join a handful of them away afterwards doubled the CPU work for the
// single-table variant. Filtering first keeps the aggregate to the rows that
// actually belong to the selected tables.
//
// sqlBufferRelStatsTmpl is the shared body of the two variants below, which
// differ only in how tables are picked: by schema (needs the extra
// pg_namespace join in the rels arms, %[1]s, and an nspname predicate) or by a
// single table OID. %[2]s is the table predicate, %[3]s the trailing ORDER BY.
// The fragments are the fixed strings in the var block below — nothing
// user-supplied is ever spliced in. Partitioned indexes (relkind 'I') have no
// storage and are left out; a partitioned table keeps its (empty) heap row so
// the per-table view still lists it.
const sqlBufferRelStatsTmpl = `
WITH rels AS (
  SELECT c.oid AS tab_oid, c.oid AS rel_oid, 'heap'::text AS kind, c.relname AS rel_name, ''::text AS am
  FROM   pg_class c%[1]s
  WHERE  %[2]s AND c.relkind IN ('r','m','p')
  UNION ALL
  SELECT c.oid, c.reltoastrelid, 'toast', t.relname, ''
  FROM   pg_class c%[1]s
  JOIN   pg_class t ON t.oid = c.reltoastrelid
  WHERE  %[2]s AND c.relkind IN ('r','m','p') AND c.reltoastrelid <> 0
  UNION ALL
  SELECT c.oid, i.indexrelid, 'index', ic.relname, am.amname::text
  FROM   pg_class c%[1]s
  JOIN   pg_index i  ON i.indrelid = c.oid
  JOIN   pg_class ic ON ic.oid = i.indexrelid AND ic.relkind = 'i'
  JOIN   pg_am am    ON am.oid = ic.relam
  WHERE  %[2]s AND c.relkind IN ('r','m','p')
),
files AS (
  SELECT r.rel_oid, pg_relation_filenode(r.rel_oid) AS fn
  FROM   rels r
  UNION ALL
  SELECT r.rel_oid, pg_relation_filenode(i.indexrelid)
  FROM   rels r
  JOIN   pg_index i ON i.indrelid = r.rel_oid
  WHERE  r.kind = 'toast'
),
bc AS (
  SELECT relfilenode,
         COUNT(*)                        AS bufs,
         COUNT(*) FILTER (WHERE isdirty)  AS dirty_bufs,
         SUM(usagecount)::bigint          AS usage_sum
  FROM   pg_buffercache
  WHERE  relfilenode IN (SELECT fn FROM files WHERE fn IS NOT NULL)
    AND  reldatabase IN (0, (SELECT oid FROM pg_database WHERE datname = current_database()))
  GROUP  BY relfilenode
),
buffered AS (
  SELECT f.rel_oid,
         COALESCE(SUM(bc.bufs), 0)::bigint        AS bufs,
         COALESCE(SUM(bc.dirty_bufs), 0)::bigint  AS dirty_bufs,
         COALESCE(SUM(bc.usage_sum), 0)::bigint   AS usage_sum
  FROM   files f
  LEFT   JOIN bc ON bc.relfilenode = f.fn
  GROUP  BY f.rel_oid
)
SELECT c.oid,
       n.nspname,
       c.relname,
       r.kind,
       r.rel_oid,
       r.rel_name,
       r.am,
       COALESCE(b.bufs, 0) * current_setting('block_size')::int       AS buffered_bytes,
       COALESCE(CASE r.kind
         WHEN 'heap' THEN pg_table_size(c.oid) - COALESCE(pg_total_relation_size(c.reltoastrelid), 0)
         ELSE pg_total_relation_size(r.rel_oid)
       END, 0)                                                        AS total_bytes,
       CASE r.kind
         WHEN 'heap'  THEN COALESCE(s.heap_blks_hit, 0)
         WHEN 'toast' THEN COALESCE(s.toast_blks_hit, 0) + COALESCE(s.tidx_blks_hit, 0)
         ELSE COALESCE(si.idx_blks_hit, 0)
       END                                                            AS hits,
       CASE r.kind
         WHEN 'heap'  THEN COALESCE(s.heap_blks_read, 0)
         WHEN 'toast' THEN COALESCE(s.toast_blks_read, 0) + COALESCE(s.tidx_blks_read, 0)
         ELSE COALESCE(si.idx_blks_read, 0)
       END                                                            AS reads,
       COALESCE(b.dirty_bufs, 0) * current_setting('block_size')::int AS dirty_bytes,
       CASE WHEN COALESCE(b.bufs, 0) > 0
            THEN b.usage_sum::float8 / b.bufs
            ELSE 0 END                                                AS usage_avg
FROM   rels r
JOIN   pg_class c     ON c.oid = r.tab_oid
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN buffered b ON b.rel_oid = r.rel_oid
LEFT   JOIN pg_statio_user_tables s   ON s.relid = r.tab_oid
LEFT   JOIN pg_statio_user_indexes si ON si.indexrelid = r.rel_oid AND r.kind = 'index'%[3]s
`

var (
	// sqlBufferRelStats lists the relations of every table in one schema
	// ($1 = nspname).
	sqlBufferRelStats = fmt.Sprintf(sqlBufferRelStatsTmpl,
		"\n  JOIN   pg_namespace n ON n.oid = c.relnamespace",
		"n.nspname = $1",
		"\nORDER  BY buffered_bytes DESC, c.relname, r.rel_name")
	// sqlBufferRelStatsByOID is the same query scoped to one table ($1 = oid),
	// for the describe-table view's cache footprint. The scanned column list is
	// identical to sqlBufferRelStats.
	sqlBufferRelStatsByOID = fmt.Sprintf(sqlBufferRelStatsTmpl, "", "c.oid = $1", "")
)

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
