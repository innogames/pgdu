package pg

// diagTable holds the 'table' category of the Diagnostics registry;
// SQL lives in queries_diag_table.go.
var diagTable = []Diagnostic{
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
		Description: "detailed table bloat estimate (>50% and >50 MB, or >25% and >1 GB)",
		SQL:         sqlDiagBloatTable,
		Bar:         "pct_bloat",
		Sort:        "bloat_bytes",
		Fix:         fixTableBloat,
		Help: `Statistical estimate of heap bloat — dead space plain VACUUM keeps
			but never returns to the OS — derived from pg_stats row widths; only
			significant offenders are shown (≥50% and ≥50 MB, or ≥25% and ≥1 GB).
			can_estimate = f means column stats were missing and the size stands
			alone. Estimates can be off for heavily padded or toasted rows —
			confirm with pgstattuple before rewriting anything. Reclaim with
			VACUUM FULL or pg_repack (the former takes an exclusive lock), then
			chase the cause: autovacuum not keeping up, or an old transaction /
			replication slot pinning the xmin horizon.`,
	},
	{
		Key:         "stale_statistics",
		PerDB:       true,
		Title:       "Stale planner statistics",
		Category:    "table",
		Description: "tables (≥10k live rows) with >10% of rows modified since the last ANALYZE — bad-plan risk",
		SQL:         sqlDiagStaleStatistics,
		Bar:         "stale_pct",
		Kinds:       map[string]DiagColumnKind{"stale_pct": DiagPercentBad},
		Fix:         fixTableStmt("ANALYZE", "schema", "table_name"),
		Help: `Tables whose planner statistics no longer describe their contents:
			modified_rows accumulated since the last ANALYZE as a share of
			live_rows (only tables ≥ 10k rows and > 10% modified appear). Stale
			stats mean wrong row estimates and bad plans — misordered joins, seq
			scans where an index was cheaper. analyzed_ago shows how long the
			staleness has built up. Fix now with ANALYZE; fix recurrence by
			lowering the table's autovacuum_analyze_scale_factor.`,
	},
	{
		Key:         "table_fillfactor",
		PerDB:       true,
		Title:       "Fillfactor advisor",
		Category:    "table",
		Description: "update-active tables ≥1 MB: current vs suggested FILLFACTOR from avg row size, update intensity and HOT ratio",
		SQL:         sqlDiagTableFillfactor,
		Bar:         "non_hot_updates",
		Kinds: map[string]DiagColumnKind{
			"hot_pct": DiagPercentGraded,
			// DiagFloat, not DiagInt: fillfactors are settings, and the Σ footer
			// sums DiagInt columns — a summed fillfactor is nonsense.
			"current_fill":    DiagFloat,
			"suggested_fill":  DiagFloat,
			"updates":         DiagCount,
			"non_hot_updates": DiagCount,
			"live_rows":       DiagCount,
		},
		DefaultHidden: []string{"updates", "live_rows"},
		Fix:           fixTableFillfactor,
		Help: `Suggests a starting FILLFACTOR per update-active table: 100 minus the
			share of an 8 kB page one average row occupies (avg_row_bytes =
			heap size / live rows), so a page keeps room for roughly one more
			row version and updates can land HOT — on the same page, touching
			no index. Rarely-updated tables (upd_per_row < 0.1) are suggested
			100: packed pages cache better and the free space would go unused.
			Update-heavy tables still missing HOT (upd_per_row ≥ 1, hot_pct
			< 80) get 10 extra points of headroom. Caveats: counters are
			cumulative since the stats reset, so an old workload skews them;
			avg_row_bytes is inflated by bloat, so trust it after ANALYZE on a
			reasonably un-bloated table; an update touching any indexed column
			can never be HOT regardless of fillfactor — check over-indexing
			first (Table HOT update ratio). Apply with ALTER TABLE … SET
			(fillfactor = N); it only affects newly written pages, so rewrite
			with VACUUM FULL or pg_repack to take effect immediately, then
			watch hot_pct, bloat and WAL volume and adjust.`,
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
		Help: `Tables larger than ~800 kB where less than 80% of row reads came via
			indexes — the rest were sequential scans. seq_scan and seq_tup_read
			show how often and how much gets scanned; a low index_read_pct on a
			large, hot table is the classic missing-index signature. Cross-check
			the actual queries (top-queries tool) before adding an index:
			intentional full scans (batch jobs, reports) and small tables that
			live in cache are fine as they are.`,
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
		Help: `Share of each table's heap block reads served from shared_buffers
			(cumulative since the stats reset); only tables below 80% appear,
			worst disk readers (from_disk) first. A low hit_pct on a hot table
			means its working set doesn't stay cached — consider more
			shared_buffers, or find what keeps evicting it (large scans). A low
			ratio on a rarely-read table is harmless. A "disk" read may still be
			served by the OS page cache, so this is an upper bound on real I/O.`,
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
		Help: `Every UPDATE is either HOT (heap-only: the new row version stays on
			the same page and no index is touched) or non-HOT (every index on the
			table gets a new entry). Sorted by non_hot_updates: the top rows
			generate the most index churn and bloat. Raise hot_pct by removing
			indexes on frequently-updated columns (an update touching any indexed
			column can never be HOT) and by lowering FILLFACTOR (e.g. 90) so
			pages keep free space for HOT chains.`,
	},
	{
		Key:         "table_show_modify_ratio",
		PerDB:       true,
		Title:       "Table modification ratio",
		Category:    "table",
		Description: "insert / update / delete split per table (since last stats reset)",
		SQL:         sqlDiagTableShowModifyRatio,
		Bar:         "upd_pct",
		Help: `The write mix per table — inserts vs updates vs deletes since the
			stats reset. Not a problem list but a workload fingerprint that says
			which other diagnostic matters where: update-heavy tables → HOT
			update ratio and FILLFACTOR; update/delete-heavy → vacuum stats and
			bloat; insert-only → BRIN candidates and partitioning. Sort by a %
			column (←/→) to group tables by workload type.`,
	},
	{
		Key:         "table_show_size",
		PerDB:       true,
		Title:       "Table sizes (with partitions)",
		Category:    "table",
		Description: "total, index, toast and heap sizes rolled up across partition trees",
		SQL:         sqlDiagTableShowSize,
		Bar:         "total_bytes",
		Help: `On-disk footprint per table, with partition trees rolled up into
			their root: total = heap (table_bytes) + index_bytes + toast_bytes.
			Read the ratios: indexes rivalling or exceeding the heap suggest
			over-indexing (see the index diagnostics); a dominant TOAST share
			means wide text/jsonb/bytea values (see TOAST table sizes).
			est_row_count is the planner's estimate and lags on write-heavy
			tables.`,
	},
	{
		Key:         "toast_show_size",
		PerDB:       true,
		Title:       "TOAST table sizes",
		Category:    "table",
		Description: "TOAST tables with their owning table, toastable columns, and live/dead tuple counts",
		SQL:         sqlDiagToastShowSize,
		Bar:         "size_bytes",
		Help: `TOAST relations store a table's oversized column values out of line —
			size_bytes is disk the owning main_table_name's own listing doesn't
			obviously show, and column_names are the columns that can toast. Many
			dead_tuples mean updates/deletes of large values are waiting for
			vacuum; TOAST is vacuumed with its parent, so persistently high dead
			counts point at autovacuum lag on the main table. Shrink levers:
			shorter values, lz4 column compression, or moving blobs out of the
			database.`,
	},
}
