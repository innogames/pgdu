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
    -- PIDs blocking this backend (pg_blocking_pids); empty when nobody blocks it.
    -- Rendered as an opt-in column so the lock-wait relationship reads inline.
    coalesce(array_to_string(pg_blocking_pids(a.pid), ' '), '') AS blocked_by,
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

// sqlToastOwners maps each TOAST relation named in $1 (bare relnames like
// pg_toast_21853, without the pg_toast. schema prefix) to the table that owns
// it — reltoastrelid points from the main relation to its TOAST relation, so we
// join back the other way. The owner is schema-qualified only when it isn't in
// public, matching how the parsed query text usually names ordinary tables.
const sqlToastOwners = `
SELECT
    t.relname,
    CASE WHEN n.nspname = 'public' THEN c.relname
         ELSE n.nspname || '.' || c.relname END
FROM pg_class t
JOIN pg_class c ON c.reltoastrelid = t.oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE t.relnamespace = 'pg_toast'::regnamespace
  AND t.relname = ANY($1)
`

// sqlActivitySummary returns one row of server-wide backend counts plus the
// connection limit, independent of the row filter. "waiting" is restricted to
// genuine contention wait classes; Client (ClientRead/Write), Activity (idle
// main loops) and Timeout (throttles like VacuumDelay) are NOT real blocks, so
// a backend parked on them counts as idle/active, never waiting. This is what
// keeps idle ClientRead connections out of the red "waiting" total.
const sqlActivitySummary = `
SELECT
    count(*) FILTER (WHERE backend_type = 'client backend')::int AS conns,
    count(*) FILTER (
        WHERE state = 'active'
          AND (wait_event_type IS NULL
               OR wait_event_type NOT IN ('Lock','LWLock','BufferPin','IO','IPC','Extension'))
    )::int AS active,
    count(*) FILTER (
        WHERE state = 'active'
          AND wait_event_type IN ('Lock','LWLock','BufferPin','IO','IPC','Extension')
    )::int AS waiting,
    count(*) FILTER (WHERE state LIKE 'idle in transaction%')::int AS idle_in_xact,
    count(*) FILTER (WHERE state = 'idle')::int AS idle,
    count(*) FILTER (
        WHERE state IS NOT NULL
          AND state <> 'active'
          AND state <> 'idle'
          AND state NOT LIKE 'idle in transaction%'
    )::int AS other,
    current_setting('max_connections')::int AS max_connections
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
`
