package pg

// diagIndex holds the 'index' category of the Diagnostics registry;
// SQL lives in queries_diag_index.go.
var diagIndex = []Diagnostic{
	{
		Key:         "bloat_index",
		PerDB:       true,
		Title:       "Index bloat (btree)",
		Category:    "index",
		Description: "estimated bloat % and wasted bytes for btree indexes (>50% bloat, >10 MB waste)",
		SQL:         sqlDiagBloatIndex,
		Bar:         "bloat_pct",
		Fix:         fixReindex("schema_name", "index_name"),
		Help: `Statistical estimate of dead space inside btree indexes, derived from
			pg_stats column widths (an estimate, not an exact measurement); only
			indexes over 50% bloat wasting more than 10 MB are listed. bloat_bytes is
			what a rebuild would reclaim; index_scans tells whether the index is
			used at all — an unused bloated index is better dropped than rebuilt
			(see Unused indexes). Fix with REINDEX INDEX CONCURRENTLY; the same
			index re-bloating points at autovacuum lagging behind churn-heavy
			updates or deletes.`,
	},
	{
		Key:         "fk_missing_index",
		PerDB:       true,
		Title:       "FKs without index",
		Category:    "index",
		Description: "foreign keys on tables >10k rows whose referencing columns have no supporting index — parent deletes/updates seq-scan the child table",
		SQL:         sqlDiagFKMissingIndex,
		Bar:         "table_size_bytes",
		Help: `Without an index on the referencing columns, every DELETE or key
			UPDATE on the referenced (parent) table sequentially scans the child
			table once per affected row — the classic source of mysteriously slow
			deletes and lock pile-ups. Only child tables over 10k rows are listed:
			table_size_bytes is what each check scans, referenced_writes how often
			the parent side is written (how often it hurts). Fix with CREATE INDEX
			CONCURRENTLY on the fk_columns (any column order works for the lookup).`,
	},
	{
		Key:         "index_brin_candidates",
		PerDB:       true,
		Title:       "BRIN candidates (btree)",
		Category:    "index",
		Description: "non-unique btree indexes on high-correlation columns (|corr| ≥ 0.7) — candidates to replace with a smaller BRIN index",
		SQL:         sqlDiagIndexBrinCandidates,
		Bar:         "correlation_pct",
		Help: `Non-unique btree indexes whose column closely follows the table's
			physical row order (correlation_pct ≥ 70 to appear) on tables over
			100k rows — the pattern where a BRIN index prunes almost as well at a
			tiny fraction of index_size. Typical for append-only timestamps and
			serial keys; ≥ 90 is flagged a STRONG candidate. BRIN pays off for
			range scans, not single-row lookups — check the workload first, create
			the BRIN, verify the plans still prune, then drop the btree.`,
	},
	{
		Key:         "index_cluster_candidates",
		PerDB:       true,
		Title:       "CLUSTER candidates (fragmented)",
		Category:    "index",
		Description: "hot btree indexes on low-correlation columns (|corr| ≤ 0.5) whose scans return many scattered rows — candidates for CLUSTER / pg_repack",
		SQL:         sqlDiagIndexClusterCandidates,
		Bar:         "scatter_pct",
		Sort:        "disk_pain",
		Fix:         fixClusterOn,
		Kinds: map[string]DiagColumnKind{
			"scatter_pct":   DiagPercentBad,
			"heap_miss_pct": DiagPercentBad,
			"disk_pain":     DiagCount,
			"scans":         DiagCount,
			"tuples_read":   DiagCount,
		},
		Help: `Btree indexes whose scans return several rows each (tup_per_scan ≥ 5)
			while the heap is ordered unlike the index (scatter_pct = (1−|corr|)×100;
			100 means heap and index order are unrelated), so every scan fetches
			rows scattered across many heap pages. The classic shape is a per-key
			detail table — a player inventory, per-user events — always queried by
			that key; rows_per_key confirms it. disk_pain, the default order, is
			tuples_read × scatter × heap-miss ratio — an estimate of fetches that
			were both scattered and served from disk; a fragmented table the
			buffer cache fully absorbs (heap_miss_pct ≈ 0) scores near zero no
			matter how hot its counters are. Ignore rows
			with rows_per_key ≈ 1 unless the scans are range scans (cleanup by
			timestamp, id ranges) — clustering only helps when scanned rows share
			pages. Fix by rewriting the heap in index order: CLUSTER <table>
			USING <index> (takes an ACCESS EXCLUSIVE lock) or pg_repack for an
			online rewrite. Churny tables re-fragment over time, so re-run
			periodically; clustered = t means the table was already CLUSTERed on
			that index and has degraded since. scans/tuples_read/heap_miss_pct are
			cumulative since the last stats reset, so freshly reset stats
			understate hotness.`,
	},
	// "All indexes" was merged into "Index sizes" below (which now also carries
	// scan counts and the unique flag, across all schemas), so it is no longer a
	// separate entry. Kept commented for reference rather than deleted.
	// {
	// 	Key:         "index_show_all",
	// 	Title:       "All indexes",
	// 	Category:    "index",
	// 	Description: "every index in the public schema with scan counts and size",
	// 	SQL:         sqlDiagIndexShowAll,
	// 	Bar:         "number_of_scans",
	// },
	{
		Key:         "index_invalid",
		PerDB:       true,
		Title:       "Invalid indexes",
		Category:    "index",
		Description: "indexes left INVALID by a failed CREATE/REINDEX CONCURRENTLY — unusable by plans but still maintained on writes",
		SQL:         sqlDiagIndexInvalid,
		Bar:         "index_size_bytes",
		Fix: fixReindex("schema", "index_name",
			"-- or, for a _ccnew/_ccold leftover of a failed CONCURRENTLY build: DROP INDEX CONCURRENTLY"),
		Help: `Indexes marked INVALID — the residue of a failed or cancelled CREATE
			INDEX CONCURRENTLY / REINDEX CONCURRENTLY. The planner never uses
			them, but every write still maintains them, so they are pure write
			amplification and wasted disk until dealt with. Rebuild with REINDEX
			INDEX CONCURRENTLY, or drop them (leftovers with a _ccnew/_ccold
			suffix are safe to drop); definition shows what a rebuild recreates.
			An empty result is the healthy state.`,
	},
	{
		Key:         "index_io",
		PerDB:       true,
		Title:       "Index I/O",
		Category:    "index",
		Description: "per-index buffer cache hits vs disk reads — hot indexes with poor hit ratios are shared_buffers pressure",
		SQL:         sqlDiagIndexIO,
		Bar:         "blks_read",
		Kinds:       map[string]DiagColumnKind{"hit_pct": DiagPercentGraded},
		Help: `Cumulative buffer-cache hits vs disk reads per index, worst disk
			readers first. A frequently-scanned index with a low hit_pct is
			repeatedly read from disk: it doesn't fit in shared_buffers, or other
			traffic keeps evicting it. Note that a "read" here may still be served
			by the OS page cache — this measures PostgreSQL's own cache only.
			Levers: more shared_buffers, a smaller index (partial, or fewer
			columns), or simply accepting it for rarely-used indexes.`,
	},
	{
		Key:         "index_redundant_prefix",
		PerDB:       true,
		Title:       "Redundant indexes (prefix)",
		Category:    "index",
		Description: "btree indexes whose key columns are a leading prefix of a wider index — usually droppable write amplification",
		SQL:         sqlDiagIndexRedundantPrefix,
		Bar:         "redundant_size_bytes",
		Fix:         fixDropRedundantIndex,
		Help: `Btree indexes whose key columns are a strict leading prefix of a
			wider index on the same table (matching column order, opclasses, sort
			options and partial predicate) — covered_by can serve every query the
			redundant index can. redundant_scans > 0 only means the planner picks
			the smaller index while it exists; that traffic moves to the wider one
			after the drop. Constraint-backed indexes are excluded, and exact
			duplicates have their own diagnostic. Drop with DROP INDEX
			CONCURRENTLY to reclaim the size and its per-write maintenance.`,
	},
	{
		Key:         "index_show_definitions",
		PerDB:       true,
		Title:       "Index definitions",
		Category:    "index",
		Description: "CREATE INDEX statement for every index in every user schema",
		SQL:         sqlDiagIndexShowDefinitions,
		Bar:         "",
		Help: `The CREATE INDEX statement for every index in every user schema — a
			reference listing for auditing and copying DDL, not a problem
			detector. Use the filter (/) to find indexes by table, column or
			expression.`,
	},
	{
		Key:         "index_show_duplicate",
		PerDB:       true,
		Title:       "Duplicate indexes",
		Category:    "index",
		Description: "indexes with identical column sets (candidates for removal)",
		SQL:         sqlDiagIndexShowDuplicate,
		Bar:         "",
		Sort:        "size",
		Fix:         fixDropDuplicateIndex,
		Help: `Indexes on the same table with identical key columns, operator
			classes, expressions and predicate — fully interchangeable, so one of
			each pair is pure write amplification and cache waste. idx1/idx2 are
			the pair (index_size is the size of one; the size column sums the
			pair). Keep the one backing a constraint — a primary key or UNIQUE
			constraint index can't be dropped directly — and drop the other with
			DROP INDEX CONCURRENTLY. If both back constraints, drop the redundant
			constraint instead.`,
	},
	{
		Key:         "index_show_size",
		PerDB:       true,
		Title:       "Indexes",
		Category:    "index",
		Description: "every index sorted by size, with scan count, unique flag and column list",
		SQL:         sqlDiagIndexShowSize,
		Bar:         "index_size_bytes",
		Help: `The inventory of every index in every user schema: size, cumulative
			scan count, unique flag and key columns. Nothing here is a problem by
			itself — it's the map for the pointed index diagnostics: 0 scans on a
			big non-unique index → Unused indexes; several entries with the same
			columns → Duplicate/Redundant indexes. scans counts planner use since
			the last stats reset, so mind how much workload that window covers.`,
	},
	{
		Key:         "index_show_unused",
		PerDB:       true,
		Title:       "Unused indexes",
		Category:    "index",
		Description: "non-constraint indexes ranked by disk footprint per scan — big indexes that are never or rarely used (candidates for removal; PK/unique indexes are excluded as they back a constraint)",
		SQL:         sqlDiagIndexShowUnused,
		Bar:         "index_size_bytes",
		Sort:        "size_per_scan_bytes",
		Fix: fixDropIndex("schema", "index_name",
			"-- verify the stats window covers periodic workloads and replicas don't rely on it"),
		Help: `Ranks indexes by amortised cost: size_per_scan = index size ÷
			(scans + 1), so large never- or rarely-used indexes float to the top.
			idx_scan counts planner lookups only. PK/unique indexes are excluded:
			they can show 0 scans yet still enforce their constraint on every
			write, so they can't simply be dropped and aren't "unused" here. What
			remains are non-constraint indexes; those with 0 scans and real size
			are drop candidates (DROP INDEX CONCURRENTLY) — but the counters run
			since the last stats reset, so make sure the window covers periodic
			workloads (nightly imports, monthly reports) before dropping anything.`,
	},
}
