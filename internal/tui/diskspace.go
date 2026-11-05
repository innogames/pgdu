package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// diskSpaceMsg carries one probe of the server's data-directory filesystem.
// A failed probe is a nil space and not an error the user sees: not being on
// the database host is the normal reason, and the disk figures simply stay
// absent (pg.ErrDiskSpaceUnknown).
type diskSpaceMsg struct{ space *pg.DiskSpace }

// diskSpaceMaxAge is how long a probe is reused before the disk tool re-reads
// it. Free space moves with every other session's work, so a figure a reindex
// gate consults must not be from the start of the session; a statfs is cheap
// enough that this only avoids re-probing within one burst of screen loads.
const diskSpaceMaxAge = 15 * time.Second

func (m *Model) diskSpaceCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		space, err := m.client.DiskSpace(ctx, m.client.DefaultDB())
		if err != nil {
			return diskSpaceMsg{}
		}
		return diskSpaceMsg{space: space}
	})
}

// refreshDiskSpaceCmd re-probes when the figure on hand is older than
// diskSpaceMaxAge, and returns nil otherwise so a caller can batch it
// unconditionally.
func (m *Model) refreshDiskSpaceCmd() tea.Cmd {
	if time.Since(m.diskProbed) < diskSpaceMaxAge {
		return nil
	}
	m.diskProbed = time.Now()
	return m.diskSpaceCmd()
}

// diskSpaceLabel is the data directory's filesystem on the status row:
// how full it is and what is left. "" when pgdu is not on the database host.
//
// It is the one figure on these screens that is about the disk rather than
// about Postgres — every size the lists show is a relation's, and a cluster
// can be the smallest thing on a full filesystem. The percentage is graded,
// so the row that matters is the one that is coloured.
func (m *Model) diskSpaceLabel() string {
	d := m.disk
	if d == nil || d.Total <= 0 {
		return ""
	}
	pct := d.UsedPct()
	return "disk: " + diskFullStyle(pct).Render(fmt.Sprintf("%.0f%% full", pct)) +
		styleMuted.Render(" · "+humanize.Bytes(d.Avail)+" free of "+humanize.Bytes(d.Total))
}

// diskFullStyle grades how full a filesystem is. Unlike the percentages the
// overview grades, more is strictly worse here and the top of the scale is a
// server that stops writing, so the bands are tight at the full end.
func diskFullStyle(pct float64) lipgloss.Style {
	switch {
	case pct >= 95:
		return lipgloss.NewStyle().Foreground(colorError).Bold(true)
	case pct >= 85:
		return lipgloss.NewStyle().Foreground(colorAccent)
	default:
		return lipgloss.NewStyle().Foreground(colorOK)
	}
}
