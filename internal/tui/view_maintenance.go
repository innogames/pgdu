package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderMaintenance renders the maintenance dashboard: a scrollable status
// panel. Uses scrollWindow so it works on any terminal height.
func (m *Model) renderMaintenance(s *screen, height int) string {
	mu := styleMuted.Render

	var b strings.Builder

	if s.loading || !s.loaded {
		// Loading state is handled by the caller (View), but on a Refresh the
		// screen goes back to loading=true, so just return blank space.
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}

	if s.maintenance.err != nil {
		b.WriteString("  " + styleErr.Render("error: "+s.maintenance.err.Error()) + "\n")
		return padInfo(&b, height)
	}

	info := s.maintenance.info

	var body strings.Builder

	// ── EXTENSION CAPACITY ────────────────────────────────────────────
	// Kept first because it owns the ↑↓ cursor and reset flow — the only
	// actionable rows on this dashboard, so they stay reachable without having
	// to scroll past the read-only status sections below.
	body.WriteString("  " + styleHeader.Render(" extension capacity ") + "\n")
	var stmtsCap, qualsCap pg.ExtCapacity
	if info != nil {
		stmtsCap = info.Statements
		qualsCap = info.Qualstats
	}
	body.WriteString(m.renderCapacityRow(s, s.db, 0, "pg_stat_statements", stmtsCap) + "\n")
	body.WriteString(m.renderCapacityRow(s, s.db, 1, "pg_qualstats", qualsCap) + "\n")
	body.WriteString(m.renderTableStatsRow(s, info, 2) + "\n")
	body.WriteString(m.renderTableStatsAllRow(s, 3) + "\n")
	body.WriteString("\n")

	v := newMaintView(s)

	// Wide terminals get a two-pane layout: compact "vitals" sections side by
	// side, verbose sections (long advisory lines, bars, blocked-query text)
	// full width below so they aren't truncated. The paned sections carry
	// annotated rows of up to ~80 cols (host memory, advice notes), so the
	// split only pays off from 160 cols; narrower terminals stack everything.
	// The recommendations panel closes the screen so the eye lands on it after
	// scrolling through the sections it summarises.
	if m.width >= 160 {
		// Split evenly: both panes carry long rows now (connections by app and
		// query text on the left, host memory and advice notes on the right),
		// keeping the right pane ≥48 cols. -3 throughout is the "│ " rule + safety.
		leftW := max(40, min(m.width-48-3, m.width/2))
		rightW := max(20, m.width-leftW-3)
		// Column heights are balanced by hand: server + transactions + table
		// activity are short, so the buffer cache goes left; the settings-heavy
		// memory + autovacuum + observability stack goes right.
		left := renderMaintServer(v) + renderMaintTransactions(v) + renderMaintTableActivity(v) +
			renderMaintBufferCache(v, min(20, max(leftW-overviewLabelW-14, 8)))
		right := renderMaintMemory(v) + renderMaintAutovacuum(v) + renderMaintObservability(v)
		body.WriteString(renderColumns(left, right, leftW, rightW))
		body.WriteString("\n")
	} else {
		body.WriteString(renderMaintServer(v))
		body.WriteString(renderMaintTransactions(v))
		body.WriteString(renderMaintTableActivity(v))
		body.WriteString(renderMaintBufferCache(v, 20))
		body.WriteString(renderMaintMemory(v))
		body.WriteString(renderMaintAutovacuum(v))
		body.WriteString(renderMaintObservability(v))
	}
	body.WriteString(renderMaintReplication(v))
	body.WriteString(renderMaintWAL(v))
	body.WriteString(renderMaintHealth(v))
	body.WriteString(renderMaintRecommendations(v))

	hintLine := m.renderMaintHint(s)

	var full strings.Builder
	if hintLine != "" {
		full.WriteString(hintLine + "\n")
	}
	full.WriteString("  " + mu("↑↓ capacity row / scroll  ·  pgdn g G  ·  ↵ reset  ·  ") +
		styleBadge.Render("s") + mu(" → settings  ·  ") +
		styleBadge.Render("a") + mu(" → activity  ·  ") +
		styleBadge.Render("w") + mu(" → wal  ·  ") +
		styleBadge.Render("r") + mu(" → replication  ·  ") +
		styleBadge.Render("o") + mu(" → i/o  ·  ") +
		styleBadge.Render("p") + mu(" → progress  ·  space refresh  ·  ") +
		styleBadge.Render("t") + mu(" auto-refresh "+m.maintRefreshLabel()) + "\n")
	full.WriteString(body.String())

	return scrollWindow(full.String(), &s.offset, height)
}

