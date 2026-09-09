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

// overviewLabelW is the label column of every system-overview row.
const overviewLabelW = 24

// maintView bundles what every overview section renders from: the snapshot,
// the two-sample rates against the previous snapshot, and the advice derived
// from it. Sections pull their inline coloured notes out of advice by key, so
// a note next to a metric and the line in the recommendations panel are the
// same value and can never disagree.
type maintView struct {
	info   *pg.MaintenanceInfo
	rates  pg.MaintRates
	advice pg.AdviceSet
}

func newMaintView(s *screen) maintView {
	v := maintView{info: s.maintenance.info}
	if v.info != nil {
		v.rates = pg.ComputeMaintRates(s.maintenance.prev, v.info)
		v.advice = pg.MaintAdvice(v.info)
	}
	return v
}

// note returns the advice reason for key as a coloured inline note ("  reason"),
// or "" when no advice fired for it.
func (v maintView) note(key string) string {
	a := v.advice.Find(key)
	if a == nil {
		return ""
	}
	return "  " + adviceStyle(a.Level).Render(a.Reason)
}

// adviceStyle maps an advice level onto the overview's colour scale: red for
// critical, accent for warnings, muted for informational notes.
func adviceStyle(l pg.AdviceLevel) lipgloss.Style {
	switch l {
	case pg.AdviceCrit:
		return styleErr
	case pg.AdviceWarn:
		return lipgloss.NewStyle().Foreground(colorAccent)
	default:
		return styleMuted
	}
}

// maintRow is the one row shape of the overview: muted label column, value,
// optional trailing note already styled by the caller.
func maintRow(label, value string) string {
	return "  " + padRight(styleMuted.Render(label), overviewLabelW) + value + "\n"
}

// gucRow renders a setting row straight from the Settings map.
func gucRow(settings map[string]string, label, key, note string) string {
	return maintRow(label, settingOr(settings, key)+note)
}

// counterLegend labels a section's cumulative counters with the window they
// cover and, once two samples exist, the window the /min rates cover — so a
// lifetime total is never mistaken for current behaviour.
func (v maintView) counterLegend(reset time.Time) string {
	mu := styleMuted.Render
	parts := []string{"counters " + sinceResetLabel(reset, v.info.StartTime)}
	if v.rates.OK {
		parts = append(parts, "rates over the last "+shortDuration(v.rates.Window))
	}
	return "  " + mu(strings.Join(parts, "  ·  ")) + "\n"
}

// sinceResetLabel says how long a cumulative counter has been accumulating.
func sinceResetLabel(reset, start time.Time) string {
	switch {
	case !reset.IsZero():
		return "since reset " + relativeAge(time.Since(reset))
	case !start.IsZero():
		return "since server start " + relativeAge(time.Since(start))
	default:
		return "cumulative"
	}
}

// rateSuffix appends "  ·  1.2k/min" to a counter when a rate is known.
func (v maintView) rateSuffix(perMin float64, fmtFn func(int64) string) string {
	if !v.rates.OK {
		return ""
	}
	return "  " + styleMuted.Render("·  "+fmtFn(int64(perMin))+"/min")
}

// renderMaintServer renders the "server" section: identity, uptime,
// connections and the session-hygiene extremes.
func renderMaintServer(v maintView) string {
	mu := styleMuted.Render
	sel := styleSelected.Render
	info := v.info
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
	b.WriteString(maintRow("version", version+"  "+mu("(")+roleStr+mu(")")))
	if !info.StartTime.IsZero() {
		b.WriteString(maintRow("uptime", formatUptime(time.Since(info.StartTime))))
	}
	if !info.ConfLoad.IsZero() {
		b.WriteString(maintRow("config reload", relativeAge(time.Since(info.ConfLoad))))
	}
	total := info.TotalConns()
	active := info.ConnByState["active"]
	idle := info.ConnByState["idle"]
	idleTxn := info.ConnByState["idle in transaction"]
	connLine := fmt.Sprintf("%d/%d", total, info.MaxConns)
	if info.MaxConns > 0 {
		pct := 100 * float64(total) / float64(info.MaxConns)
		connLine += "  " + gradeStyle(pct, 80, 95).Render(fmt.Sprintf("%.0f%%", pct))
	}
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
	b.WriteString(maintRow("connections", connLine))
	if info.LongestXactSec > 0 {
		b.WriteString(maintRow("longest xact",
			maintDurationStyle(info.LongestXactSec).Render(fmtSecsDuration(info.LongestXactSec))))
	}
	// The idle-in-txn row carries pid/app itself; the advice reason repeats
	// them for the panel, so it is not appended here.
	if s := info.Sess; s.IdleXactPID > 0 {
		b.WriteString(maintRow("idle in txn",
			gradeStyle(s.IdleXactSecs, pg.IdleXactWarnSecs, pg.IdleXactCritSecs).Render(fmtSecsDuration(s.IdleXactSecs))+
				"  "+mu(pidApp(s.IdleXactPID, s.IdleXactApp))))
	}
	if s := info.Sess; s.LongQueryPID > 0 {
		line := gradeStyle(s.LongQuerySecs, pg.LongQueryWarnSecs, 10*pg.LongQueryWarnSecs).Render(fmtSecsDuration(s.LongQuerySecs)) +
			"  " + mu(pidApp(s.LongQueryPID, s.LongQueryApp))
		if s.LongQueryText != "" {
			line += "  " + mu(oneLineQuery(s.LongQueryText))
		}
		b.WriteString(maintRow("longest query", line))
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
		b.WriteString(maintRow("sessions", sessLine))
	}
	b.WriteString("\n")
	return b.String()
}

