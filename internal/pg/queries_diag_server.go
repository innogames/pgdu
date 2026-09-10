package pg

// SQL for the 'server' diagnostics (registry: diag_defs_server.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

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

// sqlDiagDatabaseStats reports the per-database picture from pg_stat_database:
// transaction volume with a derived rollback %, cache hit ratio, tuple I/O,
// recovery-conflict and deadlock counters, temp-file pressure, block- and
// session-time totals, and the live session count. The *_time counters are
// cumulative milliseconds since stats_reset; they are divided down to seconds
// (round() needs a numeric, hence the ::numeric cast on the double-precision
// source) so the values stay readable. All columns beyond the headline hit_pct
// bar are opt-out via the C column picker; conflicts, rollback_pct and sessions
// additionally start hidden (Diagnostic.DefaultHidden).
//
// stats_age_secs is the length of the window the cumulative counters cover, so
// a reader can grade deadlocks/temp_bytes as a per-day rate instead of by raw
// total. A never-reset entry has a NULL stats_reset; the postmaster start is the
// nearest honest lower bound for it (stats never predate the running instance).
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
    stats_reset,
    extract(epoch FROM now() - COALESCE(stats_reset, pg_postmaster_start_time()))::bigint AS stats_age_secs
FROM pg_stat_database
WHERE datname IS NOT NULL
ORDER BY xact_commit + xact_rollback DESC
`

// sqlDiagSequences reports how close each sequence is to running out of values,
// restricted to the ones already more than 30% through their range. last_value
// is null without SELECT/USAGE on the sequence; those rows have an unknown
// consumed_pct and so fall below the filter (NULL > 30 is not true) and are
// excluded along with the low-usage sequences. Cycling sequences are excluded
// outright — they wrap instead of failing, so they have no exhaustion risk to
// report no matter how far along they are.
//
// consumed_pct measures progress through the sequence's *own* range, from
// start_value rather than from zero. A sequence deliberately parked high in the
// type's range (say start_value 2000000000 on an int4 to reserve a band for
// synthetic ids) is at 93% of max_value the moment it is created and stays there
// forever; measuring from start_value reports what it has actually handed out.
// remaining is the raw number of values left, which is the figure to act on —
// pair it with how fast the sequence moves to decide whether the ceiling
// matters. Both are computed in the sequence's direction of travel, so a
// descending sequence counts down toward min_value; limit_value names whichever
// bound it is heading for. The arithmetic is in numeric because the span of a
// full-range int8 sequence overflows int8.
//
// used_by names the column the sequence feeds, resolved through pg_depend in two
// passes because ownership and use are separate things in the catalog:
//
//   - SERIAL, GENERATED … AS IDENTITY and an explicit OWNED BY record an
//     auto/internal dependency from the sequence to the owning column. That is
//     the authoritative answer, so it is preferred.
//   - A sequence created standalone and wired up by hand
//     (DEFAULT nextval('…')) has no such dependency — only a normal dependency
//     from the column's pg_attrdef entry to the sequence, pointing the other
//     way. Without this second pass such a sequence reads as unowned even
//     though inserts are consuming it, which is exactly the case worth
//     alerting on. Several columns may reference one sequence, so they are
//     aggregated.
//
// Both are best-effort: a sequence consumed only from application code, a
// function body or a trigger leaves no catalog trace at all and still shows
// "—". The lookup is a LEFT JOIN LATERAL so those sequences stay in the list.
const sqlDiagSequences = `
SELECT
    s.schemaname AS schema,
    s.sequencename AS sequence,
    dep.used_by,
    s.last_value,
    CASE WHEN s.increment_by > 0 THEN s.max_value ELSE s.min_value END AS limit_value,
    u.remaining,
    round(100.0 * u.consumed / NULLIF(u.span, 0), 2) AS consumed_pct
FROM pg_sequences s
CROSS JOIN LATERAL (
    SELECT
        CASE WHEN s.increment_by > 0
             THEN s.last_value::numeric - s.start_value
             ELSE s.start_value::numeric - s.last_value END AS consumed,
        CASE WHEN s.increment_by > 0
             THEN s.max_value::numeric - s.start_value
             ELSE s.start_value::numeric - s.min_value END AS span,
        CASE WHEN s.increment_by > 0
             THEN s.max_value::numeric - s.last_value
             ELSE s.last_value::numeric - s.min_value END AS remaining
) u
LEFT JOIN LATERAL (
    SELECT coalesce(owned.col, dflt.cols) AS used_by
    FROM (
        SELECT to_regclass(quote_ident(s.schemaname) || '.' || quote_ident(s.sequencename)) AS oid
    ) sq
    LEFT JOIN LATERAL (
        SELECT refc.relname || '.' || a.attname AS col
        FROM pg_depend d
        JOIN pg_class refc ON refc.oid = d.refobjid
        JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
        WHERE d.classid = 'pg_class'::regclass
          AND d.objid = sq.oid
          AND d.refclassid = 'pg_class'::regclass
          AND d.deptype IN ('a', 'i')
        LIMIT 1
    ) owned ON true
    LEFT JOIN LATERAL (
        SELECT string_agg(DISTINCT c.relname || '.' || a.attname, ', ') AS cols
        FROM pg_depend d
        JOIN pg_attrdef ad ON ad.oid = d.objid
        JOIN pg_class c ON c.oid = ad.adrelid
        JOIN pg_attribute a ON a.attrelid = ad.adrelid AND a.attnum = ad.adnum
        WHERE d.classid = 'pg_attrdef'::regclass
          AND d.refclassid = 'pg_class'::regclass
          AND d.refobjid = sq.oid
          AND d.deptype = 'n'
    ) dflt ON true
) dep ON true
WHERE NOT s.cycle
  AND 100.0 * u.consumed / NULLIF(u.span, 0) > 30
ORDER BY consumed_pct DESC NULLS LAST
`

// sqlDiagIOStats is pg_stat_io (PG16+) with the idle rows dropped and two
// derived columns: the mean read latency (NULL until track_io_timing is on) and
// the shared-buffers hit rate of that (backend, object, context) combination.
// The *_time columns are cumulative milliseconds, divided to seconds like
// database_stats does. Rows are ordered by total traffic so the busiest
// backend/context pair leads.
const sqlDiagIOStats = `
SELECT
    backend_type,
    object,
    context,
    reads,
    round((read_time / NULLIF(reads, 0))::numeric, 3) AS read_ms,
    hits,
    round(100.0 * hits / NULLIF(hits + reads, 0), 2) AS hit_pct,
    writes,
    round(write_time::numeric / 1000.0, 1) AS write_secs,
    extends,
    evictions,
    reuses,
    fsyncs,
    round(fsync_time::numeric / 1000.0, 1) AS fsync_secs,
    stats_reset
FROM pg_stat_io
WHERE COALESCE(reads, 0) + COALESCE(writes, 0) + COALESCE(hits, 0) + COALESCE(extends, 0)
    + COALESCE(evictions, 0) + COALESCE(fsyncs, 0) > 0
ORDER BY COALESCE(reads, 0) + COALESCE(writes, 0) + COALESCE(hits, 0) DESC
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
