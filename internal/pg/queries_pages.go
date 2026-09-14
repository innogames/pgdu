package pg

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

// sqlHeapTuplesCols is sqlHeapTuples plus the row's logical content, so the
// tuple list can name the row a line pointer holds instead of only its
// physical address — in two provenances, because a page inspector's most
// interesting tuples are the ones no query can see any more:
//
//   - pk / vals: the primary key and the picked columns as text, read from the
//     live relation through this session's snapshot. The join mirrors
//     sqlToastTuples: the ctid is rebuilt from the block number ($2) and the
//     line pointer, which the planner resolves as a Tid Scan — a single buffer
//     hit on a page get_raw_page already pulled in. Only rows visible to our
//     snapshot join, so dead / aborted / uncommitted tuples project NULL (the
//     src.ctid IS NULL guard is what produces it — concat_ws / ARRAY[] would
//     otherwise render "()" / an all-NULL array for a line pointer with no
//     visible row).
//   - raws: the picked columns' on-page bytes from heap_page_item_attrs, which
//     splits every NORMAL tuple's t_data by the relation's descriptor whatever
//     its visibility. The two get_raw_page calls read the same buffer but are
//     not one atomic read; the join on lp tolerates a page pruned in between
//     (a vanished lp simply has no raws).
//
// The %[n]s are: 1 the key expression (heapKeyProjection, or NULL), 2 the
// picked columns' text projections (heapColProjection), 3 their t_attrs
// elements (heapAttrProjection), 4 the quoted regclass. $1 is the same
// regclass as text for get_raw_page / heap_page_item_attrs, $2 the block.
const sqlHeapTuplesCols = `
SELECT hpi.lp::int, hpi.lp_off::int, hpi.lp_flags::int, hpi.lp_len::int,
       hpi.t_xmin, hpi.t_xmax, hpi.t_field3, hpi.t_ctid::text,
       COALESCE(hpi.t_infomask2, 0)::int, COALESCE(hpi.t_infomask, 0)::int, hpi.t_hoff::int,
       hpi.t_bits, hpi.t_oid, hpi.t_data,
       CASE WHEN src.ctid IS NULL THEN NULL ELSE %[1]s END                  AS pk,
       CASE WHEN src.ctid IS NULL THEN NULL ELSE ARRAY[%[2]s]::text[] END   AS vals,
       ARRAY[%[3]s]::bytea[]                                               AS raws
FROM   heap_page_items(get_raw_page($1, 'main', $2::int)) hpi
LEFT   JOIN heap_page_item_attrs(get_raw_page($1, 'main', $2::int), $1::regclass) hpa
         ON hpa.lp = hpi.lp
LEFT   JOIN %[4]s src
         ON src.ctid = ('(' || $2::text || ',' || hpi.lp::text || ')')::tid
ORDER  BY hpi.lp
`

// sqlHeapTuplesDecode is sqlHeapTuplesCols without the relation join: the
// fallback for a role granted pageinspect but not SELECT on the table, which
// still gets the picked columns decoded from the page bytes. %[1]s is the
// t_attrs projection (heapAttrProjection).
const sqlHeapTuplesDecode = `
SELECT hpi.lp::int, hpi.lp_off::int, hpi.lp_flags::int, hpi.lp_len::int,
       hpi.t_xmin, hpi.t_xmax, hpi.t_field3, hpi.t_ctid::text,
       COALESCE(hpi.t_infomask2, 0)::int, COALESCE(hpi.t_infomask, 0)::int, hpi.t_hoff::int,
       hpi.t_bits, hpi.t_oid, hpi.t_data,
       ARRAY[%[1]s]::bytea[] AS raws
FROM   heap_page_items(get_raw_page($1, 'main', $2::int)) hpi
LEFT   JOIN heap_page_item_attrs(get_raw_page($1, 'main', $2::int), $1::regclass) hpa
         ON hpa.lp = hpi.lp
ORDER  BY hpi.lp
`

// sqlHeapColumns lists a relation's live columns in attnum order with the
// pg_attribute/pg_type facts the tuple list needs to offer them in its column
// picker and decode their on-page bytes. Dropped columns are left out: they
// have no name to pick and no type to decode with. $1 is the quoted regclass.
const sqlHeapColumns = `
SELECT a.attnum::int,
       a.attname::text,
       format_type(a.atttypid, a.atttypmod) AS type_name,
       a.attlen::int,
       a.attalign::text,
       COALESCE(t.typname, '')::text        AS typname,
       COALESCE(t.typcategory, '')::text    AS typcategory
FROM   pg_attribute a
LEFT   JOIN pg_type t ON t.oid = a.atttypid
WHERE  a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
ORDER  BY a.attnum
`

