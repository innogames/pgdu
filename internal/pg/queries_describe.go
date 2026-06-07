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
       idx.indisunique
FROM   pg_index idx
JOIN   pg_class i ON i.oid = idx.indexrelid
WHERE  idx.indrelid = $1
ORDER  BY idx.indisprimary DESC, i.relname
`

// sqlDescribeConstraints lists a table's constraints (PK, FK, unique, check)
// rendered by pg_get_constraintdef. $1 = table oid.
const sqlDescribeConstraints = `
SELECT conname,
       pg_get_constraintdef(oid, true) AS def
FROM   pg_constraint
WHERE  conrelid = $1
ORDER  BY contype, conname
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
