package tui

import (
	"fmt"
	"strings"
	"time"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// The maintenance panel under the parts list, built on the same doctrine as
// the system overview: a finding is printed once, as a bar row coloured by its
// severity; everything healthy folds into the section header after a ✓, with
// its figure intact. The panel used to print six rows of muted text whatever
// the table's state was, which is the same as printing none.

const (
	// partsMaintLabelW is the panel's label column. The overview's 24 cells
	// are a gutter here: the labels are one word and the panel is two rows
	// tall on a healthy table.
	partsMaintLabelW = 9

	// maintNoticeFrac is when a *folded* figure is worth the header space at
	// all. Below it a counter is background noise — the 7 %-of-insert-trigger
	// kind — and the header shows only what it replaced. It is a display rule,
	// not a grading band: the bands live in pg.TableAdvice.
	maintNoticeFrac = 0.90
)

// maintPanelRow is one line of the panel: a bar row when bar is set, a plain
// value row otherwise. note is the advice reason, already styled.
type maintPanelRow struct {
	label  string
	bar    string
	figure string
	pct    string
	note   string
}

// renderMaintPanel renders the maintenance section below the parts list.
// Returns "" until the stats have loaded; degrades to a single muted line on
// error. Two lines on a healthy table, one row per finding on a sick one.
func (m *Model) renderMaintPanel(s *screen) string {
	if s.parts.tableStats == nil && s.parts.tableStatsErr == nil {
		return ""
	}
	mu := styleMuted.Render
	if s.parts.tableStatsErr != nil {
		return "  " + mu("maintenance stats unavailable: "+s.parts.tableStatsErr.Error()) + "\n"
	}
	st := s.parts.tableStats
	adv := pg.TableAdvice(st)

	rows, folded := m.maintPanelRows(s, st, adv)
	var b strings.Builder
	b.WriteString(maintPanelHeader(adv, folded))
	if len(rows) == 0 {
		return b.String()
	}
	figs := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.bar != "" {
			figs = append(figs, r.figure)
		}
	}
	figW := figureColW(figs...)
	for _, r := range rows {
		b.WriteString(m.maintPanelLine(r, figW))
	}
	return b.String()
}

// maintPanelLine lays one row out and drops its note when the terminal cannot
// hold it — the note explains the row, it never carries a number the row does
// not already show.
//
// It lays the cells out itself rather than through barCellsW, which skips an
// empty percentage column entirely: here a bar-less row (autovac, options)
// sits in the same run as barred ones, and a column that collapses on some
// rows and not others is worse than one that is sometimes blank.
func (m *Model) maintPanelLine(r maintPanelRow, figW int) string {
	// A row with no quantity (autovac, options) is a plain value row, as in
	// the overview: it starts where the bar would, rather than reserving a bar
	// of blanks it has no use for.
	cells := r.figure
	if r.bar != "" {
		cells = r.bar + "  " + padRight(r.figure, figW) + "  " + padRight(r.pct, barPctW)
	}
	line := maintRowW(r.label, strings.TrimRight(cells+r.note, " "), partsMaintLabelW)
	if r.note == "" || m.width <= 0 || displayWidth(strings.TrimRight(line, "\n")) <= m.width {
		return line
	}
	return maintRowW(r.label, strings.TrimRight(cells, " "), partsMaintLabelW)
}

// maintPanelHeader is the section title: the worst severity's glyph, then the
// metrics that had nothing to report, each still carrying its figure. A short
// header with a long ✓ list is the good outcome, not a missing panel.
func maintPanelHeader(adv pg.AdviceSet, folded []string) string {
	h := "  " + styleMuted.Render("─── maintenance")
	worst := pg.AdviceInfo
	for _, a := range adv {
		if a.Level > worst {
			worst = a.Level
		}
	}
	if worst > pg.AdviceInfo {
		h += " " + adviceStyle(worst).Render(adviceGlyph(worst))
	}
	if len(folded) > 0 {
		h += "  " + styleMuted.Render("✓ "+strings.Join(folded, " · "))
	}
	return h + "\n"
}