// sqlPrimaryKeyColumns lists a relation's primary-key columns in key order.
// A primary key is always over plain columns — never expressions, never
// INCLUDE columns — so every indkey entry resolves to a pg_attribute row.
// Zero rows means the relation has no primary key. $1 is the quoted regclass.
const sqlPrimaryKeyColumns = `
SELECT a.attname::text
FROM   pg_index i
JOIN   LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
JOIN   pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
WHERE  i.indrelid = $1::regclass
  AND  i.indisprimary
  AND  k.ord <= i.indnkeyatts
ORDER  BY k.ord
`

// heapKeyColCap bounds each projected column's text in sqlHeapTuplesCols (key
// columns and picked columns alike). A list cell renders at most 24 cells and
// the expanded key line 120, so this is already generous — its job is to stop
// a fat text/bytea column from shipping kilobytes per line pointer (a page
// holds up to ~290 of them).
const heapKeyColCap = "64"

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

// sqlTupleAttrs splits one heap tuple into its per-attribute raw bytes via
// heap_page_item_attrs and pairs each with its pg_attribute physical metadata.
// t_attrs has one element per attribute of the relation's *current* tuple
// descriptor, dropped columns included; an element is NULL both for SQL NULLs
// and for attributes past the tuple's own natts (t_infomask2 & 0x07FF) — the
// `stored` column disambiguates. Varlena elements include their 1- or 4-byte
// header; with the default do_detoast=false an out-of-line value stays its
// 18-byte on-disk TOAST pointer and nothing is ever dereferenced, so the query
// is safe on dead-but-stored tuples.
//
// An enum attribute's stored bytes are the pg_enum row's OID, which means
// nothing to a reader — the pg_enum join decodes those bytes back to the OID
// (little-endian, the layout on every platform pageinspect runs on in
// practice, matching the TUI's byte decoders) and carries the matching
// enumlabel along so the overlay can render "19428 (closed)" without a second
// round trip. enum_label stays NULL for non-enums, NULL/not-stored values, and
// OIDs that no longer resolve.
//
// $1 doubles as get_raw_page's text arg and the ::regclass (precedent:
// sqlToastTuples); $2 is the block number; $3 the line pointer. Zero rows mean
// the lp vanished (page rewritten since the tuple list loaded) — the caller
// treats that as "tuple gone", not an error.
const sqlTupleAttrs = `
WITH tup AS (
  SELECT t_attrs, COALESCE(t_infomask2, 0)::int AS infomask2
  FROM   heap_page_item_attrs(get_raw_page($1, 'main', $2::int), $1::regclass)
  WHERE  lp = $3::int
)
SELECT a.attnum::int,
       a.attname::text,
       CASE WHEN a.attisdropped THEN ''
            ELSE format_type(a.atttypid, a.atttypmod) END AS type_name,
       a.attlen::int,
       a.attalign::text,
       a.attisdropped,
       (a.attnum <= (tup.infomask2 & 2047))               AS stored,
       COALESCE(t.typname, '')::text                      AS typname,
       COALESCE(t.typcategory, '')::text                  AS typcategory,
       e.enumlabel::text                                  AS enum_label,
       tup.t_attrs[a.attnum]                              AS value
FROM   pg_attribute a
CROSS  JOIN tup
LEFT   JOIN pg_type t ON t.oid = a.atttypid
LEFT   JOIN pg_enum e
       ON  t.typtype = 'e'
       AND e.enumtypid = t.oid
       -- CASE, not an AND'ed octet_length qual: join quals evaluate in any
       -- order, so a bare guard doesn't stop get_byte from erroring on the
       -- 1-3 B values of *other* attributes in the row.
       AND e.oid = CASE WHEN octet_length(tup.t_attrs[a.attnum]) = 4
                        THEN (get_byte(tup.t_attrs[a.attnum], 0)::bigint
                            + get_byte(tup.t_attrs[a.attnum], 1)::bigint * 256
                            + get_byte(tup.t_attrs[a.attnum], 2)::bigint * 65536
                            + get_byte(tup.t_attrs[a.attnum], 3)::bigint * 16777216)::oid
                   END
WHERE  a.attrelid = $1::regclass AND a.attnum > 0
ORDER  BY a.attnum
`

