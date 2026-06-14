package pg

// sqlActivityBase is the shared SELECT + FROM for the activity tool.  The
// WHERE clause is appended at runtime based on the ActivityFilter value.
// query_id was added to pg_stat_activity in PG 14; all supported server
// versions (PG 17+) have it, so no version gate is needed.
const sqlActivityBase = `
SELECT
    a.pid,
    coalesce(a.datname, '') AS datname,
    coalesce(a.usename, '') AS usename,
    coalesce(a.application_name, '') AS application_name,
    coalesce(host(a.client_addr), '') AS client_addr,
    coalesce(a.backend_type, '') AS backend_type,
    coalesce(a.state, '') AS state,
    coalesce(a.wait_event_type, '') AS wait_event_type,
    coalesce(a.wait_event, '') AS wait_event,
    coalesce(a.backend_xid::text, '') AS backend_xid,
    coalesce(a.backend_xmin::text, '') AS backend_xmin,
    coalesce(
        EXTRACT(epoch FROM now() - a.query_start) * 1000, 0
    )::float8 AS query_age_ms,
    coalesce(
        EXTRACT(epoch FROM now() - a.xact_start) * 1000, 0
    )::float8 AS xact_age_ms,
    coalesce(
        EXTRACT(epoch FROM now() - a.state_change) * 1000, 0
    )::float8 AS state_age_ms,
    coalesce(a.query_id, 0) AS query_id,
    coalesce(left(regexp_replace(a.query, '\s+', ' ', 'g'), 300), '') AS query
FROM pg_stat_activity a
WHERE a.pid <> pg_backend_pid()
`

// sqlActivityActiveWaiting filters to backends that are actively running or
// blocked on a wait event — the pg_activity default view.
const sqlActivityActiveWaiting = sqlActivityBase + `
  AND (a.state = 'active' OR a.wait_event IS NOT NULL)
ORDER BY a.query_start ASC NULLS LAST
`

// sqlActivityNonIdle shows everything except plain idle backends, which surfaces
// idle-in-transaction connections that may be holding locks.
const sqlActivityNonIdle = sqlActivityBase + `
  AND a.state IS NOT NULL
  AND a.state <> 'idle'
ORDER BY a.query_start ASC NULLS LAST
`

// sqlActivityAll shows every backend including idle connections.
const sqlActivityAll = sqlActivityBase + `
ORDER BY a.query_start ASC NULLS LAST
`
