package pg

// sqlTableStats gathers every per-table metric the Table overview tool shows in
// a single pass over one schema, joining the catalog (sizes, storage options,
// freeze age) with the cumulative pg_stat_all_tables / pg_statio_all_tables
// counters. The (auto)vacuum/(auto)analyze ages are computed server-side as
// milliseconds (NULL when never run) so the client needs no clock of its own.
// relfrozenxid age is forced to 0 for partitioned parents (relkind 'p'), whose
// relfrozenxid is 0 and would otherwise report a meaningless huge age. toast
// size uses pg_total_relation_size on the TOAST relation (main fork + its index
// + FSM/VM), matching sqlTables.
const sqlTableStats = `
SELECT
    c.oid,
    c.relname,
    c.relkind::text,
    COALESCE(c.reloptions, '{}'),
    c.reltuples::bigint,
    COALESCE(c.reltoastrelid, 0)::oid,
    COALESCE((SELECT 'pg_toast.' || tc.relname
              FROM pg_class tc WHERE tc.oid = c.reltoastrelid), ''),
    pg_relation_size(c.oid),
    pg_indexes_size(c.oid),
    COALESCE(pg_total_relation_size(c.reltoastrelid), 0),
    pg_total_relation_size(c.oid)                                            AS total_bytes,
    CASE WHEN c.relkind = 'p' THEN 0 ELSE age(c.relfrozenxid)::bigint END,
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
    COALESCE(s.seq_tup_read, 0),
    COALESCE(s.idx_tup_fetch, 0),
    COALESCE(s.vacuum_count, 0),
    COALESCE(s.autovacuum_count, 0),
    COALESCE(s.analyze_count, 0),
    COALESCE(s.autoanalyze_count, 0),
    (EXTRACT(EPOCH FROM now() - GREATEST(s.last_vacuum,  s.last_autovacuum))  * 1000)::float8,
    (EXTRACT(EPOCH FROM now() - GREATEST(s.last_analyze, s.last_autoanalyze)) * 1000)::float8,
    COALESCE(io.heap_blks_read, 0),
    COALESCE(io.heap_blks_hit,  0),
    COALESCE(io.idx_blks_read,  0),
    COALESCE(io.idx_blks_hit,   0)
FROM   pg_class c
JOIN   pg_namespace n            ON n.oid   = c.relnamespace
LEFT   JOIN pg_stat_all_tables   s  ON s.relid  = c.oid
LEFT   JOIN pg_statio_all_tables io ON io.relid = c.oid
WHERE  n.nspname = $1
  AND  c.relkind IN ('r','m','p')
ORDER  BY total_bytes DESC
`
