package pg

// Diagnostic describes one entry in the diagnostics tool list: a name, a
// category (used as a label in the list view), a short description, the SQL
// to run, and the name of the headline column to render as a bar chart (or
// "" for all-text queries where no bar is meaningful).
type Diagnostic struct {
	Key         string // stable identifier (matches the psql-helper filename stem)
	Title       string // short display name shown in the list
	Category    string // "index" | "table" | "vacuum" | "activity" | "wal" | "server"
	Description string // one-line explanation shown as detail in the list
	SQL         string // the query to run (no parameters)
	Bar         string // headline column name rendered as a bar, or ""
	Sort        string // default sort column name (descending); "" falls back to Bar, then column 0 ascending
	PerDB       bool   // true = query reads only the connected database; the TUI prompts for which database to run against (or all)

	// Kinds overrides the name-heuristic column kind (colKindFromName) per
	// column, so a diagnostic can opt into graded rendering the suffix rules
	// can't infer — e.g. hit ratios as DiagPercentGraded (higher is better) or
	// dead-tuple % as DiagPercentBad (higher is worse). Keys are column names.
	Kinds map[string]DiagColumnKind
}

// DiagnosticByKey looks a diagnostic up in the registry by its stable key.
func DiagnosticByKey(key string) (Diagnostic, bool) {
	for _, d := range Diagnostics {
		if d.Key == key {
			return d, true
		}
	}
	return Diagnostic{}, false
}

