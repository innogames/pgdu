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

// --- page inspector ---

// sqlHeapPagesSummary aggregates per-page heap stats across a window of
// blocks. One get_raw_page call per page is unavoidable; the LATERAL-style
// LEFT JOIN over heap_page_items lets us read the LP array once per page and
// derive every counter we care about in a single pass. The hot/external
// filters mirror access/htup_details.h: HEAP_HOT_UPDATED is bit 0x4000 of
// t_infomask2, HEAP_HASEXTERNAL is bit 0x0004 of t_infomask.
const sqlHeapPagesSummary = `
WITH pages AS (
  SELECT g.blkno, get_raw_page($1, 'main', g.blkno) AS raw
  FROM   generate_series($2::int, $2::int + $3::int - 1) AS g(blkno)
),
hdr AS (
  SELECT p.blkno, (page_header(p.raw)).*
  FROM   pages p
),
items AS (
  SELECT p.blkno,
         COUNT(*) FILTER (WHERE i.lp_flags = 1)                              AS live_lp,
         COUNT(*) FILTER (WHERE i.lp_flags = 2)                              AS redirect_lp,
         COUNT(*) FILTER (WHERE i.lp_flags = 3)                              AS dead_lp,
         COUNT(*) FILTER (WHERE i.lp_flags = 0)                              AS unused_lp,
         COALESCE(SUM(i.lp_len) FILTER (WHERE i.lp_flags = 1), 0)::bigint    AS live_bytes,
         COALESCE(SUM(i.lp_len) FILTER (WHERE i.lp_flags = 3), 0)::bigint    AS dead_bytes,
         COUNT(*) FILTER (WHERE (i.t_infomask2 & 16384) <> 0)                AS hot_updated,
         COUNT(*) FILTER (WHERE (i.t_infomask  & 4) <> 0)                    AS has_external
  FROM   pages p
  LEFT   JOIN heap_page_items(p.raw) i ON true
  GROUP  BY p.blkno
)
SELECT  h.blkno::bigint,
        h.lsn::text,
        h.lower::int, h.upper::int, h.special::int, h.pagesize::int, h.flags::int,
        (h.upper - h.lower)::int                                              AS free_bytes,
        COALESCE(it.live_lp, 0)::int,
        COALESCE(it.redirect_lp, 0)::int,
        COALESCE(it.dead_lp, 0)::int,
        COALESCE(it.unused_lp, 0)::int,
        COALESCE(it.live_bytes, 0)::bigint,
        COALESCE(it.dead_bytes, 0)::bigint,
        COALESCE(it.hot_updated, 0)::int,
        COALESCE(it.has_external, 0)::int
FROM    hdr h
LEFT    JOIN items it ON it.blkno = h.blkno
ORDER   BY h.blkno
`

// sqlHeapTuples returns the line-pointer array for one page in t_ctid order.
// t_xmin / t_xmax / t_oid / t_bits / t_data are NULL for unused or redirect
// line pointers — the caller scans into pointer targets so a NULL doesn't
// abort the row.
const sqlHeapTuples = `
SELECT lp::int, lp_off::int, lp_flags::int, lp_len::int,
       t_xmin, t_xmax, t_field3, t_ctid::text,
       COALESCE(t_infomask2, 0)::int, COALESCE(t_infomask, 0)::int, t_hoff::int,
       t_bits, t_oid, t_data
FROM   heap_page_items(get_raw_page($1, 'main', $2::int))
ORDER  BY lp
`

// sqlToastTuples mirrors sqlHeapTuples for TOAST heap pages, adding
// chunk_id/chunk_seq columns joined from the underlying TOAST relation so the
// line-pointer list can display which chunk object and sequence number each live
// row represents without the caller having to parse t_data bytes.
//
// The join uses the physical ctid built from the block number ($2) and the
// line-pointer number (lp) as the tid — this matches the live row exactly and
// yields NULL for dead/unused/redirect LPs.
//
// %s is the quoted toast regclass, e.g. `"pg_toast"."pg_toast_16431"`. $1 is
// the same regclass as text for get_raw_page; $2 is the block number.
const sqlToastTuples = `
SELECT hpi.lp::int, hpi.lp_off::int, hpi.lp_flags::int, hpi.lp_len::int,
       hpi.t_xmin, hpi.t_xmax, hpi.t_field3, hpi.t_ctid::text,
       COALESCE(hpi.t_infomask2, 0)::int, COALESCE(hpi.t_infomask, 0)::int, hpi.t_hoff::int,
       hpi.t_bits, hpi.t_oid, hpi.t_data,
       tc.chunk_id::oid, tc.chunk_seq::int
FROM   heap_page_items(get_raw_page($1, 'main', $2::int)) hpi
LEFT   JOIN %s tc
         ON tc.ctid = ('(' || $2::text || ',' || hpi.lp::text || ')')::tid
ORDER  BY hpi.lp
`

// sqlTupleRow fetches one heap row by ctid and explodes it into (column,
// value) pairs preserving declared column order. json_each_text emits keys
// in the order they appear in the json object, and row_to_json walks
// pg_attribute in attnum order — together that gives a faithful
// "SELECT * FROM t" column ordering without us having to read pg_attribute
// ourselves. NULL values surface as SQL NULL in `v` so the renderer can
// show them distinctly from an empty string.
//
// $1 is the table reference as a regclass-castable text (e.g. "s"."t");
// $2 is the ctid text "(blk,off)" — both are bind parameters, so no
// identifier-injection risk.
const sqlTupleRow = `
WITH r AS (
  SELECT row_to_json(t) AS j FROM %s t WHERE ctid = $1::tid
)
SELECT key, value FROM r, json_each_text(r.j)
`

// sqlToastTupleRow replaces sqlTupleRow for TOAST heap tables. TOAST relations
// (relkind 't') don't have a composite type registered in pg_type, so
// row_to_json raises "does not have a composite type". Since every TOAST table
// has the same three fixed columns (chunk_id, chunk_seq, chunk_data) we can
// select them explicitly. chunk_data is rendered as its hex-encoded bytea text
// representation so it's safely truncatable by the value renderer.
//
// An absent ctid yields zero rows from the CTE; ListTupleRow treats that as
// "row gone" and returns an empty slice — same behaviour as sqlTupleRow.
// %s is the quoted toast regclass; $1 is the ctid text.
const sqlToastTupleRow = `
WITH r AS (SELECT chunk_id, chunk_seq, chunk_data FROM %s WHERE ctid = $1::tid)
SELECT col, val FROM (
  SELECT 1 AS o, 'chunk_id'::text  AS col, chunk_id::text   AS val FROM r
  UNION ALL
  SELECT 2,      'chunk_seq',              chunk_seq::text   FROM r
  UNION ALL
  SELECT 3,      'chunk_data',             chunk_data::text  FROM r
) s ORDER BY o
`

