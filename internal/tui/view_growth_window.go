package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// --- size-sample browser (levelGrowthWindow) ---

// sizeSampleLabel is the row text for a sample, also the fuzzy-filter key.
func sizeSampleLabel(meta pg.SizeSampleMeta) string {
	return meta.CapturedAt.Local().Format("2006-01-02 15:04:05") + " · " + meta.Database
}

// renderGrowthWindow lists the on-disk size samples as a timeline range picker.
// Each row's bar is the database's total size at that capture, so the size
// curve is visible in the picker itself — which is the reason the tool has a
// picker rather than just taking the two newest files.
//
// Enter picks an endpoint: the first pick spans pick → now (a live end, which
// re-captures on refresh); with a start already applied, picking another row
// spans the time-ordered range between the two. Samples from another
// server/database/cluster are flagged and not loadable.
func (m *Model) renderGrowthWindow(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder

	gt := m.findLevel(levelGrowth)

	b.WriteString("\n")
	used := 5
	if s.growth.entry {
		b.WriteString("  " + styleSelected.Render("pick the window's start") + mu("  ·  "+m.snapshotDir) + "\n")
		b.WriteString("  " + m.renderGrowthEntrySummary(s) + "\n")
		b.WriteString("  " + styleBadge.Render("↵") + mu(" opens the growth table for everything since the highlighted sample · ") +
			styleBadge.Render("D") + mu(" delete · ") + styleBadge.Render("esc") + mu(" back") + "\n")
		used++
	} else {
		b.WriteString("  " + styleSelected.Render("size samples") + mu("  ·  "+m.snapshotDir) + "\n")
		b.WriteString("  " + m.renderGrowthWindowSummary(gt) + "\n")
		b.WriteString("  " + mu("↵ picks an endpoint (older=start, newer=end) · ") +
			styleBadge.Render("D") + mu(" delete · ") + styleBadge.Render("esc") + mu(" back") + "\n")
	}
	// Where new rows come from. Opening the tool takes the day's sample, but a
	// first-time user reaches this screen before ever seeing the S key, and
	// cron is still what fills the history in while nobody is looking.
	b.WriteString("  " + mu("new samples: one a day on open; ") + styleBadge.Render("S") +
		mu(" captures one now; `pgdu --sample` from cron fills the rest") + "\n\n")
	used++

	if s.growth.capturing {
		b.WriteString("  " + styleSelected.Render("capturing sizes…") +
			mu(" scanning every relation; this can take a while on a large cluster") + "\n")
		used++
	}
	if m.pendingDeleteSnap != "" {
		b.WriteString("  " + styleErr.Render("delete this sample? ") + mu("press ") +
			styleBadge.Render("y") + mu(" to confirm, any other key cancels") + "\n")
		used++
	}

	if len(s.items) == 0 {
		// The normal first run, not an error: say what to do about it. On a
		// first-ever open the daily capture is usually already running by now,
		// so the S nudge would contradict the banner just above.
		if s.growth.capturing {
			b.WriteString("  " + mu("no size samples yet — the first one is being captured now") + "\n")
		} else {
			b.WriteString("  " + mu("no size samples yet — press ") + styleBadge.Render("S") +
				mu(" to capture one now, or run ") + styleBadge.Render("pgdu --sample") +
				mu(" from cron for a real history") + "\n")
		}
		b.WriteString("\n  " + mu("two samples at least a minute apart make a window; the tool diffs them") + "\n")
		return fitInfo(&b, height)
	}

	listH := max(height-used, 1)
	startPath, endPath := "", ""
	if gt != nil {
		startPath, endPath = gt.growth.basePath, gt.growth.endPath
	}

	vis := s.visibleIndexes()
	var maxBytes int64
	for _, vi := range vis {
		if sz := s.items[vi].size; sz > maxBytes {
			maxBytes = sz
		}
	}
	barW := m.barWidth(s)
	s.offset, _ = viewportRange(s.cursor, s.offset, listH, len(vis))
	end := min(s.offset+listH, len(vis))
	for vi := s.offset; vi < end; vi++ {
		it := s.items[vi]
		anchor := it.snapPath == growthNow
		meta, _ := sizeMetaByPath(s.growth.metas, it.snapPath)
		// The anchor is the live server by definition, so it is never flagged.
		compatible, why := true, ""
		if !anchor {
			compatible, why = m.growthCompatible(s, meta)
		}

		cursor := "  "
		name := it.name
		if vi == s.cursor {
			cursor = lipgloss.NewStyle().Foreground(colorAccent).Render("▶ ")
			name = styleSelected.Render(name)
		}
		cells := 0
		if maxBytes > 0 {
			cells = int(float64(it.size) / float64(maxBytes) * float64(barW))
		}
		bar := paintBar(barW, barSegment{cells: cells, style: styleBar})
		size := padRight(humanize.Bytes(it.size), 10)
		age := padRight(m.growthSampleAge(it.snapPath, meta.CapturedAt), 9)

		var tags []string
		switch it.snapPath {
		case startPath:
			tags = append(tags, styleSelected.Render("◀ start"))
		case endPath:
			tags = append(tags, styleSelected.Render("◀ end"))
		}
		if !compatible {
			tags = append(tags, styleErr.Render(why))
		}
		row := cursor + bar + "  " + mu(size) + "  " + mu(age) + "  " + name
		if !anchor {
			row += "  " + mu(formatRows(int64(meta.RowCount))+" rel") +
				"  " + mu(filepath.Base(it.snapPath)) + "  " + mu(humanize.Bytes(meta.FileSize))
		}
		if len(tags) > 0 {
			row += "  " + strings.Join(tags, " ")
		}
		b.WriteString(row + "\n")
	}
	for i := end - s.offset; i < listH; i++ {
		b.WriteString("\n")
	}
	return fitInfo(&b, height)
}