// sqlRelations returns every heap-style table, every B-tree index, and every
// TOAST heap whose owning table lives in the named schema, mixed into one list
// and ordered by pg_relation_size. The page-inspector tool consumes it instead
// of sqlTables so the user sees tables, their indexes, and their TOAST storage
// side by side, ranked by on-disk size.
//
// btree/gist/brin/gin indexes are listed (each has its own pageinspect drill
// path keyed on access_method); hash is still filtered out — pageinspect can
// summarise its pages but pgdu has no hash drill yet.
//
// Three arms in the WHERE:
//   - Tables:  relkind IN ('r','m','p') AND namespace = $1
//   - Indexes: relkind = 'i' AND drillable AM AND parent namespace = $1
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
         (c.relkind = 'i' AND am.amname IN ('btree','gist','brin','gin') AND pn.nspname = $1)
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

// sqlRelationBlocks is the relation's exact block count, from the file size —
// not pg_class.relpages, which is an ANALYZE-time estimate that could push a
// whole-relation page walk past EOF. $1 is a regclass-castable text.
const sqlRelationBlocks = `
SELECT pg_relation_size($1::regclass) / current_setting('block_size')::bigint
`

// sqlBtreeLevelCounts aggregates every page of a B-tree (block 0, the
// metapage, excluded) into per-(btpo_level, type) page counts for the
// tree-shape banner above the page list. bt_multi_page_stats (PG 16+) walks
// the whole block range in one call — far cheaper than a LATERAL
// bt_page_stats per block, but still a full-index read, so the TUI loads it
// asynchronously and caches the result per screen. $1 is the index
// regclass-castable text; $2 the number of blocks after the metapage (the
// caller sizes it via sqlRelationBlocks and skips the call when it's zero —
// bt_multi_page_stats rejects an empty range).
const sqlBtreeLevelCounts = `
SELECT s.btpo_level::int AS level,
       s.type::text      AS type,
       count(*)          AS pages
FROM   bt_multi_page_stats($1, 1, $2::bigint) s
GROUP  BY 1, 2
ORDER  BY 1 DESC, 2
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
       COALESCE(dead, false),
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
// (vacuumed, beyond MVCC horizon, HOT-updated — see sqlHeapRedirectKeys —
// or, on internal pages, a downlink rather than a heap address). Callers
// fall back to the raw hex `data` when decoded is NULL.
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
       COALESCE(i.dead, false),
       (SELECT (%s)::text FROM %s WHERE ctid = i.ctid::tid) AS decoded
FROM   bt_page_items($1, $2::int) i
ORDER  BY i.itemoffset
`

// sqlIndexPostingTids expands the posting-list tuples on one leaf page into
// their member heap tids. bt_page_items exposes them as the tids tid[] column
// (pageinspect 1.8+, PG 13); singletons and pivots have NULL there and drop
// out. Re-reading the page is one buffer hit, cheaper than carrying the array
// through the main query and splitting it client-side. $1 is the index
// regclass-castable text; $2 the block number.
const sqlIndexPostingTids = `
SELECT i.itemoffset::int,
       t.tid::text,
       NULL::text AS decoded
FROM   bt_page_items($1, $2::int) i,
       LATERAL unnest(i.tids) WITH ORDINALITY AS t(tid, ord)
WHERE  i.tids IS NOT NULL
ORDER  BY i.itemoffset, t.ord
`

// sqlIndexPostingTidsDecoded is sqlIndexPostingTids with the same per-tid heap
// projection sqlIndexTuplesDecoded does for singletons. Dedup guarantees every
// member shares the parent's key, so the projection is not about the key — it
// tells which members still point at a visible heap row and lets Enter open
// them. %s 1 is the index expression list, %s 2 the parent's quoted regclass.
const sqlIndexPostingTidsDecoded = `
SELECT i.itemoffset::int,
       t.tid::text,
       (SELECT (%s)::text FROM %s WHERE ctid = t.tid) AS decoded
FROM   bt_page_items($1, $2::int) i,
       LATERAL unnest(i.tids) WITH ORDINALITY AS t(tid, ord)
WHERE  i.tids IS NOT NULL
ORDER  BY i.itemoffset, t.ord
`

