package pg

import "fmt"

// --- WAL inspector (toolWAL) ---

// sqlWALWindow resolves the [start, end] LSN window the WAL inspector
// analyses: end is the current write position, start is `end − $1 bytes`
// clamped at the very start of the WAL so the subtraction never underflows
// the pg_lsn range. Both are returned as text so pgx scans them as plain
// strings (pg_lsn has no registered pgx codec). $1 is the window size in
// bytes. A brand-new cluster with less than $1 of WAL yields start='0/0',
// which pg_get_wal_stats rejects as "not available" — acceptable since any
// server old enough to have pg_walinspect installed has long passed that.
//
// This is the fallback form, used only when the privileged sqlWALWindowClamped
// can't run (pg_ls_waldir needs pg_monitor/superuser). Clamping only at '0/0'
// is unsafe in general: when the write head sits less than $1 into a fresh
// segment, `cur − $1` reaches back into the previous segment, which a
// checkpoint may already have recycled — pg_get_wal_stats then fails with
// "requested WAL segment … has already been removed".
//
// The head is walHeadLSN rather than pg_current_wal_lsn() directly: that
// function raises "recovery is in progress" on a standby, where the readable
// WAL ends at the replay position instead.
const sqlWALWindow = `
SELECT (CASE
          WHEN (cur - '0/0'::pg_lsn) > $1::numeric THEN cur - $1::numeric
          ELSE '0/0'::pg_lsn
        END)::text AS start_lsn,
       cur::text AS end_lsn
FROM   (SELECT ` + walHeadLSN + ` AS cur) q
`

// walHeadLSN is the end of the WAL a pg_walinspect scan may read: the flush
// position on a primary, the replay position on a standby. pg_current_wal_lsn()
// and friends raise during recovery, and pg_walinspect itself stops at the
// replay LSN there, so every WAL-tool query that names "now" goes through this
// expression. (Maintenance's sqlMaintHeadLSN prefers the *receive* LSN because
// it measures replication lag, not what is readable.)
const walHeadLSN = `CASE WHEN pg_is_in_recovery() THEN pg_last_wal_replay_lsn() ELSE pg_current_wal_lsn() END`

// walSegmentSuffix yields the 16 hex digits of the segment file holding LSN
// %s — the logid and segment-index halves of the 24-char WAL filename, minus
// the leading timeline. It replaces pg_walfile_name(), which refuses to run
// during recovery; leaving the timeline out is what makes the value usable on
// a standby, whose timeline is only available through superuser-only
// pg_control_checkpoint(). %[1]s is the LSN column, %[2]s the segment size in
// bytes; pg_lsn subtraction is numeric, hence div/mod rather than integer
// operators, and upper() because to_hex is lowercase while segment names are
// uppercase.
const walSegmentSuffix = `upper(lpad(to_hex(div(%[1]s - '0/0'::pg_lsn, 4294967296)::bigint), 8, '0') ||
        lpad(to_hex(div(mod(%[1]s - '0/0'::pg_lsn, 4294967296), %[2]s)::bigint), 8, '0'))`

// sqlWALWindowClamped is the preferred window resolver: like sqlWALWindow but
// it floors `start` at the oldest WAL segment still present on disk rather than
// at '0/0', so the window never reaches into a segment a checkpoint already
// recycled. The real (still-readable) segments in pg_wal are those whose name
// is <= the current write segment; recycled/future ones carry higher names, so
// `min(name)` among those at or below the head's segment is the oldest
// readable segment (compared on the 16-char logid/segidx suffix, since
// pg_walfile_name is unavailable on a standby and timelines are only ever
// mismatched across a promotion). Its start
// LSN is reconstructed from the filename: name = TLI(8) || logid(8) ||
// segidx(8) hex, and a segment's byte offset is logid·2³² + segidx·seg_size,
// i.e. LSN '<logid>/<segidx·seg_size>'. GREATEST keeps the naive window when
// older segments are still present and only lifts the floor when they're gone.
// Needs pg_ls_waldir (pg_monitor / superuser); the caller falls back to
// sqlWALWindow on a privilege error.
var sqlWALWindowClamped = `
WITH cur AS (
  SELECT ` + walHeadLSN + ` AS lsn,
         pg_size_bytes(current_setting('wal_segment_size')) AS seg
),
oldest AS (
  SELECT min(name) AS nm
  FROM   pg_ls_waldir(), cur
  WHERE  name ~ '^[0-9A-F]{24}$'
    AND  substr(name, 9, 16) <= ` + fmt.Sprintf(walSegmentSuffix, "cur.lsn", "cur.seg") + `
)
SELECT GREATEST(
         CASE WHEN (cur.lsn - '0/0'::pg_lsn) > $1::numeric
              THEN cur.lsn - $1::numeric
              ELSE '0/0'::pg_lsn
         END,
         CASE WHEN oldest.nm IS NULL THEN '0/0'::pg_lsn
              ELSE (substr(oldest.nm, 9, 8) || '/' ||
                    to_hex(('x' || substr(oldest.nm, 17, 8))::bit(32)::int::bigint * cur.seg)
                   )::pg_lsn
         END
       )::text AS start_lsn,
       cur.lsn::text AS end_lsn
FROM   cur, oldest
`