// sqlRelations returns every heap-style table, every B-tree index, and every
// TOAST heap whose owning table lives in the named schema, mixed into one list
// and ordered by pg_relation_size. The page-inspector tool consumes it instead
// of sqlTables so the user sees tables, their indexes, and their TOAST storage
// side by side, ranked by on-disk size.
//
// Only B-tree indexes are listed: hash/gist/gin/brin are filtered out because
// the index-page drill relies on bt_page_stats / bt_page_items, neither of
// which works on other access methods.
//
// Three arms in the WHERE:
//   - Tables:  relkind IN ('r','m','p') AND namespace = $1
//   - Indexes: relkind = 'i' AND btree AND parent namespace = $1
//   - TOAST:   relkind = 't' AND owner's namespace = $1
//
// For tables ParentOID is 0; for indexes it's pg_index.indrelid; for TOAST it's
// the OID of the owning table (via oc.reltoastrelid = c.oid). The schema column
// reflects c's actual namespace (pg_toast for TOAST relations), so callers can
// distinguish the storage location from the user's schema.
const sqlRelations = `
SELECT c.oid,
       c.relname,
       c.relkind::text                                             AS kind,
       COALESCE(am.amname, '')                                     AS access_method,
       pg_relation_size(c.oid)                                     AS size_bytes,
       GREATEST(c.reltuples, 0)::bigint                            AS est_rows,
       c.relpages::int                                             AS pages,
       COALESCE(idx.indrelid, oc.oid, 0)::oid                      AS parent_oid,
       COALESCE(pc.relname, oc.relname, '')                        AS parent_name,
       n.nspname                                                   AS schema
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
LEFT   JOIN pg_am am ON am.oid = c.relam
LEFT   JOIN pg_index idx ON idx.indexrelid = c.oid
LEFT   JOIN pg_class pc ON pc.oid = idx.indrelid
LEFT   JOIN pg_namespace pn ON pn.oid = pc.relnamespace
LEFT   JOIN pg_class oc ON oc.reltoastrelid = c.oid
LEFT   JOIN pg_namespace onsp ON onsp.oid = oc.relnamespace
WHERE  (
         (c.relkind IN ('r','m','p') AND n.nspname = $1)
         OR
         (c.relkind = 'i' AND am.amname = 'btree' AND pn.nspname = $1)
         OR
         (c.relkind = 't' AND onsp.nspname = $1)
       )
ORDER  BY size_bytes DESC
`

// sqlIndexPagesSummary mirrors sqlHeapPagesSummary but for B-tree pages.
// bt_page_stats fails on the meta page (always block 0) so the window is
// shifted to skip it whenever it would be included — without this the
// pageinspect call would bubble up "block is a meta page" and abort the
// whole load.
//
// $1 is the index regclass-castable text; $2 the window start (in pages);
// $3 the requested count. The CASE on $2 picks max($2,1) so a request that
// starts at 0 silently skips the meta page.
const sqlIndexPagesSummary = `
SELECT s.blkno::int,
       s.type::text,
       s.live_items::int,
       s.dead_items::int,
       s.avg_item_size::int,
       s.page_size::int,
       s.free_size::int,
       s.btpo_prev::int,
       s.btpo_next::int,
       s.btpo_level::int,
       s.btpo_flags::int
FROM   generate_series(
         GREATEST($2::int, 1),
         $2::int + $3::int - 1
       ) AS g(blkno),
       LATERAL bt_page_stats($1, g.blkno) s
ORDER  BY s.blkno
`

// sqlIndexTuples returns the items on one B-tree page. data here is the
// raw key bytes as a hex text (no per-column decoding — pageinspect can't
// know the indexed types). Used as the fallback path on internal/deleted
// pages where sqlIndexTuplesDecoded doesn't apply.
const sqlIndexTuples = `
SELECT itemoffset::int,
       ctid::text,
       itemlen::int,
       nulls,
       vars,
       data,
       NULL::text AS decoded
FROM   bt_page_items($1, $2::int)
ORDER  BY itemoffset
`

// sqlIndexExprList returns the index's column expressions concatenated as
// a single SQL expression list, e.g. "a, b, lower(c)". Built from
// pg_get_indexdef on each indexed attribute number (1..indnatts). The
// result is interpolated into sqlIndexTuplesDecoded so the per-row
// subquery can project the decoded key value from the heap.
const sqlIndexExprList = `
SELECT COALESCE(string_agg(pg_get_indexdef($1::oid, k::int, false),
                           ', ' ORDER BY k), '')
FROM   generate_series(
         1,
         (SELECT indnatts FROM pg_index WHERE indexrelid = $1::oid)
       ) AS k
`

// sqlIndexTuplesDecoded mirrors sqlIndexTuples but adds a per-item
// scalar-subquery projecting the index's columns from the heap row. The
// subquery yields NULL when the ctid doesn't resolve to a live row
// (vacuumed, beyond MVCC horizon, or — on internal pages — a downlink
// rather than a heap address). Callers fall back to the raw hex `data`
// when decoded is NULL.
//
// %s 1 is the index expression list (built by sqlIndexExprList, e.g.
// "a, b, lower(c)"); %s 2 is the parent table's quoted regclass. Both
// substitutions come from quoteIdent and pg_get_indexdef output — safe
// from injection. $1 is the index regclass-castable text; $2 the block
// number.
const sqlIndexTuplesDecoded = `
SELECT i.itemoffset::int,
       i.ctid::text,
       i.itemlen::int,
       i.nulls,
       i.vars,
       i.data,
       (SELECT (%s)::text FROM %s WHERE ctid = i.ctid::tid) AS decoded
FROM   bt_page_items($1, $2::int) i
ORDER  BY i.itemoffset
`

// sqlToastValueChunks returns all chunks for one out-of-line value in a TOAST
// table, ordered by chunk_seq so the caller can concatenate them in order.
// %s is the quoted toast regclass; $1 is the chunk_id OID.
const sqlToastValueChunks = `
SELECT chunk_seq::int, chunk_data
FROM   %s
WHERE  chunk_id = $1
ORDER  BY chunk_seq
`

// sqlRelPages reports the current heap block count by dividing the file
// size by block size. pg_class.relpages is ANALYZE-driven and can lag the
// real file (zero after TRUNCATE, stale after bulk inserts), which makes it
// useless as an EOF clamp for get_raw_page; pg_relation_size just stat()s
// the file and is always accurate.
const sqlRelPages = `
SELECT (pg_relation_size($1) / current_setting('block_size')::int)::int
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

// --- describe queries (psql \d-style) ---

// sqlDescribeColumns lists a table's live columns in declaration order with
// NOT NULL and the column default expression. $1 = table oid. PG 12+.
const sqlDescribeColumns = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod)               AS type_name,
       a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '')       AS default_expr
FROM   pg_attribute a
LEFT   JOIN pg_attrdef d
       ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE  a.attrelid = $1
  AND  a.attnum   > 0
  AND  NOT a.attisdropped
ORDER  BY a.attnum
`

// sqlDescribeIndexes lists a table's indexes with their full CREATE INDEX
// definitions. Ordered primary-first then alphabetically. $1 = table oid.
const sqlDescribeIndexes = `
SELECT i.relname,
       pg_get_indexdef(idx.indexrelid) AS def,
       idx.indisprimary,
       idx.indisunique
FROM   pg_index idx
JOIN   pg_class i ON i.oid = idx.indexrelid
WHERE  idx.indrelid = $1
ORDER  BY idx.indisprimary DESC, i.relname
`

