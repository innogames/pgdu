package pg

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Maintenance gathers a best-effort server-health snapshot for the Maintenance
// dashboard. Each sub-query is independent: a failure (missing privilege, absent
// extension, unknown GUC, old server lacking pg_stat_checkpointer) is silently
// absorbed so the dashboard degrades gracefully rather than surfacing an error
// for a missing optional section.
func (c *Client) Maintenance(ctx context.Context, db string) (*MaintenanceInfo, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("maintenance in %q: %w", db, err)
	}

	info := &MaintenanceInfo{
		SampledAt:    time.Now(),
		Settings:     settingsMap(ctx, pool, sqlMaintSettings, maintSettingsKeys),
		SettingBytes: make(map[string]int64),
		ConnByState:  make(map[string]int),
	}
	for k, v := range settingsMap(ctx, pool, sqlMaintSettingBytes, maintSettingBytesKeys) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			info.SettingBytes[k] = n
		}
	}
	info.Tuning = parseMaintTuning(settingsMap(ctx, pool, sqlMaintSettingsRaw, maintSettingsRawKeys))

	// --- max_connections (also in Settings, but parse once to int) ---
	_ = pool.QueryRow(ctx, sqlMaintMaxConns).Scan(&info.MaxConns)

	// --- server version + postmaster start + conf reload ---
	_ = pool.QueryRow(ctx, sqlMaintServer).Scan(&info.Version, &info.StartTime, &info.ConfLoad)

	// --- connection counts by state ---
	connRows, err := pool.Query(ctx, sqlMaintActivity)
	if err == nil {
		defer connRows.Close()
		for connRows.Next() {
			var state string
			var cnt int
			var longestXact float64
			if connRows.Scan(&state, &cnt, &longestXact) == nil {
				info.ConnByState[state] += cnt
				if longestXact > info.LongestXactSec {
					info.LongestXactSec = longestXact
				}
			}
		}
		connRows.Close()
	}

	// --- cache hit ratio ---
	_ = pool.QueryRow(ctx, sqlMaintCacheHit).Scan(&info.CacheHitRatio, &info.CacheBlocks)
	info.CacheHitRatio *= 100 // store as percent

	// --- SLRU caches ---
	info.SLRU = collectBestEffort(ctx, pool, sqlMaintSLRU, nil, func(rows pgx.Rows) (SLRUStat, bool) {
		var s SLRUStat
		return s, rows.Scan(&s.Name, &s.Hits, &s.Reads) == nil
	})

	// --- XID age ---
	_ = pool.QueryRow(ctx, sqlMaintWraparound).Scan(&info.XidAgeDB, &info.XidAge)

	// autovacuum_freeze_max_age from settings (already fetched above)
	if v, ok := info.Settings["autovacuum_freeze_max_age"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &info.FreezeMaxAge)
	}

	// vacuum_failsafe_age is where danger actually starts; absent on PG < 14,
	// which leaves it 0 and lets the advice fall back to a fixed age.
	if v, ok := info.Settings["vacuum_failsafe_age"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &info.FailsafeAge)
	}

	// --- multixact age ---
	_ = pool.QueryRow(ctx, sqlMaintMxidWraparound).Scan(&info.MxidAgeDB, &info.MxidAge)
	if v, ok := info.Settings["autovacuum_multixact_freeze_max_age"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &info.MxidFreezeMaxAge)
	}
	if v, ok := info.Settings["vacuum_multixact_failsafe_age"]; ok {
		_, _ = fmt.Sscanf(v, "%d", &info.MxidFailsafeAge)
	}

	// --- oldest live xmin and what pins it ---
	// Best effort like its neighbours: an unreadable horizon leaves Age 0, and
	// the Restricted flag (set even when the age reads 0) is what stops that
	// from being reported as an all-clear.
	_ = pool.QueryRow(ctx, sqlMaintXminHorizon).Scan(&info.Horizon.Age, &info.Horizon.Restricted)
	if info.Horizon.Age > 0 {
		// The holder query can fail or return nothing where the horizon query
		// succeeded (a row that vanished between the two); the advice then
		// still fires, naming the kind it could not resolve.
		_ = pool.QueryRow(ctx, sqlMaintXminHolder).Scan(&info.Horizon.Kind, &info.Horizon.Holder, new(int64))
	}

	// --- session hygiene: longest idle-in-txn / longest query ---
	_ = pool.QueryRow(ctx, sqlMaintLongestIdleXact).Scan(
		&info.Sess.IdleXactPID, &info.Sess.IdleXactApp, &info.Sess.IdleXactSecs)
	_ = pool.QueryRow(ctx, sqlMaintLongestQuery).Scan(
		&info.Sess.LongQueryPID, &info.Sess.LongQueryApp, &info.Sess.LongQuerySecs, &info.Sess.LongQueryText)

	// --- checkpointer counters + costs ---
	var cpReset *time.Time
	if pool.QueryRow(ctx, sqlMaintCheckpointer).Scan(
		&info.Checkpointer.Timed, &info.Checkpointer.Requested,
		&info.Checkpointer.WriteTimeMs, &info.Checkpointer.SyncTimeMs,
		&info.Checkpointer.BuffersWritten, &cpReset) == nil {
		info.Checkpointer.HasData = true
		if cpReset != nil {
			info.Checkpointer.StatsReset = *cpReset
		}
	}

	// --- WAL in-flight: bytes since last checkpoint vs max_wal_size ---
	_ = pool.QueryRow(ctx, sqlMaintWALInFlight).Scan(
		&info.WALBytesSinceCheckpoint, &info.WALMaxBytes, &info.WALCheckpointTime)

	// --- WAL generation counters + current write position ---
	var walReset *time.Time
	if pool.QueryRow(ctx, sqlMaintWALStats).Scan(
		&info.WAL.Records, &info.WAL.FPI, &info.WAL.Bytes, &info.WAL.BuffersFull, &walReset) == nil {
		info.WAL.HasData = true
		if walReset != nil {
			info.WAL.StatsReset = *walReset
		}
	}
	_ = pool.QueryRow(ctx, sqlMaintCurrentLSN).Scan(&info.WAL.CurrentLSNBytes)
	if pool.QueryRow(ctx, sqlMaintWALDir).Scan(&info.WALDir.Bytes, &info.WALDir.Files) == nil {
		info.WALDir.HasData = true
	}

	// --- pending config changes (count + names) ---
	_ = pool.QueryRow(ctx, sqlMaintPendingConfig).Scan(&info.PendingRestart, &info.PendingReload)
	if nameRows, err := pool.Query(ctx, sqlMaintPendingNames); err == nil {
		defer nameRows.Close()
		for nameRows.Next() {
			var name string
			var needsRestart bool
			if nameRows.Scan(&name, &needsRestart) == nil {
				if needsRestart {
					info.PendingRestartSettings = append(info.PendingRestartSettings, name)
				} else {
					info.PendingReloadSettings = append(info.PendingReloadSettings, name)
				}
			}
		}
		nameRows.Close()
	}

	// --- lock waits ---
	_ = pool.QueryRow(ctx, sqlMaintLockWaits).Scan(&info.LockWaits)

	// --- temp file pressure (total + per-db) ---
	_ = pool.QueryRow(ctx, sqlMaintTempFiles).Scan(&info.TempFiles, &info.TempBytes)
	info.TempByDB = collectBestEffort(ctx, pool, sqlMaintTempByDB, nil, func(rows pgx.Rows) (TempDBStat, bool) {
		var s TempDBStat
		var reset *time.Time
		if rows.Scan(&s.DB, &s.Files, &s.Bytes, &reset) != nil {
			return s, false
		}
		if reset != nil {
			s.StatsReset = *reset
		}
		return s, true
	})

	// --- background writer LRU sweep ---
	_ = pool.QueryRow(ctx, sqlMaintBgwriter).Scan(
		&info.Bgwriter.BuffersClean, &info.Bgwriter.MaxwrittenClean, &info.Bgwriter.BuffersAlloc)

	// --- autovacuum saturation (workers cluster-wide, backlog in this db) ---
	_ = pool.QueryRow(ctx, sqlMaintAutovacWorkers).Scan(&info.Autovac.WorkersBusy)
	_ = pool.QueryRow(ctx, sqlMaintTablesOverThreshold).Scan(&info.Autovac.OverThreshold, &info.Autovac.OverTop)

	// --- WAL archiver (silently absent when archive_mode = off) ---
	var archivedAt, failedAt *time.Time
	if pool.QueryRow(ctx, sqlMaintArchiver).Scan(
		&info.ArchiveCount, &info.ArchiveFailed, &info.ArchiveLastFailed, &archivedAt, &failedAt) == nil {
		if archivedAt != nil {
			info.ArchiveLastTime = *archivedAt
		}
		if failedAt != nil {
			info.ArchiveLastFailedTime = *failedAt
		}
	}

	// --- recovery role ---
	_ = pool.QueryRow(ctx, sqlMaintRecovery).Scan(&info.InRecovery)

	// --- streaming replication (primary-side) ---
	info.Replicas = collectBestEffort(ctx, pool, sqlMaintReplication, nil, func(rows pgx.Rows) (ReplicaStat, bool) {
		var r ReplicaStat
		var writeSec, flushSec, replaySec float64
		if rows.Scan(&r.PID, &r.AppName, &r.ClientAddr, &r.State, &r.SyncState,
			&writeSec, &flushSec, &replaySec, &r.ByteLag, &r.ReplayLSNBytes) != nil {
			return r, false
		}
		r.WriteLag = time.Duration(writeSec * float64(time.Second))
		r.FlushLag = time.Duration(flushSec * float64(time.Second))
		r.ReplayLag = time.Duration(replaySec * float64(time.Second))
		return r, true
	})

	// --- replication slots ---
	info.ReplSlots = collectBestEffort(ctx, pool, sqlMaintReplSlots, nil, func(rows pgx.Rows) (ReplSlotStat, bool) {
		var s ReplSlotStat
		return s, rows.Scan(&s.Name, &s.SlotType, &s.Active, &s.ActivePID, &s.WALStatus, &s.RetainedBytes, &s.SafeWALBytes, &s.InactiveSecs) == nil
	})

	// --- WAL receiver (standby-side) ---
	var recvStatus string
	var recvMsgAgeSec float64
	if pool.QueryRow(ctx, sqlMaintWalReceiver).Scan(&recvStatus, &recvMsgAgeSec) == nil {
		info.WalReceiver = &WalReceiverStat{
			Status:     recvStatus,
			LastMsgAge: time.Duration(recvMsgAgeSec * float64(time.Second)),
		}
	}

	// --- transaction & session stats ---
	_ = pool.QueryRow(ctx, sqlMaintTxnStats).Scan(
		&info.XactCommit, &info.XactRollback, &info.Deadlocks, &info.Conflicts,
		&info.DeadlocksPerDay, &info.TempBytesPerDay)
	// PG14+ session columns; silently absent on older clusters.
	_ = pool.QueryRow(ctx, sqlMaintSessionStats).Scan(
		&info.Sessions, &info.SessAbandoned, &info.SessFatal, &info.SessKilled,
		&info.ActiveTimeMs, &info.IdleTxTimeMs)

	// --- table activity (pg_stat_user_tables, current DB) ---
	_ = pool.QueryRow(ctx, sqlMaintTableActivity).Scan(
		&info.TupInserted, &info.TupUpdated, &info.TupDeleted, &info.TupHotUpdated,
		&info.SeqScans, &info.IdxScans, &info.LiveTuples, &info.DeadTuples)

	// --- when the table/index counters above were last reset (current DB) ---
	var tblReset *time.Time
	if pool.QueryRow(ctx, sqlMaintTableStatsReset).Scan(&tblReset) == nil && tblReset != nil {
		info.TableStatsReset = *tblReset
	}

	// --- I/O stats (pg_stat_io): totals, then the by-backend-type split ---
	if pool.QueryRow(ctx, sqlMaintIO).Scan(
		&info.IO.Reads, &info.IO.Writes, &info.IO.Extends, &info.IO.Hits,
		&info.IO.Evictions, &info.IO.Fsyncs, &info.IO.BackendFsyncs) == nil {
		info.IO.HasData = true
	}
	sp := &info.IOSplit
	if pool.QueryRow(ctx, sqlMaintIOSplit).Scan(
		&sp.RelationReads, &sp.ClientReads, &sp.ClientReadTimeMs, &sp.BulkReads,
		&sp.VacuumReads, &sp.VacuumWrites,
		&sp.CheckpointerWrites, &sp.CheckpointerWriteTimeMs, &sp.BgwriterWrites,
		&sp.ClientWrites, &sp.ClientWriteTimeMs,
		&sp.WALWrites, &sp.WALWriteTimeMs, &sp.WALFsyncs, &sp.WALFsyncTimeMs) == nil {
		sp.HasData = true
	}

	// --- buffer cache occupancy (pg_buffercache; summary needs pg_monitor) ---
	if st, err := c.ProbeExtension(ctx, db, "pg_buffercache"); err == nil && st.Installed {
		info.BufCache.Installed = true
		if pool.QueryRow(ctx, sqlMaintBufSummary).Scan(
			&info.BufCache.Used, &info.BufCache.Unused, &info.BufCache.Dirty,
			&info.BufCache.Pinned, &info.BufCache.UsageAvg) == nil {
			info.BufCache.HasData = true
		}
		if counts, err := collect(ctx, pool, "buffer usage counts", sqlBufferUsageCounts, nil, scanBufferUsageCount); err == nil {
			info.BufCache.UsageCounts = counts
		}
	}

	// --- blocked queries ---
	info.Blocked = collectBestEffort(ctx, pool, sqlMaintBlocked, nil, func(rows pgx.Rows) (BlockedStat, bool) {
		var b BlockedStat
		return b, rows.Scan(&b.PID, &b.BlockedBy, &b.WaitSec, &b.Query) == nil
	})

	// --- prepared transactions ---
	_ = pool.QueryRow(ctx, sqlMaintPrepared).Scan(&info.PreparedXacts, &info.OldestPrepSec)

	// --- pg_stat_statements capacity ---
	info.Statements = c.statementsCapacity(ctx, db)

	// --- pg_qualstats capacity ---
	info.Qualstats = c.qualstatsCapacity(ctx, db)

	return info, nil
}

