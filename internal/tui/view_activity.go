package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
)

// renderActivityHeader renders the one-line summary bar above the activity list:
// server-wide counts by state (active / waiting / idle-in-transaction / idle),
// connection usage vs max_connections, the current filter mode, verbose status,
// and the refresh cadence.
func (m *Model) renderActivityHeader(s *screen) string {
	mu := styleMuted.Render
	sum := s.actSummary

	var parts []string
	label := func(n int, name string, style lipgloss.Style) string {
		if n == 0 {
			return mu(fmt.Sprintf("%d %s", n, name))
		}
		return style.Render(fmt.Sprintf("%d %s", n, name))
	}
	// Counts come from actSummary (the whole server) rather than the rows so
	// they stay accurate even when idle/background backends are hidden. "active"
	// and "waiting" already exclude genuinely idle ClientRead backends — those
	// land in the dedicated idle count below instead of bloating "waiting".
	parts = append(parts,
		label(sum.Active, "active", styleSelected),
		label(sum.Waiting, "waiting", styleErr),
		label(sum.IdleInXact, "idle-in-xact", styleMuted),
	)
	if sum.Other > 0 {
		parts = append(parts, mu(fmt.Sprintf("%d other", sum.Other)))
	}

	// Idle and auxiliary backends: shown inline when present in the list,
	// otherwise reported with a leading "+" to signal they're suppressed and
	// `v` (or the "all" filter, for idle) would reveal them.
	idleHidden := !s.actVerbose && s.actFilter != pg.ActivityAll
	if sum.Idle > 0 {
		if idleHidden {
			parts = append(parts, mu(fmt.Sprintf("+%d idle", sum.Idle)))
		} else {
			parts = append(parts, mu(fmt.Sprintf("%d idle", sum.Idle)))
		}
	}
	if !s.actVerbose {
		var aux int
		for _, r := range s.actRows {
			if isAuxBackend(r.BackendType) {
				aux++
			}
		}
		if aux > 0 {
			parts = append(parts, mu(fmt.Sprintf("+%d background", aux)))
		}
	}

	summary := strings.Join(parts, mu(" · "))

	// Connection usage vs the server limit. Turns amber past 75% and red past
	// 90% so an impending "too many connections" is visible at a glance.
	var conn string
	if sum.MaxConnections > 0 {
		text := fmt.Sprintf("%d/%d conn", sum.Conns, sum.MaxConnections)
		ratio := float64(sum.Conns) / float64(sum.MaxConnections)
		switch {
		case ratio >= 0.9:
			conn = styleErr.Render(text)
		case ratio >= 0.75:
			conn = styleSelected.Render(text)
		default:
			conn = styleBadge.Render(text)
		}
	}

	// Filter mode badge.
	filter := styleBadge.Render("filter: " + s.actFilter.Label())

	// Verbose badge — only shown when on so the line stays short by default.
	var verboseBadge string
	if s.actVerbose {
		verboseBadge = " " + styleBadge.Render("verbose: on")
	}

	// Refresh cadence badge — rendered in accent colour with a ⟳ glyph so it's
	// easy to spot and doesn't look like a static label.
	var refresh string
	if m.activityRefresh > 0 {
		refresh = styleSelected.Render(fmt.Sprintf("⟳ %s", m.activityRefresh))
	} else {
		refresh = styleMuted.Render("⟳ off")
	}

	line := "  " + summary + "  "
	if conn != "" {
		line += conn + "  "
	}
	return line + filter + verboseBadge + " " + refresh
}

// renderActColumnConfig draws the htop-style column picker for the Activity
// tool (C on levelActivity). Same look-and-feel as renderColumnConfig for the
// top-queries table.
func (m *Model) renderActColumnConfig(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("configure columns") + mu("  ·  ") +
		styleBadge.Render("space") + mu(" toggles · ") +
		styleBadge.Render("↑/↓") + mu(" move · ") +
		styleBadge.Render("C") + mu(" or ") + styleBadge.Render("esc") + mu(" to close") + "\n")
	b.WriteString("  " + mu("choose which columns the activity table shows") + "\n\n")

	m.ensureActColsInit()
	reg := actColumnRegistry()
	nameW := 0
	for _, d := range reg {
		if n := len(d.name); n > nameW {
			nameW = n
		}
	}
	for i, d := range reg {
		on := d.mandatory || m.actColEnabled(d.id, d.defaultOn)
		box := "[ ]"
		if on {
			box = "[x]"
		}
		cursor := "  "
		if i == m.actColCfgCursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
		}
		label := box + "  " + padRight(d.name, nameW)
		var rendered string
		switch i {
		case m.actColCfgCursor:
			rendered = styleSelected.Render(label) + "  " + mu(d.desc)
		default:
			rendered = label + "  " + mu(d.desc)
		}
		if d.mandatory {
			rendered += mu("  (always shown)")
		}
		b.WriteString(cursor + rendered + "\n")
	}
	return padInfo(&b, height)
}