// maintPanelRows builds the rows to print and the figures to fold, in one
// pass: a metric is one or the other, never both.
func (m *Model) maintPanelRows(s *screen, st *pg.TableMaintStats, adv pg.AdviceSet) (rows []maintPanelRow, folded []string) {
	barW := barWidthFor(m.width, partsMaintLabelW)
	// The folds are assembled in a fixed order at the end, so the header reads
	// the same whichever metrics happened to earn a row.
	var foldVacuum, foldAnalyze, foldDead, foldInserts, foldFreeze string

	// Vacuum and analyze recency never earn a row: a static table nobody has
	// vacuumed in a year is not a fault, and the dead-row count says anything
	// the clock could.
	foldVacuum = "vacuum " + maintLastAge(st.LastVacuum, st.LastAutovacuum)

	// Only a warning or worse earns a row. An Info finding is still a state,
	// and states fold — the freeze one below is the only rule that emits any.
	flagged := func(key string) *pg.Advice {
		if a := adv.Find(key); a != nil && a.Level >= pg.AdviceWarn {
			return a
		}
		return nil
	}

	// Dead rows.
	if a := flagged("table_dead_rows"); a != nil {
		as := adviceStyle(a.Level)
		rows = append(rows, maintPanelRow{
			label:  "dead",
			bar:    shareBar(st.NLive+st.NDead, barW, barPart{n: st.NDead, style: as}),
			figure: formatRows(st.NDead) + " of " + formatRows(st.NLive+st.NDead) + " rows",
			pct:    as.Render(fmt.Sprintf("%.0f%%", st.DeadShare()*100)),
			note:   "  " + as.Render(a.Reason),
		})
	} else {
		foldDead = fmt.Sprintf("dead %.0f%%", st.DeadShare()*100)
	}

	// Statistics.
	switch a := flagged("table_never_analyzed"); {
	case a != nil:
		as := adviceStyle(a.Level)
		rows = append(rows, maintPanelRow{
			label:  "analyze",
			bar:    gaugeBar(0, as, barW),
			figure: "never analyzed",
			note:   "  " + as.Render(a.Reason),
		})
	default:
		if a := flagged("table_analyze_due"); a != nil {
			as := adviceStyle(a.Level)
			trig, _ := st.AnalyzeTriggerAt()
			rows = append(rows, maintPanelRow{
				label:  "analyze",
				bar:    gaugeBar(maintFactor(st.NModSinceAnalyze, trig), as, barW),
				figure: formatRows(st.NModSinceAnalyze) + " of " + formatRows(trig) + " rows",
				pct:    as.Render(fmt.Sprintf("%.1f×", maintRatio(st.NModSinceAnalyze, trig))),
				note:   "  " + as.Render(a.Reason),
			})
		} else {
			foldAnalyze = "analyze " + maintLastAge(st.LastAnalyze, st.LastAutoanalyze)
		}
	}

	// The insert-only vacuum never earns a row of its own — falling behind it
	// costs index-only scans, not space — but it is worth naming once it is
	// nearly due, since nothing else on the screen counts inserts.
	if trig, ok := st.InsertTriggerAt(); ok && trig > 0 {
		if r := maintRatio(st.NInsSinceVacuum, trig); r >= maintNoticeFrac {
			foldInserts = fmt.Sprintf("inserts %.0f%%", r*100)
		}
	}

	// Freezing. A partitioned parent has no relfrozenxid worth the name.
	if st.RelKind != "p" && st.FreezeMaxAge > 0 {
		if a := flagged("table_freeze"); a != nil {
			as := adviceStyle(a.Level)
			rows = append(rows, maintPanelRow{
				label: "freeze",
				// The meter is distance to *danger* (the vacuum failsafe), not
				// to the next routine anti-wraparound vacuum: against the
				// latter every busy table sits permanently full.
				bar:    gaugeBar(st.FreezeDangerFrac(), as, barW),
				figure: formatRows(st.FrozenXIDAge) + " of " + formatRows(st.FreezeMaxAge),
				pct:    as.Render(fmt.Sprintf("%.1f×", st.FreezeFactor())),
				note:   "  " + as.Render(a.Reason),
			})
		} else {
			foldFreeze = fmt.Sprintf("freeze %.0f%%", st.FreezeFactor()*100)
			if adv.Find("table_freeze") != nil {
				// The Info band: freezing is behind enough that the next
				// anti-wraparound vacuum is the one that will catch it. Worth
				// naming, not worth a row — it is the system working.
				foldFreeze += " (anti-wraparound due)"
			}
		}
	}

	// A table nothing will maintain. No bar: it is a state, not a quantity.
	if a := flagged("table_autovacuum_off"); a != nil {
		as := adviceStyle(a.Level)
		rows = append(rows, maintPanelRow{
			label: "autovac", figure: as.Render(a.Current), note: "  " + as.Render(a.Reason),
		})
	}

	// Storage parameters someone set deliberately, minus the one that is
	// already its own row above.
	if opts := pg.RelOptionsNote(st.RelOptions); opts != "" {
		rows = append(rows, maintPanelRow{label: "options", figure: styleMuted.Render(opts)})
	}

	if r, ok := m.maintReclaimRow(s, barW); ok {
		rows = append(rows, r)
	}
	for _, f := range []string{foldVacuum, foldAnalyze, foldDead, foldInserts, foldFreeze} {
		if f != "" {
			folded = append(folded, f)
		}
	}
	return rows, folded
}

