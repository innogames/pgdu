package pg

// diagActivity holds the 'activity' category of the Diagnostics registry;
// SQL lives in queries_diag_activity.go.
var diagActivity = []Diagnostic{
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

	{
		Key:         "backend_io",
		Title:       "I/O by backend",
		Category:    "activity",
		Description: "per-backend reads, writes and WAL next to the query doing them (PostgreSQL 18+)",
		SQL:         sqlDiagBackendIO,
		MinVersion:  pgVer18,
		Bar:         "reads",
		Kinds: map[string]DiagColumnKind{
			// Per-backend counters: right-aligned numbers, but summing them
			// across backends is meaningless, so keep them out of the Σ footer.
			"wal_records": DiagFloat,
			"wal_fpi":     DiagFloat,
		},
		DefaultHidden: []string{"extends", "evictions", "fsyncs", "username"},
		Help: `What each *backend* has read, written and logged since it
			started — PostgreSQL 18's per-backend statistics, which the
			cluster-wide "I/O by backend type" diagnostic cannot separate.
			Use it when the server is busy and you need the one session
			responsible: a backend with a large reads/read_bytes is scanning
			far more than it should (check its query and the index
			diagnostics), one with large writes is evicting dirty pages
			itself because the checkpointer is behind, and a large wal_bytes
			with a high wal_fpi share is a write burst landing just after a
			checkpoint. The counters are cumulative for the life of that
			backend, so a long-lived pooled connection accumulates work from
			queries it has long finished — read them next to state and query,
			not as a verdict on the statement shown.`,
	},
	{
		Key:         "aios",
		Title:       "Async I/O in flight",
		Category:    "activity",
		Description: "pg_aios: the asynchronous I/O handles currently outstanding (PostgreSQL 18+)",
		SQL:         sqlDiagAIOs,
		MinVersion:  pgVer18,
		Help: `A point-in-time look at PostgreSQL 18's asynchronous I/O: the
			handles in flight right now, who submitted them and what state
			they are in. Mostly empty on a quiet server — each row is an I/O
			that has been handed off and not yet completed, so a persistently
			long list means I/O is not keeping up rather than that work is
			being done efficiently. Relevant only with io_method set to
			worker (the default) or io_uring; with io_method = sync nothing
			is ever queued here. Reading the view may need elevated
			privileges.`,
	},
}