// parseMaintTuning converts the raw pg_settings values (base units) into the
// typed numbers the advice rules need. A missing or unparsable key leaves its
// field zero.
func parseMaintTuning(raw map[string]string) MaintTuning {
	i64 := func(k string) int64 {
		n, _ := strconv.ParseInt(raw[k], 10, 64)
		return n
	}
	f64 := func(k string) float64 {
		f, _ := strconv.ParseFloat(raw[k], 64)
		return f
	}
	slru := make(map[string]int64, len(slruBufferGUCs))
	for _, k := range slruBufferGUCs {
		if _, ok := raw[k]; ok {
			slru[k] = i64(k)
		}
	}
	return MaintTuning{
		SLRUBuffers:           slru,
		CheckpointTimeoutSecs: i64("checkpoint_timeout"),
		CheckpointCompletion:  f64("checkpoint_completion_target"),
		AutovacCostDelayMs:    f64("autovacuum_vacuum_cost_delay"),
		VacuumCostDelayMs:     f64("vacuum_cost_delay"),
		AutovacCostLimit:      i64("autovacuum_vacuum_cost_limit"),
		VacuumCostLimit:       i64("vacuum_cost_limit"),
		AutovacMaxWorkers:     int(i64("autovacuum_max_workers")),
		ShmemHugePages:        i64("shared_memory_size_in_huge_pages"),
		BgwriterLRUMaxpages:   i64("bgwriter_lru_maxpages"),
		IdleInTxnTimeoutMs:    i64("idle_in_transaction_session_timeout"),
	}
}