// sqlWALSummary is the header block: current insert/flush position, the
// segment file the write head sits in, wal_level, the count and on-disk size
// of segment files in pg_wal, and the cluster-wide pg_stat_wal counters.
// Uses only built-in functions (no pg_walinspect) so the header renders even
// when the extension is absent — but pg_ls_waldir / pg_stat_wal still require
// a sufficiently-privileged role, so the caller treats a failure as non-fatal.
// wal_buffers_full rides on the same pg_stat_wal read (no extra privilege) — a
// persistent non-zero value means backends stalled waiting for wal_buffers.
//
// On a standby the write-position functions raise, so the "insert" slot
// carries the receive LSN and "flush" the replay LSN — the two positions that
// exist there — and the segment name is looked up in pg_ls_waldir by its
// logid/segidx suffix instead of pg_walfile_name() (empty when the directory
// is unreadable or the segment is not on disk).
var sqlWALSummary = `
WITH head AS (
  SELECT pg_is_in_recovery() AS standby,
         CASE WHEN pg_is_in_recovery() THEN COALESCE(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn())
              ELSE pg_current_wal_insert_lsn() END AS insert_lsn,
         ` + walHeadLSN + ` AS flush_lsn,
         pg_size_bytes(current_setting('wal_segment_size')) AS seg
)
SELECT head.standby,
       head.insert_lsn::text                                   AS insert_lsn,
       head.flush_lsn::text                                    AS flush_lsn,
       COALESCE((SELECT max(name) FROM pg_ls_waldir()
                 WHERE name ~ '^[0-9A-F]{24}$'
                   AND substr(name, 9, 16) = ` + fmt.Sprintf(walSegmentSuffix, "head.flush_lsn", "head.seg") + `), '')
                                                               AS current_file,
       current_setting('wal_level')                            AS wal_level,
       (SELECT count(*) FROM pg_ls_waldir())                   AS seg_files,
       (SELECT COALESCE(sum(size), 0)::bigint FROM pg_ls_waldir()) AS seg_bytes,
       w.wal_records,
       w.wal_fpi,
       w.wal_bytes::bigint                                     AS wal_bytes,
       w.wal_buffers_full
FROM   pg_stat_wal w, head
`

// sqlWALCheckpoint is the checkpoint-context block for the WAL header: how much
// WAL has accumulated since the last checkpoint's REDO point vs max_wal_size
// (the size-driven-checkpoint trigger), the wall-clock time the last checkpoint
// completed, and checkpoint_timeout in seconds (for the next-timed-checkpoint
// ETA). Mirrors sqlMaintWALInFlight. pg_control_checkpoint() typically needs
// superuser, a higher bar than the pg_monitor sources above, so the caller
// loads this separately and treats a failure as non-fatal. On a standby the
// distance is measured from the replay position (the last restartpoint's REDO
// is what pg_control_checkpoint reports there).
var sqlWALCheckpoint = `
SELECT (` + walHeadLSN + ` - redo_lsn)::bigint                                                     AS bytes_since_chkpt,
       COALESCE((SELECT setting::bigint * 1048576 FROM pg_settings WHERE name = 'max_wal_size'), 0) AS max_wal_bytes,
       checkpoint_time,
       COALESCE((SELECT setting::bigint FROM pg_settings WHERE name = 'checkpoint_timeout'), 0)     AS checkpoint_timeout_secs
FROM   pg_control_checkpoint()
`

