package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// Column widths shared by the progress-monitor rows and barReserve, so the
// bar auto-sizes against the same budgets the renderer prints with.
const (
	progColCmd       = 13 // "CREATE INDEX" is the widest command
	progColPhase     = 26 // e.g. "building index: scanning table"  (clipped)
	progColDoneTotal = 20 // "12345678 / 98765432" or "12.34 MB / 1.20 GB"
	progColPct       = 6  // "99.9%"
	progColAge       = 8  // fmtAge output ("31.1s", "2.4d")
	progColUser      = 12
)

// rebuildProgressItems flattens s.progressRows into s.items so the generic
// cursor/filter/viewport machinery applies. Order comes from the SQL
// (pct DESC, pid) — there is no user sort on this level.
func (m *Model) rebuildProgressItems(s *screen) {
	s.items = s.items[:0]
	s.itemsRev++
	for _, r := range s.progressRows {
		s.items = append(s.items, item{
			name: fmt.Sprintf("%d %s %s %s %s", r.PID, r.Command, r.Relation, r.Phase, r.Username),
			data: r,
		})
	}
	s.clampCursor()
}

func (m *Model) renderProgress(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	if s.progressErr != nil {
		b.WriteString("  " + styleErr.Render("error: "+s.progressErr.Error()) + "\n")
		return padToHeight(&b, height, 1)
	}

	refresh := "off"
	if m.activityRefresh > 0 {
		refresh = m.activityRefresh.String()
	}
	b.WriteString("  " + styleSelected.Render("running operations") +
		mu(fmt.Sprintf("  ·  %d ops  ·  ⟳ %s  ·  ", len(s.progressRows), refresh)) +
		styleBadge.Render("d") + mu(" describe · ") +
		styleBadge.Render("t") + mu(" cadence") + "\n")
	used := 1

	if len(s.items) == 0 {
		b.WriteString("  " + styleBadge.Render("no operations in progress") +
			mu(" — rows appear while VACUUM / CREATE INDEX / ANALYZE / CLUSTER / COPY / base backups run") + "\n")
		return padToHeight(&b, height, used+1)
	}

	barW := m.barWidth(s)
	vis := s.visibleIndexes()
	rowsH := max(height-used, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		r, _ := s.items[vis[vi]].data.(pg.ProgressRow)
		b.WriteString(m.renderProgressRow(r, vi == s.cursor, barW) + "\n")
		used++
	}
	return padToHeight(&b, height, used)
}

// renderProgressRow renders one operation: command · relation · phase ·
// done/total · progress bar · pct · running time · user.
func (m *Model) renderProgressRow(r pg.ProgressRow, selected bool, barW int) string {
	mu := styleMuted.Render

	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
	}
	cmd := padRight(r.Command, progColCmd)
	if selected {
		cmd = styleSelected.Render(cmd)
	}

	pct := r.Pct()
	var bar, pctStr string
	if pct < 0 {
		// Total still unknown (e.g. base backup before its size estimate):
		// empty bar keeps the layout stable, em-dash marks the unknown.
		bar = paintBar(barW)
		pctStr = "—"
	} else {
		filled := min(int(float64(barW)*pct/100), barW)
		bar = paintBar(barW, barSegment{cells: filled, style: styleBar})
		pctStr = fmt.Sprintf("%.1f%%", pct)
	}

	var age string
	if r.RunningMs > 0 {
		age = durationStyle(r.RunningMs).Render(padRight(fmtAge(r.RunningMs), progColAge))
	} else {
		age = strings.Repeat(" ", progColAge)
	}

	line := cursor +
		cmd + "  " +
		padRight(truncateToWidth(r.Relation, colName-1), colName) +
		mu(padRight(truncateToWidth(r.Phase, progColPhase-1), progColPhase)) +
		padLeft(progressDoneTotal(r), progColDoneTotal) + "  " +
		bar + " " +
		padLeft(pctStr, progColPct) + "  " +
		age +
		mu(truncateToWidth(r.Username, progColUser))
	if m.width > 4 && lipgloss.Width(line) > m.width {
		line = truncateToWidth(line, m.width)
	}
	return line
}

// progressDoneTotal formats the raw counters in their native unit: byte-based
// operations (COPY, base backup) humanized, block-based ones as plain counts.
// With no total yet, show just what's been done so far.
func progressDoneTotal(r pg.ProgressRow) string {
	if r.Unit == "bytes" {
		if r.Total <= 0 {
			return humanize.Bytes(r.Done)
		}
		return humanize.Bytes(r.Done) + " / " + humanize.Bytes(r.Total)
	}
	if r.Total <= 0 {
		return strconv.FormatInt(r.Done, 10)
	}
	return fmt.Sprintf("%d / %d", r.Done, r.Total)
}
