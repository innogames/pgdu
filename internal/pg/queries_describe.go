package pg

// --- describe queries (psql \d-style) ---

// sqlResolveTable resolves a (optionally schema-qualified) relation name to the
// catalog metadata DescribeTable needs. to_regclass honours search_path for an
// unqualified name and returns NULL — rather than erroring — when the name
// doesn't resolve, so a stray label can't blow up the describe path. $1 = name.
const sqlResolveTable = `
SELECT c.oid,
       n.nspname,
       c.relname,
       pg_total_relation_size(c.oid),
       c.reltuples::bigint
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  c.oid = to_regclass($1)
  AND  c.relkind IN ('r', 'p', 'm', 'f')
`

// sqlResolveIndex resolves an (optionally schema-qualified) index name to its
// OID and qualified name — sqlResolveTable's sibling for relkind 'i'/'I'.
// $1 = name.
const sqlResolveIndex = `
SELECT c.oid,
       n.nspname,
       c.relname
FROM   pg_class c
JOIN   pg_namespace n ON n.oid = c.relnamespace
WHERE  c.oid = to_regclass($1)
  AND  c.relkind IN ('i', 'I')
`

// sqlDescribeColumns lists a table's live columns in declaration order with
// NOT NULL, the column default expression, and whether any index covers the
// column. "Covers" matches what disqualifies an UPDATE from being HOT: key
// columns (indkey), plus columns referenced by index expressions or partial-
// index predicates — those aren't in indkey, but every index records a
// pg_depend entry per column it references (constraint-backed indexes don't,
// but they can't have expressions/predicates, so indkey covers them).
// $1 = table oid. PG 12+.
const sqlDescribeColumns = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod)               AS type_name,
       a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '')       AS default_expr,
       (EXISTS (SELECT 1 FROM pg_index i
                WHERE  i.indrelid = a.attrelid
                  AND  a.attnum = ANY (i.indkey))
        OR EXISTS (SELECT 1
                   FROM   pg_depend dep
                   JOIN   pg_index i ON i.indexrelid = dep.objid
                   WHERE  dep.classid    = 'pg_class'::regclass
                     AND  dep.refclassid = 'pg_class'::regclass
                     AND  dep.refobjid   = a.attrelid
                     AND  dep.refobjsubid = a.attnum
                     AND  i.indrelid     = a.attrelid)) AS indexed
FROM   pg_attribute a
LEFT   JOIN pg_attrdef d
       ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE  a.attrelid = $1
  AND  a.attnum   > 0
  AND  NOT a.attisdropped
ORDER  BY a.attnum
`

// sqlDescribeIndexes lists a table's indexes with their full CREATE INDEX
// definitions, size, and usage counters for the detail mode. The stat joins are
// LEFT JOINs: pg_stat_all_indexes has no rows for indexes on partitioned
// parents, and statio rows can lag a fresh index. last_idx_scan needs PG 16+
// (we support 17+).
//
// est_entries is the index's own pg_class.reltuples — for a partial index the
// estimated number of rows matching its predicate, which both VACUUM and
// ANALYZE maintain (ANALYZE derives it from the sampled fraction passing
// indpred). Paired with the table's reltuples it gives the covered share
// without scanning anything; -1 means "never vacuumed or analyzed".
// $1 = table oid.
const sqlDescribeIndexes = `
SELECT i.relname,
       pg_get_indexdef(idx.indexrelid) AS def,
       idx.indisprimary,
       idx.indisunique,
       idx.indisclustered,
       pg_relation_size(idx.indexrelid)  AS size_bytes,
       COALESCE(pg_get_expr(idx.indpred, idx.indrelid), '') AS predicate,
       i.reltuples::bigint               AS est_entries,
       COALESCE(st.idx_scan, 0),
       st.last_idx_scan,
       COALESCE(st.idx_tup_read, 0),
       COALESCE(st.idx_tup_fetch, 0),
       COALESCE(io.idx_blks_hit, 0),
       COALESCE(io.idx_blks_read, 0)
FROM   pg_index idx
JOIN   pg_class i ON i.oid = idx.indexrelid
LEFT   JOIN pg_stat_all_indexes   st ON st.indexrelid = idx.indexrelid
LEFT   JOIN pg_statio_all_indexes io ON io.indexrelid = idx.indexrelid
WHERE  idx.indrelid = $1
ORDER  BY idx.indisprimary DESC, i.relname
`

// sqlDescribeIndex returns the definition, metadata, size and usage counters
// for a single index. indpred is COALESCE'd to ” so it's never NULL; the stat
// joins are LEFT JOINs for the same reasons as sqlDescribeIndexes. The two
// reltuples estimates are the pair behind a partial index's covered share (see
// sqlDescribeIndexes); the parent's comes from pg_class, not the caller, so the
// ratio holds however the panel was opened. $1 = index oid. PG 16+
// (last_idx_scan).
const sqlDescribeIndex = `
SELECT pg_get_indexdef(c.oid)                                AS def,
       am.amname                                             AS access_method,
       idx.indisunique,
       idx.indisprimary,
       COALESCE(pg_get_expr(idx.indpred, idx.indrelid), '')  AS predicate,
       idx.indrelid::regclass::text                          AS parent_table,
       pg_relation_size(c.oid)                               AS size_bytes,
       c.reltuples::bigint                                   AS est_entries,
       t.reltuples::bigint                                   AS parent_est_rows,
       COALESCE(st.idx_scan, 0),
       st.last_idx_scan,
       COALESCE(st.idx_tup_read, 0),
       COALESCE(st.idx_tup_fetch, 0),
       COALESCE(io.idx_blks_hit, 0),
       COALESCE(io.idx_blks_read, 0)
