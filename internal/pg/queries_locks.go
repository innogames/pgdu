package pg

// sqlLockWaiters returns every backend that is part of a lock-wait relationship:
// either it is blocked (pg_blocking_pids returns a non-empty set) or it blocks
// someone else. For each such backend it reports identity, state, transaction
// age, the relation/lock it is waiting on (NULL when it isn't waiting), and its
// blockers as an int array — the TUI assembles the forest from the pid→blockers
// edges. A backend appears in the blocking set when its pid shows up in any
// other backend's pg_blocking_pids result.
//
// The waited-on lock is the ungranted row in pg_locks for the blocked backend;
// relation locks resolve to a regclass name, other lock types show their type.
const sqlLockWaiters = `
WITH blocking AS (
    SELECT pid, pg_blocking_pids(pid) AS blockers
    FROM pg_stat_activity
    WHERE pid <> pg_backend_pid()
),
involved AS (
    SELECT pid FROM blocking WHERE cardinality(blockers) > 0
    UNION
    SELECT DISTINCT unnest(blockers) FROM blocking
),
waited AS (
    -- The lock each blocked backend is waiting to acquire (its ungranted row).
    -- DISTINCT ON keeps one representative wait per backend.
    SELECT DISTINCT ON (l.pid)
        l.pid,
        l.locktype,
        l.mode,
        CASE WHEN l.locktype = 'relation' AND l.relation IS NOT NULL
             THEN l.relation::regclass::text END AS wait_relation
    FROM pg_locks l
    WHERE NOT l.granted
    ORDER BY l.pid
)
SELECT
    a.pid,
    coalesce(b.blockers, '{}') AS blockers,
    coalesce(a.datname, '') AS datname,
    coalesce(a.usename, '') AS usename,
    coalesce(a.application_name, '') AS application_name,
    coalesce(a.state, '') AS state,
    coalesce(a.wait_event_type, '') AS wait_event_type,
    coalesce(a.wait_event, '') AS wait_event,
    coalesce(EXTRACT(epoch FROM now() - a.xact_start) * 1000, 0)::float8 AS xact_age_ms,
    coalesce(w.locktype, '') AS wait_locktype,
    coalesce(w.mode, '') AS wait_mode,
    coalesce(w.wait_relation, '') AS wait_relation,
    coalesce(left(regexp_replace(a.query, '\s+', ' ', 'g'), 300), '') AS query
FROM involved i
JOIN pg_stat_activity a ON a.pid = i.pid
LEFT JOIN blocking b ON b.pid = i.pid
LEFT JOIN waited w ON w.pid = i.pid
ORDER BY a.pid
`
