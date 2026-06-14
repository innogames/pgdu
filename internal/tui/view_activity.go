package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderActivityHeader renders the one-line summary bar above the activity list:
// counts by state (active / waiting / idle-in-transaction / …), the current
// filter mode, verbose status, and the refresh cadence.
func (m *Model) renderActivityHeader(s *screen) string {
	mu := styleMuted.Render

	// Count states over the currently visible (filtered) rows so the summary
	// matches what the table shows, not the raw snapshot.
	visible := visibleActRows(s.actRows, s.actVerbose)
	var counts [4]int // 0=active, 1=waiting, 2=idle-in-transaction, 3=other
	for _, r := range visible {
		switch {
		case r.State == "active" && r.WaitEvent == "":
			counts[0]++
		case r.WaitEvent != "":
			counts[1]++
		case strings.HasPrefix(r.State, "idle in transaction"):
			counts[2]++
		default:
			counts[3]++
		}
	}

	var parts []string
	label := func(n int, name string, style lipgloss.Style) string {
		if n == 0 {
			return mu(fmt.Sprintf("%d %s", n, name))
		}
		return style.Render(fmt.Sprintf("%d %s", n, name))
	}
	parts = append(parts,
		label(counts[0], "active", styleSelected),
		label(counts[1], "waiting", styleErr),
		label(counts[2], "idle-in-xact", styleMuted),
	)
	if counts[3] > 0 {
		parts = append(parts, mu(fmt.Sprintf("%d other", counts[3])))
	}
	// When auxiliary backends are hidden, show how many are suppressed so the
	// user knows `v` would reveal more rows.
	if !s.actVerbose {
		hidden := len(s.actRows) - len(visible)
		if hidden > 0 {
			parts = append(parts, mu(fmt.Sprintf("+%d background", hidden)))
		}
	}

	summary := strings.Join(parts, mu(" · "))

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

	return "  " + summary + "  " + filter + verboseBadge + " " + refresh
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
	b.WriteString("    " + badge("v") + mu("  show/hide auxiliary backends (walwriter, checkpointer, launchers, io workers…)") + "\n")
	b.WriteString("    " + mu("    off by default — these run continuously, carry no query text, and rarely indicate problems.") + "\n\n")

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
	b.WriteString("    " + styleSelected.Render("active") + mu("  — executing a query (no wait event)") + "\n")
	b.WriteString("    " + styleErr.Render("waiting") + mu("  — blocked on a wait event (Lock/…, IO/…, Client/…)") + "\n")
	b.WriteString("    " + mu("idle-in-xact") + mu("  — holding an open transaction without running a query (lock risk!)") + "\n\n")

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
