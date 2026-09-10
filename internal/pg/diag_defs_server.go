package pg

// diagServer holds the 'server' category of the Diagnostics registry;
// SQL lives in queries_diag_server.go.
var diagServer = []Diagnostic{
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
		// table readable; the C picker reveals them. stats_age_secs exists for
		// per-day rate grading and duplicates stats_reset for a reader.
		DefaultHidden: []string{"conflicts", "rollback_pct", "sessions", "stats_age_secs"},
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
		Description: "sequences more than 30% through their own range, with the column each feeds (last_value needs SELECT/USAGE)",
		SQL:         sqlDiagSequences,
		Bar:         "consumed_pct",
		Kinds: map[string]DiagColumnKind{
			"consumed_pct": DiagPercentBad,
			"remaining":    DiagCount,
		},
		Help: `Sequences past 30% of their range, with the table.column each one
			feeds (used_by). consumed_pct reaching 100 means nextval() starts
			failing inserts on that table. The usual culprit is an int4 serial
			key: migrate the column to bigint (a table rewrite — plan the
			maintenance window well before the ceiling).

			consumed_pct is measured from the sequence's start_value, not from
			zero, so a sequence deliberately parked high in its type's range
			(reserving a band for synthetic ids) is not reported as nearly
			exhausted on the day it is created. remaining is the raw count of
			values left before limit_value; weigh it against how fast the
			sequence actually moves. Cycling sequences wrap rather than fail and
			are left out entirely.

			used_by prefers the owning column (SERIAL, IDENTITY, OWNED BY) and
			otherwise lists every column whose DEFAULT calls nextval() on the
			sequence; it stays "—" for a sequence driven straight from
			application code, a function or a trigger, which the catalog does
			not track — such a sequence is unowned but not necessarily unused.
			Sequences whose last_value the current role can't read (no
			SELECT/USAGE) are absent, so an empty list only vouches for the
			readable ones.`,
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
		Key:         "io_stats",
		Title:       "I/O by backend type",
		Category:    "server",
		Description: "pg_stat_io: reads, hits, writes, evictions and fsyncs per backend type / object / context — who does the I/O and why",
		SQL:         sqlDiagIOStats,
		Bar:         "reads",
		Kinds: map[string]DiagColumnKind{
			"hit_pct": DiagPercentGraded,
			"read_ms": DiagFloat,
		},
		DefaultHidden: []string{"extends", "reuses", "stats_reset"},
		Help: `Cluster-wide I/O broken down by who did it (backend_type), on what
			(object: relation, temp relation, or WAL on PG18+) and in which
			context: normal is shared_buffers traffic, bulkread/bulkwrite the
			ring buffers of large scans and COPY, vacuum autovacuum's own
			reads and writes. Client-backend reads are cache misses queries
			waited for; read_ms is their mean latency (needs track_io_timing —
			sub-millisecond means the OS page cache served them). Client-backend
			writes and fsyncs mean backends had to flush dirty pages themselves
			because the checkpointer/bgwriter were behind. evictions count pages
			pushed out of shared_buffers; reuses are ring-buffer slots recycled.`,
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
