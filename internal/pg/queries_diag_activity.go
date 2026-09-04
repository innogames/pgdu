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