// sqlWALCheckpointer reads the cumulative checkpoint counters (PG 15+; older
// clusters error and the caller leaves the counts at zero). A high
// requested/(timed+requested) ratio signals max_wal_size pressure.
const sqlWALCheckpointer = `SELECT num_timed, num_requested FROM pg_stat_checkpointer`

// sqlWALSettings reads the human-readable form of the WAL/checkpoint GUCs shown
// in the header. current_setting(name, true) returns NULL (→ empty string) for an
// unknown name rather than erroring, so a missing GUC just renders as "unknown".
const sqlWALSettings = `
SELECT name, COALESCE(current_setting(name, true), '')
FROM   pg_settings
WHERE  name = ANY($1)
`

// walSettingsKeys are the GUCs sqlWALSettings fetches for the header. wal_level
// already comes from sqlWALSummary, so it is not repeated here.
var walSettingsKeys = []string{"checkpoint_timeout", "checkpoint_completion_target", "wal_compression"}

// sqlWALRelStats aggregates the analysed window by relation: how much WAL each
// table/index generated (record data + full-page-image bytes), how many records
// touched it, and how many distinct pages they hit. The window is scanned once
// and grouped by (reldatabase, reltablespace, relfilenode); the relation name is
// then resolved once per relation by the same pg_filenode_relation / TOAST-owner
// / pg_database lateral as sqlWALBlocks. Ordered biggest-combined-first.
// Requires PostgreSQL 16+ (pg_get_wal_block_info). $1/$2 are start/end LSN.
const sqlWALRelStats = `
WITH agg AS (
  SELECT reldatabase,
         reltablespace,
         relfilenode,
         COALESCE(sum(block_data_length), 0)::bigint     AS data_bytes,
         COALESCE(sum(block_fpi_length),  0)::bigint     AS fpi_bytes,
         count(DISTINCT start_lsn)                        AS rec_count,
         count(DISTINCT (relforknumber, relblocknumber))  AS block_count,
         count(*) FILTER (WHERE relforknumber <> 0)       AS other_fork_count
  FROM   pg_get_wal_block_info($1::pg_lsn, $2::pg_lsn, false)
  GROUP  BY reldatabase, reltablespace, relfilenode
)
SELECT a.reldatabase,
       a.reltablespace,
       a.relfilenode,
       a.data_bytes,
       a.fpi_bytes,
       a.rec_count,
       a.block_count,
       a.other_fork_count,
       COALESCE(r.relname, ''),
       COALESCE(r.is_toast, false),
       COALESCE((SELECT datname FROM pg_database WHERE oid = a.reldatabase), '')
FROM   agg a
LEFT   JOIN LATERAL (
         SELECT
           CASE WHEN owner.oid IS NOT NULL
                THEN owner.oid::regclass::text
                ELSE f.relid::text
           END AS relname,
           owner.oid IS NOT NULL AS is_toast
         FROM   (SELECT pg_filenode_relation(a.reltablespace, a.relfilenode) AS relid) f
         LEFT   JOIN pg_class owner ON owner.reltoastrelid = f.relid::oid
         WHERE  f.relid IS NOT NULL
       ) r ON true
ORDER  BY (a.data_bytes + a.fpi_bytes) DESC
`

// sqlWALResolveFilenodes maps (reltablespace, relfilenode) pairs to relation
// names in the *connected* database — the second pass that resolves block refs
// belonging to databases other than the one the WAL was read from, run once per
// foreign database through its own pool. Same TOAST-owner hop as sqlWALBlocks.
// $1/$2 are parallel oid arrays; pairs that don't map (dropped) are omitted.
const sqlWALResolveFilenodes = `
SELECT x.relfilenode,
       CASE WHEN owner.oid IS NOT NULL
            THEN owner.oid::regclass::text
            ELSE f.relid::text
       END,
       owner.oid IS NOT NULL
FROM   unnest($1::oid[], $2::oid[]) AS x(reltablespace, relfilenode)
CROSS  JOIN LATERAL (SELECT pg_filenode_relation(x.reltablespace, x.relfilenode) AS relid) f
LEFT   JOIN pg_class owner ON owner.reltoastrelid = f.relid::oid
WHERE  f.relid IS NOT NULL
`