// growthCompatible reports whether a sample can be diffed against the live
// server, and names the reason when it cannot.
//
// A zero DatabaseOID or SystemID means the identity was unreadable —
// pg_control_system() is revoked from PUBLIC — and must read as compatible.
// Degrading to "unknown" is the whole point; treating it as a mismatch would
// make the tool useless for every unprivileged role.
func (m *Model) growthCompatible(s *screen, meta pg.SizeSampleMeta) (bool, string) {
	return s.growthCompatibleVia(meta, m.target)
}

// growthCompatibleVia is the screen-local half, so the rule can be exercised
// without a Model.
func (s *screen) growthCompatibleVia(meta pg.SizeSampleMeta, target string) (bool, string) {
	switch {
	case meta.Target != target || meta.Database != s.db:
		return false, "other server/db"
	case meta.SystemID != 0 && s.growth.liveSystemID != 0 && meta.SystemID != s.growth.liveSystemID:
		return false, "other cluster"
	case meta.DatabaseOID != 0 && s.growth.liveOID != 0 && meta.DatabaseOID != s.growth.liveOID:
		return false, "db recreated"
	}
	return true, ""
}

// growthSampleAge renders the age column. The live anchor has no capture time.
func (m *Model) growthSampleAge(path string, capturedAt time.Time) string {
	if path == growthNow {
		return "now"
	}
	if capturedAt.IsZero() {
		return "—"
	}
	return relativeAge(time.Since(capturedAt))
}

// renderGrowthEntrySummary previews the window the highlighted row would open,
// since in entry mode nothing is applied yet.
func (m *Model) renderGrowthEntrySummary(s *screen) string {
	mu := styleMuted.Render
	cur, ok := s.currentItem()
	if !ok || cur.snapPath == "" {
		return mu("pick a sample to start the window at")
	}
	meta, _ := sizeMetaByPath(s.growth.metas, cur.snapPath)
	if meta.CapturedAt.IsZero() {
		return mu("window: ") + styleSelected.Render("?") + mu(" → now")
	}
	// fmtDuration, not relativeAge: this is a span, and "2d ago of history"
	// reads as a timestamp rather than a length.
	span := time.Since(meta.CapturedAt)
	return mu("window: ") + styleSelected.Render(meta.CapturedAt.Local().Format("2006-01-02 15:04")) +
		mu(" → now  ·  ") + styleSelected.Render(fmtDuration(span)) + mu(" of history")
}

// renderGrowthWindowSummary describes the window the table currently shows.
func (m *Model) renderGrowthWindowSummary(gt *screen) string {
	mu := styleMuted.Render
	if gt == nil || gt.growth.base == nil {
		return mu("no window applied yet — pick a sample")
	}
	w := gt.growth.window
	endLabel := "now"
	if gt.growth.endPath != growthNow {
		endLabel = w.EndAt.Local().Format("2006-01-02 15:04")
	}
	out := mu("window: ") + styleSelected.Render(w.BaseAt.Local().Format("2006-01-02 15:04")) +
		mu(" → ") + styleSelected.Render(endLabel) +
		mu("  ·  ") + styleSelected.Render(fmtDuration(w.Duration)) + mu(" span")
	if note := growthWindowNote(w); note != "" {
		out += "  " + styleErr.Render(note)
	}
	return out
}

