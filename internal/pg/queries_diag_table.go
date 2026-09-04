package pg

// SQL for the 'table' diagnostics (registry: diag_defs_table.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

const sqlDiagTableShowHitratio = `
WITH hitratio AS (
    SELECT
        schemaname AS schema,
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
    schemaname AS schema,
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
    schemaname AS schema,
    relname,
    n_tup_upd                  AS updates,
    n_tup_hot_upd              AS hot_updates,
    n_tup_upd - n_tup_hot_upd  AS non_hot_updates,
    round(100.0 * n_tup_hot_upd / NULLIF(n_tup_upd, 0), 1) AS hot_pct
FROM pg_stat_user_tables
WHERE n_tup_upd > 0
ORDER BY non_hot_updates DESC
`

// sqlDiagTableFillfactor suggests a per-table FILLFACTOR for update-active
// tables (n_tup_upd > 0, heap ≥ 1 MB — below that the setting is noise).
// Starting point per the row-size method: 100 − ceil(avg_row/8192×100), i.e.
// leave one average row's worth of free space per page so an update can land
// HOT; clamped to ≥ 50 because wide/toasted rows would otherwise push the
// suggestion into wasteful territory. Adjustments: tables updated less than
// 0.1× per live row keep 100 (free space would only dilute the cache); tables
// updated ≥ 1× per row that still miss HOT (hot_pct < 80) get 10 extra points
// of headroom for repeated in-place rewrites. avg_row_bytes is heap/live-rows,
// so bloat and stale n_live_tup inflate it — hence the ANALYZE caveat in Help.
const sqlDiagTableFillfactor = `
WITH t AS (
    SELECT
        s.schemaname AS schema,
        s.relname,
        s.n_live_tup,
        s.n_tup_upd,
        s.n_tup_hot_upd,
        pg_relation_size(s.relid) AS table_size_bytes,
        coalesce((SELECT option_value::int
                  FROM pg_options_to_table(c.reloptions)
                  WHERE option_name = 'fillfactor'), 100) AS current_fill,
        pg_relation_size(s.relid) / s.n_live_tup AS avg_row_bytes
    FROM pg_stat_user_tables s
    JOIN pg_class c ON c.oid = s.relid
    WHERE s.n_tup_upd > 0
      AND s.n_live_tup > 0
      AND pg_relation_size(s.relid) >= 1048576
),
calc AS (
    SELECT *,
        round(100.0 * n_tup_hot_upd / n_tup_upd, 1) AS hot_pct,
        round(n_tup_upd::numeric / n_live_tup, 2)   AS upd_per_row,
        greatest(50, 100 - ceil(100.0 * avg_row_bytes / 8192))::int AS start_fill
    FROM t
)
SELECT
    schema,
    relname,
    current_fill,
    CASE
        WHEN upd_per_row < 0.1 THEN 100
        WHEN upd_per_row >= 1 AND hot_pct < 80 THEN greatest(50, start_fill - 10)
        ELSE start_fill
    END AS suggested_fill,
    hot_pct,
    n_tup_upd                 AS updates,
    n_tup_upd - n_tup_hot_upd AS non_hot_updates,
    upd_per_row,
    avg_row_bytes,
    n_live_tup                AS live_rows,
    table_size_bytes
FROM calc
ORDER BY non_hot_updates DESC
`

const sqlDiagTableScanTypes = `
SELECT
    schemaname AS schema,
    relname,
    seq_scan,
    idx_scan,
    seq_tup_read,
    idx_tup_fetch,
    round(cast(idx_tup_fetch AS numeric) / (idx_tup_fetch + seq_tup_read) * 100, 2) AS index_read_pct,
    pg_size_pretty(pg_relation_size(relid)) AS size_on_disk
FROM pg_stat_user_tables
WHERE (idx_tup_fetch + seq_tup_read) > 0
  AND cast(idx_tup_fetch AS numeric) / (idx_tup_fetch + seq_tup_read) < 0.8
  AND pg_relation_size(relid) > 800000
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