// maintRefreshLabel names the overview's auto-refresh cadence for the hint line.
func (m *Model) maintRefreshLabel() string {
	if m.maintRefresh <= 0 {
		return "off"
	}
	return shortDuration(m.maintRefresh)
}

// renderColumns joins two pre-rendered text blocks into side-by-side panes:
// each left line is truncated+padded to leftW, each right line truncated to
// rightW, separated by a muted "│ " rule. The shorter block is padded with
// blank lines so the rule stays continuous. Only compact sections belong here;
// verbose sections render full width.
func renderColumns(left, right string, leftW, rightW int) string {
	l := strings.Split(strings.TrimRight(left, "\n"), "\n")
	r := strings.Split(strings.TrimRight(right, "\n"), "\n")
	rule := styleMuted.Render("│ ")
	var b strings.Builder
	for i := 0; i < max(len(l), len(r)); i++ {
		var ll, rr string
		if i < len(l) {
			ll = truncateToWidth(l[i], leftW)
		}
		if i < len(r) {
			rr = truncateToWidth(r[i], rightW)
		}
		b.WriteString(padRight(ll, leftW) + rule + rr + "\n")
	}
	return b.String()
}

// renderCapacityRow renders one extension capacity row with a fill bar,
// counts, percentage, memory footprint, and reset-age. idx is the 0-based row
// index within the capacity section; s.maintCursor highlights the selected row.
func (m *Model) renderCapacityRow(s *screen, db string, idx int, name string, cap pg.ExtCapacity) string {
	mu := styleMuted.Render
	cursor := "  "
	if s.maintenance.cursor == idx {
		cursor = styleSelected.Render("▶ ")
	}
	if !cap.Installed {
		return cursor + padRight(mu(name), 22) + mu("not installed in "+db)
	}

	ratio := cap.FillRatio()
	barW := 20

	// Colour follows the triage thresholds so the bar and the "extension
	// capacity" health check never disagree: warn red, notice yellow, else bar-cyan.
	var barStyle lipgloss.Style
	switch {
	case ratio >= pg.ExtCapacityWarnFrac:
		barStyle = styleErr
	case ratio >= pg.ExtCapacityNoticeFrac:
		barStyle = lipgloss.NewStyle().Foreground(colorAccent)
	default:
		barStyle = styleBar
	}

	var barStr string
	if cap.Max > 0 {
		filled := min(int(float64(barW)*ratio), barW)
		barStr = paintBar(barW, barSegment{cells: filled, style: barStyle})
	} else {
		barStr = paintBar(barW, barSegment{cells: 0, style: styleBar})
	}

	pctStr := ""
	if cap.Max > 0 {
		pctStr = fmt1(ratio*100) + "%"
		if ratio >= pg.ExtCapacityWarnFrac {
			pctStr = barStyle.Render(pctStr + "!")
		} else if ratio >= pg.ExtCapacityNoticeFrac {
			pctStr = barStyle.Render(pctStr)
		}
	}

	usedMax := fmt.Sprintf("%s/%s", formatRows(cap.Used), formatRows(cap.Max))
	if cap.Max <= 0 {
		usedMax = formatRows(cap.Used) + " entries"
	}

	extra := ""
	if cap.ShmemBytes > 0 {
		mem := "~" + humanize.Bytes(cap.ShmemBytes) + " shmem"
		if cap.TextBytes > 0 {
			mem += " · " + humanize.Bytes(cap.TextBytes) + " disk"
		}
		extra += "  " + mu(mem)
	}
	if !cap.StatsReset.IsZero() {
		age := time.Since(cap.StatsReset)
		extra += "  " + mu("reset "+relativeAge(age))
	}

	nameW := 22
	line := cursor + padRight(mu(name), nameW) + barStr + "  " + padRight(usedMax, 16) + padRight(pctStr, 8) + extra
	return line
}

