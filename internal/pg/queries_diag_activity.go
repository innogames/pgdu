package pg

// SQL for the 'activity' diagnostics (registry: diag_defs_activity.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

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

// sqlDiagBackendIO is PG18's per-backend I/O and WAL accounting: who is doing
// the reads and writes *right now*, next to the query doing them. The
// cluster-wide io_stats diagnostic answers "which backend type"; this one
// answers "which backend", which is the question you have when one session is
// hurting the server.
//
// pg_stat_get_backend_io() returns pg_stat_io's own shape for a single pid, so
// the counters are summed across its object/context rows into one line per
// backend; pg_stat_get_backend_wal() returns one row. Both are LEFT JOIN
// LATERAL because a backend that exits between the pg_stat_activity snapshot
// and the function call yields no row, which must not drop the backend.
// Requires PostgreSQL 18 (Diagnostic.MinVersion) — the functions do not exist
// before it, so there is nothing to degrade to.
const sqlDiagBackendIO = `
SELECT
    a.pid,
    coalesce(a.datname, '') AS database,
    coalesce(a.usename, '') AS username,
    coalesce(a.backend_type, '') AS backend_type,
    coalesce(a.state, '') AS state,
    io.reads,
    io.read_bytes,
    io.writes,
    io.write_bytes,
    io.extends,
    io.hits,
    io.evictions,
    io.fsyncs,
    w.wal_records,
    w.wal_fpi,
    w.wal_bytes,
    coalesce(left(btrim(regexp_replace(a.query, '\s+', ' ', 'g')), 120), '') AS query
FROM pg_stat_activity a
LEFT JOIN LATERAL (
    SELECT coalesce(sum(b.reads), 0)       AS reads,
           coalesce(sum(b.read_bytes), 0)  AS read_bytes,
           coalesce(sum(b.writes), 0)      AS writes,
           coalesce(sum(b.write_bytes), 0) AS write_bytes,
           coalesce(sum(b.extends), 0)     AS extends,
           coalesce(sum(b.hits), 0)        AS hits,
           coalesce(sum(b.evictions), 0)   AS evictions,
           coalesce(sum(b.fsyncs), 0)      AS fsyncs
    FROM pg_stat_get_backend_io(a.pid) b
) io ON true
LEFT JOIN LATERAL (
    SELECT bw.wal_records, bw.wal_fpi, bw.wal_bytes::bigint AS wal_bytes
    FROM pg_stat_get_backend_wal(a.pid) bw
) w ON true
WHERE coalesce(io.reads, 0) + coalesce(io.writes, 0) + coalesce(io.hits, 0)
    + coalesce(w.wal_records, 0) > 0
ORDER BY coalesce(io.reads, 0) + coalesce(io.writes, 0) DESC
`

// sqlDiagAIOs lists the asynchronous I/O handles in flight, the view PG18 added
// alongside io_method. Columns are taken as the server defines them: this is a
// brand-new view whose shape we deliberately do not pin, and the generic result
// table renders whatever comes back. Reading it can require privileges the
// session lacks, which surfaces as the query error like any other diagnostic.
// Requires PostgreSQL 18 (Diagnostic.MinVersion).
const sqlDiagAIOs = `SELECT * FROM pg_aios`
