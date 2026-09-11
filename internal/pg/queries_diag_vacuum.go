package pg

// SQL for the 'vacuum' diagnostics (registry: diag_defs_vacuum.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

// sqlVacuumRelSetCTE pulls each relation's autovacuum_vacuum_threshold /
// autovacuum_vacuum_scale_factor storage-parameter overrides out of reloptions
// (NULL when unset, so callers COALESCE to the cluster GUC). Shared by the
// per-table vacuum-stats diagnostic and the system overview's over-threshold
// count so both apply the identical trigger formula.
const sqlVacuumRelSetCTE = `
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
`

const sqlDiagVacuumStats = sqlVacuumRelSetCTE + `
SELECT
    PSUT.schemaname AS schema,
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

// sqlDiagWraparoundTables ranks tables by how far their oldest unfrozen XID
// (relfrozenxid, and their TOAST relation's) trails the current XID.
//
// Deliberately *not* ranked by distance to the next anti-wraparound autovacuum:
// age / autovacuum_freeze_max_age measures the routine maintenance event
// PostgreSQL is designed around, so on a busy cluster every table cycles through
// 100% every few days and the number says nothing about danger. The danger
// denominators are vacuum_failsafe_age (where VACUUM drops its cost delay and
// index cleanup to catch up) and the 2^31 hard stop where the server refuses
// writes; pct_freeze_max is kept, hidden by default, as "distance to the next
// routine freeze".
//
// main_xid_age and toast_xid_age are reported separately because the folded
// greatest() hides the common case where the *TOAST* relation is what is old:
// toast_last_autovacuum NULL next to a large toast_xid_age is a TOAST table
// autovacuum has never reached. toast_xid_age is NULL (rendered "—") for a table
// with no TOAST relation rather than a misleading 0.
//
// last_autovacuum and autovacuum_count separate the remaining root causes: a
// high age next to a recent autovacuum, or a large autovacuum_count that still
// hasn't dropped the age, means the vacuums that ran were non-aggressive and
// skipped all-visible pages — or that the xmin horizon is pinned, so no vacuum
// can freeze past it (the system overview's xmin_horizon finding says which).
// An old or absent last_autovacuum instead means autovacuum isn't reaching the
// table at all.
//
// Only ordinary tables and matviews are considered; TOAST is attributed to its
// parent via reltoastrelid, and partitioned parents (relfrozenxid 0, so age() is
// meaninglessly huge) are excluded. vacuum_failsafe_age is read with missing_ok
// so a server without it (PG < 14) falls back to the 1.6 B default rather than
// failing the whole query.
const sqlDiagWraparoundTables = `
SELECT
    n.nspname AS schema,
    c.relname AS table_name,
    g.max_xid_age,
    g.main_xid_age,
    g.toast_xid_age,
    mxid_age(c.relminmxid) AS mxid_age,
    round(100.0 * g.max_xid_age / 2147483647, 3) AS pct_of_wraparound,
    round(100.0 * g.max_xid_age
          / COALESCE(NULLIF(current_setting('vacuum_failsafe_age', true)::numeric, 0),
                     1600000000), 2) AS pct_of_failsafe,
    round(100.0 * g.max_xid_age
          / current_setting('autovacuum_freeze_max_age')::numeric, 1) AS pct_freeze_max,
    pg_table_size(c.oid) AS size_bytes,
    st.n_dead_tup AS dead_tuples,
    st.autovacuum_count,
    st.last_autovacuum,
    ts.autovacuum_count AS toast_autovacuum_count,
    ts.last_autovacuum AS toast_last_autovacuum
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_class tc ON tc.oid = c.reltoastrelid
LEFT JOIN pg_stat_all_tables st ON st.relid = c.oid
LEFT JOIN pg_stat_all_tables ts ON ts.relid = c.reltoastrelid
CROSS JOIN LATERAL (
    SELECT age(c.relfrozenxid) AS main_xid_age,
           age(tc.relfrozenxid) AS toast_xid_age,
           greatest(age(c.relfrozenxid), COALESCE(age(tc.relfrozenxid), 0)) AS max_xid_age
) g
WHERE c.relkind IN ('r', 'm')
  AND c.relfrozenxid <> 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY g.max_xid_age DESC
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