// Diagnostics is the ordered registry of all built-in diagnostic queries.
// Queries are grouped by category and sorted alphabetically within each group;
// the TUI list renders them in this order.
var Diagnostics = []Diagnostic{
	// ── index ─────────────────────────────────────────────────────────────
	{
		Key:         "bloat_index",
		PerDB:       true,
		Title:       "Index bloat (btree)",
		Category:    "index",
		Description: "estimated bloat % and wasted MB for btree indexes (>50% bloat, >10 MB waste)",
		SQL:         sqlDiagBloatIndex,
		Bar:         "bloat_pct",
	},
	{
		Key:         "fk_missing_index",
		PerDB:       true,
		Title:       "FKs without index",
		Category:    "index",
		Description: "foreign keys whose referencing columns have no supporting index — parent deletes/updates seq-scan the child table",
		SQL:         sqlDiagFKMissingIndex,
		Bar:         "table_size_bytes",
	},
	{
		Key:         "index_brin_candidates",
		PerDB:       true,
		Title:       "BRIN candidates (btree)",
		Category:    "index",
		Description: "non-unique btree indexes on high-correlation columns (|corr| ≥ 0.7) — candidates to replace with a smaller BRIN index",
		SQL:         sqlDiagIndexBrinCandidates,
		Bar:         "correlation_pct",
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
	},
	{
		Key:         "index_redundant_prefix",
		PerDB:       true,
		Title:       "Redundant indexes (prefix)",
		Category:    "index",
		Description: "btree indexes whose key columns are a leading prefix of a wider index — usually droppable write amplification",
		SQL:         sqlDiagIndexRedundantPrefix,
		Bar:         "redundant_size_bytes",
	},
	{
		Key:         "index_show_definitions",
		PerDB:       true,
		Title:       "Index definitions",
		Category:    "index",
		Description: "CREATE INDEX statement for every index in every user schema",
		SQL:         sqlDiagIndexShowDefinitions,
		Bar:         "",
	},
	{
		Key:         "index_show_duplicate",
		PerDB:       true,
		Title:       "Duplicate indexes",
		Category:    "index",
		Description: "indexes with identical column sets (candidates for removal)",
		SQL:         sqlDiagIndexShowDuplicate,
		Bar:         "",
	},
	{
		Key:         "index_show_size",
		PerDB:       true,
		Title:       "Indexes",
		Category:    "index",
		Description: "every index sorted by size, with scan count, unique flag and column list",
		SQL:         sqlDiagIndexShowSize,
		Bar:         "index_size_bytes",
	},
	{
		Key:         "index_show_unused",
		PerDB:       true,
		Title:       "Unused indexes",
		Category:    "index",
		Description: "indexes ranked by disk footprint per scan — big indexes that are never or rarely used (candidates for removal)",
		SQL:         sqlDiagIndexShowUnused,
		Bar:         "index_size_bytes",
		Sort:        "size_per_scan_bytes",
	},
	// ── table ─────────────────────────────────────────────────────────────
	// "Table + index bloat (approx)" overlapped the more precise bloat_table and
	// bloat_index entries (and the main Disk tool's per-part bloat), so it is no
	// longer registered. Kept commented for reference rather than deleted.
	// {
	// 	Key:         "bloat_all",
	// 	Title:       "Table + index bloat (approx)",
	// 	Category:    "table",
	// 	Description: "estimated table and index bloat using pg_stats (no extensions required)",
	// 	SQL:         sqlDiagBloatAll,
	// 	Bar:         "wastedbytes",
	// },
	{
		Key:         "bloat_table",
		PerDB:       true,
		Title:       "Table bloat (detailed)",
		Category:    "table",
		Description: "detailed table bloat estimate (>50% and >10 MB, or >25% and >1 GB)",
		SQL:         sqlDiagBloatTable,
		Bar:         "pct_bloat",
	},
	{
		Key:         "stale_statistics",
		PerDB:       true,
		Title:       "Stale planner statistics",
		Category:    "table",
		Description: "tables whose row modifications since the last ANALYZE outgrow their live rows — bad-plan risk",
		SQL:         sqlDiagStaleStatistics,
		Bar:         "stale_pct",
		Kinds:       map[string]DiagColumnKind{"stale_pct": DiagPercentBad},
	},
	{
		Key:         "table_scan_types",
		PerDB:       true,
		Title:       "Sequential scan candidates",
		Category:    "table",
		Description: "tables with >20% sequential reads and >800 kB — potential missing-index candidates",
		SQL:         sqlDiagTableScanTypes,
		Bar:         "index_read_pct",
		Kinds:       map[string]DiagColumnKind{"index_read_pct": DiagPercentGraded},
	},
	{
		Key:         "table_show_hitratio",
		PerDB:       true,
		Title:       "Table cache hit ratio",
		Category:    "table",
		Description: "tables with heap cache hit ratio below 80%, ordered by blocks read from disk",
		SQL:         sqlDiagTableShowHitratio,
		Bar:         "hit_pct",
		Kinds:       map[string]DiagColumnKind{"hit_pct": DiagPercentGraded},
	},
	{
		Key:         "table_show_hot_ratio",
		PerDB:       true,
		Title:       "Table HOT update ratio",
		Category:    "table",
		Description: "HOT vs non-HOT update split per table; sorted by absolute non-HOT updates (index-churn offenders first)",
		SQL:         sqlDiagTableShowHotRatio,
		Bar:         "hot_pct",
		Sort:        "non_hot_updates",
		Kinds:       map[string]DiagColumnKind{"hot_pct": DiagPercentGraded},
	},
	{
		Key:         "table_show_modify_ratio",
		PerDB:       true,
		Title:       "Table modification ratio",
		Category:    "table",
		Description: "insert / update / delete split per table (since last stats reset)",
		SQL:         sqlDiagTableShowModifyRatio,
		Bar:         "upd_pct",
	},
	{
		Key:         "table_show_size",
		PerDB:       true,
		Title:       "Table sizes (with partitions)",
		Category:    "table",
		Description: "total, index, toast and heap sizes rolled up across partition trees",
		SQL:         sqlDiagTableShowSize,
		Bar:         "total_bytes",
	},
	{
		Key:         "toast_show_size",
		PerDB:       true,
		Title:       "TOAST table sizes",
		Category:    "table",
		Description: "TOAST tables with their owning table, toastable columns, and live/dead tuple counts",
		SQL:         sqlDiagToastShowSize,
		Bar:         "size_bytes",
	},
	// ── vacuum ────────────────────────────────────────────────────────────
	{
		Key:         "autovacuum_progress",
		Title:       "Autovacuum progress",
		Category:    "vacuum",
		Description: "currently running autovacuum workers with scan and vacuum progress",
		SQL:         sqlDiagAutovacuumProgress,
		Bar:         "scanned_pct",
		Kinds:       map[string]DiagColumnKind{"dead_pct": DiagPercentBad},
	},
	{
		Key:         "progress_all",
		Title:       "Running operations (progress)",
		Category:    "vacuum",
		Description: "everything with a pg_stat_progress_* view — VACUUM, CREATE INDEX, ANALYZE, CLUSTER, COPY, base backups — with % done",
		SQL:         sqlDiagProgressAll,
		Bar:         "done_pct",
	},
	{
		Key:         "vacuum_running",
		Title:       "Running vacuums",
		Category:    "vacuum",
		Description: "active VACUUM commands with phase and percent complete",
		SQL:         sqlDiagVacuumRunning,
		Bar:         "percent_complete",
	},
	{
		Key:         "vacuum_stats",
		PerDB:       true,
		Title:       "Vacuum stats",
		Category:    "vacuum",
		Description: "last vacuum/analyze timestamps, dead tuple counts and autovacuum threshold per table",
		SQL:         sqlDiagVacuumStats,
		Bar:         "dead_tuples",
	},
	// ── activity ──────────────────────────────────────────────────────────
	// Note: the "running queries" view is now the dedicated Activity tool
	// (toolActivity), which auto-refreshes and has configurable columns.
	{
		Key:         "connections",
		Title:       "Connections",
		Category:    "activity",
		Description: "connection count per database and state (active, idle, idle in transaction, …)",
		SQL:         sqlDiagConnections,
		Bar:         "connections",
	},
	{
		Key:         "idle_in_xact_holders",
		Title:       "Idle-in-transaction lock holders",
		Category:    "activity",
		Description: "open transactions sitting idle, with the locks they still hold — the usual 'why is this stuck / why is bloat growing' answer",
		SQL:         sqlDiagIdleInXactHolders,
		Bar:         "xact_age_secs",
		Kinds: map[string]DiagColumnKind{
			"xact_age_secs": DiagCostGraded,
			"state":         DiagBackendState,
		},
	},
	{
		Key:         "lock_summary",
		Title:       "Lock summary",
		Category:    "activity",
		Description: "pg_locks grouped by lock type and mode with waiter counts — the one-glance contention read",
		SQL:         sqlDiagLockSummary,
		Bar:         "locks",
		Kinds:       map[string]DiagColumnKind{"waiting": DiagCostGraded},
	},
	// ── wal ───────────────────────────────────────────────────────────────
	{
		Key:         "wal_files",
		Title:       "WAL files",
		Category:    "wal",
		Description: "WAL segment files on disk by modification time (needs superuser or pg_monitor)",
		SQL:         sqlDiagWalFiles,
		Bar:         "size_bytes",
	},
	{
		Key:         "wal_activity",
		Title:       "WAL activity",
		Category:    "wal",
		Description: "cluster-wide WAL generation counters from pg_stat_wal (PostgreSQL 14+)",
		SQL:         sqlDiagWalActivity,
		Bar:         "wal_bytes",
	},
	// ── server ────────────────────────────────────────────────────────────
	{
		Key:         "database_show_size",
		Title:       "Database sizes",
		Category:    "server",
		Description: "size of every database the current user can connect to",
		SQL:         sqlDiagDatabaseShowSize,
		Bar:         "size_bytes",
	},
	{
		Key:         "database_stats",
		Title:       "Database stats",
		Category:    "server",
		Description: "per-database commits, rollbacks, cache hit ratio, deadlocks and temp-file usage",
		SQL:         sqlDiagDatabaseStats,
		Bar:         "hit_pct",
		Kinds:       map[string]DiagColumnKind{"hit_pct": DiagPercentGraded},
	},
	{
		Key:         "foreignkeys_show_all",
		PerDB:       true,
		Title:       "Foreign keys",
		Category:    "server",
		Description: "all foreign-key constraints in every schema",
		SQL:         sqlDiagForeignkeysShowAll,
		Bar:         "",
	},
	{
		Key:         "grants_show_all",
		PerDB:       true,
		Title:       "Grants",
		Category:    "server",
		Description: "all explicit grants on schemas, tables, views, sequences, and functions",
		SQL:         sqlDiagGrantsShowAll,
		Bar:         "",
	},
	{
		Key:         "replication_slots",
		Title:       "Replication slots",
		Category:    "server",
		Description: "all replication slots with WAL retention, status and activity",
		SQL:         sqlDiagReplicationSlots,
		Bar:         "retained_wal_bytes",
	},
	{
		Key:         "sequences",
		PerDB:       true,
		Title:       "Sequence usage",
		Category:    "server",
		Description: "how much of each sequence's range is consumed (last_value needs SELECT/USAGE)",
		SQL:         sqlDiagSequences,
		Bar:         "consumed_pct",
		Kinds:       map[string]DiagColumnKind{"consumed_pct": DiagPercentBad},
	},
	{
		Key:         "settings_show_pending",
		Title:       "Pending settings",
		Category:    "server",
		Description: "settings that differ from the configured value and need reload or restart",
		SQL:         sqlDiagSettingsShowPending,
		Bar:         "",
	},
	{
		Key:         "slru_stats",
		Title:       "SLRU caches",
		Category:    "server",
		Description: "transaction-status / multixact / subtransaction cache traffic — invisible pressure from long transactions and savepoints",
		SQL:         sqlDiagSLRU,
		Bar:         "blks_read",
		Kinds:       map[string]DiagColumnKind{"hit_pct": DiagPercentGraded},
	},
	{
		Key:         "subscription_stats",
		PerDB:       true,
		Title:       "Logical subscriptions",
		Category:    "server",
		Description: "logical-replication subscriptions with worker state, message staleness and apply/sync error counts",
		SQL:         sqlDiagSubscriptionStats,
		Bar:         "",
		Kinds: map[string]DiagColumnKind{
			"apply_errors": DiagCostGraded,
			"sync_errors":  DiagCostGraded,
		},
	},
}
