package pg

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

// sqlDiagTableShowHotRatio reports the HOT (heap-only tuple) update split per
// table. A high hot_pct is good — the update stayed on the same page and
// touched no indexes; the absolute non_hot_updates count surfaces the tables
// doing the most index churn (FILLFACTOR / over-indexing candidates), which is
// why it is the default sort rather than the ratio.
const sqlDiagTableShowHotRatio = `
SELECT
    relname,
    n_tup_upd                  AS updates,
    n_tup_hot_upd              AS hot_updates,
    n_tup_upd - n_tup_hot_upd  AS non_hot_updates,
    round(100.0 * n_tup_hot_upd / NULLIF(n_tup_upd, 0), 1) AS hot_pct
FROM pg_stat_user_tables
WHERE n_tup_upd > 0
ORDER BY non_hot_updates DESC
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
const sqlDiagIndexBrinCandidates = `
SELECT
    t.relname                                          AS table_name,
    i.relname                                          AS index_name,
    a.attname                                          AS column_name,
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

const sqlDiagIndexShowDuplicate = `
SELECT
    pg_size_pretty(sum(pg_relation_size(idx))::bigint) AS size,
    pg_size_pretty((array_agg(idx_size))[1]) AS index_size,
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
      -- No EXISTS against information_schema.columns here: every pg_stats row is
      -- already an existing, privilege-visible column (that is how the pg_stats
      -- view is defined), so the check filtered nothing but forced a second full
      -- evaluation of that expensive view. The only relations it excluded were
      -- matviews, which table_estimates already drops via relkind = 'r'.
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
    -- Emit raw byte counts (not MB): the TUI's *_bytes columns are DiagBytes,
    -- which humanizes the value itself. Pre-dividing to MB made humanize.Bytes
    -- treat "86000" MB as 86000 bytes and print "86 KB".
    SELECT databasename, schemaname, tablename, can_estimate, est_rows,
        table_bytes,
        bloat_bytes,
        round(bloat_bytes * 100 / table_bytes) AS pct_bloat
    FROM table_estimates_plus
)
SELECT databasename, schemaname, tablename, can_estimate, est_rows, pct_bloat, bloat_bytes, table_bytes
FROM bloat_data
-- Thresholds are in bytes: ≥50 MB or ≥1 GB, matching the Description.
WHERE (pct_bloat >= 50 AND bloat_bytes >= 50 * 1024^2)
   OR (pct_bloat >= 25 AND bloat_bytes >= 1000 * 1024^2)
ORDER BY bloat_bytes DESC
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

// sqlDiagWraparoundTables ranks tables by transaction-ID freeze age — how far
// their oldest unfrozen XID (relfrozenxid, folded together with the table's
// TOAST relation, whichever is older) has drifted behind the current XID.
// pct_freeze_max expresses that age as a fraction of autovacuum_freeze_max_age,
// the age at which PostgreSQL forces an anti-wraparound autovacuum; a table at
// (or past) 100% is one freezing can't keep up with. It is the drill-down for
// the cluster-wide "wraparound" health check.
//
// last_autovacuum and autovacuum_count separate the root causes: a high age
// next to a recent autovacuum, or a large autovacuum_count that still hasn't
// dropped the age, means the vacuums that ran were non-aggressive and skipped
// all-visible pages (a low-churn table below vacuum_freeze_table_age never gets
// its relfrozenxid advanced until the anti-wraparound trigger at
// autovacuum_freeze_max_age forces a full scan) — or, more rarely, the horizon
// is pinned by an old snapshot (chase it with the idle-in-xact and
// replication-slot diagnostics — VACUUM can't freeze past the oldest live xmin).
// An old/absent last_autovacuum instead means autovacuum isn't reaching the
// table at all (throughput, or a table it keeps failing to vacuum).
//
// Only ordinary tables and matviews are considered; TOAST is attributed to its
// parent via reltoastrelid, and partitioned parents (relfrozenxid 0, so age() is
// meaninglessly huge) are excluded.
const sqlDiagWraparoundTables = `
SELECT
    n.nspname AS schema,
    c.relname AS table_name,
    greatest(age(c.relfrozenxid), COALESCE(age(tc.relfrozenxid), 0)) AS xid_age,
    round(100.0 * greatest(age(c.relfrozenxid), COALESCE(age(tc.relfrozenxid), 0))
          / current_setting('autovacuum_freeze_max_age')::numeric, 1) AS pct_freeze_max,
    COALESCE(age(tc.relfrozenxid), 0) AS toast_xid_age,
    pg_table_size(c.oid) AS size_bytes,
    st.n_dead_tup AS dead_tuples,
    st.autovacuum_count,
    st.last_autovacuum
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_class tc ON tc.oid = c.reltoastrelid
LEFT JOIN pg_stat_all_tables st ON st.relid = c.oid
WHERE c.relkind IN ('r', 'm')
  AND c.relfrozenxid <> 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY xid_age DESC
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
    date_trunc('second', NOW() - (js.j ->> 'inactive_since')::timestamptz) AS inactive_for,
    EXTRACT(EPOCH FROM NOW() - (js.j ->> 'inactive_since')::timestamptz)::float8 AS inactive_secs
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

