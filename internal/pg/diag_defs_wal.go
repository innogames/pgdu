package pg

// diagWal holds the 'wal' category of the Diagnostics registry;
// SQL lives in queries_diag_wal.go.
var diagWal = []Diagnostic{
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
}
