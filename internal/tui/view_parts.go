package tui

import (
	"fmt"
	"strings"
	"time"

	"pgdu/internal/pg"
)

// renderPartsLevel is the top-level renderer for levelParts. It either shows
// the parts list + vacuum output pane (when a vacuum is in progress or done for
// this table), or the parts list + maintenance stats panel below.
func (m *Model) renderPartsLevel(s *screen, height int) string {
	if m.vacuumPaneVisible(s) {
		paneH := vacuumPaneHeight(height)
		listH := max(height-paneH, 1)
		return m.renderList(s, listH) + m.renderVacuumPane(paneH)
	}
	panel := m.renderMaintPanel(s)
	if panel != "" {
		panelLines := strings.Count(panel, "\n")
		listH := height - panelLines
		if listH < 3 {
			// Terminal too short for both — drop the panel.
			return m.renderList(s, height)
		}
		return m.renderList(s, listH) + panel
	}
	return m.renderList(s, height)
}

// vacuumPaneHeight returns the number of lines reserved for the vacuum output
// pane given the total available content height.
func vacuumPaneHeight(h int) int {
	pane := min(max(h/2, 6), 16)
	if pane > h-3 {
		pane = max(h-3, 2)
	}
	return pane
}

// renderVacuumBanner renders the one-line confirmation prompt for the vacuum
// action on the parts level. Returns "" when there is nothing to show.
func (m *Model) renderVacuumBanner(s *screen) string {
	if s.level != levelParts || !s.pendingVacuum {
		return ""
	}
	return "  " + styleSelected.Render("confirm: ") +
		styleMuted.Render("VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) "+s.table.Qualified()+" — press ") +
		styleBadge.Render("y") +
		styleMuted.Render(" to run, ") +
		styleBadge.Render("n") +
		styleMuted.Render(" (or any other key) to cancel")
}

// renderVacuumPane renders the streaming VACUUM output pane (header + output body).
func (m *Model) renderVacuumPane(paneH int) string {
	vs := m.vacuum
	var hdr string
	mu := styleMuted.Render
	// While running the clock ticks live; once finished it freezes at the
	// completion time so "done in …" doesn't keep counting up on every render.
	elapsed := time.Since(vs.started)
	if !vs.running && !vs.finished.IsZero() {
		elapsed = vs.finished.Sub(vs.started)
	}
	elapsed = elapsed.Round(time.Second)
	qual := vs.table.Qualified()
	switch {
	case vs.running:
		hdr = "  " + m.spinner.View() + " " + styleSelected.Render("VACUUM") +
			mu(" "+qual) +
			mu(fmt.Sprintf("  running %s", elapsed))
	case vs.err != nil:
		hdr = "  " + styleErr.Render("VACUUM "+qual+" failed: "+shortErr(vs.err)) +
			mu(fmt.Sprintf("  (after %s) · press ", elapsed)) +
			styleBadge.Render("esc") + mu(" to dismiss")
	default:
		hdr = "  " + styleBadge.Render("VACUUM") +
			mu(" "+qual) +
			mu(fmt.Sprintf("  done in %s · press ", elapsed)) +
			styleBadge.Render("esc") + mu(" to dismiss")
	}

	bodyH := paneH - 1
	if bodyH < 1 {
		return hdr + "\n"
	}

	if vs.follow {
		// Snap offset to end so scrollWindow tail-follows.
		vs.offset = len(vs.buf)
	}
	body := strings.Join(vs.buf, "\n")
	rendered := scrollWindow(body, &vs.offset, bodyH)
	// Re-evaluate follow: if offset landed at the bottom, keep following.
	lines := strings.Count(body, "\n")
	if vs.offset >= lines-bodyH+1 {
		vs.follow = true
	}

	return hdr + "\n" + rendered
}

// shortErr returns a single-line error summary trimmed to ~80 runes.
func shortErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80]) + "…"
	}
	return s
}

// --- maintenance stats panel ---

const maintLabelW = 9 // "analyze  " — align value columns