// ListSettings returns all pg_settings rows for the Settings browser.
// Rows are ordered by category then name so they can be scrolled / filtered.
func (c *Client) ListSettings(ctx context.Context, db string) ([]SettingRow, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list settings in %q: %w", db, err)
	}
	rows, err := pool.Query(ctx, sqlAllSettings)
	if err != nil {
		return nil, fmt.Errorf("list settings in %q: %w", db, err)
	}
	defer rows.Close()
	var out []SettingRow
	for rows.Next() {
		var r SettingRow
		if err := rows.Scan(&r.Name, &r.Setting, &r.Display, &r.Unit, &r.Category, &r.ShortDesc,
			&r.Context, &r.PendingRestart, &r.IsDefault); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// extCapacity reads the fill level of one statistics extension, returning an
// ExtCapacity with Installed=false when the extension is absent. sqlCount is
// the query for (used, max). The remaining queries are optional (skipped when
// ""); each requires elevated privilege (pg_read_all_stats or superuser) and is
// best-effort — a failure leaves the corresponding field zero:
//   - sqlReset returns one stats_reset timestamptz,
//   - sqlShmem returns the reserved shared-memory bytes,
//   - sqlText returns the query-text bytes.
func (c *Client) extCapacity(ctx context.Context, db, name, sqlCount, sqlReset, sqlShmem, sqlText string) ExtCapacity {
	cap := ExtCapacity{Name: name}
	st, err := c.ProbeExtension(ctx, db, name)
	if err != nil || !st.Installed {
		return cap
	}
	cap.Installed = true

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return cap
	}

	_ = pool.QueryRow(ctx, sqlCount).Scan(&cap.Used, &cap.Max)

	if sqlReset != "" {
		var statsReset time.Time
		if pool.QueryRow(ctx, sqlReset).Scan(&statsReset) == nil {
			if !statsReset.IsZero() && statsReset.Year() > 1 {
				cap.StatsReset = statsReset
			}
		}
	}
	if sqlShmem != "" {
		_ = pool.QueryRow(ctx, sqlShmem).Scan(&cap.ShmemBytes)
	}
	if sqlText != "" {
		_ = pool.QueryRow(ctx, sqlText).Scan(&cap.TextBytes)
	}
	return cap
}

