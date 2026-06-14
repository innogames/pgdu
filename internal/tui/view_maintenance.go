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

	if s.maintErr != nil {
		b.WriteString("  " + styleErr.Render("error: "+s.maintErr.Error()) + "\n")
		for i := 1; i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	info := s.maint

	var body strings.Builder
	body.WriteString(renderMaintServer(info))
	body.WriteString(renderMaintTransactions(info))
	body.WriteString(renderMaintReplication(info))
	body.WriteString(renderMaintPgBouncer(info))
	body.WriteString(renderMaintMemory(info))
	body.WriteString(renderMaintAutovacuum(info))
	body.WriteString(renderMaintWAL(info))
	body.WriteString(renderMaintIO(info))
	body.WriteString(renderMaintHealth(info))

	// ── EXTENSION CAPACITY ────────────────────────────────────────────
	// Kept last because it owns the ↑↓ cursor and reset flow.
	body.WriteString("  " + styleHeader.Render(" extension capacity ") + "\n")
	var stmtsCap, qualsCap pg.ExtCapacity
	if info != nil {
		stmtsCap = info.Statements
		qualsCap = info.Qualstats
	}
	body.WriteString(m.renderCapacityRow(s, s.db, 0, "pg_stat_statements", stmtsCap) + "\n")
	body.WriteString(m.renderCapacityRow(s, s.db, 1, "pg_qualstats", qualsCap) + "\n")
	body.WriteString("\n")

	hintLine := m.renderMaintHint(s)

	var full strings.Builder
	if hintLine != "" {
		full.WriteString(hintLine + "\n")
	}
	full.WriteString("  " + mu("↑↓ select capacity row  ·  ↵ arm reset  ·  space refresh  ·  pgdn/pgup scroll") + "\n")
	full.WriteString(body.String())

	return scrollWindow(full.String(), &s.offset, height)
}

// renderCapacityRow renders one extension capacity row with a fill bar,
// counts, percentage, dealloc signal, and reset-age. idx is the 0-based row
// index within the capacity section; s.maintCursor highlights the selected row.
func (m *Model) renderCapacityRow(s *screen, db string, idx int, name string, cap pg.ExtCapacity) string {
	mu := styleMuted.Render
	cursor := "  "
	if s.maintCursor == idx {
		cursor = styleSelected.Render("▶ ")
	}
	if !cap.Installed {
		return cursor + padRight(mu(name), 22) + mu("not installed in "+db)
	}

	ratio := cap.FillRatio()
	barW := 20

	// Colour: ≥90% red, ≥70% yellow, else bar-cyan.
	var barStyle lipgloss.Style
	switch {
	case ratio >= 0.90:
		barStyle = styleErr
	case ratio >= 0.70:
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
		if ratio >= 0.90 {
			pctStr = barStyle.Render(pctStr + "!")
		} else if ratio >= 0.70 {
			pctStr = barStyle.Render(pctStr)
		}
	}

	usedMax := fmt.Sprintf("%s/%s", formatRows(cap.Used), formatRows(cap.Max))
	if cap.Max <= 0 {
		usedMax = formatRows(cap.Used) + " entries"
	}

	extra := ""
	if cap.Dealloc >= 0 {
		if cap.Dealloc > 0 {
			extra += "  " + styleErr.Render(fmt.Sprintf("dealloc %s", formatRows(cap.Dealloc)))
		} else {
			extra += "  " + mu("dealloc 0")
		}
	}
	if !cap.StatsReset.IsZero() {
		age := time.Since(cap.StatsReset)
		extra += "  " + mu("reset "+relativeAge(age))
	}

	nameW := 22
	line := cursor + padRight(mu(name), nameW) + barStr + "  " + padRight(usedMax, 16) + padRight(pctStr, 8) + extra
	return line
}

// renderMaintHint returns the one-line reset-confirm banner when a reset is
// armed, or "" otherwise. Mirrors renderReindexBanner.
func (m *Model) renderMaintHint(s *screen) string {
	if s.pendingReset == "" {
		return ""
	}
	name := s.pendingReset
	if name == "statements" {
		name = "pg_stat_statements"
	} else {
		name = "pg_qualstats"
	}
	return "  " + styleSelected.Render("confirm: ") +
		styleMuted.Render("reset "+name+" — press ") +
		styleBadge.Render("y") +
		styleMuted.Render(" to run, ") +
		styleBadge.Render("n") +
		styleMuted.Render(" (or any other key) to cancel")
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
// string: "1h 23m 45s", "5m 12s", or "45s".
func fmtSecsDuration(secs float64) string {
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
		for i := 1; i < height; i++ {
			b.WriteString("\n")
		}
		return b.String()
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

		cat := ""
		if ok {
			cat = row.Category
			if len(cat) > 28 {
				cat = cat[:25] + "…"
			}
		}

		b.WriteString(cursor + padRight(nameStyle.Render(it.name), 36) +
			padRight(valStyle.Render(it.detail), 20) +
			styleMuted.Render(cat) + "\n")
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
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("maintenance dashboard reference") +
		mu("  ·  press ") + styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") +
		mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" extension capacity ") + "\n")
	b.WriteString("    " + mu("pg_stat_statements and pg_qualstats both pre-allocate a fixed shared-memory array (the .max") + "\n")
	b.WriteString("    " + mu("GUC). Once the array is full, new queries either evict old entries (pg_stat_statements,") + "\n")
	b.WriteString("    " + mu("tracked via dealloc) or are silently dropped (pg_qualstats). A bar near 100% means the") + "\n")
	b.WriteString("    " + mu("tool is losing data. Reset clears the array; raise .max + restart to prevent recurrence.") + "\n\n")

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

	b.WriteString("  " + styleHeader.Render(" i/o (pg_stat_io) ") + "\n")
	b.WriteString("    " + mu("BackendFsyncs > 0 means client backends are calling fsync themselves — this happens when") + "\n")
	b.WriteString("    " + mu("the checkpointer cannot keep up with dirty-buffer flushing. It stalls the writing query.") + "\n")
	b.WriteString("    " + mu("Tune checkpoint_completion_target (raise toward 0.9) and/or max_wal_size.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" pgbouncer ") + "\n")
	b.WriteString("    " + mu("cl_active/cl_waiting: client connections actively querying / waiting for a server slot.") + "\n")
	b.WriteString("    " + mu("sv_active/sv_idle: server connections in use / pooled and waiting for a client.") + "\n")
	b.WriteString("    " + mu("max wait > 0 means clients are queuing — consider raising pool_size or switching mode.") + "\n\n")

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

// humanizeGUCBytes converts a GUC value like "4096" (in 8-kB pages for
// shared_buffers) or "64MB" into a human-readable bytes string. If the
// string is already formatted with a unit suffix (kB/MB/GB/TB) it's left
// as-is; otherwise it's treated as a plain integer and passed through
// humanize.Bytes (which assumes bytes).
func humanizeGUCBytes(v string) string {
	if v == "" {
		return v
	}
	// pg_settings already formats memory GUCs with a unit suffix (kB/MB/…).
	return v
}

var _ = humanize.Bytes // used elsewhere, referenced here to keep import live
var _ = humanizeGUCBytes
