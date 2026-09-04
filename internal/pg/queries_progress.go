package pg

// Progress-tool SQL: the normalized union over pg_stat_progress_* views
// shared by the progress level and the progress_all diagnostic.

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
