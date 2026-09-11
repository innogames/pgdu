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
		Description: "tables ranked by how far their oldest unfrozen XID trails the current one, against the limits that actually matter (vacuum_failsafe_age, 2^31) — the drill-down for the wraparound health check",
		SQL:         sqlDiagWraparoundTables,
		// pct_of_failsafe is the bar because its denominator is a real danger
		// threshold, and it is constant across rows, so the bar order matches
		// the max_xid_age sort. Sorting on the age itself keeps the ranking
		// readable when every row rounds to 0.0% of the failsafe.
		Bar:  "pct_of_failsafe",
		Sort: "max_xid_age",
		Kinds: map[string]DiagColumnKind{
			// Higher is worse, graded on an absolute scale.
			"pct_of_wraparound": DiagPercentBad,
			"pct_of_failsafe":   DiagPercentBad,
			"pct_freeze_max":    DiagPercentBad,
			// XID/multixact ages and the per-table autovacuum counters are
			// counts, but summing them across tables is meaningless — DiagFloat
			// renders them right-aligned yet keeps them out of the Σ footer
			// (which still totals dead_tuples and size_bytes).
			"max_xid_age":            DiagFloat,
			"main_xid_age":           DiagFloat,
			"toast_xid_age":          DiagFloat,
			"mxid_age":               DiagFloat,
			"autovacuum_count":       DiagFloat,
			"toast_autovacuum_count": DiagFloat,
		},
		// pct_freeze_max is the metric this view used to lead with; it stays
		// fetched and one C keystroke away, but it grades routine maintenance
		// and must not be the headline. dead_tuples belongs to Vacuum stats.
		DefaultHidden: []string{"pct_freeze_max", "dead_tuples"},
		Note: "ages near autovacuum_freeze_max_age are normal — an anti-wraparound autovacuum is about to run; " +
			"investigate past 2× that, or when the xmin horizon check fires",
		Fix: fixTableStmt("VACUUM (FREEZE, VERBOSE)", "schema", "table_name",
			"-- run off-peak; if the xmin horizon is pinned this cannot freeze anything — check that first"),
		Help: `Each table's XID freeze age: how far its oldest unfrozen transaction
			ID trails the current XID, for the table (main_xid_age) and its TOAST
			relation (toast_xid_age) separately, with max_xid_age the worse of
			the two and the default sort.

			Read the percentages in this order. pct_of_wraparound is the age
			against the 2^31 hard limit, where the server stops accepting
			writes — the only number that means the cluster is in danger.
			pct_of_failsafe is the age against vacuum_failsafe_age, where VACUUM
			abandons its cost delay and index cleanup to catch up; approaching
			100% there is the last comfortable warning. pct_freeze_max (hidden
			by default, C to show) is the distance to the next *routine*
			anti-wraparound autovacuum — it reaches 100% constantly on a busy
			cluster and is not a problem signal.

			So: ages climbing toward autovacuum_freeze_max_age are how freezing
			is supposed to work. Investigate when an age is several times that
			limit, which means a forced anti-wraparound cycle already ran without
			advancing relfrozenxid. Two causes to separate. A large
			toast_xid_age next to a small main_xid_age — especially with
			toast_last_autovacuum empty and toast_autovacuum_count 0 — is a TOAST
			relation autovacuum has never reached. Otherwise compare the age
			with last_autovacuum and autovacuum_count: a recent, repeated
			autovacuum that never drops the age means it could not freeze, which
			is almost always the xmin horizon pinned by an idle transaction,
			a replication slot or a prepared transaction (the system overview's
			xmin_horizon finding names the holder). An old or absent
			last_autovacuum means autovacuum is not reaching the table at all.
			mxid_age tracks the independent multixact counter, driven by shared
			row locks and FK checks, and can be old while the XID age is fine.`,
	},
}