// sqlDiagDatabaseStats reports the per-database picture from pg_stat_database:
// transaction volume with a derived rollback %, cache hit ratio, tuple I/O,
// recovery-conflict and deadlock counters, temp-file pressure, block- and
// session-time totals, and the live session count. The *_time counters are
// cumulative milliseconds since stats_reset; they are divided down to seconds
// (round() needs a numeric, hence the ::numeric cast on the double-precision
// source) so the values stay readable. All columns beyond the headline hit_pct
// bar are opt-out via the C column picker; conflicts, rollback_pct and sessions
// additionally start hidden (Diagnostic.DefaultHidden).
const sqlDiagDatabaseStats = `
SELECT
    datname AS database,
    numbackends AS backends,
    xact_commit AS commits,
    xact_rollback AS rollbacks,
    round(100.0 * xact_rollback / NULLIF(xact_commit + xact_rollback, 0), 2) AS rollback_pct,
    round(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) AS hit_pct,
    blks_read,
    tup_returned,
    tup_fetched,
    tup_inserted,
    tup_updated,
    tup_deleted,
    conflicts,
    deadlocks,
    temp_files,
    temp_bytes,
    round(blk_read_time::numeric / 1000.0, 1) AS blk_read_secs,
    round(blk_write_time::numeric / 1000.0, 1) AS blk_write_secs,
    sessions,
    round(active_time::numeric / 1000.0, 1) AS active_secs,
    round(idle_in_transaction_time::numeric / 1000.0, 1) AS idle_in_xact_secs,
    round(session_time::numeric / 1000.0, 1) AS session_secs,
    stats_reset
FROM pg_stat_database
WHERE datname IS NOT NULL
ORDER BY xact_commit + xact_rollback DESC
`

// sqlDiagSequences reports how much of each sequence's range has been consumed,
// restricted to sequences already past 30% so the list surfaces only the ones
// worth watching for exhaustion. last_value is null without SELECT/USAGE on the
// sequence; those rows have an unknown consumed_pct and so fall below the filter
// (NULL > 30 is not true) and are excluded along with the low-usage sequences.
//
// owned_by_table resolves the table each sequence backs via pg_depend — the
// auto/internal dependency SERIAL, GENERATED … AS IDENTITY and explicit OWNED BY
// all create from the sequence to its owning column. A standalone sequence has
// no such dependency and shows "—". The lookup is a LEFT JOIN LATERAL so those
// unowned sequences still appear.
const sqlDiagSequences = `
SELECT
    s.schemaname AS schema,
    s.sequencename AS sequence,
    dep.table_name AS owned_by_table,
    s.last_value,
    s.max_value,
    round(100.0 * s.last_value / NULLIF(s.max_value, 0), 2) AS consumed_pct
FROM pg_sequences s
LEFT JOIN LATERAL (
    SELECT refc.relname AS table_name
    FROM pg_class seqc
    JOIN pg_namespace seqn ON seqn.oid = seqc.relnamespace
    JOIN pg_depend d ON d.objid = seqc.oid
        AND d.classid = 'pg_class'::regclass
        AND d.refclassid = 'pg_class'::regclass
        AND d.deptype IN ('a', 'i')
    JOIN pg_class refc ON refc.oid = d.refobjid
    WHERE seqc.relkind = 'S'
      AND seqc.relname = s.sequencename
      AND seqn.nspname = s.schemaname
    LIMIT 1
) dep ON true
WHERE 100.0 * s.last_value / NULLIF(s.max_value, 0) > 30
ORDER BY consumed_pct DESC NULLS LAST
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

// sqlDiagStaleStatistics ranks tables by how stale their planner statistics
// are: rows modified since the last ANALYZE relative to the live row count.
// Tables past autovacuum_analyze_scale_factor (10% by default) risk bad plans.
// Two floors keep the list to tables where staleness actually matters:
//   - under 10k live rows a seq scan is cheap regardless of stats, so a high
//     stale_pct there is noise;
//   - under 5% modified the planner's row estimates are still close enough.
//
// The 5% floor is applied to the same expression that computes stale_pct (a
// column alias can't be referenced in WHERE). A never-analyzed table surfaces
// only once its modifications push it past 5% too.
const sqlDiagStaleStatistics = `
SELECT
    schemaname AS schema,
    relname AS table_name,
    n_live_tup AS live_rows,
    n_mod_since_analyze AS modified_rows,
    round(100.0 * n_mod_since_analyze / GREATEST(n_live_tup, 1), 1) AS stale_pct,
    date_trunc('second', now() - GREATEST(last_analyze, last_autoanalyze)) AS analyzed_ago
