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
    t.n_live_tup AS estimated_rows_covered
FROM pg_catalog.pg_stat_user_indexes i
JOIN pg_catalog.pg_stat_user_tables t ON t.relid = i.relid
WHERE i.schemaname NOT IN ('pg_catalog','information_schema')
  AND i.schemaname NOT LIKE 'pg\_toast%'
  AND t.n_live_tup >= 100
ORDER BY pg_relation_size(i.indexrelid) DESC
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