// sqlHeapRedirectKeys resolves the index entries sqlIndexTuplesDecoded had to
// give up on because their ctid names a HOT-chain root. After a HOT update the
// index entry keeps pointing at the original line pointer, which pruning turns
// into an LP_REDIRECT to the current tuple elsewhere on the same page. A Tid
// Scan (ctid = …) does not follow redirects — only an index scan does — so the
// heap projection comes back NULL even though the row is perfectly alive.
//
// This walks one hop by hand: read the referenced heap pages, keep their
// redirect line pointers, and project the key from the tuple each one targets.
// One hop is enough for a pruned page (the redirect is rewritten to the newest
// tuple); if the chain still continues past that tuple, decoded stays NULL and
// the caller shows the entry unresolved rather than guessing.
//
// The block filter is what keeps the get_raw_page calls safe: pivot and
// posting-list tuples encode markers in their ctid rather than heap addresses,
// and a concurrent truncation can shrink the relation under us — either way an
// out-of-range block would error out the whole query instead of one row. Cost
// is bounded by the number of distinct heap blocks one index page references,
// and those are the same blocks sqlIndexTuplesDecoded's Tid Scans already
// touched, so this adds no reads the decode path didn't already do.
//
// %s 1 is the index expression list (sqlIndexExprList), %s 2 the parent
// table's quoted regclass. $1 is the "(blk,off)" texts to resolve, $2 the
// parent regclass as text, $3 the distinct blocks those ctids live on.
//
// $2 is spelled $2::text at both use sites on purpose. Postgres fixes an
// untyped parameter's type at its first appearance, and the CTE is parsed
// first: a bare $2::regclass there would make the later get_raw_page($2, …)
// see a regclass, which has no implicit cast to the text that function wants.
// The prepare then fails, collectBestEffort swallows the error, and every
// HOT-updated entry silently renders as unresolved.
const sqlHeapRedirectKeys = `
WITH blk AS (
    SELECT DISTINCT b
    FROM   unnest($3::int[]) AS b
    WHERE  b >= 0
      AND  b < pg_relation_size(($2::text)::regclass) / current_setting('block_size')::int
), red AS (
    SELECT '(' || blk.b || ',' || hpi.lp     || ')' AS root,
           '(' || blk.b || ',' || hpi.lp_off || ')' AS live
    FROM   blk, LATERAL heap_page_items(get_raw_page($2::text, 'main', blk.b)) hpi
    WHERE  hpi.lp_flags = 2
)
SELECT red.root,
       red.live,
       (SELECT (%s)::text FROM %s WHERE ctid = red.live::tid) AS decoded
FROM   red
WHERE  red.root = ANY($1::text[])
`

// sqlBtreePageType reports just the bt_page_stats type ('l'/'r'/'i'/'d') for one
// block. Used to resolve a child page's type when descending through an
// internal-page downlink, where the type isn't known up front but decides the
// decode-vs-raw tuple path and whether further downlinks exist. $1 is the index
// regclass-castable text; $2 the block number.
const sqlBtreePageType = `
SELECT type::text, btpo_level::int FROM bt_page_stats($1, $2::int)
`

// sqlBtreeMeta reads the B-tree metapage (block 0) via bt_metap. Every column
// is cast to int so a bigint block number scans into the int32 struct fields
// (block numbers never approach 2^31 in practice). allequalimage is the PG13+
// dedup-capable flag. $1 is the index regclass-castable text.
const sqlBtreeMeta = `
SELECT magic::int, version::int, root::int, level::int,
       fastroot::int, fastlevel::int, allequalimage
FROM   bt_metap($1)
`