FROM pg_stat_user_tables
WHERE n_live_tup >= 10000
  AND 100.0 * n_mod_since_analyze / GREATEST(n_live_tup, 1) > 10
ORDER BY stale_pct DESC NULLS LAST
`

// sqlProgressBase unifies every pg_stat_progress_* view into one normalized
// live-operations CTE: what long-running maintenance/DDL is in flight and how
// far along it is. done/total pick each phase's own counter — the one that
// actually moves right now — and unit records what it counts (blocks, tuples,
// lockers, indexes, bytes, rows) so callers can format it; ProgressRow.
// OverallPct composes those per-phase counters into one 0–100 estimate.
// datname carries the operation's own database — the views are cluster-wide,
// so relid can only be resolved there.
const sqlProgressBase = `
WITH prog AS (
    -- heap_blks_scanned only moves during the scanning-heap phase; the index
    -- and second heap passes have their own counters (PG17+), so pick per
    -- phase — otherwise a vacuum reads 100% for its entire index pass.
    SELECT pid, datname, 'VACUUM' AS command, relid, phase,
           CASE phase
                WHEN 'vacuuming indexes'   THEN indexes_processed::numeric
                WHEN 'cleaning up indexes' THEN indexes_processed::numeric
                WHEN 'vacuuming heap'      THEN heap_blks_vacuumed::numeric
                ELSE heap_blks_scanned::numeric
           END AS done,
           CASE phase
                WHEN 'vacuuming indexes'   THEN indexes_total::numeric
                WHEN 'cleaning up indexes' THEN indexes_total::numeric
                ELSE heap_blks_total::numeric
           END AS total,
           CASE phase
                WHEN 'vacuuming indexes'   THEN 'indexes'
                WHEN 'cleaning up indexes' THEN 'indexes'
                ELSE 'blocks'
           END::text AS unit, false AS approx
    FROM pg_stat_progress_vacuum
    UNION ALL
    -- The scan phases count blocks, the sort/load phases tuples, and the
    -- "waiting for …" phases only move their lockers counters — pick per
    -- phase (mirrors sqlReindexProgress) so the counters keep moving through
    -- the whole build instead of parking at 0.
    SELECT pid, datname, 'CREATE INDEX', relid, phase,
           CASE WHEN phase LIKE 'waiting%' THEN lockers_done::numeric
                WHEN blocks_total > 0      THEN blocks_done::numeric
                ELSE tuples_done::numeric
           END,
           CASE WHEN phase LIKE 'waiting%' THEN lockers_total::numeric
                WHEN blocks_total > 0      THEN blocks_total::numeric
                ELSE tuples_total::numeric
           END,
           CASE WHEN phase LIKE 'waiting%' THEN 'lockers'
                WHEN blocks_total > 0      THEN 'blocks'
                ELSE 'tuples'
           END, false
    FROM pg_stat_progress_create_index
    UNION ALL
    SELECT pid, datname, 'ANALYZE', relid, phase, sample_blks_scanned::numeric, sample_blks_total::numeric, 'blocks', false
    FROM pg_stat_progress_analyze
    UNION ALL
    -- heap_blks_scanned/heap_blks_total only apply to the seq-scan phase; the
    -- later phases count tuples with no total, so zero the counters there and
    -- let the phase itself carry the progress (clusterPhaseSpan).
    SELECT pid, datname, 'CLUSTER', relid, phase,
           CASE WHEN phase = 'seq scanning heap' THEN heap_blks_scanned::numeric ELSE 0 END,
           CASE WHEN phase = 'seq scanning heap' THEN heap_blks_total::numeric ELSE 0 END,
           'blocks', false
    FROM pg_stat_progress_cluster
    UNION ALL
    -- COPY reports bytes_processed but bytes_total is 0 for STDIN and PROGRAM/PIPE
    -- sources (nothing seekable to size). For COPY TO of a plain table we can still
    -- estimate progress from tuples_processed against pg_class.reltuples (a stale-able
    -- ANALYZE estimate — hence approx, and pct is clamped since reltuples may lag the
    -- live count), moving the byte volume into the phase column. With a real
    -- bytes_total (file target) or no row estimate, fall back to the byte counters
    -- with rows + IO type in the phase.
    SELECT c.pid, c.datname, c.command, c.relid,
           CASE WHEN c.est_rows IS NOT NULL AND c.bytes_total <= 0
                THEN pg_size_pretty(c.bytes_processed)
                     || CASE WHEN COALESCE(c.type, '') <> '' THEN ' · ' || lower(c.type) ELSE '' END
                ELSE c.tuples_processed || ' rows'
                     || CASE WHEN c.tuples_excluded > 0 THEN ' (' || c.tuples_excluded || ' skipped)' ELSE '' END
                     || CASE WHEN COALESCE(c.type, '') <> '' THEN ' · ' || lower(c.type) ELSE '' END
           END,
           CASE WHEN c.est_rows IS NOT NULL AND c.bytes_total <= 0
                THEN c.tuples_processed::numeric ELSE c.bytes_processed::numeric END,
           CASE WHEN c.est_rows IS NOT NULL AND c.bytes_total <= 0
                THEN c.est_rows ELSE c.bytes_total::numeric END,
           CASE WHEN c.est_rows IS NOT NULL AND c.bytes_total <= 0 THEN 'rows' ELSE 'bytes' END,
           c.est_rows IS NOT NULL AND c.bytes_total <= 0
    FROM (
        SELECT pc.pid, pc.datname, pc.command, pc.relid, pc.type,
               pc.bytes_processed, pc.bytes_total, pc.tuples_processed, pc.tuples_excluded,
               CASE WHEN pc.command = 'COPY TO' AND pc.relid <> 0
                    THEN NULLIF(GREATEST(cl.reltuples, 0), 0)::numeric END AS est_rows
        FROM pg_stat_progress_copy pc
        LEFT JOIN pg_class cl ON cl.oid = pc.relid
    ) c
    UNION ALL
    SELECT pid, NULL::name, 'BASE BACKUP', NULL::oid, phase, backup_streamed::numeric, backup_total::numeric, 'bytes', false
    FROM pg_stat_progress_basebackup
)
`

// sqlDiagProgressAll is the point-in-time diagnostic over sqlProgressBase; the
// live progress monitor uses sqlProgressOps for the same rows with raw counters.
const sqlDiagProgressAll = sqlProgressBase + `
SELECT
    p.pid,
    p.command,
    -- regclass only sees the current database's catalog; for an operation in
    -- another database show "db.<oid>" rather than a bare mystery number.
    CASE WHEN p.relid IS NULL OR p.relid = 0 THEN ''
         WHEN p.datname IS DISTINCT FROM current_database() THEN coalesce(p.datname || '.', '') || p.relid
         ELSE p.relid::regclass::text
    END AS relation,
    p.phase,
    round(100.0 * p.done / NULLIF(p.total, 0), 1) AS done_pct,
    date_trunc('second', now() - a.xact_start) AS running_for,
    a.usename AS username