// sqlDescribeConstraints lists a table's constraints (PK, FK, unique, check)
// rendered by pg_get_constraintdef. $1 = table oid.
const sqlDescribeConstraints = `
SELECT conname,
       pg_get_constraintdef(oid, true) AS def
FROM   pg_constraint
WHERE  conrelid = $1
ORDER  BY contype, conname
`

// sqlDescribeIndex returns the definition and metadata for a single index.
// indpred is COALESCE'd to ” so it's never NULL. $1 = index oid. PG 12+.
const sqlDescribeIndex = `
SELECT pg_get_indexdef(c.oid)                                AS def,
       am.amname                                             AS access_method,
       idx.indisunique,
       idx.indisprimary,
       COALESCE(pg_get_expr(idx.indpred, idx.indrelid), '')  AS predicate,
       idx.indrelid::regclass::text                          AS parent_table
FROM   pg_index idx
JOIN   pg_class c  ON c.oid = idx.indexrelid
JOIN   pg_am am    ON am.oid = c.relam
WHERE  idx.indexrelid = $1
`

// --- diagnostic query SQL ---
// Each constant below corresponds to one entry in the Diagnostics registry
// (diagnostic_defs.go). They are plain SELECT statements with no parameters;
// any identifier filtering (e.g. schemaname='public') is baked in.

const sqlDiagTableShowHitratio = `
WITH hitratio AS (
    SELECT
        relname,
        round(cast(heap_blks_hit AS numeric) / (heap_blks_hit + heap_blks_read) * 100, 2) AS hit_pct,
        heap_blks_hit AS from_cache,
        heap_blks_read AS from_disk
    FROM pg_statio_user_tables
    WHERE (heap_blks_hit + heap_blks_read) > 0
)
SELECT * FROM hitratio WHERE hit_pct < 80 ORDER BY from_disk DESC
`

const sqlDiagTableShowModifyRatio = `
SELECT
    relname,
    round(cast(n_tup_ins AS numeric) / (n_tup_ins + n_tup_upd + n_tup_del) * 100, 2) AS ins_pct,
    round(cast(n_tup_upd AS numeric) / (n_tup_ins + n_tup_upd + n_tup_del) * 100, 2) AS upd_pct,
    round(cast(n_tup_del AS numeric) / (n_tup_ins + n_tup_upd + n_tup_del) * 100, 2) AS del_pct
FROM pg_stat_user_tables
WHERE (n_tup_ins + n_tup_upd + n_tup_del) > 0
ORDER BY relname
`

const sqlDiagTableScanTypes = `
SELECT
    relname,
    seq_scan,
    idx_scan,
    seq_tup_read,
    idx_tup_fetch,
    round(cast(idx_tup_fetch AS numeric) / (idx_tup_fetch + seq_tup_read) * 100, 2) AS index_read_pct,
    pg_size_pretty(pg_relation_size(to_regclass(relname))) AS size_on_disk
FROM pg_stat_user_tables
WHERE (idx_tup_fetch + seq_tup_read) > 0
  AND cast(idx_tup_fetch AS numeric) / (idx_tup_fetch + seq_tup_read) < 0.8
  AND pg_relation_size(to_regclass(relname)) > 800000
ORDER BY seq_tup_read DESC
`

const sqlDiagTableShowSize = `
WITH RECURSIVE pg_inherit(inhrelid, inhparent) AS (
    SELECT inhrelid, inhparent FROM pg_inherits
    UNION
    SELECT child.inhrelid, parent.inhparent
    FROM pg_inherit child, pg_inherits parent
    WHERE child.inhparent = parent.inhrelid
),
pg_inherit_short AS (
    SELECT * FROM pg_inherit WHERE inhparent NOT IN (SELECT inhrelid FROM pg_inherit)
)
SELECT
    table_schema,
    table_name,
    est_row_count,
    total_bytes,
    index_bytes,
    toast_bytes,
    table_bytes
FROM (
    SELECT *, total_bytes - index_bytes - COALESCE(toast_bytes, 0) AS table_bytes
    FROM (
        SELECT c.oid,
               nspname AS table_schema,
               relname AS table_name,
               CEIL(SUM(c.reltuples) OVER (PARTITION BY parent)) AS est_row_count,
               SUM(pg_total_relation_size(c.oid)) OVER (PARTITION BY parent) AS total_bytes,
               SUM(pg_indexes_size(c.oid)) OVER (PARTITION BY parent) AS index_bytes,
               SUM(pg_total_relation_size(reltoastrelid)) OVER (PARTITION BY parent) AS toast_bytes,
               parent
        FROM (
            SELECT pg_class.oid,
                   reltuples,
                   relname,
                   relnamespace,
                   pg_class.reltoastrelid,
                   COALESCE(inhparent, pg_class.oid) parent
            FROM pg_class
            LEFT JOIN pg_inherit_short ON inhrelid = oid
            WHERE relkind IN ('r', 'p')
        ) c
        LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
    ) a
    WHERE oid = parent
) a
ORDER BY total_bytes DESC
`

const sqlDiagToastShowSize = `
SELECT
    t.relname AS toast_table_name,
    pg_table_size(t.oid) AS size_bytes,
    m.relname AS main_table_name,
    array_agg(att.attname) AS column_names,
    COALESCE(s.n_live_tup, 0) AS live_tuples,
    COALESCE(s.n_dead_tup, 0) AS dead_tuples
FROM pg_class t
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_class m ON m.reltoastrelid = t.oid
JOIN pg_attribute att ON att.attrelid = m.oid
LEFT JOIN pg_stat_all_tables s ON s.relid = t.oid
WHERE t.relkind = 't'
  AND att.attnum > 0
  AND NOT att.attisdropped
  AND att.attstorage IN ('x', 'e')
  AND pg_table_size(t.oid) > 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
GROUP BY t.relname, t.oid, m.relname, s.n_live_tup, s.n_dead_tup
ORDER BY pg_table_size(t.oid) DESC
`

const sqlDiagIndexShowUnused = `
SELECT
    i.schemaname AS schema,
    i.relname AS table_name,
    i.indexrelname AS index_name,
    pg_relation_size(i.indexrelid) AS index_size_bytes,
    i.idx_scan,
    t.n_live_tup AS estimated_rows_covered
FROM pg_catalog.pg_stat_user_indexes i
JOIN pg_catalog.pg_stat_user_tables t ON t.relid = i.relid
WHERE i.schemaname NOT IN ('pg_catalog','information_schema')
  AND i.schemaname NOT LIKE 'pg\_toast%'
  AND t.n_live_tup >= 100
ORDER BY i.idx_scan ASC, pg_relation_size(i.indexrelid) DESC
`