// sqlIndexKeyColumns lists an index's columns in definition order, splitting
// key columns (k <= indnkeyatts) from INCLUDE/covering columns. Each Def is the
// per-column pg_get_indexdef projection (a bare name or an expression). The
// generate_series over 1..indnatts is LATERAL (it reads idx.indnatts) and must
// come before the pg_attribute/pg_type joins, which key off its k. Works for
// any access method.
//
// The physical type columns (typlen/typalign/typname/typcategory) drive the
// type-aware key decoder used on internal-page separators, where no heap
// projection is available. They come from the index relation's *own*
// pg_attribute rows (attnum k maps 1:1 to the k-th indexed column, with the
// type already resolved — even for expression and INCLUDE columns). $1 is the
// index oid.
const sqlIndexKeyColumns = `
SELECT k::int,
       pg_get_indexdef($1::oid, k::int, false) AS def,
       (k <= idx.indnkeyatts)                  AS is_key,
       a.attlen::int                           AS typlen,
       a.attalign::text                        AS typalign,
       t.typname::text                         AS typname,
       t.typcategory::text                     AS typcategory
FROM   pg_index idx
CROSS  JOIN LATERAL generate_series(1, idx.indnatts) AS k
JOIN   pg_attribute a ON a.attrelid = idx.indexrelid AND a.attnum = k
JOIN   pg_type      t ON t.oid = a.atttypid
WHERE  idx.indexrelid = $1::oid
ORDER  BY k
`

// sqlToastValueChunks returns the chunks of one out-of-line value in a TOAST
// table, ordered by chunk_seq so the caller can concatenate them in order, plus
// the value's total chunk count and byte size. Only the first $2 chunks ship
// their data (ReadToastValue caps what it reassembles — a 60 MB value must not
// cross the wire whole); the aggregates still cover every chunk. %s is the
// quoted toast regclass; $1 is the chunk_id OID.
const sqlToastValueChunks = `
SELECT chunk_seq::int,
       chunk_data,
       count(*)                 OVER ()::int    AS chunks,
       sum(octet_length(chunk_data)) OVER ()::bigint AS total_bytes
FROM   %s
WHERE  chunk_id = $1
ORDER  BY chunk_seq
LIMIT  $2
`

// sqlToastOwnerTypes lists the distinct types of the columns that can land in
// a TOAST table: the owner's (reltoastrelid = $1) live varlena columns whose
// storage allows out-of-line ('x' extended, 'e' external — 'm' main only
// toasts as a last resort, but is included since it can). The TOAST table
// itself stores untyped bytes, so this is the only clue to a value's type;
// one distinct type means the decoder can trust it.
const sqlToastOwnerTypes = `
SELECT DISTINCT t.typname::text, t.typcategory::text
FROM   pg_class     c
JOIN   pg_attribute a ON a.attrelid = c.oid
JOIN   pg_type      t ON t.oid = a.atttypid
WHERE  c.reltoastrelid = $1::oid
AND    a.attnum > 0 AND NOT a.attisdropped
AND    a.attlen = -1 AND a.attstorage IN ('x', 'e', 'm')
ORDER  BY 1
`

// sqlRelPages reports the current heap block count by dividing the file
// size by block size. pg_class.relpages is ANALYZE-driven and can lag the
// real file (zero after TRUNCATE, stale after bulk inserts), which makes it
// useless as an EOF clamp for get_raw_page; pg_relation_size just stat()s
// the file and is always accurate.
const sqlRelPages = `
SELECT (pg_relation_size($1) / current_setting('block_size')::int)::int
`

// sqlResolveRelByOID builds the Table metadata for a relation known only by OID
// — used to jump into a TOAST relation's page inspector from a tuple's TOAST
// pointer. Unlike sqlResolveTable it is keyed by oid and not relkind-filtered,
// so it resolves toast relations (relkind 't', which sqlResolveTable excludes).
const sqlResolveRelByOID = `
SELECT n.nspname, c.relname, pg_relation_size(c.oid), c.reltuples::bigint
FROM   pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE  c.oid = $1::oid
`

// sqlToastChunkBlock returns the heap block and line pointer of a TOAST value's
// first chunk, so the page inspector can open that chunk's byte layout
// directly. %s is the quoted toast regclass; $1 is the chunk_id OID. No row
// (value vacuumed/updated away) is not an error — the caller falls back to the
// relation's first page.
const sqlToastChunkBlock = `
SELECT (t.ctid::text::point)[0]::int, (t.ctid::text::point)[1]::int
FROM   %s t
WHERE  t.chunk_id = $1::oid
ORDER  BY t.chunk_seq
LIMIT  1
`

// --- GiST page inspector ---