// renderMaintPanel renders the ~6-line maintenance stats section shown below
// the parts list. Returns "" when stats haven't loaded yet. On error, degrades
// to a single muted line. Always ends with a vacuum hint.
func (m *Model) renderMaintPanel(s *screen) string {
	if s.tableStats == nil && s.tableStatsErr == nil {
		return ""
	}
	mu := styleMuted.Render
	if s.tableStatsErr != nil {
		return "  " + mu("maintenance stats unavailable: "+s.tableStatsErr.Error()) + "\n" +
			"  " + mu("hint: press ") + styleBadge.Render("v") + mu(" to run VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) on this table") + "\n" +
			m.maintReindexHint(s) + "\n"
	}
	st := s.tableStats
	var b strings.Builder

	b.WriteString("  " + mu("─── maintenance") + "\n")
	b.WriteString("  " + mu(padRight("vacuum", maintLabelW)) + m.maintVacuumLine(st) + "\n")
	b.WriteString("  " + mu(padRight("analyze", maintLabelW)) + m.maintAnalyzeLine(st) + "\n")
	b.WriteString("  " + mu(padRight("tuples", maintLabelW)) + m.maintTuplesLine(st) + "\n")
	if st.RelKind != "p" {
		b.WriteString("  " + mu(padRight("freeze", maintLabelW)) + m.maintFreezeLine(st) + "\n")
	}
	if len(st.RelOptions) > 0 {
		b.WriteString("  " + mu(padRight("options", maintLabelW)) + mu(strings.Join(st.RelOptions, " · ")) + "\n")
	}
	b.WriteString("  " + mu("hint: press ") + styleBadge.Render("v") + mu(" to run VACUUM (VERBOSE, ANALYZE, SKIP_LOCKED) on this table") + "\n")
	b.WriteString(m.maintReindexHint(s) + "\n")
	return b.String()
}

// maintReindexHint returns the per-index REINDEX hint shown beneath the vacuum
// hint. Unlike vacuum, REINDEX has no always-visible affordance: it only arms
// when the highlighted row is an index whose *measured* bloat exceeds
// reindexBloatThreshold. So it silently disappears when bloat measuring is off
// (the `b` toggle leaves hasBloat false for every row) or once an index drops
// back under the threshold — which is why it can look like the option vanished.
// The hint names the index when the current row qualifies and explains the
// gate otherwise.
func (m *Model) maintReindexHint(s *screen) string {
	mu := styleMuted.Render
	if !m.fetchBloat {
		return "  " + mu("hint: press ") + styleBadge.Render("b") +
			mu(" to measure bloat — REINDEX is offered on indexes over 5% bloat")
	}
	if cand := reindexCandidate(s); cand != "" {
		return "  " + mu("hint: press ") + styleBadge.Render("enter") +
			mu(" to REINDEX "+cand+" CONCURRENTLY")
	}
	return "  " + mu("hint: select an index over 5% bloat, then press ") +
		styleBadge.Render("enter") + mu(" to REINDEX it CONCURRENTLY")
}

func (m *Model) maintVacuumLine(st *pg.TableMaintStats) string {
	mu := styleMuted.Render
	var parts []string

	// Last vacuum with source tag.
	switch {
	case st.LastVacuum != nil && (st.LastAutovacuum == nil || st.LastVacuum.After(*st.LastAutovacuum)):
		parts = append(parts, mu("last ")+relativeAge(time.Since(*st.LastVacuum))+mu(" (manual)"))
	case st.LastAutovacuum != nil:
		parts = append(parts, mu("last ")+relativeAge(time.Since(*st.LastAutovacuum))+mu(" (auto)"))
	default:
		parts = append(parts, mu("never vacuumed"))
	}

	// Insert-based trigger progress.
	if st.NInsSinceVacuum > 0 {
		if trig, ok := st.InsertTriggerAt(); ok && trig > 0 {
			pct := int(float64(st.NInsSinceVacuum) / float64(trig) * 100)
			parts = append(parts, mu(fmt.Sprintf("%s inserts (%d%% of insert trigger)", formatRows(st.NInsSinceVacuum), pct)))
		} else {
			parts = append(parts, mu(formatRows(st.NInsSinceVacuum)+" inserts since last vacuum"))
		}
	}

	return strings.Join(parts, mu(" · "))
}