// sqlDiagIndexShowSize is the single "Indexes" listing. It folds in the scan
// counters and unique flag that the old separate index_show_all query carried,
// and covers every user schema (not just public).
const sqlDiagIndexShowSize = `
SELECT
    n.nspname AS schema,
    t.relname AS table,
    i.relname AS index,
    pg_relation_size(i.oid) AS index_size_bytes,
    COALESCE(psai.idx_scan, 0) AS scans,
    COALESCE(psai.idx_tup_read, 0) AS tuples_read,
    CASE WHEN ix.indisunique THEN 'Y' ELSE 'N' END AS unique,
    string_agg(a.attname, ', ' ORDER BY a.attnum) AS columns
FROM pg_index AS ix
JOIN pg_class AS t ON t.oid = ix.indrelid
JOIN pg_class AS i ON i.oid = ix.indexrelid
JOIN pg_namespace AS n ON n.oid = t.relnamespace
LEFT JOIN pg_attribute AS a ON a.attnum = ANY(ix.indkey) AND a.attrelid = t.oid
LEFT JOIN pg_stat_all_indexes AS psai ON psai.indexrelid = i.oid
WHERE n.nspname NOT IN ('pg_catalog','information_schema')
  AND n.nspname NOT LIKE 'pg\_toast%'
GROUP BY n.nspname, t.relname, i.relname, i.oid, ix.indisunique, psai.idx_scan, psai.idx_tup_read
ORDER BY pg_relation_size(i.oid) DESC
`

// sqlDiagIndexShowAll was the old per-index listing (public schema only). Its
// useful columns (scan count, tuples read, unique flag) were folded into
// sqlDiagIndexShowSize, so it is no longer registered. Kept commented for
// reference rather than deleted.
//
// const sqlDiagIndexShowAll = `
// SELECT
//     t.tablename,
//     indexname,
//     c.reltuples AS num_rows,
//     pg_size_pretty(pg_relation_size(quote_ident(t.tablename)::text)) AS table_size,
//     pg_size_pretty(pg_relation_size(quote_ident(indexrelname)::text)) AS index_size,
//     CASE WHEN indisunique THEN 'Y' ELSE 'N' END AS unique,
//     idx_scan AS number_of_scans,
//     idx_tup_read AS tuples_read,
//     idx_tup_fetch AS tuples_fetched
// FROM pg_tables t
// LEFT OUTER JOIN pg_class c ON t.tablename = c.relname
// LEFT OUTER JOIN (
//     SELECT c.relname AS ctablename, ipg.relname AS indexname,
//            x.indnatts AS number_of_columns, idx_scan, idx_tup_read, idx_tup_fetch,
//            indexrelname, indisunique
//     FROM pg_index x
//     JOIN pg_class c ON c.oid = x.indrelid
//     JOIN pg_class ipg ON ipg.oid = x.indexrelid
//     JOIN pg_stat_all_indexes psai ON x.indexrelid = psai.indexrelid AND psai.schemaname = 'public'
// ) AS foo ON t.tablename = foo.ctablename
// WHERE t.schemaname = 'public'
// ORDER BY 1, 2
// `

const sqlDiagIndexShowInvalid = `
SELECT
    i.relname AS index_name,
    idx.indrelid::regclass AS location,
    idx.indexrelid AS relation_id,
    am.amname AS type,
    ARRAY(
        SELECT pg_get_indexdef(idx.indexrelid, k + 1, true)
        FROM generate_subscripts(idx.indkey, 1) AS k
        ORDER BY k
    ) AS index_key_names
FROM pg_index AS idx
JOIN pg_class AS i ON i.oid = idx.indexrelid
JOIN pg_am AS am ON i.relam = am.oid
WHERE idx.indisvalid IS FALSE
`

const sqlDiagIndexShowDuplicate = `
SELECT
    pg_size_pretty(sum(pg_relation_size(idx))::bigint) AS size,
    (array_agg(idx))[1] AS idx1,
    (array_agg(idx))[2] AS idx2,
    (array_agg(idx))[3] AS idx3,
    (array_agg(idx))[4] AS idx4
FROM (
    SELECT
        indexrelid::regclass AS idx,
        (indrelid::text || E'\n' || indclass::text || E'\n' || indkey::text || E'\n' ||
         coalesce(indexprs::text, '') || E'\n' || coalesce(indpred::text, '')) AS key
    FROM pg_index
) sub
GROUP BY key
HAVING count(*) > 1
ORDER BY sum(pg_relation_size(idx)) DESC
`

const sqlDiagIndexShowDefinitions = `
SELECT schemaname AS schema, tablename AS table, indexname AS index, indexdef
FROM pg_indexes
WHERE schemaname NOT IN ('pg_catalog','information_schema')
  AND schemaname NOT LIKE 'pg\_toast%'
ORDER BY schemaname, tablename, indexname
`

const sqlDiagBloatIndex = `
WITH btree_index_atts AS (
    SELECT nspname,
        indexclass.relname AS index_name,
        indexclass.reltuples,
        indexclass.relpages,
        indrelid, indexrelid,
        indexclass.relam,
        tableclass.relname AS tablename,
        indexrelid AS index_oid
    FROM pg_index
    JOIN pg_class AS indexclass ON pg_index.indexrelid = indexclass.oid
    JOIN pg_class AS tableclass ON pg_index.indrelid = tableclass.oid
    JOIN pg_namespace ON pg_namespace.oid = indexclass.relnamespace
    JOIN pg_am ON indexclass.relam = pg_am.oid
    WHERE pg_am.amname = 'btree' AND indexclass.relpages > 0
      AND nspname NOT IN ('pg_catalog', 'information_schema')
),
index_item_sizes AS (
    SELECT
        ind_atts.nspname, ind_atts.index_name,
        ind_atts.reltuples, ind_atts.relpages, ind_atts.relam,
        indrelid AS table_oid, index_oid,
        current_setting('block_size')::numeric AS bs,
        8 AS maxalign,
        24 AS pagehdr,
        CASE WHEN max(coalesce(pg_stats.null_frac, 0)) = 0 THEN 2 ELSE 6 END AS index_tuple_hdr,
        sum((1 - coalesce(pg_stats.null_frac, 0)) * coalesce(pg_stats.avg_width, 1024)) AS nulldatawidth
    FROM pg_attribute
    JOIN btree_index_atts AS ind_atts ON pg_attribute.attrelid = ind_atts.indexrelid
    JOIN pg_stats ON pg_stats.schemaname = ind_atts.nspname
        AND ((pg_stats.tablename = ind_atts.tablename
              AND pg_stats.attname = pg_catalog.pg_get_indexdef(pg_attribute.attrelid, pg_attribute.attnum, TRUE))
          OR (pg_stats.tablename = ind_atts.index_name AND pg_stats.attname = pg_attribute.attname))
    WHERE pg_attribute.attnum > 0
    GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9
),
index_aligned_est AS (
    SELECT maxalign, bs, nspname, index_name, reltuples,
        relpages, relam, table_oid, index_oid,
        coalesce(ceil(reltuples * (6 + maxalign
            - CASE WHEN index_tuple_hdr % maxalign = 0 THEN maxalign ELSE index_tuple_hdr % maxalign END
            + nulldatawidth + maxalign
            - CASE WHEN nulldatawidth::integer % maxalign = 0 THEN maxalign ELSE nulldatawidth::integer % maxalign END
        )::numeric / (bs - pagehdr::NUMERIC) + 1), 0) AS expected
    FROM index_item_sizes
),
raw_bloat AS (
    SELECT current_database() AS dbname, nspname, pg_class.relname AS table_name, index_name,
        bs * (index_aligned_est.relpages)::bigint AS totalbytes, expected,
        CASE WHEN index_aligned_est.relpages <= expected THEN 0
             ELSE bs * (index_aligned_est.relpages - expected)::bigint END AS wastedbytes,
        CASE WHEN index_aligned_est.relpages <= expected THEN 0
             ELSE bs * (index_aligned_est.relpages - expected)::bigint * 100
                  / (bs * (index_aligned_est.relpages)::bigint) END AS realbloat,
        pg_relation_size(index_aligned_est.table_oid) AS table_bytes,
        stat.idx_scan AS index_scans
    FROM index_aligned_est
    JOIN pg_class ON pg_class.oid = index_aligned_est.table_oid
    JOIN pg_stat_user_indexes AS stat ON index_aligned_est.index_oid = stat.indexrelid
),
format_bloat AS (
    SELECT dbname AS database_name, nspname AS schema_name, table_name, index_name,
        round(realbloat) AS bloat_pct,
        round(wastedbytes / (1024^2)::NUMERIC) AS bloat_mb,
        round(totalbytes / (1024^2)::NUMERIC, 3) AS index_mb,
        round(table_bytes / (1024^2)::NUMERIC, 3) AS table_mb,
        index_scans
    FROM raw_bloat
)
SELECT *
FROM format_bloat
WHERE bloat_pct > 50 AND bloat_mb > 10
ORDER BY bloat_mb DESC
`

