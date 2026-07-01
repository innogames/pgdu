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
		Key:         "table_scan_types",
		PerDB:       true,
		Title:       "Sequential scan candidates",
		Category:    "table",
		Description: "tables with >20% sequential reads and >800 kB — potential missing-index candidates",
		SQL:         sqlDiagTableScanTypes,
		Bar:         "index_read_pct",
	},
	{
		Key:         "table_show_hitratio",
		PerDB:       true,
		Title:       "Table cache hit ratio",
		Category:    "table",
		Description: "tables with heap cache hit ratio below 80%, ordered by blocks read from disk",
		SQL:         sqlDiagTableShowHitratio,
		Bar:         "hit_pct",
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
	},
	{
		Key:         "settings_show_pending",
		Title:       "Pending settings",
		Category:    "server",
		Description: "settings that differ from the configured value and need reload or restart",
		SQL:         sqlDiagSettingsShowPending,
		Bar:         "",
	},
}
