package pg

// SQL for the 'wal' diagnostics (registry: diag_defs_wal.go). Plain
// SELECTs with no parameters; any identifier filtering is baked in.

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
