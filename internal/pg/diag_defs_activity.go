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
}