// renderTableStatsRow renders the third actionable capacity-section row: the
// reset for the built-in table/index counters (pg_stat_reset) that back the
// Table overview. Unlike the two extension rows there is no fill bar — the
// counters are unbounded — so it shows the database name and the last-reset age
// only. idx is its position in the maintCursor sequence.
func (m *Model) renderTableStatsRow(s *screen, info *pg.MaintenanceInfo, idx int) string {
	mu := styleMuted.Render
	cursor := "  "
	if s.maintenance.cursor == idx {
		cursor = styleSelected.Render("▶ ")
	}
	detail := "table & index counters in " + s.db
	if info != nil && !info.TableStatsReset.IsZero() {
		detail += "  ·  reset " + relativeAge(time.Since(info.TableStatsReset))
	}
	return cursor + padRight(mu("table statistics"), 22) + mu(detail)
}

// renderTableStatsAllRow renders the fourth actionable capacity-section row: the
// cluster-wide counterpart to renderTableStatsRow. It runs pg_stat_reset() in
// every connectable database (ResetTableStatsAllDBs) rather than just the
// current one. There is no single last-reset age to show (the timestamp is
// per-database), so the row only describes the scope. idx is its position in the
// maintCursor sequence.
func (m *Model) renderTableStatsAllRow(s *screen, idx int) string {
	mu := styleMuted.Render
	cursor := "  "
	if s.maintenance.cursor == idx {
		cursor = styleSelected.Render("▶ ")
	}
	return cursor + padRight(mu("table stats · all dbs"), 22) +
		mu("table & index counters in every database (pg_stat_reset)")
}

// maintResetTarget maps a pendingReset key to the human-readable name shown in
// the confirm banner.
func maintResetTarget(which string) string {
	switch which {
	case "statements":
		return "pg_stat_statements"
	case "qualstats":
		return "pg_qualstats"
	case "tablestats":
		return "table statistics (pg_stat_reset)"
	case "tablestats-all":
		return "table statistics in ALL databases (pg_stat_reset)"
	default:
		return which
	}
}

// renderMaintHint returns the one-line reset-confirm banner when a reset is
// armed, or "" otherwise. Mirrors renderReindexBanner.
func (m *Model) renderMaintHint(s *screen) string {
	if s.maintenance.pendingReset == "" {
		return ""
	}
	return confirmBanner("reset " + maintResetTarget(s.maintenance.pendingReset))
}

