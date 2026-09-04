package pg

// diagVacuum holds the 'vacuum' category of the Diagnostics registry;
// SQL lives in queries_diag_vacuum.go.
var diagVacuum = []Diagnostic{
	// "Autovacuum progress" was dropped: the running autovacuum workers themselves
	// are in the Activity tool (they are ordinary pg_stat_activity backends), and
	// their per-phase progress is covered by "Running vacuums" (heap scan detail)
	// and "Running operations (progress)" (every pg_stat_progress_* view).
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
		Fix:         fixTableStmt("VACUUM (ANALYZE, VERBOSE)", "schema", "relname"),
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
		Fix: fixTableStmt("VACUUM (FREEZE, VERBOSE)", "schema", "table_name",
			"-- run off-peak; check for an old xmin (idle transactions, stalled slots) first"),
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
}
