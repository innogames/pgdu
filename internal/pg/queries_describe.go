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
// NOT NULL and the column default expression. $1 = table oid. PG 12+.
const sqlDescribeColumns = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod)               AS type_name,
       a.attnotnull,
       COALESCE(pg_get_expr(d.adbin, d.adrelid), '')       AS default_expr
FROM   pg_attribute a
LEFT   JOIN pg_attrdef d
       ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE  a.attrelid = $1
  AND  a.attnum   > 0
  AND  NOT a.attisdropped
ORDER  BY a.attnum
`

// sqlDescribeIndexes lists a table's indexes with their full CREATE INDEX
// definitions. Ordered primary-first then alphabetically. $1 = table oid.
const sqlDescribeIndexes = `
SELECT i.relname,
       pg_get_indexdef(idx.indexrelid) AS def,
       idx.indisprimary,
       idx.indisunique,
       idx.indisclustered
FROM   pg_index idx
JOIN   pg_class i ON i.oid = idx.indexrelid
WHERE  idx.indrelid = $1
ORDER  BY idx.indisprimary DESC, i.relname
`

// sqlDescribeIndex returns the definition and metadata for a single index.
// indpred is COALESCE'd to ” so it's never NULL. $1 = index oid. PG 12+.
const sqlDescribeIndex = `
SELECT pg_get_indexdef(c.oid)                                AS def,
       am.amname                                             AS access_method,
       idx.indisunique,
       idx.indisprimary,
       COALESCE(pg_get_expr(idx.indpred, idx.indrelid), '')  AS predicate,
       idx.indrelid::regclass::text                          AS parent_table
FROM   pg_index idx
JOIN   pg_class c  ON c.oid = idx.indexrelid
JOIN   pg_am am    ON am.oid = c.relam
WHERE  idx.indexrelid = $1
`