// formatUptime formats a duration as "Xd Yh Zm" or "Yh Zm" or "Zm" depending
// on scale, appropriate for server uptime display.
func formatUptime(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// fmtSecsDuration formats a duration given in seconds as a human-readable
// string: "1h 23m 45s", "5m 12s", "45s", or "<1s" for a sub-second value (a
// query that has just started is not a 0 s query).
func fmtSecsDuration(secs float64) string {
	if secs > 0 && secs < 1 {
		return "<1s"
	}
	d := time.Duration(secs) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// maintDurationStyle returns a colour style for a duration expressed in seconds:
// green for brief (< 1 min), yellow for moderate (< 10 min), red for long.
// Used to grade the longest-running transaction age.
func maintDurationStyle(secs float64) lipgloss.Style {
	switch {
	case secs >= 600:
		return styleErr
	case secs >= 60:
		return lipgloss.NewStyle().Foreground(colorAccent)
	default:
		return lipgloss.NewStyle().Foreground(colorOK)
	}
}

// renderSettingsList renders the read-only pg_settings browser for levelSettings.
// Items are pre-loaded into s.items (name + value) by settingsToItems; the
// existing filter/cursor/viewport machinery handles navigation. Each row is
// color-coded: red = pending restart, yellow = non-default value.
func (m *Model) renderSettingsList(s *screen, height int) string {
	if s.loading || !s.loaded {
		var b strings.Builder
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}
	if s.err != nil {
		var b strings.Builder
		b.WriteString("  " + styleErr.Render("error: "+s.err.Error()) + "\n")
		return padInfo(&b, height)
	}

	vis := s.visibleIndexes()
	rowsH := max(
		// reserve one line for the hint
		height-1, 1)

	var b strings.Builder
	b.WriteString("  " + styleMuted.Render("red=restart required  ·  yellow=non-default  ·  / filter  ·  q back") + "\n")
	rowsH--

	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vis[vi]]
		cursor := "  "
		if vi == s.cursor {
			cursor = styleSelected.Render("▶ ")
		}

		var nameStyle, valStyle lipgloss.Style
		row, ok := it.data.(pg.SettingRow)
		switch {
		case ok && row.PendingRestart:
			nameStyle = styleErr
			valStyle = styleErr
		case ok && !row.IsDefault:
			nameStyle = lipgloss.NewStyle().Foreground(colorAccent)
			valStyle = lipgloss.NewStyle().Foreground(colorAccent)
		default:
			nameStyle = styleMuted
			valStyle = lipgloss.NewStyle()
		}

		// name | value | description | category. The value is what the user
		// came for; the description gets whatever width is left, the category
		// a fixed tail. Every column is truncated so a row never wraps.
		const nameW, valW, catW = 40, 22, 28
		val := ""
		cat := ""
		if ok {
			val = row.Display
			if val == "" {
				val = strings.TrimSpace(row.Setting + " " + row.Unit)
			}
			cat = row.Category
		}
		descW := m.width - 2 - nameW - valW - catW - 2
		desc := ""
		if descW > 8 {
			desc = padRight(truncateToWidth(styleMuted.Render(it.detail), descW), descW) + "  "
		}
		b.WriteString(cursor + padRight(truncateToWidth(nameStyle.Render(it.name), nameW-1), nameW) +
			padRight(truncateToWidth(valStyle.Render(val), valW-1), valW) +
			desc + truncateToWidth(styleMuted.Render(cat), catW) + "\n")
	}
	for i := end - s.offset; i < rowsH; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// renderMaintenanceInfo is the ? overlay for the maintenance dashboard and
// settings browser. It explains the less obvious metrics so operators can act
// on them without needing to look things up.
func (m *Model) renderMaintenanceInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "system overview reference")

	b.WriteString("  " + styleHeader.Render(" extension capacity ") + "\n")
	b.WriteString("    " + mu("pg_stat_statements and pg_qualstats both pre-allocate a fixed shared-memory array (the .max") + "\n")
	b.WriteString("    " + mu("GUC). Once the array is full, new queries either evict old entries (pg_stat_statements)") + "\n")
	b.WriteString("    " + mu("or are silently dropped (pg_qualstats). A bar near 100% means the tool is losing data.") + "\n")
	b.WriteString("    " + mu("Reset clears the array; raise .max + restart to prevent recurrence. The memory figure is") + "\n")
	b.WriteString("    " + mu("the reserved shared memory (+ deduplicated query-text bytes for pg_stat_statements).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" xid age ") + "\n")
	b.WriteString("    " + mu("Postgres uses 32-bit transaction IDs. After ~2 billion transactions the counter wraps") + "\n")
	b.WriteString("    " + mu("around — rows whose XID is older than the horizon would appear to be 'in the future'") + "\n")
	b.WriteString("    " + mu("and become invisible. VACUUM FREEZE prevents this by rewriting old XIDs to a special") + "\n")
	b.WriteString("    " + mu("'frozen' value. autovacuum_freeze_max_age (typically 200 M) is the point at which") + "\n")
	b.WriteString("    " + mu("autovacuum is forced to run regardless of other settings. At ~80% of that limit,") + "\n")
	b.WriteString("    " + mu("autovacuum starts 'emergency' freezing that can overwhelm I/O. At 100% Postgres") + "\n")
	b.WriteString("    " + mu("halts all writes until freeze completes.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" rollback ratio ") + "\n")
	b.WriteString("    " + mu("A high rollback% (> 5%) usually means application errors or contention. Rollbacks are") + "\n")
	b.WriteString("    " + mu("not free: each one generates dead tuples that autovacuum must later reclaim. Persistent") + "\n")
	b.WriteString("    " + mu("deadlocks (> 0) always warrant investigation — they represent conflicting lock orders.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" replication slots ") + "\n")
	b.WriteString("    " + mu("An inactive slot with large retained WAL is a serious hazard: pg_wal grows unboundedly") + "\n")
	b.WriteString("    " + mu("until the slot is dropped or the replica reconnects. wal_status='lost' means the WAL") + "\n")
	b.WriteString("    " + mu("has already been recycled — the replica can no longer stream and must be rebuilt.") + "\n")
	b.WriteString("    " + mu("Drop stale slots that are no longer needed: SELECT pg_drop_replication_slot('name').") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" prepared transactions ") + "\n")
	b.WriteString("    " + mu("PREPARE TRANSACTION suspends a transaction but holds its locks until COMMIT/ROLLBACK") + "\n")
	b.WriteString("    " + mu("PREPARED is issued. An abandoned prepared xact pins the xmin horizon for the whole") + "\n")
	b.WriteString("    " + mu("cluster, preventing autovacuum from reclaiming dead tuples. Always rollback orphaned") + "\n")
	b.WriteString("    " + mu("prepared transactions: ROLLBACK PREPARED 'gid'.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" host memory & huge pages ") + "\n")
	b.WriteString("    " + mu("Host rows come from /proc/meminfo on the machine pgdu runs on, so they only mean") + "\n")
	b.WriteString("    " + mu("something when that is the database host. 'page cache' is what effective_cache_size") + "\n")
	b.WriteString("    " + mu("should roughly reflect (plus shared_buffers). huge_pages=try falls back to 4 kB pages") + "\n")
	b.WriteString("    " + mu("silently when the kernel pool (vm.nr_hugepages) is empty — the 'needs N' figure is") + "\n")
	b.WriteString("    " + mu("shared_memory_size_in_huge_pages, the pool size that lets the next start succeed.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" buffer cache ") + "\n")
	b.WriteString("    " + mu("occupancy/dirty come from pg_buffercache_summary(); temperature is the clock-sweep") + "\n")
	b.WriteString("    " + mu("usagecount histogram (cold = evictable, hot = reused often). Mostly hot with no cold") + "\n")
	b.WriteString("    " + mu("tail means the working set exceeds shared_buffers; a big cold tail means slack.") + "\n")
	b.WriteString("    " + mu("read latency is pg_stat_io read_time/reads for client backends (needs track_io_timing):") + "\n")
	b.WriteString("    " + mu("well under 1 ms means misses are served from the OS page cache, > 5 ms is disk.") + "\n")
	b.WriteString("    " + mu("bulkread share is the ring-buffer context large sequential scans use; vacuum share is") + "\n")
	b.WriteString("    " + mu("autovacuum's part of the traffic. o opens the full pg_stat_io table per backend type.") + "\n")
	b.WriteString("    " + mu("Backend fsyncs > 0 means the checkpointer cannot keep up and queries stall on fsync.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" counters vs rates ") + "\n")
	b.WriteString("    " + mu("Cumulative counters carry a 'since reset …' label. Once the screen has two samples") + "\n")
	b.WriteString("    " + mu("(space, or t for auto-refresh) per-minute rates appear next to them with the window") + "\n")
	b.WriteString("    " + mu("they cover. A stats reset between samples discards the window rather than showing junk.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" checkpoint sizing ") + "\n")
	b.WriteString("    " + mu("A requested checkpoint fires when WAL since the last one reaches") + "\n")
	b.WriteString("    " + mu("max_wal_size / (1 + checkpoint_completion_target); to let the timer win, max_wal_size") + "\n")
	b.WriteString("    " + mu("must cover WAL rate × checkpoint_timeout × (1 + target). The suggested value uses the") + "\n")
	b.WriteString("    " + mu("since-reset average rate. 'dirty-page writes' shows who flushes dirty buffers: the") + "\n")
	b.WriteString("    " + mu("checkpointer and bgwriter should; backends writing means their queries waited for it") + "\n")
	b.WriteString("    " + mu("(raise bgwriter_lru_maxpages). Full-page images are the WAL cost of frequent checkpoints.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" observability ") + "\n")
	b.WriteString("    " + mu("track_io_timing off leaves pg_stat_io and EXPLAIN (BUFFERS) without timings.") + "\n")
	b.WriteString("    " + mu("pg_stat_statements.track = none keeps the extension installed but empty; a fill level") + "\n")
	b.WriteString("    " + mu("past 90% means the least-used entries are being evicted (raise .max or reset).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" recommendations ") + "\n")
	b.WriteString("    " + mu("Every red/yellow note (and informational ones with a concrete change) collected worst") + "\n")
	b.WriteString("    " + mu("first, with a copyable ALTER SYSTEM line. pg_reload_conf() applies reload-level GUCs;") + "\n")
	b.WriteString("    " + mu("'restart required' ones wait for the next restart. sysctl lines are host-side.") + "\n")
	b.WriteString("    " + mu("Nothing is applied by pgdu.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" pending config ") + "\n")
	b.WriteString("    " + mu("need restart  — the setting was changed in postgresql.conf but requires a full server") + "\n")
	b.WriteString("    " + mu("              restart to take effect (e.g. shared_buffers, max_connections).") + "\n")
	b.WriteString("    " + mu("need reload   — a SIGHUP or SELECT pg_reload_conf() is enough (e.g. work_mem).") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" checkpoints & WAL in-flight ") + "\n")
	b.WriteString("    " + mu("Timed checkpoints fire every checkpoint_timeout (default 5 min).") + "\n")
	b.WriteString("    " + mu("Requested checkpoints fire when the WAL written since the last checkpoint reaches") + "\n")
	b.WriteString("    " + mu("max_wal_size. The 'since checkpoint' bar shows exactly that fill level — at 100% a") + "\n")
	b.WriteString("    " + mu("requested checkpoint fires. A high requested% in the cumulative counter means WAL is") + "\n")
	b.WriteString("    " + mu("filling faster than the timed interval: raise max_wal_size or checkpoint_completion_target.") + "\n")
	b.WriteString("    " + mu("For per-record WAL detail, use the WAL tool from the main menu.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" temp files ") + "\n")
	b.WriteString("    " + mu("Postgres spills sorts and hash joins to disk when they exceed work_mem. High temp-file") + "\n")
	b.WriteString("    " + mu("usage on a specific database usually points to a few expensive queries; increasing") + "\n")
	b.WriteString("    " + mu("work_mem or adding indexes reduces spilling. Counters are cumulative since last reset.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" wal archiver ") + "\n")
	b.WriteString("    " + mu("When archive_mode is on, each WAL segment is handed to archive_command or") + "\n")
	b.WriteString("    " + mu("archive_library before it can be recycled. A non-zero failed_count means segments are") + "\n")
	b.WriteString("    " + mu("piling up in pg_wal — if left unresolved the disk fills and Postgres stops.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" settings browser (s) ") + "\n")
	b.WriteString("    " + mu("red    = pending_restart: the value was changed but needs a server restart") + "\n")
	b.WriteString("    " + mu("yellow = non-default: the value differs from the compiled-in default (boot_val)") + "\n")
	b.WriteString("    " + mu("use / to filter by name, ↑↓ to navigate, q/esc to go back") + "\n")

	return padInfo(&b, height)
}