// growthWindowNote names whatever makes this window less than straightforward,
// so a caveat is stated once rather than repeated per row.
func growthWindowNote(w pg.GrowthWindow) string {
	var notes []string
	if w.Identity != pg.IdentityOK {
		notes = append(notes, w.Identity.String())
	}
	if w.ClockSkew {
		notes = append(notes, "samples were out of order — ordered by capture time")
	}
	// Not a refusal: the byte deltas are measured whatever the spacing, and a
	// pair of captures seconds apart answers "what just grew". Only the
	// per-day extrapolation is worth a warning.
	if w.ShortWindow {
		notes = append(notes, "short window — per-day rates extrapolate from very little")
	}
	if w.StatsReset {
		notes = append(notes, "statistics were reset: row counts not comparable")
	}
	if w.FloorChanged {
		notes = append(notes, "different size floors: new/gone may be an artefact")
	}
	return strings.Join(notes, " · ")
}

// sizeMetaByPath finds a listed sample by its file path. It mirrors
// metaByPath for the snapshot browser rather than sharing it: the two metas
// are unrelated structs, and an interface to unify six lines would cost more
// than it saves.
func sizeMetaByPath(metas []pg.SizeSampleMeta, path string) (pg.SizeSampleMeta, bool) {
	for _, meta := range metas {
		if meta.Path == path {
			return meta, true
		}
	}
	return pg.SizeSampleMeta{}, false
}

// growthTrendNote names the trend column's axis, so the traces beside it can be
// read as a measurement rather than a decoration: a glyph is a span of time,
// and the reader has to be told which. It is empty when the column is absent.
//
// The span comes from the samples that actually landed on the axis, not from
// the retention setting — a history with a week missing out of its middle still
// spans the week, and the gaps say so glyph by glyph.
func growthTrendNote(t *growthTrend) string {
	switch t.state {
	case trendLoading:
		return "trend loading…"
	case trendReady:
		if t.hist == nil {
			return ""
		}
		first, last := t.hist.Span()
		if first.IsZero() {
			return ""
		}
		// Say what is on the axis, not what was read: under the uniform layout
		// samples sharing a cell fold together, and a header promising six
		// points over a trace showing three is the kind of small lie that
		// makes the rest of the screen harder to trust.
		samples := fmt.Sprintf("%d samples", t.hist.Samples)
		if t.hist.Plotted < t.hist.Samples {
			samples = fmt.Sprintf("%d of %d samples", t.hist.Plotted, t.hist.Samples)
		}
		// Only the uniform layout has a single span per cell to quote.
		axis := "gaps to scale"
		if t.hist.Bucket > 0 {
			axis = fmtDuration(t.hist.Bucket) + " per cell"
		}
		note := fmt.Sprintf("trend %s → %s · %s · %s",
			first.Local().Format("01-02"), last.Local().Format("01-02"),
			samples, axis)
		if t.hist.Skipped > 0 {
			// Samples from a restored cluster or a recreated database are left
			// off the axis; saying so is the difference between a short history
			// and a history that was censored.
			note += fmt.Sprintf(" (%d skipped)", t.hist.Skipped)
		}
		return note
	case trendNone:
		if t.note != "" {
			return "trend: " + t.note
		}
	}
	return ""
}

// growthHeaderLine is the one-line banner above the growth table: the window,
// the scope and any narrowing.
func (m *Model) growthHeaderLine(s *screen) string {
	mu := styleMuted.Render
	parts := []string{styleBadge.Render(s.growth.view.label())}
	if g := s.growth.group; g != nil {
		parts = append(parts, mu("in ")+styleSelected.Render(g.display))
	}
	w := s.growth.window
	endLabel := "now"
	if s.growth.endPath != growthNow {
		endLabel = w.EndAt.Local().Format("01-02 15:04")
	}
	parts = append(parts, mu(fmt.Sprintf("%s → %s (%s)",
		w.BaseAt.Local().Format("01-02 15:04"), endLabel, fmtDuration(w.Duration))))
	if s.growth.showGone {
		parts = append(parts, mu("incl. dropped"))
	}
	if o := s.growth.other; o.Parts > 0 {
		parts = append(parts, mu(fmt.Sprintf("+%d under the size floor", o.Parts)))
	}
	if t := growthTrendNote(&s.growth.trend); t != "" {
		parts = append(parts, mu(t))
	}
	line := "  " + strings.Join(parts, mu("  ·  "))
	if note := growthWindowNote(w); note != "" {
		line += "\n  " + styleErr.Render(note)
	}
	return line
}
