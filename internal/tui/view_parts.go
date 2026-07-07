package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderPartsLevel is the top-level renderer for levelParts. It either shows
// the parts list + vacuum output pane (when a vacuum is in progress or done for
// this table), or the parts list + maintenance stats panel below.
func (m *Model) renderPartsLevel(s *screen, height int) string {
	// Footer lines that sit directly beneath the parts list (with the table
	// they annotate): the Σ totals line and the size/bloat colour legend.
	// These are dropped first when the terminal is too short to fit everything.
	footer, footerH := m.partsFooter(s)

	if m.vacuumPaneVisible(s) {
		// Keep a fixed slice of the list for context (partsVacuumListRows rows
		// plus the 1-line header) and hand the rest of the area to the
		// scrollable log pane.
		listH := partsVacuumListRows + 1
		paneH := height - listH - footerH
		if paneH < vacuumPaneMin {
			// Too short for list + footer + a usable pane: drop the footer, then
			// shrink the list, keeping the pane at its minimum.
			footer, footerH = "", 0
			paneH = height - listH
			if paneH < vacuumPaneMin {
				listH = max(height-vacuumPaneMin, 1)
				paneH = max(height-listH, 1)
			}
		}
		return m.renderList(s, listH) + footer + m.renderVacuumPane(paneH)
	}

	panel := m.renderMaintPanel(s)
	if panel != "" {
		panelLines := strings.Count(panel, "\n")
		listH := height - footerH - panelLines
		if listH < 3 {
			// Terminal too short for list + footer + panel — drop the panel.
			if listH = height - footerH; listH < 3 {
				return m.renderList(s, height)
			}
			return m.renderList(s, listH) + footer
		}
		return m.renderList(s, listH) + footer + panel
	}
	if listH := height - footerH; listH >= 1 {
		return m.renderList(s, listH) + footer
	}
	return m.renderList(s, height)
}

// partsVacuumListRows is how many parts-list rows stay visible for context
// while a VACUUM log pane is shown; the log gets everything else.
const partsVacuumListRows = 8

// vacuumPaneMin is the smallest usable vacuum log pane (header + a few lines).
const vacuumPaneMin = 4

// partsFooter builds the totals + legend lines shown directly under the parts
// list and returns them (each newline-terminated) with their line count.
func (m *Model) partsFooter(s *screen) (string, int) {
	var b strings.Builder
	n := 0
	if totals := m.renderPartsTotals(s); totals != "" {
		b.WriteString(totals + "\n")
		n++
	}
	if legend := renderPartsLegend(s); legend != "" {
		b.WriteString(legend + "\n")
		n++
	}
	return b.String(), n
}

// renderPartsLegend is the size/bloat colour legend for the parts level. Unlike
// the generic bottom-of-screen legend, it renders directly beneath the parts
// list so it sits with the table it describes (see renderPartsLevel).
func renderPartsLegend(s *screen) string {
	if s.level != levelParts {
		return ""
	}
	sep := styleMuted.Render("  ·  ")
	return "  " + swatch(styleBar) + " " + styleMuted.Render("size") + sep +
		swatch(styleBloat) + " " + styleMuted.Render("bloat")
}

// renderPartsTotals sums the parts (heap / index / toast) sizes and renders a
// one-line Σ footer beneath the parts list, mirroring the per-table breakdown
// shown on the tables level. Returns "" until parts have loaded.
func (m *Model) renderPartsTotals(s *screen) string {
	if s.level != levelParts || len(s.items) == 0 {
		return ""
	}
	var heap, idx, toast, bloat int64
	anyBloat := false
	for _, it := range s.items {
		p, ok := it.data.(pg.Part)
		if !ok {
			continue
		}
		switch p.Kind {
		case pg.PartHeap:
			heap += p.SizeBytes
		case pg.PartIndex:
			idx += p.SizeBytes
		case pg.PartToast:
			toast += p.SizeBytes
		}
		if p.HasBloat {
			bloat += p.WastedBytes
			anyBloat = true
		}
	}
	total := heap + idx + toast
	if total == 0 {
		return ""
	}
	mu := styleMuted.Render
	sep := mu("  ·  ")
	parts := []string{styleSelected.Render(humanize.Bytes(total)) + mu(" total")}
	if heap > 0 {
		parts = append(parts, mu("heap "+humanize.Bytes(heap)))
	}
	if idx > 0 {
		parts = append(parts, mu("index "+humanize.Bytes(idx)))
	}
	if toast > 0 {
		parts = append(parts, mu("toast "+humanize.Bytes(toast)))
	}
	if anyBloat && bloat > 0 {
		pct := int(float64(bloat) / float64(total) * 100)
		parts = append(parts, styleBloat.Render(fmt.Sprintf("bloat %s (%d%%)", humanize.Bytes(bloat), pct)))
	}
	return "  " + mu("Σ") + "  " + strings.Join(parts, sep)
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
	// Pointer, not a copy: the follow-snap and follow-re-evaluation below must
	// persist to m.vacuum so tail-follow survives across renders.
	vs := &m.vacuum
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

// maintAgeStr renders a "last <age>" fragment with the age graded by how stale
// it is: under a week stays muted (routine), over a week yellow, over a month
// red — the same instinct a DBA applies reading last_autovacuum by hand.
func maintAgeStr(age time.Duration) string {
	s := relativeAge(age)
	switch {
	case age >= 30*24*time.Hour:
		return styleBloat.Render(s)
	case age >= 7*24*time.Hour:
		return styleBarAlt.Render(s)
	}
	return styleMuted.Render(s)
}

func (m *Model) maintVacuumLine(st *pg.TableMaintStats) string {
	mu := styleMuted.Render
	var parts []string

	// Last vacuum with source tag.
	switch {
	case st.LastVacuum != nil && (st.LastAutovacuum == nil || st.LastVacuum.After(*st.LastAutovacuum)):
		parts = append(parts, mu("last ")+maintAgeStr(time.Since(*st.LastVacuum))+mu(" (manual)"))
	case st.LastAutovacuum != nil:
		parts = append(parts, mu("last ")+maintAgeStr(time.Since(*st.LastAutovacuum))+mu(" (auto)"))
	default:
		parts = append(parts, styleBarAlt.Render("never vacuumed"))
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
		parts = append(parts, mu("last ")+maintAgeStr(time.Since(*st.LastAnalyze))+mu(" (manual)"))
	case st.LastAutoanalyze != nil:
		parts = append(parts, mu("last ")+maintAgeStr(time.Since(*st.LastAutoanalyze))+mu(" (auto)"))
	default:
		parts = append(parts, styleBarAlt.Render("never analyzed"))
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
			// Grade dead share by absolute band: past the default autovacuum
			// scale factor (20%) is a real problem, double that is red.
			s := mu(fmt.Sprintf("(%d%%)", deadPct))
			switch {
			case deadPct >= 40:
				s = styleBloat.Render(fmt.Sprintf("(%d%% dead)", deadPct))
			case deadPct >= 20:
				s = styleBarAlt.Render(fmt.Sprintf("(%d%% dead)", deadPct))
			case deadPct >= 10:
				s = lipgloss.NewStyle().Foreground(colorCostLow).Render(fmt.Sprintf("(%d%%)", deadPct))
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