// renderActivityInfo is the ? overlay for the Activity tool. It explains the
// filter / verbose / refresh model and what each column means.
func (m *Model) renderActivityInfo(height int) string {
	mu := styleMuted.Render
	badge := func(s string) string { return styleBadge.Render(s) }
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Activity reference") + mu("  ·  press ") +
		badge("?") + mu(" or ") + badge("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" what you're seeing ") + "\n")
	b.WriteString("    " + mu("Live view of pg_stat_activity. Rows are backends connected to the selected database.") + "\n")
	b.WriteString("    " + mu("The list auto-refreshes; press ") + badge("t") +
		mu(" to cycle the cadence (500ms · 1s · 2s · 5s · 10s · off).") + "\n")
	b.WriteString("    " + mu("Press ") + badge("space") + mu(" to force an immediate refresh at any time.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" filters ") + "\n")
	b.WriteString("    " + badge("f") + mu("  cycle state filter: active+waiting · non-idle · all") + "\n")
	b.WriteString("    " + mu("    active+waiting  — backends running a query or blocked on a wait event (default)") + "\n")
	b.WriteString("    " + mu("    non-idle        — everything except plain idle (surfaces idle-in-transaction locks)") + "\n")
	b.WriteString("    " + mu("    all             — every backend including idle") + "\n")
	b.WriteString("    " + badge("v") + mu("  show/hide background noise: auxiliary backends (walwriter, checkpointer, launchers…)") + "\n")
	b.WriteString("    " + mu("    and genuinely idle client connections (parked on Client/ClientRead). Both are hidden by") + "\n")
	b.WriteString("    " + mu("    default — they carry no running query and rarely indicate a problem. The header still") + "\n")
	b.WriteString("    " + mu("    reports the suppressed counts (\"+N idle\", \"+N background\") so nothing is lost.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" backend actions ") + "\n")
	b.WriteString("    " + badge("k") + mu("  pg_cancel_backend (SIGINT)  — cancels the current statement, leaves the connection open") + "\n")
	b.WriteString("    " + badge("x") + mu("  pg_terminate_backend (SIGTERM) — closes the connection entirely") + "\n")
	b.WriteString("    " + mu("    Both require a confirmation: press y to execute, any other key to abort.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" navigation ") + "\n")
	b.WriteString("    " + badge("↵") + mu("  drill into top-queries detail for the selected row's query_id (when available)") + "\n")
	b.WriteString("    " + badge("C") + mu("  configure visible columns") + "\n")
	b.WriteString("    " + badge("←") + mu("/") + badge("→") + mu("  cycle sort column · ") +
		badge("r") + mu(" reverse sort order") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" columns ") + mu("  C opens the column picker — use it to enable opt-in columns") + "\n")
	col := func(name, desc string) {
		b.WriteString("    " + padRight(name, 10) + mu(desc) + "\n")
	}
	for _, d := range actColumnRegistry() {
		col(d.name, d.desc)
	}
	b.WriteString("\n")
	b.WriteString("  " + styleHeader.Render(" OS metrics (rss / cpu% / read/s / write/s) ") + "\n")
	b.WriteString("    " + mu("Sourced from /proc/<pid>/{status,stat,io} — only available on Linux when pgdu runs on the same host.") + "\n")
	b.WriteString("    " + mu("rss and cpu% are readable as any user. read/s and write/s require the same UID as the postgres") + "\n")
	b.WriteString("    " + mu("process (or root); they show — when permission is denied. cpu% shows — on the first sample") + "\n")
	b.WriteString("    " + mu("(needs two readings to compute a delta); it tracks one full core = 100%.") + "\n")
	b.WriteString("\n")

	b.WriteString("  " + styleHeader.Render(" state colours ") + "\n")
	b.WriteString("    " + styleSelected.Render("active") + mu("  — executing a query (running, or in a Client/Activity/Timeout non-contention wait)") + "\n")
	b.WriteString("    " + styleErr.Render("waiting") + mu("  — running but blocked on real contention (Lock/LWLock/BufferPin/IO/IPC/Extension)") + "\n")
	b.WriteString("    " + mu("idle-in-xact") + mu("  — holding an open transaction without running a query (lock risk!)") + "\n")
	b.WriteString("    " + mu("idle") + mu("  — parked on Client/ClientRead waiting for the next statement (hidden unless v / all)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" age colours ") + mu("  query_age · xact_age · state_age, by magnitude") + "\n")
	b.WriteString("    " + durationStyle(1).Render("ms") + mu(" sub-second (fresh) · ") +
		durationStyle(2000).Render("s") + mu(" seconds · ") +
		durationStyle(120000).Render("min") + mu(" minutes · ") +
		durationStyle(7200000).Render("h+") + mu(" an hour or more (long-running — investigate)") + "\n")

	return padInfo(&b, height)
}

// renderActivityError renders the error state for the activity level.  This is
// called from the standard error path in view.go; only the notice-like status
// is rendered here for the activity-specific backend action pending state, used
// inline in renderDiagResult.
func activityPendingBanner(s *screen) string {
	if s.pendingBackendAction == "" {
		return ""
	}
	var action string
	switch s.pendingBackendAction {
	case "cancel":
		action = "cancel (SIGINT)"
	case "terminate":
		action = "terminate (SIGTERM)"
	default:
		action = s.pendingBackendAction
	}
	return styleErr.Render(fmt.Sprintf(
		"  ⚠  %s backend %d — press y to confirm, any other key to cancel",
		action, s.pendingBackendPID,
	))
}
