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
	progColPct       = 7  // "99.9%", or "~100.0%" for an estimated (approx) total
	progColAge       = 8  // fmtAge output ("31.1s", "2.4d")
	progColEta       = 8  // fmtAge output for the extrapolated time remaining
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
			name: fmt.Sprintf("%d %s %s %s %s %s", r.PID, r.Command, r.Database, r.Relation, r.Phase, r.Username),
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
	b.WriteString(m.renderProgressHeader(barW) + "\n")
	used++
	vis := s.visibleIndexes()
	rowsH := max(height-used, 0)
	if rowsH > 0 {
		s.offset, _ = viewportRange(s.cursor, s.offset, rowsH, len(vis))
	}
	end := min(s.offset+rowsH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		r, _ := s.items[vis[vi]].data.(pg.ProgressRow)
		b.WriteString(m.renderProgressRow(r, s.db, vi == s.cursor, barW) + "\n")
		used++
	}
	return padToHeight(&b, height, used)
}

// renderProgressRow renders one operation: command · relation · phase ·
// done/total · progress bar · pct · running time · user. db is the database
// the screen is connected through — operations elsewhere in the cluster get
// their relation prefixed with their own database name.
func (m *Model) renderProgressRow(r pg.ProgressRow, db string, selected bool, barW int) string {
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
		if r.Approx {
			// reltuples-derived estimate, not a server counter — flag it so the
			// bar doesn't read as an exact promise, and cap the display at 100%
			// since a stale reltuples can undershoot the live row count.
			pctStr = "~" + fmt.Sprintf("%.1f%%", min(pct, 100))
		}
	}

	var age string
	if r.RunningMs > 0 {
		age = durationStyle(r.RunningMs).Render(padRight(fmtAge(r.RunningMs), progColAge))
	} else {
		age = strings.Repeat(" ", progColAge)
	}

	// ETA is a derived estimate, not a server-reported counter — keep it muted so
	// it reads as a hint rather than a promise. Em-dash when it can't be computed.
	eta := mu(padRight(progressETA(r), progColEta))

	relation := r.Relation
	if r.Database != "" && r.Database != db {
		relation = r.Database + "." + relation
	}

	line := cursor +
		cmd + "  " +
		padRight(truncateToWidth(relation, colName-1), colName) +
		mu(padRight(truncateToWidth(r.Phase, progColPhase-1), progColPhase)) +
		padLeft(progressDoneTotal(r), progColDoneTotal) + "  " +
		bar + " " +
		padLeft(pctStr, progColPct) + "  " +
		age +
		eta +
		mu(truncateToWidth(r.Username, progColUser))
	if m.width > 4 && lipgloss.Width(line) > m.width {
		line = truncateToWidth(line, m.width)
	}
	return line
}

// renderProgressHeader is the muted column-label row above the operation list.
// Widths and gutters mirror renderProgressRow exactly so each label sits over
// its column; the bar span (barW inner cells + the two brackets) is left blank
// since the [▇░] fill is self-describing.
func (m *Model) renderProgressHeader(barW int) string {
	line := "  " + // cursor slot (colCursor)
		padRight("command", progColCmd) + "  " +
		padRight("relation", colName) +
		padRight("phase", progColPhase) +
		padLeft("done / total", progColDoneTotal) + "  " +
		strings.Repeat(" ", barW+colBrackets) + " " +
		padLeft("pct", progColPct) + "  " +
		padRight("elapsed", progColAge) +
		padRight("~eta", progColEta) +
		"user"
	if m.width > 4 && lipgloss.Width(line) > m.width {
		line = truncateToWidth(line, m.width)
	}
	return styleMuted.Render(line)
}

// progressETA linearly extrapolates the time remaining from the elapsed runtime
// and the done/total ratio: remaining ≈ elapsed × (total-done)/done. It is
// deliberately a "~eta" — phases that reset their counters (VACUUM cycles) or a
// non-uniform cost per unit make it a rough guide. Unknown totals, an un-started
// counter, or an already-complete phase yield an em-dash.
func progressETA(r pg.ProgressRow) string {
	if r.RunningMs <= 0 || r.Done <= 0 || r.Total <= 0 || r.Done >= r.Total {
		return "—"
	}
	remainingMs := r.RunningMs * float64(r.Total-r.Done) / float64(r.Done)
	return fmtAge(remainingMs)
}

// progressDoneTotal formats the raw counters in their native unit: byte-based
// operations (COPY, base backup) humanized, count-based ones (blocks, indexes,
// rows) as plain counts. With no total yet, show just what's been done so far.
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