func (m *Model) maintAnalyzeLine(st *pg.TableMaintStats) string {
	mu := styleMuted.Render
	var parts []string

	switch {
	case st.LastAnalyze != nil && (st.LastAutoanalyze == nil || st.LastAnalyze.After(*st.LastAutoanalyze)):
		parts = append(parts, mu("last ")+relativeAge(time.Since(*st.LastAnalyze))+mu(" (manual)"))
	case st.LastAutoanalyze != nil:
		parts = append(parts, mu("last ")+relativeAge(time.Since(*st.LastAutoanalyze))+mu(" (auto)"))
	default:
		parts = append(parts, mu("never analyzed"))
	}

	if st.NModSinceAnalyze > 0 {
		if trig, ok := st.AnalyzeTriggerAt(); ok && trig > 0 {
			pct := int(float64(st.NModSinceAnalyze) / float64(trig) * 100)
			parts = append(parts, mu(fmt.Sprintf("%s modified rows (%d%% of analyze trigger)", formatRows(st.NModSinceAnalyze), pct)))
		} else {
			parts = append(parts, mu(formatRows(st.NModSinceAnalyze)+" modified rows since last analyze"))
		}
	}

	return strings.Join(parts, mu(" · "))
}

func (m *Model) maintTuplesLine(st *pg.TableMaintStats) string {
	mu := styleMuted.Render
	var parts []string

	total := st.NLive + st.NDead
	if total > 0 {
		deadPct := int(float64(st.NDead) / float64(total) * 100)
		parts = append(parts, mu(formatRows(st.NLive)+" live · "+formatRows(st.NDead)+" dead"))
		if deadPct > 0 {
			s := mu(fmt.Sprintf("(%d%%)", deadPct))
			if deadPct >= 10 {
				s = styleBarAlt.Render(fmt.Sprintf("(%d%%)", deadPct))
			}
			parts = append(parts, s)
		}
	} else {
		parts = append(parts, mu("0 rows"))
	}

	if trig, ok := st.VacuumTriggerAt(); ok {
		pct := 0
		if trig > 0 {
			pct = int(float64(st.NDead) / float64(trig) * 100)
		}
		trigStr := mu(fmt.Sprintf("autovacuum threshold ~%s dead rows", formatRows(trig)))
		if st.NDead >= trig && trig > 0 {
			trigStr = styleErr.Render(fmt.Sprintf("autovacuum due (>%s dead rows)", formatRows(trig)))
		} else if pct >= 80 {
			trigStr = styleBarAlt.Render(fmt.Sprintf("autovacuum soon (~%s dead rows, %d%%)", formatRows(trig), pct))
		}
		parts = append(parts, trigStr)
	}

	if !st.AutovacuumEnabled() {
		parts = append(parts, styleErr.Render("autovacuum DISABLED"))
	}

	return strings.Join(parts, mu(" · "))
}

func (m *Model) maintFreezeLine(st *pg.TableMaintStats) string {
	mu := styleMuted.Render
	var parts []string

	if st.FreezeMaxAge > 0 || st.FrozenXIDAge > 0 {
		frac := st.FreezeFrac()
		pct := int(frac * 100)
		freezeStr := mu(fmt.Sprintf("transaction age %s / %s (%d%%)",
			formatRows(st.FrozenXIDAge), formatRows(st.FreezeMaxAge), pct))
		if pct >= 85 {
			freezeStr = styleErr.Render(fmt.Sprintf("transaction age %s / %s (%d%%) — freeze needed!",
				formatRows(st.FrozenXIDAge), formatRows(st.FreezeMaxAge), pct))
		}
		parts = append(parts, freezeStr)
	}

	// Last scan activity.
	if st.LastSeqScan != nil {
		parts = append(parts, mu("sequential scan ")+relativeAge(time.Since(*st.LastSeqScan)))
	}
	if st.LastIdxScan != nil {
		parts = append(parts, mu("index scan ")+relativeAge(time.Since(*st.LastIdxScan)))
	}

	return strings.Join(parts, mu(" · "))
}