const sqlDiagBloatTable = `
WITH constants AS (
    SELECT current_setting('block_size')::numeric AS bs, 23 AS hdr, 8 AS ma
),
no_stats AS (
    SELECT table_schema, table_name,
        n_live_tup::numeric AS est_rows,
        pg_table_size(relid)::numeric AS table_size
    FROM information_schema.columns
    JOIN pg_stat_user_tables AS psut
        ON table_schema = psut.schemaname AND table_name = psut.relname
    LEFT OUTER JOIN pg_stats
        ON table_schema = pg_stats.schemaname
        AND table_name = pg_stats.tablename
        AND column_name = attname
    WHERE attname IS NULL
      AND table_schema NOT IN ('pg_catalog', 'information_schema')
    GROUP BY table_schema, table_name, relid, n_live_tup
),
null_headers AS (
    SELECT
        hdr + 1 + (sum(CASE WHEN null_frac <> 0 THEN 1 ELSE 0 END) / 8) AS nullhdr,
        SUM((1 - null_frac) * avg_width) AS datawidth,
        MAX(null_frac) AS maxfracsum,
        schemaname, tablename, hdr, ma, bs
    FROM pg_stats CROSS JOIN constants
    LEFT OUTER JOIN no_stats ON schemaname = no_stats.table_schema AND tablename = no_stats.table_name
    WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
      AND no_stats.table_name IS NULL
      AND EXISTS (SELECT 1 FROM information_schema.columns
                  WHERE schemaname = columns.table_schema AND tablename = columns.table_name)
    GROUP BY schemaname, tablename, hdr, ma, bs
),
data_headers AS (
    SELECT
        ma, bs, hdr, schemaname, tablename,
        (datawidth + (hdr + ma - (CASE WHEN hdr % ma = 0 THEN ma ELSE hdr % ma END)))::numeric AS datahdr,
        (maxfracsum * (nullhdr + ma - (CASE WHEN nullhdr % ma = 0 THEN ma ELSE nullhdr % ma END))) AS nullhdr2
    FROM null_headers
),
table_estimates AS (
    SELECT schemaname, tablename, bs,
        reltuples::numeric AS est_rows,
        relpages * bs AS table_bytes,
        CEIL((reltuples * (datahdr + nullhdr2 + 4 + ma -
            (CASE WHEN datahdr % ma = 0 THEN ma ELSE datahdr % ma END)
        ) / (bs - 20))) * bs AS expected_bytes,
        reltoastrelid
    FROM data_headers
    JOIN pg_class ON tablename = relname
    JOIN pg_namespace ON relnamespace = pg_namespace.oid AND schemaname = nspname
    WHERE pg_class.relkind = 'r'
),
estimates_with_toast AS (
    SELECT schemaname, tablename, TRUE AS can_estimate, est_rows,
        table_bytes + (coalesce(toast.relpages, 0) * bs) AS table_bytes,
        expected_bytes + (ceil(coalesce(toast.reltuples, 0) / 4) * bs) AS expected_bytes
    FROM table_estimates
    LEFT OUTER JOIN pg_class AS toast ON table_estimates.reltoastrelid = toast.oid AND toast.relkind = 't'
),
table_estimates_plus AS (
    SELECT current_database() AS databasename, schemaname, tablename, can_estimate, est_rows,
        CASE WHEN table_bytes > 0 THEN table_bytes::NUMERIC ELSE NULL::NUMERIC END AS table_bytes,
        CASE WHEN expected_bytes > 0 THEN expected_bytes::NUMERIC ELSE NULL::NUMERIC END AS expected_bytes,
        CASE WHEN expected_bytes > 0 AND table_bytes > 0 AND expected_bytes <= table_bytes
             THEN (table_bytes - expected_bytes)::NUMERIC ELSE 0::NUMERIC END AS bloat_bytes
    FROM estimates_with_toast
    UNION ALL
    SELECT current_database() AS databasename, table_schema, table_name, FALSE,
        est_rows, table_size, NULL::NUMERIC, NULL::NUMERIC
    FROM no_stats
),
bloat_data AS (
    SELECT current_database() AS databasename, schemaname, tablename, can_estimate,
        table_bytes,
        round(table_bytes / (1024^2)::NUMERIC, 3) AS table_mb,
        expected_bytes,
        round(expected_bytes / (1024^2)::NUMERIC, 3) AS expected_mb,
        round(bloat_bytes * 100 / table_bytes) AS pct_bloat,
        round(bloat_bytes / (1024::NUMERIC ^ 2), 2) AS mb_bloat,
        table_bytes, expected_bytes, est_rows
    FROM table_estimates_plus
)
SELECT databasename, schemaname, tablename, can_estimate, est_rows, pct_bloat, mb_bloat, table_mb
FROM bloat_data
WHERE (pct_bloat >= 50 AND mb_bloat >= 10)
   OR (pct_bloat >= 25 AND mb_bloat >= 1000)
ORDER BY mb_bloat DESC
`

