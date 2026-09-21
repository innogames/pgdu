package tui

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// --- snapshots browser (levelSnapshots) ---

// snapshotLabel is the row text for a snapshot, also the fuzzy-filter key. It
// leads with the capture time (newest-first list) and the database it covers.
func snapshotLabel(meta pg.SnapshotMeta) string {
	return meta.CapturedAt.Local().Format("2006-01-02 15:04:05") + " · " + meta.Database
}

// renderStatementSnapshots lists the on-disk snapshots as a timeline range
// picker. The query count is drawn as a bar so the relative size of each capture
// reads at a glance; each row shows its age, database and — for the rows backed
// by a file — its name and compressed on-disk size. The applied window's
// endpoints carry ◀ start / ◀ end markers and the header sums the window up
// (live with its refresh cadence, or frozen). Enter picks an endpoint: the first
// pick spans pick → now (live); with a start already applied, picking another
// row spans the time-ordered range between the two — frozen unless an endpoint
// is "now". Snapshots from another server/database are flagged, not loadable.
func (m *Model) renderStatementSnapshots(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	st := m.findLevel(levelStatements)
	curDB := ""
	if st != nil {
		curDB = st.db
	}

	b.WriteString("\n")
	used := 0
	if s.stat.entry {
		// Entry picker: nothing is applied yet, so the summary previews the window
		// the highlighted row would open rather than describing the table's state.
		b.WriteString("  " + styleSelected.Render("pick the window's base") + mu("  ·  "+m.snapshotDir) + "\n")
		b.WriteString("  " + m.renderSnapshotEntrySummary(s) + "\n")
		b.WriteString("  " + styleBadge.Render("↵") + mu(" opens the queries table with everything since the highlighted point · ") +
			styleBadge.Render("D") + mu(" delete · ") + styleBadge.Render("esc") + mu(" back") + "\n")
		// Point at where new rows come from: the list only grows through S in the
		// table, and a first-time user sees this picker before ever seeing that key.
		b.WriteString("  " + mu("new snapshots: press ") + styleBadge.Render("S") + mu(" (shift-s) in the queries table to save the current counters here; ") +
			styleBadge.Render("L") + mu(" reopens this browser") + "\n\n")
		used++
	} else {
		b.WriteString("  " + styleSelected.Render("query snapshots") + mu("  ·  "+m.snapshotDir) + "\n")
		b.WriteString("  " + m.renderSnapshotWindowSummary(st) + "\n")
		b.WriteString("  " + mu("↵ picks an endpoint (older=start, newer=end) · ") +
			styleBadge.Render("D") + mu(" delete · ") + styleBadge.Render("esc") + mu(" back") + "\n\n")
	}
	used += 5

	if m.pendingDeleteSnap != "" {
		b.WriteString("  " + styleErr.Render("delete this snapshot? ") + mu("press ") +
			styleBadge.Render("y") + mu(" to confirm, any other key cancels") + "\n")
		used++
	}

	if len(s.items) == 0 {
		b.WriteString("  " + mu("no snapshots yet — press ") + styleBadge.Render("S") +
			mu(" in the queries view to save one") + "\n")
		return padInfo(&b, height)
	}

	listH := max(height-used, 1)
	// The applied window's endpoints, for the ◀ start / ◀ end row markers.
	startPath, endPath := "", ""
	if st != nil {
		startPath, endPath = m.appliedWindowPaths(st, s)
	}
	vis := s.visibleIndexes()
	var maxCount int64
	for _, vi := range vis {
		if sz := s.items[vi].size; sz > maxCount {
			maxCount = sz
		}
	}
	barW := m.barWidth(s)
	s.offset, _ = viewportRange(s.cursor, s.offset, listH, len(vis))
	end := min(s.offset+listH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vi]
		anchor := it.snapPath == snapNow || it.snapPath == snapReset || it.snapPath == snapSession
		meta, _ := metaByPath(s.stat.snapMetas, it.snapPath)
		// Anchors carry no server/db identity — they always apply to the current
		// database, so they're never flagged incompatible.
		compatible := anchor || (meta.Target == m.target && meta.Database == curDB)

		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		cells := 0
		if maxCount > 0 {
			cells = int(float64(it.size) / float64(maxCount) * float64(barW))
		}
		bar := paintBar(barW, barSegment{cells: cells, style: styleBar})
		count := padRight(formatRows(it.size)+"q", 7)
		age := padRight(m.snapshotAge(s, it.snapPath, meta.CapturedAt), 9)

		var tags []string
		switch it.snapPath {
		case startPath:
			tags = append(tags, styleSelected.Render("◀ start"))
		case endPath:
			tags = append(tags, styleSelected.Render("◀ end"))
		}
		if s.stat.entry && it.snapPath == snapSession {
			tags = append(tags, mu("default"))
		}
		if !compatible {
			tags = append(tags, styleErr.Render("other server/db"))
		}
		// Only real snapshots have a backing file to show; anchors are virtual.
		row := cursor + bar + "  " + mu(count) + "  " + mu(age) + "  " + name
		if !anchor {
			row += "  " + mu(filepath.Base(it.snapPath)) + "  " + mu(humanize.Bytes(meta.FileSize))
		}
		if len(tags) > 0 {
			row += "  " + strings.Join(tags, " ")
		}
		b.WriteString(row + "\n")
	}
	for i := end - s.offset; i < listH; i++ {
		b.WriteString("\n")
	}

	return padInfo(&b, height)
}