// maintReclaimRow is the measured waste against the table's total size, split
// by the statement that would hand it back. Index waste a REINDEX returns to
// the filesystem; heap waste VACUUM only makes reusable by the table — the
// file does not shrink. Silent until something has been measured.
func (m *Model) maintReclaimRow(s *screen, barW int) (maintPanelRow, bool) {
	var heapWaste, idxWaste, total int64
	for _, it := range s.items {
		p, ok := it.data.(pg.Part)
		if !ok {
			continue
		}
		total += p.SizeBytes
		if !it.hasBloat {
			continue
		}
		if p.Kind == pg.PartIndex {
			idxWaste += it.bloat
		} else {
			heapWaste += it.bloat
		}
	}
	waste := heapWaste + idxWaste
	if waste == 0 || total == 0 {
		return maintPanelRow{}, false
	}
	pct := int(float64(waste) / float64(total) * 100)
	st := bloatPercentStyle(pct)
	var parts []string
	if idxWaste > 0 {
		parts = append(parts, humanize.Bytes(idxWaste)+" idx (REINDEX frees)")
	}
	if heapWaste > 0 {
		parts = append(parts, humanize.Bytes(heapWaste)+" heap (VACUUM reuses)")
	}
	return maintPanelRow{
		label:  "reclaim",
		bar:    gaugeBar(float64(waste)/float64(total), st, barW),
		figure: humanize.Bytes(waste) + " of " + humanize.Bytes(total),
		pct:    st.Render(fmt.Sprintf("%d%%", pct)),
		note:   "  " + styleMuted.Render(strings.Join(parts, " · ")),
	}, true
}

// maintLastAge is the "3d ago" / "never" text of a folded recency, preferring
// whichever of the manual and automatic timestamps is newer and tagging the
// manual one — autovacuum is the expected actor, a hand-run vacuum is news.
func maintLastAge(manual, auto *time.Time) string {
	switch {
	case manual != nil && (auto == nil || manual.After(*auto)):
		return maintShortAge(time.Since(*manual)) + " (manual)"
	case auto != nil:
		return maintShortAge(time.Since(*auto))
	}
	return "never"
}

// maintShortAge is relativeAge without the "ago": the ✓ header is a list of
// states, not of events, and the word is six cells repeated four times.
func maintShortAge(d time.Duration) string {
	return strings.TrimSuffix(relativeAge(d), " ago")
}

// maintRatio is n/trigger as an unclamped ratio; 0 when the trigger is unknown.
func maintRatio(n, trigger int64) float64 {
	if trigger <= 0 {
		return 0
	}
	return float64(n) / float64(trigger)
}

// maintFactor is maintRatio clamped for a gauge.
func maintFactor(n, trigger int64) float64 {
	return min(maintRatio(n, trigger), 1)
}
