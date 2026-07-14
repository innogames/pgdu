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

	// DefaultHidden lists columns hidden on first view, one keystroke from
	// being shown via the C column picker. Unlike dropping them from the SQL,
	// the data is still fetched — this just declutters the default table for a
	// wide result. Empty = every column shown.
	DefaultHidden []string

	// Help is the long-form explanation shown in the ? reference overlay:
	// what the diagnostic is for and how to interpret its result (which
	// columns matter, what good/bad looks like, what action a bad row
	// suggests). Free prose — whitespace is collapsed and the text re-wrapped
	// to the terminal width at render time.
	Help string
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
		Description: "estimated bloat % and wasted bytes for btree indexes (>50% bloat, >10 MB waste)",
		SQL:         sqlDiagBloatIndex,
		Bar:         "bloat_pct",
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
		Description: "detailed table bloat estimate (>50% and >50 MB, or >25% and >1 GB)",
		SQL:         sqlDiagBloatTable,
		Bar:         "pct_bloat",
		Sort:        "bloat_bytes",
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
		Help: `Tables whose planner statistics no longer describe their contents:
			modified_rows accumulated since the last ANALYZE as a share of
			live_rows (only tables ≥ 10k rows and > 10% modified appear). Stale
			stats mean wrong row estimates and bad plans — misordered joins, seq
			scans where an index was cheaper. analyzed_ago shows how long the
			staleness has built up. Fix now with ANALYZE; fix recurrence by
			lowering the table's autovacuum_analyze_scale_factor.`,
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
	// ── vacuum ────────────────────────────────────────────────────────────
	{
		Key:         "autovacuum_progress",
		Title:       "Autovacuum progress",
		Category:    "vacuum",
		Description: "currently running autovacuum workers with scan and vacuum progress",
		SQL:         sqlDiagAutovacuumProgress,
		Bar:         "scanned_pct",
		Kinds:       map[string]DiagColumnKind{"dead_pct": DiagPercentBad},
		Help: `Every vacuum currently running (autovacuum and manual), with phase
			and progress. scanned_pct tracks the heap scan; vacuumed_pct lags
			behind while index vacuuming runs in between. dead_pct is the
			dead-item store filling up — at 100% another index-vacuum round
			starts (index_vacuum_count counts the rounds; more than 1 means
			raising autovacuum_work_mem would save whole index passes). waiting
			names the event a stalled worker is blocked on. mode "wraparound" is
			the aggressive anti-wraparound run: let it finish — cancelling it
			only defers a forced rerun.`,
	},
	{
		Key:         "progress_all",
		Title:       "Running operations (progress)",
		Category:    "vacuum",
		Description: "everything with a pg_stat_progress_* view — VACUUM, CREATE INDEX, ANALYZE, CLUSTER, COPY, base backups — with % done",
		SQL:         sqlDiagProgressAll,
		Bar:         "done_pct",
		Help: `One row for every operation that reports progress — VACUUM, CREATE
			INDEX, ANALYZE, CLUSTER, COPY and base backups — with a unified
			done_pct. The pre-deploy / pre-restart glance: is anything
			long-running still in flight, and how far along is it? done_pct can
			be blank while an operation is in a phase with no measurable total,
			and COPY's figure may be estimated from row counts. running_for is
			the age of the operation's transaction; the dedicated vacuum
			diagnostics carry the per-phase detail.`,
	},
	{
		Key:         "vacuum_running",
		Title:       "Running vacuums",
		Category:    "vacuum",
		Description: "active VACUUM commands with phase and percent complete",
		SQL:         sqlDiagVacuumRunning,
		Bar:         "percent_complete",
		Help: `VACUUMs executing right now, with phase, heap blocks scanned vs
			total, and duration. percent_complete covers the heap scan only —
			index-vacuum cycles in between can make the whole run far longer.
			dead_tuple_bytes (PG17+) is the memory the collected dead items
			occupy. A vacuum pinned at a low percentage for a long duration is
			usually throttled by the vacuum cost limits or waiting on something
			(buffer pins, locks) — cross-check the lock diagnostics.`,
	},
	{
		Key:         "vacuum_stats",
		PerDB:       true,
		Title:       "Vacuum stats",
		Category:    "vacuum",
		Description: "last vacuum/analyze timestamps, dead tuple counts and autovacuum threshold per table",
		SQL:         sqlDiagVacuumStats,
		Bar:         "dead_tuples",
		Help: `Per-table vacuum and analyze recency, plus dead tuples against the
			autovacuum trigger (av_threshold = threshold + scale_factor × rows,
			honouring per-table overrides). A * in expect_av means dead_tuples
			already exceeds the trigger, so autovacuum should visit the table
			soon; a * that persists while last_autovacuum stays old means
			autovacuum can't keep up — busy workers, cost limits, or something
			repeatedly cancelling it. A stale last_analyze on a modified table
			also risks bad plans (see Stale planner statistics).`,
	},
	{
		Key:         "wraparound_tables",
		PerDB:       true,
		Title:       "Wraparound freeze age",
		Category:    "vacuum",
		Description: "tables ranked by XID freeze age as % of autovacuum_freeze_max_age — the drill-down for the wraparound health check (last_autovacuum tells a pinned horizon from a lagging autovacuum)",
		SQL:         sqlDiagWraparoundTables,
		Bar:         "pct_freeze_max",
		Kinds: map[string]DiagColumnKind{
			"pct_freeze_max": DiagPercentBad, // higher is worse, graded on an absolute scale
			// XID ages and the per-table autovacuum counter are counts, but summing
			// them across tables is meaningless — DiagFloat renders them right-aligned
			// yet keeps them out of the Σ footer (which still totals dead_tuples and
			// size_bytes).
			"xid_age":          DiagFloat,
			"toast_xid_age":    DiagFloat,
			"autovacuum_count": DiagFloat,
		},
		Help: `Each table's XID freeze age: how far its oldest unfrozen transaction
			ID (including its TOAST relation) trails the current XID.
			pct_freeze_max is that age against autovacuum_freeze_max_age — at
			100% PostgreSQL forces an aggressive anti-wraparound autovacuum, and
			a cluster whose freezing can't keep up eventually stops accepting
			writes. Steadily climbing ages are normal (regular vacuums skip
			all-visible pages and rarely advance the age); worry when rows
			approach 100%: schedule VACUUM (FREEZE) off-peak, and check for an
			old xmin pinning the horizon — idle-in-transaction sessions and
			stalled replication slots (both have their own diagnostics).`,
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
		Help: `pg_stat_activity rolled up by database and state. Read it against
			max_connections: a large idle count is pool oversizing (every
			connection costs server memory); "idle in transaction" is the harmful
			state — those sessions hold locks and pin the xmin horizon
			(max_state_age_secs shows the oldest one; drill in with the
			Idle-in-transaction diagnostic). A sustained high active count is
			genuine saturation — the Activity tool shows what they are running.`,
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
		Help: `Sessions holding a transaction open while doing nothing. They block
			VACUUM from cleaning any tuple deleted since their snapshot (bloat
			grows across the whole cluster) and keep locks (locked_relations)
			that stall DDL and autovacuum. xact_age_secs is how long the
			transaction has been open; last_query is what ran before the app
			went idle — usually the clue to the missing COMMIT or leaked pool
			connection. Kill an offender via the Activity tool (cancel /
			terminate); prevent recurrence with
			idle_in_transaction_session_timeout.`,
	},
	{
		Key:         "lock_summary",
		Title:       "Lock summary",
		Category:    "activity",
		Description: "pg_locks grouped by lock type and mode with waiter counts — the one-glance contention read",
		SQL:         sqlDiagLockSummary,
		Bar:         "locks",
		Kinds:       map[string]DiagColumnKind{"waiting": DiagCostGraded},
		Help: `pg_locks rolled up by lock type and mode. Held locks are normal
			bookkeeping — the column that matters is waiting: nonzero means
			backends are queued behind a conflicting holder. AccessExclusiveLock
			traffic is DDL colliding with queries; waiting transactionid/tuple
			locks are row-update contention. sample_relation names one affected
			table. For who blocks whom, use the Activity tool and its lock tree
			(b).`,
	},
	// ── wal ───────────────────────────────────────────────────────────────
	{
		Key:         "wal_files",
		Title:       "WAL files",
		Category:    "wal",
		Description: "WAL segment files on disk by modification time (needs superuser or pg_monitor)",
		SQL:         sqlDiagWalFiles,
		Bar:         "size_bytes",
		Help: `The WAL segment files in pg_wal (16 MB each by default), newest
			first; needs pg_monitor or superuser. Their total size should hover
			around max_wal_size — steady growth means recycling is blocked: a
			stalled or inactive replication slot (see Replication slots), a
			failing archive_command, or a generous wal_keep_size. A pg_wal
			partition that fills up crashes the server, so a growing file count
			deserves prompt attention.`,
	},
	{
		Key:         "wal_activity",
		Title:       "WAL activity",
		Category:    "wal",
		Description: "cluster-wide WAL generation counters from pg_stat_wal (PostgreSQL 14+)",
		SQL:         sqlDiagWalActivity,
		Bar:         "wal_bytes",
		Help: `Cluster-wide WAL production since stats_reset. wal_bytes divided by
			the elapsed time is the generation rate — the number capacity
			planning, archiving and replica sizing care about. A high wal_fpi
			share (full-page images vs wal_records) means many pages take their
			first write shortly after each checkpoint: lengthening checkpoints
			(max_wal_size, checkpoint_timeout) cuts WAL volume. A growing
			wal_buffers_full counter says wal_buffers is too small for the write
			bursts.`,
	},
	// ── server ────────────────────────────────────────────────────────────
	{
		Key:         "database_show_size",
		Title:       "Database sizes",
		Category:    "server",
		Description: "size of every database the current user can connect to",
		SQL:         sqlDiagDatabaseShowSize,
		Bar:         "size_bytes",
		Help: `Every database on the server with its total on-disk size (heap,
			indexes, TOAST and visibility/free-space maps together). A NULL size
			only means the current role lacks CONNECT on that database and can't
			measure it. To see what's inside a database, open the Disk tool
			against it.`,
	},
	{
		Key:         "database_stats",
		Title:       "Database stats",
		Category:    "server",
		Description: "per-database transactions & rollback %, cache hit ratio, tuple I/O, conflicts, deadlocks, temp files, and block/session time",
		SQL:         sqlDiagDatabaseStats,
		Bar:         "hit_pct",
		// Rarely-nonzero or niche columns start hidden to keep the wide default
		// table readable; the C picker reveals them.
		DefaultHidden: []string{"conflicts", "rollback_pct", "sessions"},
		Kinds: map[string]DiagColumnKind{
			"hit_pct":      DiagPercentGraded, // higher is better
			"rollback_pct": DiagPercentBad,    // higher is worse
			// 0-is-good counter: green at zero, graded up to the worst database
			// in the window so a nonzero value stands out.
			"conflicts": DiagCostGraded,
			// Cumulative time totals: plain floats (no bogus Σ footer, no
			// magnitude colouring — they are always large on a long-lived cluster).
			"blk_read_secs":     DiagFloat,
			"blk_write_secs":    DiagFloat,
			"active_secs":       DiagFloat,
			"idle_in_xact_secs": DiagFloat,
			"session_secs":      DiagFloat,
		},
		Help: `The per-database health card from pg_stat_database, cumulative since
			stats_reset. hit_pct (the headline bar) is the buffer-cache hit
			ratio — sustained values below ~99% on an OLTP database mean the
			working set outgrows shared_buffers. temp_files/temp_bytes are sorts
			and hashes spilling past work_mem; deadlocks should stay at zero
			(application lock-ordering bug); a high rollback_pct means lots of
			failing transactions; conflicts only occur on standbys. C reveals
			the columns hidden by default.`,
	},
	{
		Key:         "foreignkeys_show_all",
		PerDB:       true,
		Title:       "Foreign keys",
		Category:    "server",
		Description: "all foreign-key constraints in every schema",
		SQL:         sqlDiagForeignkeysShowAll,
		Bar:         "",
		Help: `Every FOREIGN KEY constraint with its referencing (table_name /
			column_name) and referenced (foreign_*) side — a schema reference for
			dependency spelunking, not a problem detector. Use the filter (/) to
			trace what points at a table before dropping or rewriting it; the
			"FKs without index" diagnostic flags the subset that is an actual
			performance risk.`,
	},
	{
		Key:         "grants_show_all",
		PerDB:       true,
		Title:       "Grants",
		Category:    "server",
		Description: "all explicit grants on schemas, tables, views, sequences, and functions",
		SQL:         sqlDiagGrantsShowAll,
		Bar:         "",
		Help: `Every explicit ACL entry on schemas, tables, views, sequences and
			functions: who (grantee) may do what (privilege_type) on which
			object, and who granted it. Owner-implicit rights and default
			privileges are not listed — only explicit grants. The audit view:
			filter (/) by a role to see its reach, or by an object to see who can
			touch it; is_grantable = t means the grantee can pass the privilege
			on, which is worth a second look.`,
	},
	{
		Key:         "replication_slots",
		Title:       "Replication slots",
		Category:    "server",
		Description: "all replication slots with WAL retention, status and activity",
		SQL:         sqlDiagReplicationSlots,
		Bar:         "retained_wal_bytes",
		Help: `Replication slots and the WAL each one forces the server to keep.
			retained_wal_bytes is the cost: an inactive slot (active = f, see
			inactive_for) retains WAL indefinitely and will eventually fill
			pg_wal. safe_wal_size is the headroom left before
			max_slot_wal_keep_size invalidates the slot; wal_status "lost" means
			that already happened and the consumer must be re-synced. Drop
			abandoned slots with pg_drop_replication_slot(); for logical slots
			also check the subscriber side (Logical subscriptions).`,
	},
	{
		Key:         "sequences",
		PerDB:       true,
		Title:       "Sequence usage",
		Category:    "server",
		Description: "sequences more than 30% through their range, with the table each backs (last_value needs SELECT/USAGE)",
		SQL:         sqlDiagSequences,
		Bar:         "consumed_pct",
		Kinds:       map[string]DiagColumnKind{"consumed_pct": DiagPercentBad},
		Help: `Sequences past 30% of their range, with the table each one backs
			(owned_by_table). consumed_pct reaching 100 means nextval() starts
			failing inserts on that table. The usual culprit is an int4 serial
			key: migrate the column to bigint (a table rewrite — plan the
			maintenance window well before the ceiling). Sequences whose
			last_value the current role can't read (no SELECT/USAGE) are absent,
			so an empty list only vouches for the readable ones.`,
	},
	{
		Key:         "settings_show_pending",
		Title:       "Pending settings",
		Category:    "server",
		Description: "settings that differ from the configured value and need reload or restart",
		SQL:         sqlDiagSettingsShowPending,
		Bar:         "",
		Help: `Settings whose value on disk differs from what the running server
			uses, with the action needed to apply each: "restart" needs a full
			server restart, "reload" just pg_reload_conf() or SIGHUP, "new
			session" applies only to sessions started from now on. Rows lingering
			here mean a config change was made but never activated — resolve them
			deliberately, before an unplanned restart activates them for you. An
			empty result means the running config matches disk.`,
	},
	{
		Key:         "slru_stats",
		Title:       "SLRU caches",
		Category:    "server",
		Description: "transaction-status / multixact / subtransaction cache traffic — invisible pressure from long transactions and savepoints",
		SQL:         sqlDiagSLRU,
		Bar:         "blks_read",
		Kinds:       map[string]DiagColumnKind{"hit_pct": DiagPercentGraded},
		Help: `Traffic in the small fixed-size SLRU caches PostgreSQL keeps beside
			shared_buffers: transaction status (Xact/CommitTs), MultiXact,
			Subtrans, Notify and friends. Normally near-silent — heavy blks_read
			or a poor hit_pct on Subtrans points at deep savepoint nesting under
			long transactions, on MultiXact at SELECT FOR SHARE / foreign-key
			contention, on Notify at LISTEN/NOTIFY volume. This pressure is
			invisible in the normal cache stats; on PG17+ the cache sizes are
			tunable (subtransaction_buffers, multixact_*_buffers, …).`,
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
		Help: `Logical-replication subscriptions in this database, with worker
			state and error counters. An enabled subscription without a
			worker_pid means the apply worker is down — check the logs. The
			subscriber can't compute byte lag, so staleness is the signal:
			last_msg_age growing means nothing arrives (publisher or network),
			report_age growing means apply has stalled. Nonzero
			apply_errors/sync_errors are usually constraint conflicts — the
			worker retries in a loop until the conflicting row is fixed or the
			change is skipped. syncing_table shows an initial table copy still
			running.`,
	},
}