const sqlDiagVacuumStats = `
WITH rel_set AS (
    SELECT oid,
        CASE split_part(split_part(array_to_string(reloptions, ','), 'autovacuum_vacuum_threshold=', 2), ',', 1)
            WHEN '' THEN NULL
            ELSE split_part(split_part(array_to_string(reloptions, ','), 'autovacuum_vacuum_threshold=', 2), ',', 1)::BIGINT
        END AS rel_av_vac_threshold,
        CASE split_part(split_part(array_to_string(reloptions, ','), 'autovacuum_vacuum_scale_factor=', 2), ',', 1)
            WHEN '' THEN NULL
            ELSE split_part(split_part(array_to_string(reloptions, ','), 'autovacuum_vacuum_scale_factor=', 2), ',', 1)::NUMERIC
        END AS rel_av_vac_scale_factor
    FROM pg_class
)
SELECT
    PSUT.relname,
    to_char(PSUT.last_vacuum, 'YYYY-MM-DD HH24:MI') AS last_vacuum,
    to_char(PSUT.last_autovacuum, 'YYYY-MM-DD HH24:MI') AS last_autovacuum,
    to_char(PSUT.last_analyze, 'YYYY-MM-DD HH24:MI') AS last_analyze,
    to_char(PSUT.last_autoanalyze, 'YYYY-MM-DD HH24:MI') AS last_autoanalyze,
    to_char(C.reltuples, '9G999G999G999') AS n_tup,
    PSUT.n_dead_tup AS dead_tuples,
    to_char(
        coalesce(RS.rel_av_vac_threshold, current_setting('autovacuum_vacuum_threshold')::BIGINT)
        + coalesce(RS.rel_av_vac_scale_factor, current_setting('autovacuum_vacuum_scale_factor')::NUMERIC)
        * C.reltuples,
        '9G999G999G999'
    ) AS av_threshold,
    CASE WHEN (
        coalesce(RS.rel_av_vac_threshold, current_setting('autovacuum_vacuum_threshold')::BIGINT)
        + coalesce(RS.rel_av_vac_scale_factor, current_setting('autovacuum_vacuum_scale_factor')::NUMERIC)
        * C.reltuples
    ) < PSUT.n_dead_tup THEN '*' ELSE '' END AS expect_av
FROM pg_stat_user_tables PSUT
JOIN pg_class C ON PSUT.relid = C.oid
JOIN rel_set RS ON PSUT.relid = RS.oid
ORDER BY C.reltuples DESC
`

const sqlDiagVacuumRunning = `
SELECT
    p.pid,
    p.relid::regclass AS table_name,
    p.phase,
    p.heap_blks_total,
    p.heap_blks_scanned,
    -- dead_tuple_bytes was added in PostgreSQL 17; read via jsonb so it is NULL
    -- (rendered "—") on older servers rather than failing the whole query.
    (to_jsonb(p) ->> 'dead_tuple_bytes')::bigint AS dead_tuple_bytes,
    CASE
        WHEN p.heap_blks_total > 0
        THEN ROUND(100.0 * p.heap_blks_scanned / p.heap_blks_total, 2)
        ELSE 0
    END AS percent_complete,
    NOW() - a.xact_start AS duration
FROM pg_stat_progress_vacuum p
JOIN pg_stat_activity a ON p.pid = a.pid
`

const sqlDiagAutovacuumProgress = `
SELECT
    p.pid,
    now() - a.xact_start AS duration,
    coalesce(wait_event_type || '.' || wait_event, 'f') AS waiting,
    CASE
        WHEN a.query ~* '^autovacuum.*to prevent wraparound' THEN 'wraparound'
        WHEN a.query ~* '^vacuum' THEN 'user'
        ELSE 'regular'
    END AS mode,
    p.datname AS database,
    p.relid::regclass AS table_name,
    p.phase,
    pg_size_pretty(p.heap_blks_total * current_setting('block_size')::int) AS table_size,
    pg_size_pretty(pg_total_relation_size(relid)) AS total_size,
    pg_size_pretty(p.heap_blks_scanned * current_setting('block_size')::int) AS scanned,
    pg_size_pretty(p.heap_blks_vacuumed * current_setting('block_size')::int) AS vacuumed,
    round(100.0 * p.heap_blks_scanned / NULLIF(p.heap_blks_total, 0), 1) AS scanned_pct,
    round(100.0 * p.heap_blks_vacuumed / NULLIF(p.heap_blks_total, 0), 1) AS vacuumed_pct,
    p.index_vacuum_count,
    -- The dead-item-id counters were renamed in PostgreSQL 17
    -- (num/max_dead_tuples → num/max_dead_item_ids). Read them through jsonb so
    -- a missing key yields NULL instead of erroring on older servers.
    round(100.0
          * COALESCE((jp.j ->> 'num_dead_item_ids')::numeric, (jp.j ->> 'num_dead_tuples')::numeric)
          / NULLIF(COALESCE((jp.j ->> 'max_dead_item_ids')::numeric, (jp.j ->> 'max_dead_tuples')::numeric), 0), 1) AS dead_pct
FROM pg_stat_progress_vacuum p
JOIN pg_stat_activity a USING (pid)
CROSS JOIN LATERAL (SELECT to_jsonb(p) AS j) jp
ORDER BY now() - a.xact_start DESC
`

const sqlDiagReplicationSlots = `
SELECT
    s.slot_name,
    s.slot_type,
    s.active,
    s.active_pid,
    r.client_hostname,
    s.wal_status,
    s.restart_lsn,
    s.confirmed_flush_lsn,
    pg_wal_lsn_diff(
        CASE WHEN pg_is_in_recovery()
            THEN pg_last_wal_receive_lsn()
            ELSE pg_current_wal_lsn()
        END,
        s.restart_lsn
    ) AS retained_wal_bytes,
    pg_size_pretty(s.safe_wal_size) AS safe_wal_size,
    -- conflicting/invalidation_reason arrived in PG16 and inactive_since in PG17;
    -- read via jsonb so older servers return NULL instead of erroring.
    (js.j ->> 'conflicting')::boolean AS conflicting,
    js.j ->> 'invalidation_reason' AS invalidation_reason,
    date_trunc('second', NOW() - (js.j ->> 'inactive_since')::timestamptz) AS inactive_for
FROM pg_replication_slots s
LEFT JOIN pg_stat_replication r ON r.pid = s.active_pid
CROSS JOIN LATERAL (SELECT to_jsonb(s) AS j) js
ORDER BY s.slot_type, s.slot_name
`

const sqlDiagSettingsShowPending = `
SELECT
    name,
    setting AS current_value,
    CASE
        WHEN pending_restart THEN 'restart'
        WHEN context = 'sighup' THEN 'reload'
        WHEN context IN ('backend', 'superuser-backend') THEN 'new session'
        ELSE 'unknown'
    END AS needed_action,
    reset_val AS configured_value,
    context
FROM pg_settings
WHERE pending_restart = true
   OR (context IN ('sighup', 'backend', 'superuser-backend') AND setting <> reset_val)
ORDER BY
    CASE
        WHEN pending_restart THEN 1
        WHEN context = 'sighup' THEN 2
        WHEN context IN ('backend', 'superuser-backend') THEN 3
        ELSE 4
    END,
    name
`

const sqlDiagDatabaseShowSize = `
SELECT
    d.datname AS name,
    pg_catalog.pg_get_userbyid(d.datdba) AS owner,
    CASE WHEN pg_catalog.has_database_privilege(d.datname, 'CONNECT')
        THEN pg_catalog.pg_database_size(d.datname)
        ELSE NULL
    END AS size_bytes
FROM pg_catalog.pg_database d
ORDER BY size_bytes DESC NULLS LAST
`

const sqlDiagForeignkeysShowAll = `
SELECT
    tc.table_schema,
    tc.constraint_name,
    tc.table_name,
    kcu.column_name,
    ccu.table_schema AS foreign_table_schema,
    ccu.table_name AS foreign_table_name,
    ccu.column_name AS foreign_column_name
FROM information_schema.table_constraints AS tc
JOIN information_schema.key_column_usage AS kcu
    ON tc.constraint_name = kcu.constraint_name
    AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage AS ccu
    ON ccu.constraint_name = tc.constraint_name
    AND ccu.table_schema = tc.table_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
ORDER BY tc.table_schema, tc.table_name, tc.constraint_name
`

