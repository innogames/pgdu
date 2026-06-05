package pg

// --- WAL inspector (toolWAL) ---

// sqlWALWindow resolves the [start, end] LSN window the WAL inspector
// analyses: end is the current write position, start is `end − $1 bytes`
// clamped at the very start of the WAL so the subtraction never underflows
// the pg_lsn range. Both are returned as text so pgx scans them as plain
// strings (pg_lsn has no registered pgx codec). $1 is the window size in
// bytes. A brand-new cluster with less than $1 of WAL yields start='0/0',
// which pg_get_wal_stats rejects as "not available" — acceptable since any
// server old enough to have pg_walinspect installed has long passed that.
const sqlWALWindow = `
SELECT (CASE
          WHEN (cur - '0/0'::pg_lsn) > $1::numeric THEN cur - $1::numeric
          ELSE '0/0'::pg_lsn
        END)::text AS start_lsn,
       cur::text AS end_lsn
FROM   (SELECT pg_current_wal_lsn() AS cur) q
`

// sqlWALSummary is the header block: current insert/flush position, the
// segment file the write head sits in, wal_level, the count and on-disk size
// of segment files in pg_wal, and the cluster-wide pg_stat_wal counters.
// Uses only built-in functions (no pg_walinspect) so the header renders even
// when the extension is absent — but pg_ls_waldir / pg_stat_wal still require
// a sufficiently-privileged role, so the caller treats a failure as non-fatal.
const sqlWALSummary = `
SELECT pg_current_wal_insert_lsn()::text                       AS insert_lsn,
       pg_current_wal_lsn()::text                              AS flush_lsn,
       pg_walfile_name(pg_current_wal_lsn())                   AS current_file,
       current_setting('wal_level')                            AS wal_level,
       (SELECT count(*) FROM pg_ls_waldir())                   AS seg_files,
       (SELECT COALESCE(sum(size), 0)::bigint FROM pg_ls_waldir()) AS seg_bytes,
       w.wal_records,
       w.wal_fpi,
       w.wal_bytes::bigint                                     AS wal_bytes
FROM   pg_stat_wal w
`

// sqlWALRmgrStats aggregates the window by resource manager: count, the bytes
// spent on record data vs. full-page images, and their sum. Ordered biggest
// combined-size first; callers may resort. $1/$2 are start/end LSN.
// NOTE: pg_get_wal_stats names its first output column
// "resource_manager/record_type" (a literal slash) — the same column doubles
// as the record-type label when per_record=true. It must be double-quoted.
const sqlWALRmgrStats = `
SELECT "resource_manager/record_type" AS resource_manager,
       count,
       record_size,
       fpi_size,
       combined_size
FROM   pg_get_wal_stats($1::pg_lsn, $2::pg_lsn, false)
WHERE  count > 0
ORDER  BY combined_size DESC
`

// sqlWALRecordTypeStats is the same pg_get_wal_stats source as
// sqlWALRmgrStats but with per_record=true, so the byte/count breakdown is
// per record-type instead of per resource-manager. The first column then
// reads "Rmgr/RecordType" (e.g. "Heap/INSERT"); $3 filters to one rmgr by
// its "<rmgr>/" prefix. Powers the summary table above the records list.
const sqlWALRecordTypeStats = `
SELECT "resource_manager/record_type" AS record_type,
       count,
       record_size,
       fpi_size,
       combined_size
FROM   pg_get_wal_stats($1::pg_lsn, $2::pg_lsn, true)
WHERE  count > 0
  AND  "resource_manager/record_type" LIKE $3 || '/%'
ORDER  BY combined_size DESC
`

// sqlWALRecords lists individual records in the window for one resource
// manager, in LSN (chronological) order. $1/$2 are start/end LSN, $3 the
// resource_manager name to filter on.
const sqlWALRecords = `
SELECT start_lsn::text,
       end_lsn::text,
       prev_lsn::text,
       xid::text,
       resource_manager,
       record_type,
       record_length,
       main_data_length,
       fpi_length,
       COALESCE(description, ''),
       COALESCE(block_ref, '')
FROM   pg_get_wal_records_info($1::pg_lsn, $2::pg_lsn)
WHERE  resource_manager = $3
ORDER  BY start_lsn
`

// sqlWALBlocks lists the block references of a single record spanning
// [$1, $2) — the record's own start and end LSN. The range must include the
// record (a zero-width [start, start) range matches nothing), so the caller
// passes the record's end_lsn as the upper bound. show_data=false skips the
// raw block/FPI bytes — pgdu only needs the lengths and the FPI flag.
// Requires PostgreSQL 16+ (the function did not exist in 15).
// block_id and relforknumber are smallint; cast to int so they scan into
// int32 regardless of pgx's int2 widening rules. block_fpi_info is text[]
// (a list of flag names) and arrives as a Go []string — NULL becomes nil, so
// no COALESCE (and an array can't be COALESCEd with a text literal anyway).
// The lateral resolves the relfilenode back to a relation name via
// pg_filenode_relation: NULL (→ ”) when the relation lives in another
// database or has been dropped, in which case the caller falls back to the
// numeric relfilenode. pg_filenode_relation normalises the WAL's tablespace
// OID to pg_class's 0-for-default form internally, so passing reltablespace
// straight through is correct. When the resolved relation is a TOAST table we
// hop to its owning table (pg_class.reltoastrelid) and report that name plus an
// is_toast flag, so the row shows the user-facing table rather than the opaque
// pg_toast.pg_toast_<oid>. reldatabase is resolved to a datname against the
// shared pg_database catalog (” for OID 0 / shared relations → numeric
// fallback).
const sqlWALBlocks = `
SELECT block_id::int,
       reltablespace,
       reldatabase,
       relfilenode,
       relforknumber::int,
       relblocknumber,
       resource_manager,
       record_type,
       block_data_length,
       block_fpi_length,
       block_fpi_info,
       COALESCE(description, ''),
       COALESCE(r.relname, ''),
       COALESCE(r.is_toast, false),
       COALESCE((SELECT datname FROM pg_database WHERE oid = b.reldatabase), '')
FROM   pg_get_wal_block_info($1::pg_lsn, $2::pg_lsn, false) AS b
LEFT   JOIN LATERAL (
         SELECT
           CASE WHEN owner.oid IS NOT NULL
                THEN owner.oid::regclass::text
                ELSE f.relid::text
           END AS relname,
           owner.oid IS NOT NULL AS is_toast
         FROM   (SELECT pg_filenode_relation(b.reltablespace, b.relfilenode) AS relid) f
         LEFT   JOIN pg_class owner ON owner.reltoastrelid = f.relid::oid
         WHERE  f.relid IS NOT NULL
       ) r ON true
ORDER  BY block_id
`