// avgMsTUI is time/count with an ok flag, for the timed pg_stat_io counters.
func avgMsTUI(timeMs float64, n int64) (float64, bool) {
	if n <= 0 || timeMs <= 0 {
		return 0, false
	}
	return timeMs / float64(n), true
}

// pidApp formats "pid 42 (app)" for the session rows.
func pidApp(pid int32, app string) string {
	if app == "" {
		return fmt.Sprintf("pid %d", pid)
	}
	return fmt.Sprintf("pid %d (%s)", pid, app)
}

// oneLineQuery collapses a query's whitespace so it fits on the row.
func oneLineQuery(q string) string {
	return strings.Join(strings.Fields(q), " ")
}

// renderMaintTransactions renders the "transactions" section.
func renderMaintTransactions(v maintView) string {
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" transactions ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString(maintRow("cache hit %", gradedPercentStyle(info.CacheHitRatio).Render(fmt1(info.CacheHitRatio)+"%")))
	if info.XactCommit+info.XactRollback > 0 {
		total := info.XactCommit + info.XactRollback
		rollPct := float64(info.XactRollback) / float64(total) * 100
		// Two decimals below 1 % so a healthy ratio reads 0.07 %, not 0.0 %;
		// a handful of rollbacks in billions of commits is "<0.01 %", not zero.
		pctStr := fmt1(rollPct)
		switch {
		case rollPct > 0 && rollPct < 0.01:
			pctStr = "<0.01"
		case rollPct < 1:
			pctStr = strconv.FormatFloat(rollPct, 'f', 2, 64)
		}
		txnLine := fmt.Sprintf("%s commit  %s rollback  ", formatRows(info.XactCommit), formatRows(info.XactRollback)) +
			gradeStyle(rollPct, 5, 20).Render(pctStr+"% rollback")
		b.WriteString(maintRow("transactions", txnLine))
	}
	if info.Deadlocks > 0 {
		b.WriteString(maintRow("deadlocks", styleErr.Render(formatRows(info.Deadlocks)+" detected")))
	} else {
		b.WriteString(maintRow("deadlocks", lipgloss.NewStyle().Foreground(colorOK).Render("0")))
	}
	if info.Conflicts > 0 {
		b.WriteString(maintRow("conflicts", lipgloss.NewStyle().Foreground(colorAccent).Render(formatRows(info.Conflicts))))
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintTableActivity renders the "table activity" section: tuple-level
// write/scan counters aggregated across pg_stat_user_tables for the current
// database. Ratios are derived here from the raw counters.
func renderMaintTableActivity(v maintView) string {
	mu := styleMuted.Render
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" table activity ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}

	// Freshly reset counters make every ratio below look alarming or perfect
	// for no reason; say so before the numbers.
	if !info.TableStatsReset.IsZero() && time.Since(info.TableStatsReset) < pg.StatsFreshWindow {
		b.WriteString("  " + mu("counters reset "+relativeAge(time.Since(info.TableStatsReset))+
			" — dead tuples / index usage not yet meaningful") + "\n")
	}

	if info.TupInserted+info.TupUpdated+info.TupDeleted > 0 {
		b.WriteString(maintRow("writes", fmt.Sprintf("%s ins  %s upd  %s del",
			formatRows(info.TupInserted), formatRows(info.TupUpdated), formatRows(info.TupDeleted))))
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
		b.WriteString(maintRow("hot ratio", hotStyle.Render(fmt1(hotPct)+"%")+
			"  "+mu(fmt.Sprintf("(%s of %s upd)", formatRows(info.TupHotUpdated), formatRows(info.TupUpdated)))))
	}

	// Index usage is good: high ratio means few seq scans relative to index scans.
	if info.SeqScans+info.IdxScans > 0 {
		idxPct := float64(info.IdxScans) / float64(info.SeqScans+info.IdxScans) * 100
		b.WriteString(maintRow("index usage", gradedPercentStyle(idxPct).Render(fmt1(idxPct)+"%")+
			"  "+mu(fmt.Sprintf("(%s idx / %s seq)", formatRows(info.IdxScans), formatRows(info.SeqScans)))))
	}

	// Dead tuples are bad: a high fraction signals bloat / vacuum lag.
	if info.LiveTuples+info.DeadTuples > 0 {
		deadPct := float64(info.DeadTuples) / float64(info.LiveTuples+info.DeadTuples) * 100
		b.WriteString(maintRow("dead tuples",
			fmt.Sprintf("%s / %s  ", formatRows(info.DeadTuples), formatRows(info.LiveTuples+info.DeadTuples))+
				gradeStyle(deadPct, 10, 20).Render(fmt1(deadPct)+"%")))
	}

	b.WriteString("\n")
	return b.String()
}

// renderMaintReplication renders the "replication & slots" section.
// Returns "" when there is no replication data to show.
func renderMaintReplication(v maintView) string {
	info := v.info
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
		b.WriteString(maintRow("wal receiver", wrLine))
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
		b.WriteString(maintRow("replica", r.AppName+"  "+mu(r.ClientAddr)+"  "+r.State+"  "+syncMark+lagStr+byteStr))
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
		b.WriteString(maintRow("slot", slot.Name+"  "+mu(slot.SlotType)+"  "+activeStr+"  "+statusStyle+retStr))
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintMemory renders the "memory & resources" section: the sizing GUCs
// put against the host they run on (when pgdu runs there).
func renderMaintMemory(v maintView) string {
	mu := styleMuted.Render
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" memory & resources ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	host := info.Host
	set := info.Settings

	// shared_buffers: the advice note (outside the 15–40 % band) replaces the
	// plain share so the percentage isn't printed twice.
	sbNote := v.note("shared_buffers")
	if sb := info.SettingBytes["shared_buffers"]; sbNote == "" && host.Total > 0 && sb > 0 {
		sbNote = "  " + mu(fmt.Sprintf("%.0f%% of host RAM", 100*float64(sb)/float64(host.Total)))
	}
	b.WriteString(gucRow(set, "shared_buffers", "shared_buffers", sbNote))

	wmNote := v.note("work_mem")
	if wm := info.SettingBytes["work_mem"]; wmNote == "" && wm > 0 && info.MaxConns > 0 {
		wmNote = "  " + mu(fmt.Sprintf("× %d conns = %s", info.MaxConns, humanize.Bytes(wm*int64(info.MaxConns))))
	}
	b.WriteString(gucRow(set, "work_mem", "work_mem", wmNote))
	b.WriteString(gucRow(set, "maintenance_work_mem", "maintenance_work_mem", ""))

	avwm := settingOr(set, "autovacuum_work_mem")
	if eff, ok := info.EffectiveAutovacWorkMem(); ok && info.SettingBytes["autovacuum_work_mem"] < 0 {
		avwm += "  " + mu("→ "+humanize.Bytes(eff)+" (maintenance_work_mem)")
	}
	b.WriteString(maintRow("autovacuum_work_mem", avwm))

	ecsNote := v.note("effective_cache_size")
	if sb := info.SettingBytes["shared_buffers"]; ecsNote == "" && host.Cached > 0 && sb > 0 {
		ecsNote = "  " + mu("host cache ~"+humanize.Bytes(sb+host.Cached))
	}
	b.WriteString(gucRow(set, "effective_cache_size", "effective_cache_size", ecsNote))
	inUse := fmt.Sprintf("%d in use", info.TotalConns())
	if info.MaxConns > 0 {
		inUse += fmt.Sprintf(" (%.0f%%)", 100*float64(info.TotalConns())/float64(info.MaxConns))
	}
	b.WriteString(gucRow(set, "max_connections", "max_connections", "  "+mu(inUse)))

	// Huge pages: the setting alone says nothing — "try" silently falls back
	// when the kernel pool is empty, which only the host can tell.
	hp := settingOr(set, "huge_pages")
	if host.Total > 0 && (hp == "try" || hp == "on") {
		if host.HugePagesTotal > 0 {
			hp += "  " + mu(fmt.Sprintf("host pool %d × %s, %d free", host.HugePagesTotal,
				humanize.Bytes(host.HugePageSize), host.HugePagesFree))
		}
		if n := info.Tuning.ShmemHugePages; n > 0 && v.advice.Find("huge_pages") == nil {
			hp += "  " + mu(fmt.Sprintf("needs %d", n))
		}
	}
	b.WriteString(maintRow("huge_pages", hp+v.note("huge_pages")))

	if host.Total > 0 {
		b.WriteString(maintRow("host memory", fmt.Sprintf("%s total  %s available  %s page cache",
			humanize.Bytes(host.Total), humanize.Bytes(host.Available), humanize.Bytes(host.Cached))))
		if host.SwapTotal == 0 {
			b.WriteString(maintRow("swap", mu("none")))
		} else {
			b.WriteString(maintRow("swap", fmt.Sprintf("%s used / %s", humanize.Bytes(host.SwapUsed()),
				humanize.Bytes(host.SwapTotal))+v.note("swap")))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintAutovacuum renders the "autovacuum & wraparound" section.
func renderMaintAutovacuum(v maintView) string {
	mu := styleMuted.Render
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" autovacuum & wraparound ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	set := info.Settings
	// autovacuum = on is the only sane state and not worth a row; off is.
	if v := set["autovacuum"]; v != "" && v != "on" {
		b.WriteString(maintRow("autovacuum", styleErr.Render(v)))
	}

	workers := fmt.Sprintf("%d / %s busy", info.Autovac.WorkersBusy, settingOr(set, "autovacuum_max_workers"))
	if maxW := info.Tuning.AutovacMaxWorkers; maxW > 0 && info.Autovac.WorkersBusy >= maxW {
		workers = lipgloss.NewStyle().Foreground(colorAccent).Render(workers) + "  " + mu("all workers busy right now")
	}
	b.WriteString(maintRow("workers", workers))
	b.WriteString(gucRow(set, "naptime", "autovacuum_naptime", ""))
	// -1 means "inherit the plain VACUUM setting"; spell out what that is.
	costDelay := settingOr(set, "autovacuum_vacuum_cost_delay")
	if info.Tuning.AutovacCostDelayMs < 0 {
		costDelay += "  " + mu(fmt.Sprintf("→ %gms (vacuum_cost_delay)", info.Tuning.VacuumCostDelayMs))
	}
	b.WriteString(maintRow("cost_delay", costDelay))
	costLimit := settingOr(set, "autovacuum_vacuum_cost_limit")
	if info.Tuning.AutovacCostLimit < 0 {
		costLimit += "  " + mu(fmt.Sprintf("→ %d (vacuum_cost_limit)", info.Tuning.VacuumCostLimit))
	}
	b.WriteString(maintRow("cost_limit", costLimit+v.note("autovacuum_cost")))

	over := mu("0 tables")
	if n := info.Autovac.OverThreshold; n > 0 {
		over = formatRows(n) + " tables"
		if n == 1 {
			over = "1 table"
		}
		if note := v.note("autovacuum_backlog"); note != "" {
			over += note
		} else if len(info.Autovac.OverTop) > 0 {
			over += "  " + mu(strings.Join(info.Autovac.OverTop, ", "))
		}
	}
	b.WriteString(maintRow("over threshold", over))

	b.WriteString(gucRow(set, "freeze_max_age", "autovacuum_freeze_max_age", ""))
	b.WriteString(gucRow(set, "mxid_freeze_max_age", "autovacuum_multixact_freeze_max_age", ""))
	b.WriteString(freezeAgeLine("xid age", info.XidAge, info.FreezeMaxAge, info.XidAgeDB))
	b.WriteString(freezeAgeLine("mxid age", info.MxidAge, info.MxidFreezeMaxAge, info.MxidAgeDB))
	b.WriteString("\n")
	return b.String()
}

// freezeAgeLine renders one "<label>  age / max  pct%  (db)" overview line,
// colouring the percentage by how close the counter is to a forced
// anti-wraparound autovacuum and naming the database that holds the oldest
// horizon (template0/postgres often turn out to be the culprit). Empty when
// the age is unknown; bare age when the max is.
func freezeAgeLine(label string, age, maxAge int64, db string) string {
	mu := styleMuted.Render
	dbStr := ""
	if db != "" {
		dbStr = "  " + mu("in "+db)
	}
	switch {
	case age <= 0:
		return ""
	case maxAge <= 0:
		return maintRow(label, formatRows(age)+dbStr)
	}
	pct := float64(age) / float64(maxAge) * 100
	return maintRow(label, fmt.Sprintf("%s / %s  ", formatRows(age), formatRows(maxAge))+
		gradeStyle(pct, 50, 80).Render(fmt1(pct)+"%")+dbStr)
}

// renderMaintBufferCache renders the "buffer cache" section: shared_buffers
// occupancy and temperature (pg_buffercache), then the pg_stat_io counters
// with their interpretation. barW is the temperature bar width.
func renderMaintBufferCache(v maintView, barW int) string {
	mu := styleMuted.Render
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" buffer cache ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	bc := info.BufCache
	switch {
	case !bc.Installed:
		b.WriteString(maintRow("pg_buffercache", mu("not installed — needed for cache analysis")))
	case !bc.HasData:
		b.WriteString(maintRow("occupancy", mu("n/a (needs pg_monitor)")))
	default:
		total := bc.Used + bc.Unused
		occ := fmt.Sprintf("%s / %s buffers", formatRows(bc.Used), formatRows(total))
		if total > 0 {
			occ += "  " + mu(fmt.Sprintf("%.0f%% used · avg usage %.1f", 100*float64(bc.Used)/float64(total), bc.UsageAvg))
		}
		b.WriteString(maintRow("occupancy", occ))
		dirty := formatRows(bc.Dirty)
		if bc.Used > 0 {
			pct := fmt1(bc.DirtyFrac()*100) + "%"
			if note := v.note("buffercache_dirty"); note != "" {
				dirty += "  " + adviceStyle(pg.AdviceWarn).Render(pct) + note
			} else {
				dirty += "  " + mu(pct)
			}
		}
		if bc.Pinned > 0 {
			dirty += "  " + mu(fmt.Sprintf("· %s pinned", formatRows(bc.Pinned)))
		}
		b.WriteString(maintRow("dirty", dirty))
	}
	if len(bc.UsageCounts) > 0 {
		b.WriteString(maintRow("temperature", renderUsageHeatBar(bc.UsageCounts, barW)+"  "+mu("cold → hot")))
		if note := v.note("buffercache_tight") + v.note("buffercache_slack"); note != "" {
			b.WriteString("  " + padRight("", overviewLabelW) + strings.TrimLeft(note, " ") + "\n")
		}
	}

	io := info.IO
	if !io.HasData {
		b.WriteString(maintRow("i/o", mu("n/a (pg_stat_io unreadable)")))
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString(v.counterLegend(time.Time{}))
	b.WriteString(maintRow("reads", formatRows(io.Reads)+"  "+mu("hits ")+formatRows(io.Hits)+
		v.rateSuffix(v.rates.ReadsPerMin, formatRows)))
	b.WriteString(maintRow("writes", formatRows(io.Writes)+"  "+mu("extends ")+formatRows(io.Extends)+
		v.rateSuffix(v.rates.WritesPerMin, formatRows)))
	ev := formatRows(io.Evictions)
	if io.Hits > 0 {
		ev += "  " + mu(fmt.Sprintf("%.1f%% of hits", 100*float64(io.Evictions)/float64(io.Hits)))
	}
	b.WriteString(maintRow("evictions", ev+v.rateSuffix(v.rates.EvictionsPerMin, formatRows)))

	if sp := info.IOSplit; sp.HasData {
		switch lat, ok := sp.ClientReadLatencyMs(); {
		case ok:
			hint := ""
			switch {
			case lat < 0.3:
				hint = "misses served from the OS page cache"
			case lat > 5:
				hint = "misses hitting disk"
			}
			line := fmt.Sprintf("%.2f ms", lat)
			if hint != "" {
				line += "  " + mu(hint)
			}
			b.WriteString(maintRow("read latency", line))
		case info.Settings["track_io_timing"] == "off":
			b.WriteString(maintRow("read latency", mu("n/a — track_io_timing is off")))
		default:
			b.WriteString(maintRow("read latency", mu("no timed reads yet")))
		}
		if sp.RelationReads > 0 {
			pct := 100 * float64(sp.BulkReads) / float64(sp.RelationReads)
			line := fmt1(pct) + "%"
			if pct > 30 {
				line += "  " + mu("large sequential scans (ring buffer) — check for missing indexes")
			}
			b.WriteString(maintRow("bulkread share", line))
		}
		if r, w, ok := sp.VacuumFracs(); ok {
			b.WriteString(maintRow("vacuum share", fmt1(r*100)+"% of reads  "+fmt1(w*100)+"% of writes"))
		}
		// Write latencies: the checkpointer's is the storage's paced write
		// speed; a backend's is time a query lost evicting a dirty page.
		var lat []string
		if ms, ok := sp.CheckpointerWriteLatencyMs(); ok {
			lat = append(lat, fmt.Sprintf("checkpointer %.2f ms", ms))
		}
		if ms, ok := sp.ClientWriteLatencyMs(); ok {
			lat = append(lat, fmt.Sprintf("backends %.2f ms", ms))
		}
		if len(lat) > 0 {
			b.WriteString(maintRow("write latency", strings.Join(lat, "  ")))
		}
		// PG18 accounts WAL I/O here; the fsync latency is the floor under
		// every synchronous commit.
		if sp.WALFsyncs > 0 {
			line := formatRows(sp.WALFsyncs) + " fsyncs"
			if ms, ok := sp.WALFsyncLatencyMs(); ok {
				line += "  " + gradeStyle(ms, 2, 10).Render(fmt.Sprintf("%.2f ms avg", ms))
			}
			if ms, ok := avgMsTUI(sp.WALWriteTimeMs, sp.WALWrites); ok {
				line += "  " + mu(fmt.Sprintf("writes %.2f ms avg", ms))
			}
			b.WriteString(maintRow("wal i/o", line))
		}
	}

	fsyncs := formatRows(io.Fsyncs)
	if io.BackendFsyncs > 0 {
		fsyncs += "  " + styleErr.Render(formatRows(io.BackendFsyncs)+" by backends") + v.note("backend_fsyncs")
	}
	b.WriteString(maintRow("fsyncs", fsyncs))
	b.WriteString("\n")
	return b.String()
}

// renderMaintWAL renders the "wal & checkpoints" section: what WAL the server
// generates and where it sits, then how checkpoints are keeping up with it.
func renderMaintWAL(v maintView) string {
	mu := styleMuted.Render
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" wal & checkpoints ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	set := info.Settings
	cp, w := info.Checkpointer, info.WAL
	switch {
	case cp.HasData:
		b.WriteString(v.counterLegend(cp.StatsReset))
	case w.HasData:
		b.WriteString(v.counterLegend(w.StatsReset))
	}

	// --- WAL ---
	b.WriteString(gucRow(set, "wal_level", "wal_level", ""))
	if w.HasData {
		rate := ""
		if bps, ok := info.WALBytesPerSecSinceReset(); ok {
			rate = humanize.Bytes(int64(bps*60)) + "/min " + mu("("+sinceResetLabel(w.StatsReset, info.StartTime)+")")
		}
		if v.rates.OK && v.rates.WALBytesPerMin > 0 {
			if rate != "" {
				rate += "  "
			}
			rate += humanize.Bytes(int64(v.rates.WALBytesPerMin)) + "/min " + mu("(last "+shortDuration(v.rates.Window)+")")
		}
		if rate == "" {
			rate = mu("unknown")
		}
		b.WriteString(maintRow("wal rate", rate))
		b.WriteString(maintRow("wal generated", humanize.Bytes(w.Bytes)+"  "+mu(formatRows(w.Records)+" records")))
		if frac, ok := w.FPIFrac(); ok {
			b.WriteString(maintRow("full-page images", fmt1(frac*100)+"% of records"+v.note("wal_fpi")))
		}
	}
	if d := info.WALDir; d.HasData {
		b.WriteString(maintRow("pg_wal on disk", humanize.Bytes(d.Bytes)+"  "+mu(formatRows(d.Files)+" segments")))
	} else {
		b.WriteString(maintRow("pg_wal on disk", mu("n/a (needs pg_monitor)")))
	}
	b.WriteString(gucRow(set, "wal_buffers", "wal_buffers", v.note("wal_buffers")))

	// --- checkpoints ---
	b.WriteString(gucRow(set, "checkpoint_timeout", "checkpoint_timeout", ""))
	b.WriteString(gucRow(set, "completion_target", "checkpoint_completion_target", v.note("checkpoint_completion_target")))
	b.WriteString(gucRow(set, "max_wal_size", "max_wal_size", v.note("max_wal_size")))
	b.WriteString(gucRow(set, "min_wal_size", "min_wal_size", ""))

	if !cp.HasData {
		b.WriteString(maintRow("checkpoints", mu("no data")))
	} else {
		total := cp.Timed + cp.Requested
		cpLine := fmt.Sprintf("%s timed  %s requested", formatRows(cp.Timed), formatRows(cp.Requested))
		if total > 0 {
			reqPct := float64(cp.Requested) / float64(total) * 100
			cpLine += "  " + gradeStyle(reqPct, 100*pg.CheckpointReqWarnFrac, 100*pg.CheckpointReqCritFrac).Render(fmt1(reqPct)+"% requested")
		}
		if v.rates.OK && v.rates.CheckpointsPerMin > 0 {
			cpLine += "  " + mu(fmt.Sprintf("·  %.1f/min", v.rates.CheckpointsPerMin))
		}
		b.WriteString(maintRow("checkpoints", cpLine))
		if iv, ok := info.AvgCheckpointInterval(); ok {
			line := shortDuration(iv)
			if t := info.Tuning.CheckpointTimeoutSecs; t > 0 {
				frac := iv.Seconds() / float64(t)
				st := mu
				if frac < 0.5 {
					st = lipgloss.NewStyle().Foreground(colorAccent).Render
				}
				line = st(shortDuration(iv)) + "  " + mu(fmt.Sprintf("(timeout %s — %.0f%% of it)",
					shortDuration(time.Duration(t)*time.Second), frac*100))
			}
			b.WriteString(maintRow("avg interval", line))
		}
		if total > 0 {
			line := fmt.Sprintf("write %s  sync %s  %s buffers", fmtAge(cp.WriteTimeMs/float64(total)),
				fmtAge(cp.SyncTimeMs/float64(total)), formatRows(cp.BuffersWritten/total))
			b.WriteString(maintRow("per checkpoint", line+v.note("checkpoint_sync")))
		}
	}

	if info.WALMaxBytes > 0 {
		ratio := min(float64(info.WALBytesSinceCheckpoint)/float64(info.WALMaxBytes), 1)
		walBarStyle := gradeStyle(ratio, 0.5, 0.8)
		barW := 20
		filled := min(int(float64(barW)*ratio), barW)
		bar := paintBar(barW, barSegment{cells: filled, style: walBarStyle})
		detail := fmt.Sprintf("%s / %s  %s", humanize.Bytes(info.WALBytesSinceCheckpoint),
			humanize.Bytes(info.WALMaxBytes), walBarStyle.Render(fmt1(ratio*100)+"%"))
		if !info.WALCheckpointTime.IsZero() {
			detail += "  " + mu("last checkpoint "+relativeAge(time.Since(info.WALCheckpointTime)))
		}
		b.WriteString(maintRow("since checkpoint", bar+"  "+detail))
	}

	// Who writes dirty buffers: the checkpointer (planned) and the bgwriter
	// (ahead of demand) should; a backend writing means it had to evict a
	// dirty page itself and its query waited for the write.
	if sp := info.IOSplit; sp.HasData {
		total := sp.CheckpointerWrites + sp.BgwriterWrites + sp.ClientWrites
		if total > 0 {
			barW := 20
			cpC := int(float64(barW) * float64(sp.CheckpointerWrites) / float64(total))
			bgC := int(float64(barW) * float64(sp.BgwriterWrites) / float64(total))
			clC := max(barW-cpC-bgC, 0)
			bar := paintBar(barW,
				barSegment{cells: cpC, style: styleBar},
				barSegment{cells: bgC, style: styleBarAlt},
				barSegment{cells: clC, style: styleErr})
			pct := func(n int64) string { return fmt1(100 * float64(n) / float64(total)) }
			legend := styleBar.Render("■") + mu(" checkpointer "+pct(sp.CheckpointerWrites)+"%  ") +
				styleBarAlt.Render("■") + mu(" bgwriter "+pct(sp.BgwriterWrites)+"%  ") +
				styleErr.Render("■") + mu(" backends "+pct(sp.ClientWrites)+"%")
			b.WriteString(maintRow("dirty-page writes", bar+"  "+legend))
		}
		// Both bgwriter_lru_maxpages signals (backends writing, sweeps capped)
		// land here, next to the knob they are about; without advice the row
		// still shows how often the cap was hit.
		bg := info.Bgwriter
		bgLine := formatRows(bg.BuffersClean) + " cleaned"
		if note := v.note("bgwriter_lru_maxpages"); note != "" {
			bgLine += note
		} else if bg.MaxwrittenClean > 0 {
			bgLine += "  " + mu(fmt.Sprintf("%s sweeps stopped at bgwriter_lru_maxpages (%s)",
				formatRows(bg.MaxwrittenClean), settingOr(set, "bgwriter_lru_maxpages")))
		}
		b.WriteString(maintRow("bgwriter", bgLine))
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintObservability renders the "observability" section: the settings
// that decide whether the rest of pgdu has data to show. The extension blocks
// follow the extension probe, not the GUC's presence — a preloaded library
// whose extension isn't created in this db shows as not installed above and
// must not print a settings block here.
func renderMaintObservability(v maintView) string {
	info := v.info
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" observability ") + "\n")
	if info == nil {
		b.WriteString("\n")
		return b.String()
	}
	set := info.Settings
	b.WriteString(gucRow(set, "track_io_timing", "track_io_timing", v.note("track_io_timing")))
	b.WriteString(gucRow(set, "track_wal_io_timing", "track_wal_io_timing", ""))
	b.WriteString(gucRow(set, "track_functions", "track_functions", ""))
	b.WriteString(gucRow(set, "log_min_duration_stmt", "log_min_duration_statement", ""))
	b.WriteString(gucRow(set, "log_autovacuum_min_dur", "log_autovacuum_min_duration", ""))
	b.WriteString(gucRow(set, "log_checkpoints", "log_checkpoints", v.note("log_checkpoints")))

	if info.Statements.Installed {
		b.WriteString("\n  " + styleHeader.Render(" pg_stat_statements ") + "\n")
		b.WriteString(gucRow(set, "track", "pg_stat_statements.track", v.note("pg_stat_statements.track")))
		b.WriteString(gucRow(set, "track_planning", "pg_stat_statements.track_planning", ""))
		b.WriteString(gucRow(set, "max", "pg_stat_statements.max", v.note("pg_stat_statements.max")))
	}
	if info.Qualstats.Installed {
		b.WriteString("\n  " + styleHeader.Render(" pg_qualstats ") + "\n")
		b.WriteString(gucRow(set, "enabled", "pg_qualstats.enabled", ""))
		b.WriteString(gucRow(set, "max", "pg_qualstats.max", ""))
		b.WriteString(gucRow(set, "sample_rate", "pg_qualstats.sample_rate", ""))
		b.WriteString(gucRow(set, "track_constants", "pg_qualstats.track_constants", ""))
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintHealth renders the "operational health" section.
func renderMaintHealth(v maintView) string {
	mu := styleMuted.Render
	info := v.info
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
		b.WriteString(maintRow("pending config", restartStr))
		b.WriteString(maintRow("", reloadStr+mu("  ·  s browses pg_settings")))

		lockStr := mu("0 waiting")
		if info.LockWaits > 0 {
			lockStr = styleErr.Render(fmt.Sprintf("%d waiting", info.LockWaits))
		}
		b.WriteString(maintRow("lock waits", lockStr))

		for _, bl := range info.Blocked {
			blockers := make([]string, len(bl.BlockedBy))
			for i, pid := range bl.BlockedBy {
				blockers[i] = strconv.Itoa(int(pid))
			}
			b.WriteString(maintRow("", styleErr.Render("▸ ")+fmt.Sprintf("pid %d blocked by %s  %s  %s",
				bl.PID, strings.Join(blockers, ","), fmtSecsDuration(bl.WaitSec), mu(bl.Query))))
		}

		if info.PreparedXacts > 0 {
			prepLine := styleErr.Render(fmt.Sprintf("%d prepared xact(s)", info.PreparedXacts))
			if info.OldestPrepSec > 0 {
				prepLine += mu("  oldest: "+fmtSecsDuration(info.OldestPrepSec)) + "  " + mu("— may pin xmin horizon")
			}
			b.WriteString(maintRow("prepared xacts", prepLine))
		}

		if info.TempFiles > 0 {
			tmp := fmt.Sprintf("%s files  %s", formatRows(info.TempFiles), humanize.Bytes(info.TempBytes))
			if v.rates.OK && v.rates.TempBytesPerMin > 0 {
				tmp += "  " + mu("·  "+humanize.Bytes(int64(v.rates.TempBytesPerMin))+"/min (last "+
					shortDuration(v.rates.Window)+") — queries spilling: work_mem or bad plans")
			}
			b.WriteString(maintRow("temp files", tmp))
			for _, t := range info.TempByDB {
				fileWord := "files"
				if t.Files == 1 {
					fileWord = "file"
				}
				line := fmt.Sprintf("  %s:  %s %s  %s", t.DB, formatRows(t.Files), fileWord, humanize.Bytes(t.Bytes))
				if w, ok := info.StatsWindow(t.StatsReset); ok && w > time.Hour {
					line += fmt.Sprintf("  (%s/day, %s)", humanize.Bytes(int64(float64(t.Bytes)/w.Hours()*24)),
						sinceResetLabel(t.StatsReset, info.StartTime))
				}
				b.WriteString(maintRow("", mu(line)))
			}
		} else {
			b.WriteString(maintRow("temp files", mu("none")))
		}

		if info.ArchiveFailed > 0 {
			line := styleErr.Render(fmt.Sprintf("%s archived  %s failed", formatRows(info.ArchiveCount), formatRows(info.ArchiveFailed)))
			if info.ArchiveLastFailed != "" {
				line += mu("  last: " + info.ArchiveLastFailed)
			}
			b.WriteString(maintRow("wal archiver", line))
		} else if info.ArchiveCount > 0 {
			archiveAge := ""
			if !info.ArchiveLastTime.IsZero() && info.ArchiveLastTime.Year() > 1 {
				archiveAge = "  " + mu("last "+relativeAge(time.Since(info.ArchiveLastTime)))
			}
			b.WriteString(maintRow("wal archiver",
				lipgloss.NewStyle().Foreground(colorOK).Render(formatRows(info.ArchiveCount)+" archived")+archiveAge))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderMaintRecommendations lists the actionable advice — every warning and
// critical note plus informational ones that come with a concrete change —
// worst first, each with its copyable fix line. It is the same advice the
// sections annotate inline, collected in one place.
func renderMaintRecommendations(v maintView) string {
	mu := styleMuted.Render
	var b strings.Builder
	b.WriteString("  " + styleHeader.Render(" recommendations ") + "\n")
	if v.info == nil {
		b.WriteString("\n")
		return b.String()
	}
	act := v.advice.Actionable()
	if len(act) == 0 {
		b.WriteString("  " + mu("none — nothing graded worse than informational") + "\n\n")
		return b.String()
	}
	for _, a := range act {
		st := adviceStyle(a.Level)
		glyph := "·"
		switch a.Level {
		case pg.AdviceCrit:
			glyph = "!"
		case pg.AdviceWarn:
			glyph = "~"
		}
		name := a.Setting
		if name == "" {
			name = a.Key
		}
		head := name
		if a.Current != "" {
			head += " " + a.Current
		}
		if a.Suggested != "" {
			head += " → " + a.Suggested
		}
		b.WriteString("  " + st.Render(glyph) + " " + padRight(st.Render(head), 44) + "  " + mu(a.Reason) + "\n")
		if a.Fix != "" {
			b.WriteString("      " + mu(a.Fix) + "\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}
