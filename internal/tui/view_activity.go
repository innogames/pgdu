package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderActivityHeader renders the one-line summary bar above the activity list:
// counts by state (active / waiting / idle-in-transaction / …), the current
// filter mode, and the refresh cadence.
func (m *Model) renderActivityHeader(s *screen) string {
	mu := styleMuted.Render
	var counts [4]int // 0=active, 1=waiting, 2=idle-in-transaction, 3=other
	for _, r := range s.actRows {
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

	summary := strings.Join(parts, mu(" · "))

	// Filter mode and refresh cadence badges on the right side.
	filter := styleBadge.Render("filter: " + s.actFilter.Label())
	var refresh string
	if m.activityRefresh > 0 {
		refresh = styleBadge.Render(fmt.Sprintf("refresh: %s", m.activityRefresh))
	} else {
		refresh = styleBadge.Render("refresh: off")
	}

	return "  " + summary + "  " + filter + " " + refresh
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