const sqlDiagGrantsShowAll = `
WITH rol AS (
    SELECT oid, rolname::text AS role_name FROM pg_authid
    UNION
    SELECT 0::oid, 'public'::text
),
schemas AS (
    SELECT oid AS schema_oid, n.nspname::text AS schema_name, n.nspowner AS owner_oid,
           'schema'::text AS object_type,
           coalesce(n.nspacl, acldefault('n'::"char", n.nspowner)) AS acl
    FROM pg_catalog.pg_namespace n
    WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
),
classes AS (
    SELECT schemas.schema_oid, schemas.schema_name AS object_schema, c.oid,
           c.relname::text AS object_name, c.relowner AS owner_oid,
           CASE c.relkind
               WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
               WHEN 'S' THEN 'sequence' WHEN 'f' THEN 'foreign table' WHEN 'p' THEN 'partitioned table'
               ELSE c.relkind::text END AS object_type,
           CASE WHEN c.relkind = 'S'
               THEN coalesce(c.relacl, acldefault('s'::"char", c.relowner))
               ELSE coalesce(c.relacl, acldefault('r'::"char", c.relowner)) END AS acl
    FROM pg_class c
    JOIN schemas ON schemas.schema_oid = c.relnamespace
    WHERE c.relkind IN ('r', 'v', 'm', 'S', 'f', 'p')
),
procs AS (
    SELECT schemas.schema_oid, schemas.schema_name AS object_schema, p.oid,
           p.proname::text AS object_name, p.proowner AS owner_oid,
           CASE p.prokind WHEN 'a' THEN 'aggregate' WHEN 'p' THEN 'procedure' ELSE 'function' END AS object_type,
           pg_catalog.pg_get_function_arguments(p.oid) AS calling_arguments,
           coalesce(p.proacl, acldefault('f'::"char", p.proowner)) AS acl
    FROM pg_proc p
    JOIN schemas ON schemas.schema_oid = p.pronamespace
),
all_objects AS (
    SELECT schema_name AS object_schema, object_type, schema_name AS object_name,
           null::text AS calling_arguments, owner_oid, acl FROM schemas
    UNION
    SELECT object_schema, object_type, object_name, null::text, owner_oid, acl FROM classes
    UNION
    SELECT object_schema, object_type, object_name, calling_arguments, owner_oid, acl FROM procs
),
acl_base AS (
    SELECT object_schema, object_type, object_name, calling_arguments, owner_oid,
           (aclexplode(acl)).grantor AS grantor_oid,
           (aclexplode(acl)).grantee AS grantee_oid,
           (aclexplode(acl)).privilege_type AS privilege_type,
           (aclexplode(acl)).is_grantable AS is_grantable
    FROM all_objects
)
SELECT acl_base.object_schema, acl_base.object_type, acl_base.object_name,
       acl_base.calling_arguments,
       owner.role_name AS object_owner, grantor.role_name AS grantor, grantee.role_name AS grantee,
       acl_base.privilege_type, acl_base.is_grantable
FROM acl_base
JOIN rol owner ON owner.oid = acl_base.owner_oid
JOIN rol grantor ON grantor.oid = acl_base.grantor_oid
JOIN rol grantee ON grantee.oid = acl_base.grantee_oid
WHERE acl_base.grantor_oid <> acl_base.grantee_oid
ORDER BY acl_base.object_schema, acl_base.object_type, acl_base.object_name
`

// sqlDiagActivityRunning lists non-idle backends ordered by how long the current
// statement has been running. Excludes this connection's own backend.
const sqlDiagActivityRunning = `
SELECT
    pid,
    usename,
    coalesce(host(client_addr), 'local') AS client_addr,
    state,
    (EXTRACT(epoch FROM now() - query_start) * 1000)::bigint AS duration_ms,
    wait_event_type,
    wait_event,
    left(regexp_replace(query, '\s+', ' ', 'g'), 300) AS query
FROM pg_stat_activity
WHERE state IS NOT NULL
  AND state <> 'idle'
  AND pid <> pg_backend_pid()
ORDER BY now() - query_start DESC NULLS LAST
`

// sqlDiagConnections aggregates pg_stat_activity into a per-database, per-state
// connection count — a quick read on pool saturation and idle-in-transaction.
const sqlDiagConnections = `
SELECT
    coalesce(datname, '(none)') AS database,
    coalesce(state, '(none)') AS state,
    count(*) AS connections,
    coalesce(max(EXTRACT(epoch FROM now() - state_change))::int, 0) AS max_state_age_secs
FROM pg_stat_activity
GROUP BY datname, state
ORDER BY count(*) DESC
`

// sqlDiagWalFiles lists the WAL segment files on disk. pg_ls_waldir() requires
// superuser or membership in pg_monitor.
const sqlDiagWalFiles = `
SELECT
    name,
    size AS size_bytes,
    modification
FROM pg_ls_waldir()
ORDER BY modification DESC
`

// sqlDiagWalActivity reports cluster-wide WAL generation counters. pg_stat_wal
// requires PostgreSQL 14 or newer.
const sqlDiagWalActivity = `
SELECT
    wal_records,
    wal_fpi,
    wal_bytes,
    wal_buffers_full,
    stats_reset
FROM pg_stat_wal
`

// sqlDiagDatabaseStats reports per-database commit/rollback, cache hit ratio,
// deadlocks and temp-file usage from pg_stat_database.
const sqlDiagDatabaseStats = `
SELECT
    datname AS database,
    numbackends AS backends,
    xact_commit AS commits,
    xact_rollback AS rollbacks,
    round(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) AS hit_pct,
    deadlocks,
    temp_files,
    temp_bytes
FROM pg_stat_database
WHERE datname IS NOT NULL
ORDER BY xact_commit + xact_rollback DESC
`

// sqlDiagSequences reports how much of each sequence's range has been consumed.
// last_value is null without SELECT/USAGE on the sequence, leaving consumed_pct
// null for those rows.
const sqlDiagSequences = `
SELECT
    schemaname AS schema,
    sequencename AS sequence,
    last_value,
    max_value,
    round(100.0 * last_value / NULLIF(max_value, 0), 2) AS consumed_pct
FROM pg_sequences
ORDER BY consumed_pct DESC NULLS LAST
`

// --- WAL inspector (toolWAL) ---

// sqlWALWindow resolves the [start, end] LSN window the WAL inspector
// analyses: end is the current write position, start is `end − $1 bytes`
// clamped at the very start of the WAL so the subtraction never underflows
// the pg_lsn range. Both are returned as text so pgx scans them as plain
// strings (pg_lsn has no registered pgx codec). $1 is the window size in
// bytes. A brand-new cluster with less than $1 of WAL yields start='0/0',
// which pg_get_wal_stats rejects as "not available" — acceptable since any
// server old enough to have pg_walinspect installed has long passed that.
const sqlWALWindow = `
SELECT (CASE
          WHEN (cur - '0/0'::pg_lsn) > $1::numeric THEN cur - $1::numeric
          ELSE '0/0'::pg_lsn
        END)::text AS start_lsn,
       cur::text AS end_lsn
FROM   (SELECT pg_current_wal_lsn() AS cur) q
`

