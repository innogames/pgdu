package pg

// Size-history SQL: the capture a growth window diffs. All of it is
// version-neutral — PG 17, 18 and 19 accept every construct unchanged — so this
// is the first of CLAUDE.md's four version tools: no version ladder, no
// to_jsonb hatch, no catalog probe. A newer per-table counter wanted here later
// should reach for tool (2) the way sqlTableStats does.

// sqlSizeSample is sqlTables' database-wide sibling: one row per table, matview
// and partition, carrying the parent link and partition bound the growth
// roll-up needs plus the vacuum counters that explain a delta.
//
// It deliberately does NOT roll partitions into their root the way
// sqlDiagTableShowSize does. The partition tree differs between any two
// samples — partitions get attached, detached and dropped — so the fold is a
// property of the matched pair, and only the client holds both files. Rolling
// up here would also make the per-partition scope unrecoverable from the
// stored sample.
//
// Summing total_bytes over these rows is correct and not a double count:
// pg_total_relation_size does not recurse into partitions, and pg_indexes_size
// on a partitioned parent is 0, so a parent contributes ~0 and every byte is
// attributed to exactly the partition that holds it. That is simultaneously why
// the sum works and why the roll-up has to exist at all.
//
// COALESCE around the size calls covers the NULL a relation dropped between the
// catalog scan and the size call yields; it reappears as a `gone` row in the
// next window. An outright error fails the capture, which for a one-shot cron
// is the right answer — the next run succeeds.
const sqlSizeSample = `
SELECT c.oid,
       n.nspname,
       c.relname,
       c.relkind::text,
       c.relfilenode,
       COALESCE(i.inhparent, 0)::oid                          AS parent_oid,
       COALESCE(pg_get_expr(c.relpartbound, c.oid, true), '') AS part_bound,
       COALESCE(pg_relation_size(c.oid), 0)                   AS heap_bytes,
       COALESCE(pg_indexes_size(c.oid), 0)                    AS index_bytes,
       COALESCE(pg_total_relation_size(c.reltoastrelid), 0)   AS toast_bytes,
       COALESCE(pg_total_relation_size(c.oid), 0)             AS total_bytes,
       c.reltuples::bigint                                    AS est_rows,
       COALESCE(s.n_live_tup, 0),
       COALESCE(s.n_dead_tup, 0),
       COALESCE(s.n_tup_ins, 0),
       COALESCE(s.n_tup_del, 0),
       COALESCE(s.n_ins_since_vacuum, 0)
FROM   pg_class c
JOIN   pg_namespace n            ON n.oid = c.relnamespace
LEFT   JOIN pg_inherits i        ON i.inhrelid = c.oid
LEFT   JOIN pg_stat_all_tables s ON s.relid = c.oid
WHERE  c.relkind IN ('r','m','p')
  AND  n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
  AND  n.nspname NOT LIKE 'pg_temp_%'
  AND  n.nspname NOT LIKE 'pg_toast_temp_%'
ORDER  BY total_bytes DESC
`

// sqlSizeMeta is the one-row identity read: which database this is, its block
// size, and when its statistics were last reset (differing resets between two
// samples mean the row counters cannot be diffed, though the sizes still can).
// Every column is readable by any role that can connect.
const sqlSizeMeta = `
SELECT (SELECT oid FROM pg_database WHERE datname = current_database()),
       current_setting('block_size')::bigint,
       (SELECT stats_reset FROM pg_stat_database WHERE datname = current_database())
`

// sqlSizeSystemID is kept a separate statement on purpose: EXECUTE on
// pg_control_system() is revoked from PUBLIC, so folding it into sqlSizeMeta
// would lose the database oid to the same permission error. Read best-effort,
// an unprivileged role simply yields 0, which SizeSample documents as unknown.
const sqlSizeSystemID = `SELECT system_identifier FROM pg_control_system()`

// sqlSizeDatabaseSize is the current database's total on-disk size, for the
// growth browser's live "now" row. It is one cheap call, so the picker can bar
// the live end without running a whole capture.
const sqlSizeDatabaseSize = `SELECT pg_database_size(current_database())`
