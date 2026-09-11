package pg

// SQL for the 'index' diagnostics (registry: diag_defs_index.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

const sqlDiagIndexShowUnused = `
SELECT
    i.schemaname AS schema,
    i.relname AS table_name,
    i.indexrelname AS index_name,
    pg_relation_size(i.indexrelid) AS index_size_bytes,
    i.idx_scan,
    -- Amortised footprint per scan: how much disk this index costs for each
    -- time it was actually used. The +1 smooths the 0-scan case (never used →
    -- ranks at full size) into the same scale as rarely-used ones, so a huge
    -- index hit only a handful of times floats to the top next to truly unused
    -- ones, while heavily-scanned indexes collapse toward zero. Naming it with
    -- the _bytes suffix lets the renderer humanise and sort it as a byte size.
    pg_relation_size(i.indexrelid) / (COALESCE(i.idx_scan, 0) + 1) AS size_per_scan_bytes,
    t.n_live_tup AS estimated_rows_covered
FROM pg_catalog.pg_stat_user_indexes i
JOIN pg_catalog.pg_stat_user_tables t ON t.relid = i.relid
JOIN pg_catalog.pg_index ix ON ix.indexrelid = i.indexrelid
WHERE i.schemaname NOT IN ('pg_catalog','information_schema')
  AND i.schemaname NOT LIKE 'pg\_toast%'
  AND t.n_live_tup >= 100
  -- Exclude PK/unique indexes: idx_scan counts only planner lookups, not the
  -- uniqueness checks a constraint index runs on every INSERT/UPDATE, so they
  -- can read as "0 scans" while still being load-bearing — they enforce a
  -- constraint and can't be dropped in isolation, so they aren't "unused" in
  -- the sense this diagnostic surfaces.
  AND NOT ix.indisprimary
  AND NOT ix.indisunique
ORDER BY pg_relation_size(i.indexrelid) / (COALESCE(i.idx_scan, 0) + 1) DESC
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

// sqlDiagIndexBrinCandidates flags btree indexes whose leading column is highly
// correlated with the table's physical row order (|correlation| ≥ 0.7). Such an
// index is the textbook case where a BRIN index would be a fraction of the size
// while still pruning blocks effectively, so these are candidates for a
// btree → BRIN conversion. Unique and primary-key indexes are excluded (BRIN
// cannot enforce uniqueness). correlation_pct (abs correlation × 100) is the
// headline bar: the _pct suffix classifies it as DiagPercent so it renders as a
// 0–100 bar graded green→yellow, mirroring the STRONG/Possible split.
// index_columns spells the btree's whole key list (one row per correlated
// column, so a multi-column index can appear several times): the fix builds
// the BRIN on column_name alone and needs to know whether the btree also
// serves lookups on other columns before it suggests dropping it.
const sqlDiagIndexBrinCandidates = `
SELECT
    n.nspname                                          AS schema,
    t.relname                                          AS table_name,
    i.relname                                          AS index_name,
    a.attname                                          AS column_name,
    (SELECT string_agg(pg_get_indexdef(i.oid, kc::int, true), ', ' ORDER BY kc)
       FROM generate_series(1, idx.indnkeyatts) AS kc)  AS index_columns,
    round((abs(s.correlation) * 100)::numeric, 1)      AS correlation_pct,
    pg_size_pretty(pg_relation_size(i.oid))            AS index_size,
    pg_size_pretty(pg_relation_size(t.oid))            AS table_size,
    CASE
        WHEN abs(s.correlation) >= 0.9
            THEN 'STRONG candidate'
        ELSE 'Possible candidate'
    END                                                AS brin_recommendation