// statementsCapacity reads the pg_stat_statements fill level.
// Returns an ExtCapacity with Installed=false when the extension is absent.
func (c *Client) statementsCapacity(ctx context.Context, db string) ExtCapacity {
	return c.extCapacity(ctx, db, "pg_stat_statements", sqlStatementsCount,
		sqlStatementsReset, sqlStatementsShmem, sqlStatementsTextBytes)
}

// qualstatsCapacity reads the pg_qualstats fill level.
// Returns an ExtCapacity with Installed=false when the extension is absent.
func (c *Client) qualstatsCapacity(ctx context.Context, db string) ExtCapacity {
	// pg_qualstats has no reset-info or per-entry text; only shmem is available.
	return c.extCapacity(ctx, db, "pg_qualstats", sqlQualstatsCapacity, "", sqlQualstatsShmem, "")
}

// resetExtStats runs resetSQL in db, wrapping any error with label.
// The two public Reset* methods share this body — they differ only in the
// SQL constant and the label used in error messages.
func (c *Client) resetExtStats(ctx context.Context, db, label, resetSQL string) error {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, resetSQL); err != nil {
		return fmt.Errorf("reset %s in %q: %w", label, db, err)
	}
	return nil
}

// ResetStatements runs pg_stat_statements_reset() in db.
// Requires pg_read_all_stats or superuser.
func (c *Client) ResetStatements(ctx context.Context, db string) error {
	return c.resetExtStats(ctx, db, "pg_stat_statements", sqlStatementsResetAll)
}