// sqlWALRelBlocks lists every block reference of one relation across the window,
// full-page-image-heaviest first — the drill-down behind a sqlWALRelStats row.
// It is sqlWALBlocks's body plus a relfilenode filter and a size-first order
// ($3 = relfilenode). Requires PostgreSQL 16+.
const sqlWALRelBlocks = `
SELECT start_lsn::text,
       end_lsn::text,
       block_id::int,
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
WHERE  b.relfilenode = $3::oid
ORDER  BY block_fpi_length DESC, block_id
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
SELECT start_lsn::text,
       end_lsn::text,
       block_id::int,
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

// sqlWALBlockDetail fetches one block reference *with its payload*: the
// per-block change data (block_data — for a heap INSERT that is the tuple
// itself, minus the fixed 23-byte tuple header) and the full-page image
// (block_fpi_data, already decompressed and hole-restored by pg_walinspect,
// so it is a complete 8 KiB page). show_data=true is the only reason this
// query exists separately from sqlWALBlocks: pulling every record's bytes into
// a list would be prohibitively heavy, so the payload is fetched for a single
// (record, block_id) on demand. [$1, $2) is the record's own start/end LSN,
// $3 its block_id. Also carries the record-level fields the detail header
// shows (xid, prev_lsn, lengths) so no second pg_get_wal_records_info call is
// needed. Requires PostgreSQL 16+.
const sqlWALBlockDetail = `
SELECT start_lsn::text,
       end_lsn::text,
       prev_lsn::text,
       xid::text,
       record_length,
       main_data_length,
       block_id::int,
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
       block_data,
       block_fpi_data
FROM   pg_get_wal_block_info($1::pg_lsn, $2::pg_lsn, true)
WHERE  block_id = $3::int
ORDER  BY start_lsn
LIMIT  1
`

// sqlWALRelKind resolves a (tablespace, relfilenode) pair to the relation's
// kind and access method in the *connected* database — the guard that decides
// whether a page image / block payload can be decoded as a heap page (relkind
// r/t/m/p with a heap AM) or is an index page, where heap_page_items would
// return garbage. NULL relid (dropped / other database) yields no row.
const sqlWALRelKind = `
SELECT c.oid, c.relkind::text, COALESCE(am.amname, '')
FROM   pg_class c
LEFT   JOIN pg_am am ON am.oid = c.relam
WHERE  c.oid = pg_filenode_relation($1::oid, $2::oid)
`

// sqlWALHeapAttrs lists a heap relation's physical attribute layout — every
// attnum > 0 including dropped columns (they still occupy space in the tuple,
// so the walk must step over them). Same typlen/typalign/typname/typcategory
// tuple the index-key decoder consumes, so one decoder serves both.
const sqlWALHeapAttrs = `
SELECT a.attnum::int,
       a.attname::text,
       a.attisdropped,
       a.attlen::int,
       a.attalign::text,
       COALESCE(t.typname::text, ''),
       COALESCE(t.typcategory::text, '')
FROM   pg_attribute a
LEFT   JOIN pg_type t ON t.oid = a.atttypid
WHERE  a.attrelid = $1::oid
  AND  a.attnum > 0
ORDER  BY a.attnum
`

// sqlWALPageHeader decodes a raw page image's header with pageinspect. The
// page's own LSN is what tells the reader whether the image predates or
// includes this record's change (an FPI is the page *before* the change is
// applied — the record's redo then modifies it).
const sqlWALPageHeader = `
SELECT lsn::text, checksum::int, flags::int, lower::int, upper::int, special::int,
       pagesize::int, version::int, prune_xid::text
FROM   page_header($1::bytea)
`

// sqlWALHeapPageItems is sqlHeapTuples over an in-memory page image instead of
// get_raw_page — the same column list so scanHeapTuple can scan it.
const sqlWALHeapPageItems = `
SELECT lp::int, lp_off::int, lp_flags::int, lp_len::int,
       t_xmin, t_xmax, t_field3, t_ctid::text,
       COALESCE(t_infomask2, 0)::int, COALESCE(t_infomask, 0)::int, t_hoff::int,
       t_bits, t_oid, t_data
FROM   heap_page_items($1::bytea)
ORDER  BY lp
`

// sqlWALBtreePageItems is sqlIndexTuples over an in-memory page image (the
// bytea form of bt_page_items, PostgreSQL 13+). Same column list, so the
// index-tuples scanner and key decoder apply unchanged.
const sqlWALBtreePageItems = `
SELECT itemoffset::int,
       ctid::text,
       itemlen::int,
       nulls,
       vars,
       data,
       COALESCE(dead, false),
       NULL::text AS decoded
FROM   bt_page_items($1::bytea)
ORDER  BY itemoffset
`