// sqlGistPagesSummary mirrors sqlIndexPagesSummary for GiST. GiST has no
// metapage (block 0 is the root), so the window starts at $2 with no skip.
// Leaf/deleted come from gist_page_opaque_info.flags; the item count is a
// LATERAL count over gist_page_items_bytea (the raw variant needs no index
// oid). $1 is the index regclass-castable text; $2 the window start; $3 count.
const sqlGistPagesSummary = `
WITH pages AS (
  SELECT g.blkno, get_raw_page($1, g.blkno) AS raw
  FROM   generate_series($2::int, $2::int + $3::int - 1) AS g(blkno)
)
SELECT p.blkno::int,
       ('leaf'    = ANY(o.flags))        AS is_leaf,
       ('deleted' = ANY(o.flags))        AS is_deleted,
       COALESCE(it.items, 0)::int        AS items,
       (hdr.upper - hdr.lower)::int      AS free_size,
       hdr.pagesize::int                 AS page_size,
       o.rightlink::bigint               AS rightlink
FROM   pages p,
       LATERAL page_header(p.raw)            AS hdr,
       LATERAL gist_page_opaque_info(p.raw)  AS o
LEFT   JOIN LATERAL (
         SELECT count(*) AS items FROM gist_page_items_bytea(p.raw)
       ) it ON true
ORDER  BY p.blkno
`

// sqlGistItems lists one GiST page's items. keys is pageinspect's opclass-decoded
// key text — already human-readable, so there's no heap-projection path like
// B-tree's. ctid points at the heap row (leaf) or a child page (internal). $1 is
// the index regclass-castable text; $2 the block number.
const sqlGistItems = `
SELECT itemoffset::int,
       ctid::text,
       itemlen::int,
       dead,
       keys
FROM   gist_page_items(get_raw_page($1, $2::int), $1::regclass)
ORDER  BY itemoffset
`

// sqlGistItemsBytea is the raw-bytes fallback for sqlGistItems. gist_page_items
// formats the key via the opclass's output function, which some opclasses lack —
// notably btree_gist's gbtreekey* types, which raise "cannot display a value of
// type gbtreekeyNN" (SQLSTATE 0A000). gist_page_items_bytea returns the raw key
// bytes (no index oid needed, no formatting), which we render as hex instead.
const sqlGistItemsBytea = `
SELECT itemoffset::int,
       ctid::text,
       itemlen::int,
       dead,
       key_data
FROM   gist_page_items_bytea(get_raw_page($1, $2::int))
ORDER  BY itemoffset
`

// sqlGistPageFlags resolves a child page's leaf/deleted role when descending a
// GiST downlink (the parent only gave us the block number). Mirrors
// sqlBtreePageType. $1 is the index regclass-castable text; $2 the block number.
const sqlGistPageFlags = `
SELECT ('leaf' = ANY(flags)) AS is_leaf,
       ('deleted' = ANY(flags)) AS is_deleted
FROM   gist_page_opaque_info(get_raw_page($1, $2::int))
`

// --- BRIN page inspector ---

// sqlBrinMeta reads the BRIN metapage (block 0) via brin_metapage_info. magic is
// text; the rest cast to fit the int32/int64 struct fields. $1 is the index
// regclass-castable text.
const sqlBrinMeta = `
SELECT magic::text, version::int, pagesperrange::int, lastrevmappage::bigint
FROM   brin_metapage_info(get_raw_page($1, 0))
`

// sqlBrinPagesSummary summarises BRIN pages by type (meta/revmap/regular) and
// free space. It deliberately does NOT count items: brin_page_items errors on
// non-regular pages and there's no reliable way to guard the set-returning call
// per row, so item counts come from the drill (sqlBrinItems). Block 0 (meta) is
// browsable here — brin_page_type handles it. $1 index text; $2 start; $3 count.
const sqlBrinPagesSummary = `
WITH pages AS (
  SELECT g.blkno, get_raw_page($1, g.blkno) AS raw
  FROM   generate_series($2::int, $2::int + $3::int - 1) AS g(blkno)
)
SELECT p.blkno::int,
       brin_page_type(p.raw)        AS page_type,
       (hdr.upper - hdr.lower)::int AS free_size,
       hdr.pagesize::int            AS page_size
FROM   pages p,
       LATERAL page_header(p.raw) AS hdr
ORDER  BY p.blkno
`

