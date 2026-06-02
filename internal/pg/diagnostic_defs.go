package pg

// Diagnostic describes one entry in the diagnostics tool list: a name, a
// category (used as a label in the list view), a short description, the SQL
// to run, and the name of the headline column to render as a bar chart (or
// "" for all-text queries where no bar is meaningful).
type Diagnostic struct {
	Key         string // stable identifier (matches the psql-helper filename stem)
	Title       string // short display name shown in the list
	Category    string // "index" | "table" | "vacuum" | "server"
	Description string // one-line explanation shown as detail in the list
	SQL         string // the query to run (no parameters)
	Bar         string // headline column name rendered as a bar, or ""
}

// Diagnostics is the ordered registry of all built-in diagnostic queries.
// Queries are grouped by category and sorted alphabetically within each group;
// the TUI list renders them in this order.
var Diagnostics = []Diagnostic{
	// ── index ─────────────────────────────────────────────────────────────
	{
		Key:         "bloat_index",
		Title:       "Index bloat (btree)",
		Category:    "index",
		Description: "estimated bloat % and wasted MB for btree indexes (>50% bloat, >10 MB waste)",
		SQL:         sqlDiagBloatIndex,
		Bar:         "bloat_pct",
	},
	{
		Key:         "index_show_all",
		Title:       "All indexes",
		Category:    "index",
		Description: "every index in the public schema with scan counts and size",
		SQL:         sqlDiagIndexShowAll,
		Bar:         "number_of_scans",
	},
	{
		Key:         "index_show_definitions",
		Title:       "Index definitions",
		Category:    "index",
		Description: "CREATE INDEX statement for every index in the public schema",
		SQL:         sqlDiagIndexShowDefinitions,
		Bar:         "",
	},
	{
		Key:         "index_show_duplicate",
		Title:       "Duplicate indexes",
		Category:    "index",
		Description: "indexes with identical column sets (candidates for removal)",
		SQL:         sqlDiagIndexShowDuplicate,
		Bar:         "",
	},
	{
		Key:         "index_show_invalid",
		Title:       "Invalid indexes",
		Category:    "index",
		Description: "indexes marked indisvalid=false (failed CONCURRENTLY builds)",
		SQL:         sqlDiagIndexShowInvalid,
		Bar:         "",
	},
	{
		Key:         "index_show_size",
		Title:       "Index sizes",
		Category:    "index",
		Description: "all indexes sorted by size with estimated row count and column list",
		SQL:         sqlDiagIndexShowSize,
		Bar:         "estimated_entries",
	},
	{
		Key:         "index_show_unused",
		Title:       "Unused indexes",
		Category:    "index",
		Description: "indexes in public schema with 0 scans, ordered by size (candidates for removal)",
		SQL:         sqlDiagIndexShowUnused,
		Bar:         "idx_scan",
	},
	// ── table ─────────────────────────────────────────────────────────────
	{
		Key:         "bloat_all",
		Title:       "Table + index bloat (approx)",
		Category:    "table",
		Description: "estimated table and index bloat using pg_stats (no extensions required)",
		SQL:         sqlDiagBloatAll,
		Bar:         "wastedbytes",
	},
	{
		Key:         "bloat_table",
		Title:       "Table bloat (detailed)",
		Category:    "table",
		Description: "detailed table bloat estimate (>50% and >10 MB, or >25% and >1 GB)",
		SQL:         sqlDiagBloatTable,
		Bar:         "pct_bloat",
	},
	{
		Key:         "table_scan_types",
		Title:       "Sequential scan candidates",
		Category:    "table",
		Description: "tables with >20% sequential reads and >800 kB — potential missing-index candidates",
		SQL:         sqlDiagTableScanTypes,
		Bar:         "index_read_pct",
	},
	{
		Key:         "table_show_hitratio",
		Title:       "Table cache hit ratio",
		Category:    "table",
		Description: "tables with heap cache hit ratio below 80%, ordered by blocks read from disk",
		SQL:         sqlDiagTableShowHitratio,
		Bar:         "hit_pct",
	},
	{
		Key:         "table_show_modify_ratio",
		Title:       "Table modification ratio",
		Category:    "table",
		Description: "insert / update / delete split per table (since last stats reset)",
		SQL:         sqlDiagTableShowModifyRatio,
		Bar:         "upd_pct",
	},
	{
		Key:         "table_show_size",
		Title:       "Table sizes (with partitions)",
		Category:    "table",
		Description: "total, index, toast and heap sizes rolled up across partition trees",
		SQL:         sqlDiagTableShowSize,
		Bar:         "est_row_count",
	},
	{
		Key:         "toast_show_size",
		Title:       "TOAST table sizes",
		Category:    "table",
		Description: "TOAST tables with their owning table, toastable columns, and live/dead tuple counts",
		SQL:         sqlDiagToastShowSize,
		Bar:         "dead_tuples",
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
		Title:       "Vacuum stats",
		Category:    "vacuum",
		Description: "last vacuum/analyze timestamps, dead tuple counts and autovacuum threshold per table",
		SQL:         sqlDiagVacuumStats,
		Bar:         "",
	},
	// ── server ────────────────────────────────────────────────────────────
	{
		Key:         "database_show_size",
		Title:       "Database sizes",
		Category:    "server",
		Description: "size of every database the current user can connect to",
		SQL:         sqlDiagDatabaseShowSize,
		Bar:         "",
	},
	{
		Key:         "foreignkeys_show_all",
		Title:       "Foreign keys",
		Category:    "server",
		Description: "all foreign-key constraints in every schema",
		SQL:         sqlDiagForeignkeysShowAll,
		Bar:         "",
	},
	{
		Key:         "grants_show_all",
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
		Bar:         "",
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