FROM pg_index idx
JOIN pg_class i        ON i.oid = idx.indexrelid          -- the index
JOIN pg_class t        ON t.oid = idx.indrelid            -- the table
JOIN pg_namespace n    ON n.oid = t.relnamespace
JOIN pg_am am          ON am.oid = i.relam                -- access method
JOIN LATERAL unnest(idx.indkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
JOIN pg_attribute a    ON a.attrelid = t.oid AND a.attnum = k.attnum
LEFT JOIN pg_stats s   ON s.schemaname = n.nspname
                       AND s.tablename  = t.relname
                       AND s.attname    = a.attname
WHERE am.amname = 'btree'                       -- only B-tree indexes
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND a.attnum > 0                              -- skip system columns
  AND NOT idx.indisunique                       -- exclude unique
  AND NOT idx.indisprimary                      -- exclude primary keys
  AND t.reltuples > 100000                       -- only tables big enough for BRIN to pay off
  AND s.correlation IS NOT NULL
  AND abs(s.correlation) >= 0.7                 -- hide low-correlation "NO" rows
  -- Require high cardinality. A near-constant column (boolean, enum, status
  -- flag) reports a trivially high correlation but is a poor BRIN candidate:
  -- its per-block min/max summary can prune almost nothing. n_distinct < 0 is a
  -- fraction-of-rows estimate (so it scales with the table, always plenty
  -- distinct); n_distinct > 0 is an absolute count, which we require to clear a
  -- floor. Low-cardinality columns are better served by a partial index.
  AND (s.n_distinct < 0 OR s.n_distinct > 100)
ORDER BY abs(s.correlation) DESC, pg_relation_size(t.oid) DESC
`

// sqlDiagIndexClusterCandidates is the inverse of the BRIN query: btree indexes
// whose leading column has *low* physical correlation (|correlation| ≤ 0.5) yet
// whose scans return many rows each — the fragmented-detail-table pattern (e.g.
// a player inventory always fetched by user id) where every index scan touches
// scattered heap pages and a CLUSTER/pg_repack rewrite collapses them onto few.
// Only the leading column matters (k.ord = 1): its correlation decides the heap
// locality of a prefix scan. Unique/primary indexes stay in deliberately — the
// tuples-per-scan floor already drops point lookups (~1 tuple/scan), and a PK
// like (user_id, item_id) scanned by prefix is exactly the target. Expression
// indexes drop out naturally (indkey attnum 0 has no pg_attribute/pg_stats row).
// scatter_pct ((1−|corr|)×100, higher = worse) is the bar. heap_miss_pct
// (per-table, from pg_statio_user_tables) separates fragmented tables that
// actually hit disk from ones the buffer cache absorbs anyway, and the default
// sort is disk_pain — tuples_read × scatter × heap-miss ratio — so the rows
// whose scattered fetches actually cost disk reads rank first, not merely the
// hottest counters on fully-cached tables.
const sqlDiagIndexClusterCandidates = `
SELECT
    n.nspname                                           AS schema,
    t.relname                                           AS table_name,
    i.relname                                           AS index_name,
    a.attname                                           AS column_name,
    round(((1 - abs(s.correlation)) * 100)::numeric, 1) AS scatter_pct,
    -- Fraction of the table's heap block reads that missed shared_buffers: a
    -- fragmented table that lives entirely in cache costs little; one that
    -- hits disk on scattered pages is the real CLUSTER payoff.
    round(100 * sio.heap_blks_read::numeric
          / nullif(sio.heap_blks_hit + sio.heap_blks_read, 0), 1) AS heap_miss_pct,
    -- Composite ranking: tuple fetches × scattered fraction × disk-miss
    -- fraction ≈ fetches that were both on a random page AND read from disk.
    -- Table size needs no extra factor — a table the cache absorbs already
    -- scores ~0 through the miss ratio. NULL (no heap I/O counted) sorts last.
    round(st.idx_tup_read * (1 - abs(s.correlation))
          * sio.heap_blks_read::numeric
          / nullif(sio.heap_blks_hit + sio.heap_blks_read, 0)) AS disk_pain,
    st.idx_scan                                         AS scans,
    st.idx_tup_read                                     AS tuples_read,
    round(st.idx_tup_read::numeric / st.idx_scan, 1)    AS tup_per_scan,
    CASE
        WHEN s.n_distinct > 0
            THEN round((t.reltuples / s.n_distinct)::numeric, 1)
        WHEN s.n_distinct < 0
            THEN round((-1 / s.n_distinct)::numeric, 1)
    END                                                 AS rows_per_key,
    idx.indisclustered                                  AS clustered,
    pg_size_pretty(pg_relation_size(t.oid))             AS table_size,
    pg_size_pretty(pg_relation_size(i.oid))             AS index_size
FROM pg_index idx
JOIN pg_class i        ON i.oid = idx.indexrelid           -- the index
JOIN pg_class t        ON t.oid = idx.indrelid             -- the table
JOIN pg_namespace n    ON n.oid = t.relnamespace
JOIN pg_am am          ON am.oid = i.relam                 -- access method
-- pg_stat_user_indexes also scopes the result to user schemas, so no explicit
-- pg_catalog/information_schema exclusion is needed here.
JOIN pg_stat_user_indexes st ON st.indexrelid = idx.indexrelid
JOIN pg_statio_user_tables sio ON sio.relid = t.oid
JOIN LATERAL unnest(idx.indkey) WITH ORDINALITY AS k(attnum, ord) ON k.ord = 1
JOIN pg_attribute a    ON a.attrelid = t.oid AND a.attnum = k.attnum
LEFT JOIN pg_stats s   ON s.schemaname = n.nspname
                       AND s.tablename  = t.relname
                       AND s.attname    = a.attname
WHERE am.amname = 'btree'                       -- only B-tree (CLUSTER's home turf)
  AND idx.indisvalid
  AND a.attnum > 0                              -- skip expression-index entries
  AND t.reltuples > 100000                      -- only tables big enough to matter
  AND s.correlation IS NOT NULL
  AND abs(s.correlation) <= 0.5                 -- heap order does not follow the index
  AND st.idx_scan > 0
  -- Multi-row scans only, written multiplication-side to avoid division by
  -- zero before the idx_scan > 0 predicate is applied: point lookups
  -- (~1 tuple/scan) don't suffer from fragmentation and would be noise.
  AND st.idx_tup_read >= 5 * st.idx_scan
ORDER BY disk_pain DESC NULLS LAST
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

// sqlDiagIndexShowDuplicate groups indexes by (table, opclasses, key columns,
// expressions, predicate): more than one per group means fully interchangeable
// copies. wasted_bytes is what dropping all but the largest copy would free —
// the figure the overview's schema-health sweep sums.
const sqlDiagIndexShowDuplicate = `
SELECT
    pg_size_pretty(sum(pg_relation_size(idx))::bigint) AS size,
    pg_size_pretty((array_agg(idx_size))[1]) AS index_size,
    (sum(idx_size) - max(idx_size))::bigint AS wasted_bytes,
    (array_agg(tbl))[1] AS "table",
    (array_agg(idx))[1] AS idx1,
    (array_agg(idx))[2] AS idx2,
    (array_agg(cols))[1] AS columns
FROM (
    SELECT
        indexrelid::regclass AS idx,
        pg_relation_size(indexrelid) AS idx_size,
        indrelid::regclass AS tbl,
        (SELECT string_agg(pg_get_indexdef(indexrelid, k + 1, true), ', ' ORDER BY k)
         FROM generate_subscripts(indkey, 1) AS k) AS cols,
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
        indrelid, indexrelid, indkey,
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
-- One row per index column, resolved to the (relation, column) pair whose
-- pg_stats row describes it: a plain key column is described by the table
-- column it indexes (indkey[i] <> 0), an expression column by the index's own
-- attribute (indkey[i] = 0). Resolving this via indkey instead of matching
-- pg_get_indexdef() output against attname keeps the pg_stats join a plain
-- equality the planner can hash; the OR/function form ran a per-row nested
-- loop over every (index attribute × pg_stats) pair and took seconds on
-- mid-sized catalogs. MATERIALIZED is essential: inlined, the planner sees
-- CASE/COALESCE expressions as join keys, refuses to hash on them and falls
-- back to the same nested loop keyed on nspname alone.
index_stat_targets AS MATERIALIZED (
    SELECT ia.*, ia_att.attnum AS index_attnum,
        CASE WHEN ta.attnum IS NULL THEN ia.index_name ELSE ia.tablename END AS stat_relname,
        coalesce(ta.attname, ia_att.attname) AS stat_attname
    FROM btree_index_atts AS ia
    JOIN pg_attribute AS ia_att ON ia_att.attrelid = ia.indexrelid AND ia_att.attnum > 0
    LEFT JOIN pg_attribute AS ta ON ia.indkey[ia_att.attnum - 1] <> 0
        AND ta.attrelid = ia.indrelid AND ta.attnum = ia.indkey[ia_att.attnum - 1]
),
index_item_sizes AS (
    SELECT
        t.nspname, t.index_name,
        t.reltuples, t.relpages, t.relam,
        t.indrelid AS table_oid, t.index_oid,
        current_setting('block_size')::numeric AS bs,
        8 AS maxalign,
        24 AS pagehdr,
        CASE WHEN max(coalesce(s.null_frac, 0)) = 0 THEN 2 ELSE 6 END AS index_tuple_hdr,
        sum((1 - coalesce(s.null_frac, 0)) * coalesce(s.avg_width, 1024)) AS nulldatawidth
    FROM index_stat_targets AS t
    JOIN pg_stats AS s ON s.schemaname = t.nspname
        AND s.tablename = t.stat_relname
        AND s.attname = t.stat_attname
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
    -- Raw byte counts, not MB: the *_bytes columns are DiagBytes and humanize
    -- the value themselves (see sqlDiagBloatTable for the same fix).
    SELECT dbname AS database_name, nspname AS schema_name, table_name, index_name,
        round(realbloat) AS bloat_pct,
        wastedbytes AS bloat_bytes,
        totalbytes AS index_bytes,
        table_bytes,
        index_scans
    FROM raw_bloat
)
SELECT *
FROM format_bloat
WHERE bloat_pct > 50 AND bloat_bytes > 10 * 1024^2
ORDER BY bloat_bytes DESC
`

// sqlDiagFKMissingIndex finds foreign keys on the referencing side that have no
// supporting index: no valid index whose leading columns contain the FK columns
// (any order — a btree lookup works for any permutation, hence the
// unnest/EXCEPT set-containment check on the 0-based smallint[] slice of
// indkey, rather than the @>/<@ operators). Some clusters have a third-party
// extension (e.g. intarray) registering its own smallint[]-compatible @>
// overload — even schema-qualified as OPERATOR(pg_catalog.@>) that overload
// can still tie with the built-in one and PostgreSQL reports "operator is not
// unique", so containment is spelled out with core relational operations that
// have no competing overload instead. Without a supporting index, every
// DELETE/UPDATE on the referenced table sequentially scans the referencing
// table per row.
//
// Only referencing tables estimated at more than 10k rows (t.reltuples) are
// reported: on a small child table the per-row seq scan is cheap enough not to
// matter, so a missing FK index there is noise. reltuples is -1 on a
// never-analyzed table, which is below the floor and so excluded.
const sqlDiagFKMissingIndex = `
SELECT
    n.nspname AS schema,
    t.relname AS table_name,
    c.conname AS fk_name,
    string_agg(a.attname, ', ' ORDER BY x.n) AS fk_columns,
    c.confrelid::regclass::text AS referenced_table,
    ps.n_tup_upd + ps.n_tup_del AS referenced_writes,
    pg_relation_size(c.conrelid) AS table_size_bytes
FROM pg_constraint c
CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS x(attnum, n)
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = x.attnum
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
LEFT JOIN pg_stat_all_tables ps ON ps.relid = c.confrelid
WHERE c.contype = 'f'
  AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
  AND t.reltuples > 10000
  AND NOT EXISTS (
      SELECT 1
      FROM pg_index i
      WHERE i.indrelid = c.conrelid
        AND i.indisvalid
        AND NOT EXISTS (
            SELECT unnest(c.conkey)
            EXCEPT
            SELECT unnest((i.indkey::smallint[])[0:cardinality(c.conkey)-1])
        )
  )
GROUP BY n.nspname, t.relname, c.oid, c.conname, c.confrelid, c.conrelid, ps.n_tup_upd, ps.n_tup_del
ORDER BY pg_relation_size(c.conrelid) DESC
`

// sqlDiagIndexRedundantPrefix finds btree indexes whose key columns are a strict
// leading prefix of another valid btree index on the same table (same column
// order, opclasses, sort options and partial predicate) — the wider index can
// serve every query the narrower one can, so the narrower one usually just costs
// write amplification and disk. Unique / constraint-backed indexes are excluded
// (they enforce something the wider index doesn't); exact duplicates are covered
// by the separate duplicate-indexes diagnostic. Expression indexes are skipped
// (their indkey entries are 0 and would compare equal across different
// expressions).
const sqlDiagIndexRedundantPrefix = `
SELECT
    n.nspname AS schema,
    t.relname AS table_name,
    ri.relname AS redundant_index,
    ci.relname AS covered_by,
    s.idx_scan AS redundant_scans,
    pg_relation_size(a.indexrelid) AS redundant_size_bytes
FROM pg_index a
JOIN pg_index b
    ON a.indrelid = b.indrelid
    AND a.indexrelid <> b.indexrelid
    AND b.indisvalid
    AND b.indnkeyatts > a.indnkeyatts
    AND (b.indkey::smallint[])[0:a.indnkeyatts-1] = (a.indkey::smallint[])[0:a.indnkeyatts-1]
    AND (b.indclass::oid[])[0:a.indnkeyatts-1] = (a.indclass::oid[])[0:a.indnkeyatts-1]
    AND (b.indoption::smallint[])[0:a.indnkeyatts-1] = (a.indoption::smallint[])[0:a.indnkeyatts-1]
JOIN pg_class ri ON ri.oid = a.indexrelid
JOIN pg_class ci ON ci.oid = b.indexrelid
JOIN pg_class t ON t.oid = a.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_am ra ON ra.oid = ri.relam AND ra.amname = 'btree'
JOIN pg_am ca ON ca.oid = ci.relam AND ca.amname = 'btree'
LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = a.indexrelid
WHERE a.indisvalid
  AND NOT a.indisunique
  AND a.indexprs IS NULL AND b.indexprs IS NULL
  AND coalesce(pg_get_expr(a.indpred, a.indrelid), '') = coalesce(pg_get_expr(b.indpred, b.indrelid), '')
  AND NOT EXISTS (SELECT 1 FROM pg_constraint cc WHERE cc.conindid = a.indexrelid)
  AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
ORDER BY pg_relation_size(a.indexrelid) DESC
`

// sqlDiagIndexIO reports per-index buffer I/O from pg_statio_user_indexes: how
// often index blocks came from cache vs disk, next to the scan count and size —
// a hot index with a poor hit ratio is a shared_buffers sizing signal.
const sqlDiagIndexIO = `
SELECT
    io.schemaname AS schema,
    io.relname AS table_name,
    io.indexrelname AS index_name,
    io.idx_blks_read AS blks_read,
    io.idx_blks_hit AS blks_hit,
    round(100.0 * io.idx_blks_hit / NULLIF(io.idx_blks_hit + io.idx_blks_read, 0), 2) AS hit_pct,
    st.idx_scan AS scans,
    pg_relation_size(io.indexrelid) AS index_size_bytes
FROM pg_statio_user_indexes io
JOIN pg_stat_user_indexes st USING (indexrelid)
ORDER BY io.idx_blks_read DESC
`

// sqlDiagIndexInvalid lists indexes flagged NOT indisvalid — the residue of a
// failed CREATE INDEX CONCURRENTLY / REINDEX CONCURRENTLY. Plans never use
// them but every write still maintains them, so they are pure overhead until
// rebuilt or dropped. pg_toast is included on purpose (REINDEX CONCURRENTLY
// can strand TOAST indexes too); catalog and temp schemas are not.
const sqlDiagIndexInvalid = `
SELECT
    n.nspname AS schema,
    t.relname AS table_name,
    ic.relname AS index_name,
    pg_relation_size(i.indexrelid) AS index_size_bytes,
    pg_get_indexdef(i.indexrelid) AS definition
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE NOT i.indisvalid
  AND n.nspname <> 'information_schema'
  AND n.nspname !~ '^pg_(catalog|temp_)'
ORDER BY pg_relation_size(i.indexrelid) DESC
`
