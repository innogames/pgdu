package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderMaintServer renders the "server" section of the Maintenance dashboard.
func renderMaintServer(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	sel := styleSelected.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" server ") + "\n")
	if info == nil {
		return b.String()
	}
	version := info.Version
	if len(version) > 60 {
		if i := strings.Index(version, ","); i > 0 {
			version = version[:i]
		}
	}
	roleStr := lipgloss.NewStyle().Foreground(colorOK).Render("primary")
	if info.InRecovery {
		roleStr = styleErr.Render("standby (recovery)")
	}
	b.WriteString("  " + padRight(mu("version"), 22) + version + "  " + mu("(") + roleStr + mu(")") + "\n")
	if !info.StartTime.IsZero() {
		uptime := time.Since(info.StartTime)
		b.WriteString("  " + padRight(mu("uptime"), 22) + formatUptime(uptime) + "\n")
	}
	if !info.ConfLoad.IsZero() {
		confAge := time.Since(info.ConfLoad)
		b.WriteString("  " + padRight(mu("config reload"), 22) + relativeAge(confAge) + "\n")
	}
	total := 0
	for _, n := range info.ConnByState {
		total += n
	}
	active := info.ConnByState["active"]
	idle := info.ConnByState["idle"]
	idleTxn := info.ConnByState["idle in transaction"]
	maxStr := strconv.Itoa(info.MaxConns)
	connLine := fmt.Sprintf("%d/%s", total, maxStr)
	var connParts []string
	if active > 0 {
		connParts = append(connParts, sel(strconv.Itoa(active))+" active")
	}
	if idle > 0 {
		connParts = append(connParts, mu(fmt.Sprintf("%d idle", idle)))
	}
	if idleTxn > 0 {
		connParts = append(connParts, styleErr.Render(strconv.Itoa(idleTxn))+" idle-in-txn")
	}
	if len(connParts) > 0 {
		connLine += mu("  (") + strings.Join(connParts, mu("  ·  ")) + mu(")")
	}
	b.WriteString("  " + padRight(mu("connections"), 22) + connLine + "\n")
	if info.LongestXactSec > 0 {
		b.WriteString("  " + padRight(mu("longest xact"), 22) +
			maintDurationStyle(info.LongestXactSec).Render(fmtSecsDuration(info.LongestXactSec)) + "\n")
	}
	if info.Sessions > 0 {
		sessLine := formatRows(info.Sessions) + " total"
		var sessBad []string
		if info.SessAbandoned > 0 {
			sessBad = append(sessBad, styleErr.Render(formatRows(info.SessAbandoned))+" abandoned")
		}
		if info.SessFatal > 0 {
			sessBad = append(sessBad, styleErr.Render(formatRows(info.SessFatal))+" fatal")
		}
		if info.SessKilled > 0 {
			sessBad = append(sessBad, styleErr.Render(formatRows(info.SessKilled))+" killed")
		}
		if len(sessBad) > 0 {
			sessLine += "  " + strings.Join(sessBad, "  ")
		}
		b.WriteString("  " + padRight(mu("sessions"), 22) + sessLine + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintTransactions renders the "transactions" section.
func renderMaintTransactions(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" transactions ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString("  " + padRight(mu("cache hit %"), 22) +
		gradedPercentStyle(info.CacheHitRatio).Render(fmt1(info.CacheHitRatio)+"%") + "\n")
	if info.XactCommit+info.XactRollback > 0 {
		total := info.XactCommit + info.XactRollback
		rollPct := float64(info.XactRollback) / float64(total) * 100
		rollStyle := lipgloss.NewStyle().Foreground(colorOK)
		if rollPct >= 20 {
			rollStyle = styleErr
		} else if rollPct >= 5 {
			rollStyle = lipgloss.NewStyle().Foreground(colorAccent)
		}
		txnLine := fmt.Sprintf("%s commit  %s rollback",
			formatRows(info.XactCommit), formatRows(info.XactRollback))
		if rollPct >= 1 {
			txnLine += "  " + rollStyle.Render(fmt1(rollPct)+"% rollback ratio")
		}
		b.WriteString("  " + padRight(mu("transactions"), 22) + txnLine + "\n")
	}
	if info.Deadlocks > 0 {
		b.WriteString("  " + padRight(mu("deadlocks"), 22) +
			styleErr.Render(formatRows(info.Deadlocks)+" detected") + "\n")
	} else {
		b.WriteString("  " + padRight(mu("deadlocks"), 22) + mu("0") + "\n")
	}
	if info.Conflicts > 0 {
		b.WriteString("  " + padRight(mu("conflicts"), 22) +
			lipgloss.NewStyle().Foreground(colorAccent).Render(formatRows(info.Conflicts)) + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintTableActivity renders the "table activity" section: tuple-level
// write/scan counters aggregated across pg_stat_user_tables for the current
// database. Ratios are derived here from the raw counters.
func renderMaintTableActivity(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" table activity ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}

	if info.TupInserted+info.TupUpdated+info.TupDeleted > 0 {
		writes := fmt.Sprintf("%s ins  %s upd  %s del",
			formatRows(info.TupInserted), formatRows(info.TupUpdated), formatRows(info.TupDeleted))
		b.WriteString("  " + padRight(mu("writes"), 22) + writes + "\n")
	}

	// HOT updates are good: a high ratio means updates avoided index churn.
	if info.TupUpdated > 0 {
		hotPct := float64(info.TupHotUpdated) / float64(info.TupUpdated) * 100
		hotStyle := lipgloss.NewStyle().Foreground(colorOK)
		switch {
		case hotPct < 50:
			hotStyle = styleErr
		case hotPct < 80:
			hotStyle = lipgloss.NewStyle().Foreground(colorAccent)
		}
		b.WriteString("  " + padRight(mu("hot ratio"), 22) +
			hotStyle.Render(fmt1(hotPct)+"%") +
			"  " + mu(fmt.Sprintf("(%s of %s upd)", formatRows(info.TupHotUpdated), formatRows(info.TupUpdated))) + "\n")
	}

	// Index usage is good: high ratio means few seq scans relative to index scans.
	if info.SeqScans+info.IdxScans > 0 {
		idxPct := float64(info.IdxScans) / float64(info.SeqScans+info.IdxScans) * 100
		b.WriteString("  " + padRight(mu("index usage"), 22) +
			gradedPercentStyle(idxPct).Render(fmt1(idxPct)+"%") +
			"  " + mu(fmt.Sprintf("(%s idx / %s seq)", formatRows(info.IdxScans), formatRows(info.SeqScans))) + "\n")
	}

	// Dead tuples are bad: a high fraction signals bloat / vacuum lag.
	if info.LiveTuples+info.DeadTuples > 0 {
		deadPct := float64(info.DeadTuples) / float64(info.LiveTuples+info.DeadTuples) * 100
		deadStyle := lipgloss.NewStyle().Foreground(colorOK)
		switch {
		case deadPct >= 20:
			deadStyle = styleErr
		case deadPct >= 10:
			deadStyle = lipgloss.NewStyle().Foreground(colorAccent)
		}
		b.WriteString("  " + padRight(mu("dead tuples"), 22) +
			fmt.Sprintf("%s / %s  ", formatRows(info.DeadTuples), formatRows(info.LiveTuples+info.DeadTuples)) +
			deadStyle.Render(fmt1(deadPct)+"%") + "\n")
	}

	b.WriteString("\n")
	return b.String()
}

// renderMaintReplication renders the "replication & slots" section.
// Returns "" when there is no replication data to show.
func renderMaintReplication(info *pg.MaintenanceInfo) string {
	if info == nil || (len(info.Replicas) == 0 && len(info.ReplSlots) == 0 && info.WalReceiver == nil) {
		return ""
	}
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" replication & slots ") + "\n")
	if info.WalReceiver != nil {
		wr := info.WalReceiver
		wrLine := wr.Status
		if wr.LastMsgAge > 0 {
			wrLine += "  " + mu("last msg "+relativeAge(wr.LastMsgAge))
		}
		b.WriteString("  " + padRight(mu("wal receiver"), 22) + wrLine + "\n")
	}
	for _, r := range info.Replicas {
		lagStr := ""
		if r.ReplayLag > 0 {
			lagStr = "  lag " + fmtSecsDuration(r.ReplayLag.Seconds())
		}
		byteStr := ""
		if r.ByteLag > 0 {
			byteStr = "  " + mu(humanize.Bytes(r.ByteLag)+" behind")
		}
		syncMark := mu(r.SyncState)
		if r.SyncState == "sync" || r.SyncState == "quorum" {
			syncMark = lipgloss.NewStyle().Foreground(colorOK).Render(r.SyncState)
		}
		replLine := r.AppName + "  " + mu(r.ClientAddr) +
			"  " + r.State + "  " + syncMark + lagStr + byteStr
		b.WriteString("  " + padRight(mu("replica"), 22) + replLine + "\n")
	}
	for _, slot := range info.ReplSlots {
		activeStr := mu("inactive")
		if slot.Active {
			activeStr = lipgloss.NewStyle().Foreground(colorOK).Render("active")
		}
		retStr := ""
		if slot.RetainedBytes > 0 {
			retStr = "  " + humanize.Bytes(slot.RetainedBytes) + " retained"
		}
		statusStyle := mu(slot.WALStatus)
		if slot.WALStatus == "lost" || slot.WALStatus == "unreserved" {
			statusStyle = styleErr.Render(slot.WALStatus)
		} else if !slot.Active && slot.RetainedBytes > 1<<30 {
			// Inactive slot holding > 1 GB of WAL is a disk hazard.
			statusStyle = lipgloss.NewStyle().Foreground(colorAccent).Render(slot.WALStatus)
		}
		slotLine := slot.Name + "  " + mu(slot.SlotType) + "  " + activeStr + "  " + statusStyle + retStr
		b.WriteString("  " + padRight(mu("slot"), 22) + slotLine + "\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintPgBouncer renders the "pgbouncer" section.
// Returns "" when PgBouncer data is absent.
func renderMaintPgBouncer(info *pg.MaintenanceInfo) string {
	if info == nil || info.PgBouncer == nil {
		return ""
	}
	mu := styleMuted.Render
	pb := info.PgBouncer
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" pgbouncer ") + "\n")
	pbVer := pb.Version
	if i := strings.Index(pbVer, " on "); i > 0 {
		pbVer = pbVer[:i]
	}
	b.WriteString("  " + padRight(mu("version"), 22) + pbVer + "\n")
	waitStr := mu("max wait 0s")
	if pb.MaxWaitSec > 0 {
		waitStr = styleErr.Render("max wait " + fmtSecsDuration(pb.MaxWaitSec))
	}
	poolsLine := fmt.Sprintf("cl %d active  %d waiting  sv %d active  %d idle  %s",
		pb.ClActive, pb.ClWaiting, pb.SvActive, pb.SvIdle, waitStr)
	b.WriteString("  " + padRight(mu("pools"), 22) + poolsLine + "\n")
	shown := 0
	for _, p := range pb.Pools {
		if p.ClActive+p.ClWaiting+p.SvActive == 0 {
			continue
		}
		waitMark := ""
		if p.MaxWaitSec > 0 {
			waitMark = "  " + styleErr.Render("wait "+fmtSecsDuration(p.MaxWaitSec))
		}
		pLine := mu(fmt.Sprintf("%s/%s %s", p.Database, p.User, p.Mode)) +
			fmt.Sprintf("  cl %d/%d  sv %d/%d idle", p.ClActive, p.ClWaiting, p.SvActive, p.SvIdle) +
			waitMark
		b.WriteString("  " + padRight("", 22) + pLine + "\n")
		shown++
		if shown >= 5 {
			break
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintMemory renders the "memory & resources" section.
func renderMaintMemory(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" memory & resources ") + "\n")
	if info != nil {
		for _, guc := range []struct{ label, key string }{
			{"shared_buffers", "shared_buffers"},
			{"work_mem", "work_mem"},
			{"maintenance_work_mem", "maintenance_work_mem"},
			{"effective_cache_size", "effective_cache_size"},
			{"max_connections", "max_connections"},
		} {
			v, ok := info.Settings[guc.key]
			if !ok {
				v = mu("unknown")
			}
			b.WriteString("  " + padRight(mu(guc.label), 24) + v + "\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintAutovacuum renders the "autovacuum & wraparound" section.
func renderMaintAutovacuum(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" autovacuum & wraparound ") + "\n")
	if info != nil {
		for _, guc := range []struct{ label, key string }{
			{"autovacuum", "autovacuum"},
			{"max_workers", "autovacuum_max_workers"},
			{"naptime", "autovacuum_naptime"},
			{"freeze_max_age", "autovacuum_freeze_max_age"},
		} {
			v, ok := info.Settings[guc.key]
			if !ok {
				v = mu("unknown")
			}
			b.WriteString("  " + padRight(mu(guc.label), 24) + v + "\n")
		}
		if info.XidAge > 0 && info.FreezeMaxAge > 0 {
			pct := float64(info.XidAge) / float64(info.FreezeMaxAge) * 100
			pctStr := fmt1(pct) + "%"
			var wrapStyle lipgloss.Style
			switch {
			case pct >= 80:
				wrapStyle = styleErr
			case pct >= 50:
				wrapStyle = lipgloss.NewStyle().Foreground(colorAccent)
			default:
				wrapStyle = lipgloss.NewStyle().Foreground(colorOK)
			}
			b.WriteString("  " + padRight(mu("xid age"), 24) +
				fmt.Sprintf("%s / %s  ", formatRows(info.XidAge), formatRows(info.FreezeMaxAge)) +
				wrapStyle.Render(pctStr) + "\n")
		} else if info.XidAge > 0 {
			b.WriteString("  " + padRight(mu("xid age"), 24) + formatRows(info.XidAge) + "\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintWAL renders the "wal & checkpoints" section.
func renderMaintWAL(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" wal & checkpoints ") + "\n")
	if info != nil {
		for _, guc := range []struct{ label, key string }{
			{"wal_level", "wal_level"},
			{"max_wal_size", "max_wal_size"},
			{"min_wal_size", "min_wal_size"},
			{"checkpoint_timeout", "checkpoint_timeout"},
		} {
			v, ok := info.Settings[guc.key]
			if !ok {
				v = mu("unknown")
			}
			b.WriteString("  " + padRight(mu(guc.label), 24) + v + "\n")
		}
		if info.CheckpointsTimed+info.CheckpointsReq > 0 {
			total := info.CheckpointsTimed + info.CheckpointsReq
			reqPct := float64(info.CheckpointsReq) / float64(total) * 100
			reqStyle := lipgloss.NewStyle().Foreground(colorOK)
			if reqPct >= 50 {
				reqStyle = styleErr
			} else if reqPct >= 20 {
				reqStyle = lipgloss.NewStyle().Foreground(colorAccent)
			}
			cpLine := fmt.Sprintf("%s timed  %s requested",
				formatRows(info.CheckpointsTimed), formatRows(info.CheckpointsReq))
			if reqPct >= 10 {
				cpLine += "  " + reqStyle.Render(fmt1(reqPct)+"% requested — may need larger max_wal_size")
			}
			b.WriteString("  " + padRight(mu("checkpoints"), 24) + cpLine + "\n")
		} else {
			b.WriteString("  " + padRight(mu("checkpoints"), 24) + mu("no data") + "\n")
		}
		if info.WALMaxBytes > 0 {
			ratio := float64(info.WALBytesSinceCheckpoint) / float64(info.WALMaxBytes)
			if ratio > 1 {
				ratio = 1
			}
			var walBarStyle lipgloss.Style
			switch {
			case ratio >= 0.80:
				walBarStyle = styleErr
			case ratio >= 0.50:
				walBarStyle = lipgloss.NewStyle().Foreground(colorAccent)
			default:
				walBarStyle = lipgloss.NewStyle().Foreground(colorOK)
			}
			barW := 20
			filled := min(int(float64(barW)*ratio), barW)
			bar := paintBar(barW, barSegment{cells: filled, style: walBarStyle})
			pctStr := walBarStyle.Render(fmt1(ratio*100) + "%")
			detail := fmt.Sprintf("%s / %s  %s",
				humanize.Bytes(info.WALBytesSinceCheckpoint),
				humanize.Bytes(info.WALMaxBytes),
				pctStr)
			if !info.WALCheckpointTime.IsZero() {
				detail += "  " + mu("last checkpoint "+relativeAge(time.Since(info.WALCheckpointTime)))
			}
			b.WriteString("  " + padRight(mu("since checkpoint"), 24) + bar + "  " + detail + "\n")
		}
		if info.WALBuffersFull > 0 {
			b.WriteString("  " + padRight(mu("wal_buffers stalls"), 24) +
				styleErr.Render(formatRows(info.WALBuffersFull)+" times") +
				mu("  — increase wal_buffers (or set to -1 for auto)") + "\n")
		}
		if t, ok := info.Settings["pg_stat_statements.track"]; ok {
			_ = t
			b.WriteString("\n  " + styleHeader.Render(" pg_stat_statements settings ") + "\n")
			for _, guc := range []struct{ label, key string }{
				{"track", "pg_stat_statements.track"},
				{"track_planning", "pg_stat_statements.track_planning"},
				{"max", "pg_stat_statements.max"},
			} {
				v, ok2 := info.Settings[guc.key]
				if !ok2 {
					v = mu("unknown")
				}
				b.WriteString("  " + padRight(mu(guc.label), 24) + v + "\n")
			}
		}
		if _, ok := info.Settings["pg_qualstats.max"]; ok {
			b.WriteString("\n  " + styleHeader.Render(" pg_qualstats settings ") + "\n")
			for _, guc := range []struct{ label, key string }{
				{"enabled", "pg_qualstats.enabled"},
				{"max", "pg_qualstats.max"},
				{"sample_rate", "pg_qualstats.sample_rate"},
				{"track_constants", "pg_qualstats.track_constants"},
			} {
				v, ok2 := info.Settings[guc.key]
				if !ok2 {
					v = mu("unknown")
				}
				b.WriteString("  " + padRight(mu(guc.label), 24) + v + "\n")
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintIO renders the "i/o" section.
// Returns "" when pg_stat_io data is unavailable (PG < 16).
func renderMaintIO(info *pg.MaintenanceInfo) string {
	if info == nil || !info.IO.HasData {
		return ""
	}
	mu := styleMuted.Render
	io := info.IO
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" i/o ") + "\n")
	b.WriteString("  " + padRight(mu("reads"), 24) + formatRows(io.Reads) +
		"  " + mu("hits ") + formatRows(io.Hits) + "\n")
	b.WriteString("  " + padRight(mu("writes"), 24) + formatRows(io.Writes) +
		"  " + mu("extends ") + formatRows(io.Extends) + "\n")
	b.WriteString("  " + padRight(mu("evictions"), 24) + formatRows(io.Evictions) + "\n")
	fsyncsLine := formatRows(io.Fsyncs)
	if io.BackendFsyncs > 0 {
		fsyncsLine += "  " + styleErr.Render(formatRows(io.BackendFsyncs)+" by backends") +
			mu("  — checkpointer can't keep up")
	}
	b.WriteString("  " + padRight(mu("fsyncs"), 24) + fsyncsLine + "\n")
	b.WriteString("\n")
	return b.String()
}

// renderMaintHealth renders the "operational health" section.
func renderMaintHealth(info *pg.MaintenanceInfo) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" operational health ") + "\n")
	if info != nil {
		restartStr := mu("0 need restart")
		if info.PendingRestart > 0 {
			restartStr = styleErr.Render(fmt.Sprintf("%d need restart", info.PendingRestart))
			if len(info.PendingRestartSettings) > 0 {
				restartStr += mu("  (" + strings.Join(info.PendingRestartSettings, ", ") + ")")
			}
		}
		reloadStr := mu("0 need reload")
		if info.PendingReload > 0 {
			reloadStr = lipgloss.NewStyle().Foreground(colorAccent).Render(fmt.Sprintf("%d need reload", info.PendingReload))
			if len(info.PendingReloadSettings) > 0 {
				reloadStr += mu("  (" + strings.Join(info.PendingReloadSettings, ", ") + ")")
			}
		}
		b.WriteString("  " + padRight(mu("pending config"), 24) + restartStr + "\n")
		b.WriteString("  " + padRight("", 24) + reloadStr + "\n")

		lockStr := mu("0 waiting")
		if info.LockWaits > 0 {
			lockStr = styleErr.Render(fmt.Sprintf("%d waiting", info.LockWaits))
		}
		b.WriteString("  " + padRight(mu("lock waits"), 24) + lockStr + "\n")

		for _, bl := range info.Blocked {
			blockers := make([]string, len(bl.BlockedBy))
			for i, pid := range bl.BlockedBy {
				blockers[i] = strconv.Itoa(int(pid))
			}
			blLine := fmt.Sprintf("pid %d blocked by %s  %s  %s",
				bl.PID,
				strings.Join(blockers, ","),
				fmtSecsDuration(bl.WaitSec),
				mu(bl.Query))
			b.WriteString("  " + padRight("", 24) + styleErr.Render("▸ ") + blLine + "\n")
		}

		if info.PreparedXacts > 0 {
			prepLine := styleErr.Render(fmt.Sprintf("%d prepared xact(s)", info.PreparedXacts))
			if info.OldestPrepSec > 0 {
				prepLine += mu("  oldest: "+fmtSecsDuration(info.OldestPrepSec)) +
					"  " + mu("— may pin xmin horizon")
			}
			b.WriteString("  " + padRight(mu("prepared xacts"), 24) + prepLine + "\n")
		}

		if info.TempFiles > 0 {
			b.WriteString("  " + padRight(mu("temp files"), 24) +
				fmt.Sprintf("%s files  %s", formatRows(info.TempFiles), humanize.Bytes(info.TempBytes)) + "\n")
			for _, t := range info.TempByDB {
				fileWord := "files"
				if t.Files == 1 {
					fileWord = "file"
				}
				b.WriteString("  " + padRight("", 24) +
					mu(fmt.Sprintf("  %s:  %s %s  %s", t.DB, formatRows(t.Files), fileWord, humanize.Bytes(t.Bytes))) + "\n")
			}
		} else {
			b.WriteString("  " + padRight(mu("temp files"), 24) + mu("none") + "\n")
		}

		// Background writer pressure: shown only when pg_stat_io data is absent
		// (PG < 16). On PG16+ the I/O section above already covers this.
		if !info.IO.HasData && info.BgwBuffersAlloc > 0 {
			backendPct := float64(info.BgwBuffersBackend) / float64(info.BgwBuffersAlloc) * 100
			bgwStyle := lipgloss.NewStyle().Foreground(colorOK)
			if backendPct >= 50 {
				bgwStyle = styleErr
			} else if backendPct >= 20 {
				bgwStyle = lipgloss.NewStyle().Foreground(colorAccent)
			}
			bgwLine := fmt.Sprintf("%s backend-written / %s alloc",
				formatRows(info.BgwBuffersBackend), formatRows(info.BgwBuffersAlloc))
			if backendPct >= 5 {
				bgwLine += "  " + bgwStyle.Render(fmt1(backendPct)+"% direct — tune bgwriter or max_wal_size")
			}
			b.WriteString("  " + padRight(mu("bgwriter"), 24) + bgwLine + "\n")
		}

		if info.ArchiveFailed > 0 {
			b.WriteString("  " + padRight(mu("wal archiver"), 24) +
				styleErr.Render(fmt.Sprintf("%s archived  %s failed", formatRows(info.ArchiveCount), formatRows(info.ArchiveFailed))))
			if info.ArchiveLastFailed != "" {
				b.WriteString(mu("  last: " + info.ArchiveLastFailed))
			}
			b.WriteString("\n")
		} else if info.ArchiveCount > 0 {
			archiveAge := ""
			if !info.ArchiveLastTime.IsZero() && info.ArchiveLastTime.Year() > 1 {
				archiveAge = "  " + mu("last "+relativeAge(time.Since(info.ArchiveLastTime)))
			}
			b.WriteString("  " + padRight(mu("wal archiver"), 24) +
				lipgloss.NewStyle().Foreground(colorOK).Render(formatRows(info.ArchiveCount)+" archived") + archiveAge + "\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}