FROM prog p
LEFT JOIN pg_stat_activity a USING (pid)
ORDER BY done_pct DESC NULLS LAST
`

// sqlProgressOps feeds the live progress monitor: same operations as the
// diagnostic but with raw done/total counters (so the bar can show
// blocks-done/blocks-total) and running_ms in the activity view's float8
// epoch-ms convention. The pid tiebreak keeps equal-pct rows from swapping
// places between refresh ticks.
const sqlProgressOps = sqlProgressBase + `
SELECT
    p.pid,
    p.command,
    -- regclass only sees the current database's catalog; foreign-database
    -- operations come back empty here and ListProgress resolves them through
    -- that database's own pool via relid/database below.
    CASE WHEN p.relid IS NOT NULL AND p.relid <> 0 AND p.datname IS NOT DISTINCT FROM current_database()
         THEN p.relid::regclass::text ELSE '' END AS relation,
    coalesce(p.relid, 0)::oid AS relid,
    coalesce(p.datname, '') AS database,
    p.phase,
    p.unit,
    coalesce(p.done, 0)::bigint AS done,
    coalesce(p.total, 0)::bigint AS total,
    p.approx,
    coalesce(EXTRACT(epoch FROM now() - a.xact_start) * 1000, 0)::float8 AS running_ms,
    coalesce(a.usename::text, '') AS username