// snapshotAge renders the age column for a browser row. The synthetic anchors
// have no meta CapturedAt, so their reference time is derived: "now" is live,
// "session start" dates from the recorded session baseline, and "since last
// reset" from the live pg_stat_statements stats_reset (unknown until sampled).
func (m *Model) snapshotAge(s *screen, path string, capturedAt time.Time) string {
	switch path {
	case snapNow:
		return "now"
	case snapSession:
		if st := m.findLevel(levelStatements); st != nil && !st.stat.sessionStart.IsZero() {
			return relativeAge(time.Since(st.stat.sessionStart))
		}
		return "now" // entry picker: the session starts when the pick lands
	case snapReset:
		if s.stat.liveReset.IsZero() {
			return "—"
		}
		return relativeAge(time.Since(s.stat.liveReset))
	default:
		return relativeAge(time.Since(capturedAt))
	}
}

// renderSnapshotEntrySummary is the entry picker's header line: the window the
// highlighted row would open, always ending at the live "now" since no start is
// applied yet to pair a frozen end with.
func (m *Model) renderSnapshotEntrySummary(s *screen) string {
	mu := styleMuted.Render
	start := "session start"
	if cur, ok := s.currentItem(); ok {
		switch cur.snapPath {
		case snapSession:
		case snapReset:
			start = "since last reset"
		default:
			if meta, ok := metaByPath(s.stat.snapMetas, cur.snapPath); ok {
				start = meta.CapturedAt.Local().Format("2006-01-02 15:04:05")
			}
		}
	}
	return mu("window: ") + styleSelected.Render(start) + mu(" → ") +
		styleSelected.Render("now") + mu("  ·  live · refresh "+m.refreshLabel())
}

// renderSnapshotWindowSummary is the browser header's one-line description of
// the applied window: "window: <start> → <end> · live · refresh 2s" (or
// "· frozen" when the end is a snapshot, where nothing re-samples).
func (m *Model) renderSnapshotWindowSummary(st *screen) string {
	mu := styleMuted.Render
	if st == nil {
		return mu("window: —")
	}
	start := "since " + st.stat.baselineAt.Format("15:04:05") // a fresh R re-base
	switch {
	case st.stat.cumulative:
		start = "since last reset"
	case st.stat.baseSnap != nil:
		start = st.stat.baseSnap.CapturedAt.Local().Format("2006-01-02 15:04:05")
	case !st.stat.sessionStart.IsZero() && st.stat.baselineAt.Equal(st.stat.sessionStart):
		start = "session start"
	}
	end, mode := "now", "live · refresh "+m.refreshLabel()
	if st.stat.endSnap != nil {
		end = st.stat.endSnap.CapturedAt.Local().Format("2006-01-02 15:04:05")
		mode = "frozen"
	}
	return mu("window: ") + styleSelected.Render(start) + mu(" → ") +
		styleSelected.Render(end) + mu("  ·  "+mode)
}