// sqlBrinItems lists one BRIN regular page's summary tuples: per heap-block-range
// (blknum), per indexed attribute (attnum), the opclass-rendered summary value
// plus the null/placeholder/empty flags. The empty column arrived in pageinspect
// 1.12 (PG 16) — but its presence tracks the *installed extension version*, not
// the server: a pg_upgraded cluster never `ALTER EXTENSION pageinspect UPDATE`d
// off an older layout lacks it even on PG 17/18. Read it through jsonb so a
// missing column yields NULL (rendered as no badge) rather than failing the whole
// query. $1 is the index regclass-castable text (also the oid arg via ::regclass);
// $2 the block.
const sqlBrinItems = `
SELECT bpi.itemoffset::int,
       bpi.blknum::bigint,
       bpi.attnum::int,
       bpi.allnulls,
       bpi.hasnulls,
       bpi.placeholder,
       (to_jsonb(bpi) ->> 'empty')::bool AS empty,
       bpi.value
FROM   brin_page_items(get_raw_page($1, $2::int), $1::regclass) AS bpi
ORDER  BY bpi.blknum, bpi.attnum
`

// --- GIN page inspector ---

// sqlGinMeta reads the GIN metapage (block 0) via gin_metapage_info. $1 is the
// index regclass-castable text.
const sqlGinMeta = `
SELECT n_pending_pages::int,
       n_pending_tuples::bigint,
       n_total_pages::int,
       n_entry_pages::int,
       n_data_pages::int,
       n_entries::bigint,
       version::int
FROM   gin_metapage_info(get_raw_page($1, 0))
`

// sqlGinPagesSummary summarises GIN pages by opaque flags (entry/data/leaf/…) +
// free space. The metapage (block 0) is skipped via GREATEST($2,1) — its opaque
// area differs and it's already shown in the banner (mirrors B-tree's meta skip).
// The item count is the opaque maxoff only on data pages (posting-tree pages
// store PostingItems in the opaque area, so it's the real count there); entry
// pages keep maxoff at 0 and hold ordinary line pointers, so their count comes
// from the page header instead.
// $1 index text; $2 start; $3 count.
const sqlGinPagesSummary = `
WITH pages AS (
  SELECT g.blkno, get_raw_page($1, g.blkno) AS raw
  FROM   generate_series(GREATEST($2::int, 1), $2::int + $3::int - 1) AS g(blkno)
)
SELECT p.blkno::int,
       array_to_string(o.flags, ' ') AS flags,
       CASE WHEN 'data' = ANY(o.flags) THEN o.maxoff::int
            ELSE GREATEST((hdr.lower - 24) / 4, 0)::int END AS maxoff,
       (hdr.upper - hdr.lower)::int  AS free_size,
       hdr.pagesize::int             AS page_size
FROM   pages p,
       LATERAL page_header(p.raw)           AS hdr,
       LATERAL gin_page_opaque_info(p.raw)  AS o
ORDER  BY p.blkno
`

// sqlGinNextDataLeaf finds the first compressed posting-tree leaf page at or
// after block $2 (exclusive upper bound $3, normally relpages). Data-leaf pages
// are the only GIN pages pageinspect can itemize, and in a typical GIN they are
// a handful lost among tens of thousands of entry pages — a client-side sort
// only ever sees the loaded window, so the search has to run server-side.
// LIMIT 1 stops the scan at the first hit; the pages read on the way still
// land in shared buffers, the same cost a window load pays.
// $1 index text; $2 first block to consider; $3 block count of the relation.
const sqlGinNextDataLeaf = `
SELECT g.blkno::int
FROM   generate_series(GREATEST($2::int, 1), $3::int - 1) AS g(blkno),
       LATERAL gin_page_opaque_info(get_raw_page($1, g.blkno)) AS o
WHERE  o.flags @> ARRAY['data', 'leaf']::text[]
ORDER  BY g.blkno
LIMIT  1
`

// sqlGinItems lists posting-list segments on a compressed GIN data-leaf page via
// gin_leafpage_items. tids is a tid[]; we return its length and a space-joined
// sample of the first 20 (a segment can pack thousands). Entry-tree pages aren't
// itemizable by pageinspect, so callers only run this on data-leaf pages. $1 is
// the index regclass-castable text; $2 the block number.
const sqlGinItems = `
SELECT first_tid::text,
       nbytes::int,
       COALESCE(array_length(tids, 1), 0)::int AS tid_count,
       COALESCE(array_to_string(tids[1:20], ' '), '') AS tids_text
FROM   gin_leafpage_items(get_raw_page($1, $2::int))
ORDER  BY first_tid
`