FROM   pg_index idx
JOIN   pg_class c  ON c.oid = idx.indexrelid
JOIN   pg_class t  ON t.oid = idx.indrelid
JOIN   pg_am am    ON am.oid = c.relam
LEFT   JOIN pg_stat_all_indexes   st ON st.indexrelid = idx.indexrelid
LEFT   JOIN pg_statio_all_indexes io ON io.indexrelid = idx.indexrelid
WHERE  idx.indexrelid = $1
`

// sqlDescribeStats gathers the per-table counters the describe detail mode
// shows: size split (heap/indexes/toast), tuple churn incl. HOT updates, scan
// mix with last-scan times, vacuum/analyze recency and xid age. GREATEST folds
// manual and auto timestamps into one "most recent" each (NULL only when both
// are NULL). LEFT JOIN because foreign tables and partitioned parents may lack
// a pg_stat row. $1 = table oid. PG 16+ (last_seq_scan/last_idx_scan).
const sqlDescribeStats = `
SELECT pg_relation_size(c.oid),
       pg_indexes_size(c.oid),
       COALESCE(pg_total_relation_size(NULLIF(c.reltoastrelid, 0)), 0),
       COALESCE(s.n_live_tup, 0),
       COALESCE(s.n_dead_tup, 0),
       COALESCE(s.n_tup_ins, 0),
       COALESCE(s.n_tup_upd, 0),
       COALESCE(s.n_tup_del, 0),
       COALESCE(s.n_tup_hot_upd, 0),
       COALESCE(s.n_mod_since_analyze, 0),
       COALESCE(s.n_ins_since_vacuum, 0),
       COALESCE(s.seq_scan, 0),
       COALESCE(s.idx_scan, 0),
       s.last_seq_scan,
       s.last_idx_scan,
       GREATEST(s.last_vacuum,  s.last_autovacuum),
       GREATEST(s.last_analyze, s.last_autoanalyze),
       COALESCE(s.vacuum_count, 0)  + COALESCE(s.autovacuum_count, 0),
       COALESCE(s.analyze_count, 0) + COALESCE(s.autoanalyze_count, 0),
       CASE WHEN c.relkind = 'p' THEN 0 ELSE age(c.relfrozenxid)::bigint END
FROM   pg_class c
LEFT   JOIN pg_stat_all_tables s ON s.relid = c.oid
WHERE  c.oid = $1
`

// sqlDescribeOptions returns a table's storage options (pg_class.reloptions)
// plus its TOAST table's options prefixed with "toast.", matching how psql's
// \d+ folds both into one "Options:" line. Empty array when neither has any.
// $1 = table oid.
const sqlDescribeOptions = `
SELECT COALESCE(c.reloptions, '{}') ||
       COALESCE((SELECT array_agg('toast.' || opt)
                 FROM   unnest(tc.reloptions) AS opt), '{}')
FROM   pg_class c
LEFT   JOIN pg_class tc ON tc.oid = c.reltoastrelid
WHERE  c.oid = $1
`

// sqlDescribeFKOutgoing lists foreign keys this table declares (it is the
// referencing side, conrelid = $1). Column lists are rebuilt from conkey/confkey
// via unnest WITH ORDINALITY so multi-column keys keep their declared order.
// Action codes (confdeltype/confupdtype) are cast to text and mapped to labels
// in Go. $1 = table oid. PG 12+.
const sqlDescribeFKOutgoing = `
SELECT c.conname,
       (SELECT string_agg(a.attname, ', ' ORDER BY k.ord)
          FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) AS local_cols,
       c.confrelid::regclass::text AS other_table,
       (SELECT string_agg(a.attname, ', ' ORDER BY k.ord)
          FROM unnest(c.confkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.attnum) AS other_cols,
       c.confdeltype::text,
       c.confupdtype::text
FROM   pg_constraint c
WHERE  c.conrelid = $1 AND c.contype = 'f'
ORDER  BY c.conname
`

// sqlDescribeFKIncoming lists foreign keys other tables declare against this one
// (it is the referenced side, confrelid = $1). Roles are swapped relative to the
// outgoing query: LocalCols come from confkey on this table, OtherTable/OtherCols
// from the referencing child (conrelid/conkey). $1 = table oid. PG 12+.
const sqlDescribeFKIncoming = `
SELECT c.conname,
       (SELECT string_agg(a.attname, ', ' ORDER BY k.ord)
          FROM unnest(c.confkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.attnum) AS local_cols,
       c.conrelid::regclass::text AS other_table,
       (SELECT string_agg(a.attname, ', ' ORDER BY k.ord)
          FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord)
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum) AS other_cols,
       c.confdeltype::text,
       c.confupdtype::text
FROM   pg_constraint c
WHERE  c.confrelid = $1 AND c.contype = 'f'
ORDER  BY c.conrelid::regclass::text, c.conname
`