// ResetQualstats runs pg_qualstats_reset() in db.
// Requires superuser or pg_monitor depending on the qualstats version.
func (c *Client) ResetQualstats(ctx context.Context, db string) error {
	return c.resetExtStats(ctx, db, "pg_qualstats", sqlQualstatsResetAll)
}

// ResetTableStats runs pg_stat_reset() in db, zeroing the cumulative
// table/index/IO counters behind the Table overview for the whole database
// (and bumping its stats_reset timestamp). Requires pg_monitor or superuser.
// Unlike the extension resets, this targets a built-in view, so there is no
// extension to probe first.
func (c *Client) ResetTableStats(ctx context.Context, db string) error {
	return c.resetExtStats(ctx, db, "table statistics", sqlTableStatsResetAll)
}

// ResetTableStatsAllDBs runs pg_stat_reset() in every database the current user
// can connect to, zeroing the cumulative table/index/IO counters cluster-wide.
// pg_stat_reset() is per-database (it only touches current_database()), so the
// whole-cluster reset must visit each database in turn — mirroring the fan-out
// in RunDiagnosticAllDBs. A database that fails to connect or reset is skipped
// and its first error remembered; the sweep reports that error (with the
// succeeded/total tally) whenever any database failed, so a partial reset is
// never silently reported as a full success. Requires pg_monitor (or superuser)
// in each database.
func (c *Client) ResetTableStatsAllDBs(ctx context.Context) error {
	dbs, err := c.ListDatabases(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	var done int
	for _, db := range dbs {
		if rerr := c.resetExtStats(ctx, db.Name, "table statistics", sqlTableStatsResetAll); rerr != nil {
			if firstErr == nil {
				firstErr = rerr
			}
			continue
		}
		done++
	}
	if firstErr != nil {
		return fmt.Errorf("reset table statistics in %d/%d databases: %w", done, len(dbs), firstErr)
	}
	return nil
}