FROM prog p
LEFT JOIN pg_stat_activity a USING (pid)
ORDER BY p.done / NULLIF(p.total, 0) DESC NULLS LAST, p.pid
`

// sqlProgressRelNames resolves relation OIDs to names inside the database the
// operation actually runs in (issued through that database's pool). regclass
// schema-qualifies exactly like the in-database path in sqlProgressOps does.
const sqlProgressRelNames = `
SELECT oid, oid::regclass::text FROM pg_class WHERE oid = ANY($1)
`

// sqlDiagLockSummary aggregates pg_locks by lock type and mode: how many locks
// are out, how many backends hold them, and whether anyone is waiting — the
// one-glance contention read before drilling into per-backend detail.
const sqlDiagLockSummary = `
SELECT
    l.locktype,
    l.mode,
    count(*) AS locks,
    count(*) FILTER (WHERE NOT l.granted) AS waiting,
    count(DISTINCT l.pid) AS backends,
    min(l.relation::regclass::text) FILTER (WHERE l.locktype = 'relation') AS sample_relation
FROM pg_locks l
GROUP BY l.locktype, l.mode
ORDER BY count(*) DESC
`

// sqlDiagIdleInXactHolders lists idle-in-transaction backends together with the
// locks their open transaction is still holding — the usual answer to "why is
// this DDL/autovacuum stuck" and "why is bloat growing". xact_age_secs carries
// the numeric sort/bar; locked_relations resolves names only for the current
// database (other databases' relations show as bare OIDs).
const sqlDiagIdleInXactHolders = `
SELECT
    a.pid,
    a.usename AS username,
    a.datname AS database,
    a.state,
    round(EXTRACT(epoch FROM now() - a.xact_start))::bigint AS xact_age_secs,
    date_trunc('second', now() - a.state_change) AS idle_for,
    count(*) FILTER (WHERE l.granted) AS locks_held,
    string_agg(DISTINCT l.relation::regclass::text, ', ')
        FILTER (WHERE l.granted AND l.locktype = 'relation') AS locked_relations,
    a.query AS last_query
FROM pg_stat_activity a
LEFT JOIN pg_locks l ON l.pid = a.pid
WHERE a.state IN ('idle in transaction', 'idle in transaction (aborted)')
GROUP BY a.pid, a.usename, a.datname, a.state, a.xact_start, a.state_change, a.query
ORDER BY a.xact_start
`

// sqlDiagSLRU reports the SLRU (simple LRU) cache counters — transaction status
// (Xact), multixacts, subtransactions, notify, etc. A poor hit ratio or heavy
// blks_read on MultiXact/Subtrans is otherwise-invisible pressure from long
// transactions, SELECT FOR SHARE, or deep savepoint nesting.
const sqlDiagSLRU = `
SELECT
    name,
    blks_hit,
    blks_read,
    round(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) AS hit_pct,
    blks_written,
    blks_exists,
    flushes,
    truncates,
    stats_reset
FROM pg_stat_slru
ORDER BY blks_read DESC
`

// sqlDiagSubscriptionStats shows logical-replication subscriptions in the
// current database with their worker state and error counters. Lag toward the
// publisher can't be computed on the subscriber; the message/report ages are the
// staleness signal instead.
const sqlDiagSubscriptionStats = `
SELECT
    su.subname AS subscription,
    su.subenabled AS enabled,
    st.pid AS worker_pid,
    CASE WHEN st.relid IS NOT NULL THEN st.relid::regclass::text ELSE '' END AS syncing_table,
    st.received_lsn::text AS received_lsn,
    date_trunc('second', now() - st.last_msg_receipt_time) AS last_msg_age,
    date_trunc('second', now() - st.latest_end_time) AS report_age,
    ss.apply_error_count AS apply_errors,
    ss.sync_error_count AS sync_errors
FROM pg_subscription su
LEFT JOIN pg_stat_subscription st ON st.subid = su.oid
LEFT JOIN pg_stat_subscription_stats ss ON ss.subid = su.oid
WHERE su.subdbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY su.subname
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