// sqlWALSummary is the header block: current insert/flush position, the
// segment file the write head sits in, wal_level, the count and on-disk size
// of segment files in pg_wal, and the cluster-wide pg_stat_wal counters.
// Uses only built-in functions (no pg_walinspect) so the header renders even
// when the extension is absent — but pg_ls_waldir / pg_stat_wal still require
// a sufficiently-privileged role, so the caller treats a failure as non-fatal.
const sqlWALSummary = `
SELECT pg_current_wal_insert_lsn()::text                       AS insert_lsn,
       pg_current_wal_lsn()::text                              AS flush_lsn,
       pg_walfile_name(pg_current_wal_lsn())                   AS current_file,
       current_setting('wal_level')                            AS wal_level,
       (SELECT count(*) FROM pg_ls_waldir())                   AS seg_files,
       (SELECT COALESCE(sum(size), 0)::bigint FROM pg_ls_waldir()) AS seg_bytes,
       w.wal_records,
       w.wal_fpi,
       w.wal_bytes::bigint                                     AS wal_bytes
FROM   pg_stat_wal w
`

// sqlWALRmgrStats aggregates the window by resource manager: count, the bytes
// spent on record data vs. full-page images, and their sum. Ordered biggest
// combined-size first; callers may resort. $1/$2 are start/end LSN.
// NOTE: pg_get_wal_stats names its first output column
// "resource_manager/record_type" (a literal slash) — the same column doubles
// as the record-type label when per_record=true. It must be double-quoted.
const sqlWALRmgrStats = `
SELECT "resource_manager/record_type" AS resource_manager,
       count,
       record_size,
       fpi_size,
       combined_size
FROM   pg_get_wal_stats($1::pg_lsn, $2::pg_lsn, false)
WHERE  count > 0
ORDER  BY combined_size DESC
`

// sqlWALRecordTypeStats is the same pg_get_wal_stats source as
// sqlWALRmgrStats but with per_record=true, so the byte/count breakdown is
// per record-type instead of per resource-manager. The first column then
// reads "Rmgr/RecordType" (e.g. "Heap/INSERT"); $3 filters to one rmgr by
// its "<rmgr>/" prefix. Powers the summary table above the records list.
const sqlWALRecordTypeStats = `
SELECT "resource_manager/record_type" AS record_type,
       count,
       record_size,
       fpi_size,
       combined_size
FROM   pg_get_wal_stats($1::pg_lsn, $2::pg_lsn, true)
WHERE  count > 0
  AND  "resource_manager/record_type" LIKE $3 || '/%'
ORDER  BY combined_size DESC
`

// sqlWALRecords lists individual records in the window for one resource
// manager, in LSN (chronological) order. $1/$2 are start/end LSN, $3 the
// resource_manager name to filter on.
const sqlWALRecords = `
SELECT start_lsn::text,
       end_lsn::text,
       prev_lsn::text,
       xid::text,
       resource_manager,
       record_type,
       record_length,
       main_data_length,
       fpi_length,
       COALESCE(description, ''),
       COALESCE(block_ref, '')
FROM   pg_get_wal_records_info($1::pg_lsn, $2::pg_lsn)
WHERE  resource_manager = $3
ORDER  BY start_lsn
`

// sqlWALBlocks lists the block references of a single record spanning
// [$1, $2) — the record's own start and end LSN. The range must include the
// record (a zero-width [start, start) range matches nothing), so the caller
// passes the record's end_lsn as the upper bound. show_data=false skips the
// raw block/FPI bytes — pgdu only needs the lengths and the FPI flag.
// Requires PostgreSQL 16+ (the function did not exist in 15).
// block_id and relforknumber are smallint; cast to int so they scan into
// int32 regardless of pgx's int2 widening rules. block_fpi_info is text[]
// (a list of flag names) and arrives as a Go []string — NULL becomes nil, so
// no COALESCE (and an array can't be COALESCEd with a text literal anyway).
// The lateral resolves the relfilenode back to a relation name via
// pg_filenode_relation: NULL (→ ”) when the relation lives in another
// database or has been dropped, in which case the caller falls back to the
// numeric relfilenode. pg_filenode_relation normalises the WAL's tablespace
// OID to pg_class's 0-for-default form internally, so passing reltablespace
// straight through is correct. When the resolved relation is a TOAST table we
// hop to its owning table (pg_class.reltoastrelid) and report that name plus an
// is_toast flag, so the row shows the user-facing table rather than the opaque
// pg_toast.pg_toast_<oid>. reldatabase is resolved to a datname against the
// shared pg_database catalog (” for OID 0 / shared relations → numeric
// fallback).
const sqlWALBlocks = `
SELECT block_id::int,
       reltablespace,
       reldatabase,
       relfilenode,
       relforknumber::int,
       relblocknumber,
       resource_manager,
       record_type,
       block_data_length,
       block_fpi_length,
       block_fpi_info,
       COALESCE(description, ''),
       COALESCE(r.relname, ''),
       COALESCE(r.is_toast, false),
       COALESCE((SELECT datname FROM pg_database WHERE oid = b.reldatabase), '')
FROM   pg_get_wal_block_info($1::pg_lsn, $2::pg_lsn, false) AS b
LEFT   JOIN LATERAL (
         SELECT
           CASE WHEN owner.oid IS NOT NULL
                THEN owner.oid::regclass::text
                ELSE f.relid::text
           END AS relname,
           owner.oid IS NOT NULL AS is_toast
         FROM   (SELECT pg_filenode_relation(b.reltablespace, b.relfilenode) AS relid) f
         LEFT   JOIN pg_class owner ON owner.reltoastrelid = f.relid::oid
         WHERE  f.relid IS NOT NULL
       ) r ON true
ORDER  BY block_id
`

// sqlStatements reads the pg_stat_statements 1.11 column set for the current
// database only. The 1.11 names (shared_blk_read_time etc., renamed from the
// pre-17 blk_read_time) work on PG17/18; we avoid the 1.12-only
// parallel_workers_* columns so the same query runs on both. wal_bytes is a
// numeric in the catalog — cast to bigint so it scans into int64. Rows whose
// queryid is NULL (text unavailable / insufficient privilege) are skipped.
const sqlStatements = `
SELECT queryid, userid, dbid, query,
       calls, rows,
       total_exec_time, min_exec_time, max_exec_time, mean_exec_time, stddev_exec_time,
       plans, total_plan_time,
       shared_blks_hit, shared_blks_read, shared_blks_dirtied, shared_blks_written,
       local_blks_hit, local_blks_read, local_blks_dirtied, local_blks_written,
       temp_blks_read, temp_blks_written,
       shared_blk_read_time, shared_blk_write_time,
       local_blk_read_time, local_blk_write_time,
       temp_blk_read_time, temp_blk_write_time,
       wal_records, wal_fpi, wal_bytes::bigint
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
  AND  query NOT LIKE 'EXPLAIN (GENERIC_PLAN%'
  AND  query NOT LIKE 'PREPARE pgdu_infer_params%'
`
