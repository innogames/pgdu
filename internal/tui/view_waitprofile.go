package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Column widths for the wait-profile ranked list.
const (
	waitPctColW   = 5  // "100%"
	waitSparkColW = 24 // per-class trend, one glyph per recent bucket
)

// waitProfileMaxClasses caps the ranked list / bar at the palette size; rarer
// classes fold into a single "other" line so colours stay unambiguous.
const waitProfileMaxClasses = 10

// waitGloss is a short human explanation for the wait classes worth knowing on
// sight. Anything unlisted renders without a gloss.
var waitGloss = map[string]string{
	waitCPUClass:           "running, not waiting",
	"LWLock:WALWrite":      "WAL flush contention",
	"LWLock:WALInsert":     "WAL insert-slot contention",
	"LWLock:BufferMapping": "buffer-table lookup contention",
	"IO:DataFileRead":      "heap/index reads from disk",
	"IO:DataFileWrite":     "heap/index writes to disk",
	"IO:WALWrite":          "WAL write I/O",
	"IO:WALSync":           "WAL fsync",
	"Lock:transactionid":   "row-lock waits (blocked on another xact)",
	"Lock:tuple":           "row-lock acquisition queue",
	"Lock:relation":        "table-level lock waits",
	"IPC:SyncRep":          "waiting for synchronous replica",
	"Client:ClientRead":    "waiting for the client to send",
	"Client:ClientWrite":   "waiting for the client to receive",
}

// waitClassAgg is one wait class aggregated over the retained window.
type waitClassAgg struct {
	name   string
	count  int       // Σ samples in this class
	series []float64 // per-bucket share of that bucket's total, oldest→newest
}

// aggregateWaitClasses folds the ring into ranked per-class aggregates plus
// the total sample count. Classes beyond waitProfileMaxClasses collapse into
// a trailing "other" aggregate (its series summed likewise).
func aggregateWaitClasses(buckets []waitBucket) (classes []waitClassAgg, totalSamples int) {
	counts := make(map[string]int)
	for _, b := range buckets {
		for class, n := range b.counts {
			counts[class] += n
		}
		totalSamples += b.total
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(a, b int) bool {
		if counts[names[a]] != counts[names[b]] {
			return counts[names[a]] > counts[names[b]]
		}
		return names[a] < names[b]
	})

	series := func(match func(class string) bool) []float64 {
		out := make([]float64, len(buckets))
		for i, b := range buckets {
			if b.total == 0 {
				continue
			}
			n := 0
			for class, c := range b.counts {
				if match(class) {
					n += c
				}
			}
			out[i] = float64(n) / float64(b.total)
		}
		return out
	}

	top := names
	if len(top) > waitProfileMaxClasses {
		top = names[:waitProfileMaxClasses]
	}
	for _, name := range top {
		classes = append(classes, waitClassAgg{
			name:   name,
			count:  counts[name],
			series: series(func(c string) bool { return c == name }),
		})
	}
	if len(names) > len(top) {
		rest := names[len(top):]
		inRest := make(map[string]bool, len(rest))
		restCount := 0
		for _, name := range rest {
			inRest[name] = true
			restCount += counts[name]
		}
		classes = append(classes, waitClassAgg{
			name:   "other",
			count:  restCount,
			series: series(func(c string) bool { return inRest[c] }),
		})
	}
	return classes, totalSamples
}

// renderWaitProfile draws the wait-event profile: an honest window label, the
// window's stacked class-mix bar, and the ranked class list with per-class
// trend sparklines. Renders straight from Model.waitRing — the screen itself
// has no loaded data.
func (m *Model) renderWaitProfile(s *screen, height int) string {
	var b strings.Builder
	lines := 0
	put := func(line string) {
		b.WriteString(truncateToWidth(line, m.width) + "\n")
		lines++
	}

	if m.waitRing == nil || m.waitRing.n == 0 {
		put("  " + styleMuted.Render("no samples yet — the profile fills as the Activity view refreshes"))
		for ; lines < height; lines++ {
			b.WriteString("\n")
		}
		return b.String()
	}

	buckets := m.waitRing.ordered()
	classes, totalSamples := aggregateWaitClasses(buckets)

	// Window label: span, sample count, cadence. Honest about granularity —
	// this is a sampled profile, not a continuous trace.
	span := buckets[len(buckets)-1].at.Sub(buckets[0].at).Round(time.Second)
	cadence := "paused"
	if m.activityRefresh > 0 {
		cadence = m.activityRefresh.String()
	}
	put("  " + styleMuted.Render(fmt.Sprintf(
		"window: last %s · %d snapshots · %d samples · cadence %s",
		span, len(buckets), totalSamples, cadence)))
	put("")

	// Stacked class-mix bar over the whole window (integer truncation leaves at
	// most a few cells of ░ tail — paintBar pads).
	barW := min(m.width-4, summaryBarMax)
	if barW > 0 && totalSamples > 0 {
		segs := make([]barSegment, 0, len(classes))
		for i, c := range classes {
			segs = append(segs, barSegment{cells: c.count * barW / totalSamples, style: waitClassStyle(i, c.name)})
		}
		put("  " + paintBar(barW, segs...))
		// Legend: colour-matched class names in rank order.
		legend := make([]string, 0, len(classes))
		for i, c := range classes {
			legend = append(legend, waitClassStyle(i, c.name).Render(c.name))
		}
		put("  " + strings.Join(legend, styleMuted.Render(" · ")))
		put("")
	}

	if totalSamples == 0 {
		put("  " + styleMuted.Render("all snapshots were idle — nothing was running or waiting"))
	}

	for i, c := range classes {
		pct := 0.0
		if totalSamples > 0 {
			pct = 100 * float64(c.count) / float64(totalSamples)
		}
		gloss := ""
		if g, ok := waitGloss[c.name]; ok {
			gloss = "  " + styleMuted.Render(g)
		}
		style := waitClassStyle(i, c.name)
		put("  " + padLeft(fmt.Sprintf("%.0f%%", pct), waitPctColW) + "  " +
			style.Render(sparkline(c.series, waitSparkColW, 0)) + "  " +
			style.Render(c.name) + gloss)
	}

	for ; lines < height; lines++ {
		b.WriteString("\n")
	}
	return b.String()
}

// waitClassStyle colours a ranked wait class: the shared slice palette by
// rank, with the fold-over "other" line muted.
func waitClassStyle(rank int, name string) lipgloss.Style {
	if name == "other" {
		return styleMuted
	}
	return bufferSliceStyle(rank)
}

// renderWaitProfileInfo is the ? reference for the wait profile.
func (m *Model) renderWaitProfileInfo(height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	infoHeader(&b, "Wait-profile reference")

	b.WriteString("  " + styleHeader.Render(" what you're seeing ") + "\n")
	b.WriteString("    " + mu("Every Activity refresh samples pg_stat_activity; each non-idle backend is bucketed by") + "\n")
	b.WriteString("    " + mu("wait_event_type:wait_event (or \""+waitCPUClass+"\" when it runs with no wait event).") + "\n")
	b.WriteString("    " + mu("The bar and percentages are each class's share of all samples in the retained window —") + "\n")
	b.WriteString("    " + mu("\"where did time go\", pg_wait_sampling-style, with no extension required.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" reading it honestly ") + "\n")
	b.WriteString("    " + mu("This is a sampled profile, not a continuous trace: waits that start and end between two") + "\n")
	b.WriteString("    " + mu("refreshes are invisible, and short spikes are under-represented at slow cadences.") + "\n")
	b.WriteString("    " + mu("Cycle a faster cadence with ") + styleBadge.Render("t") + mu(" on the Activity view (500ms is supported) while profiling.") + "\n")
	b.WriteString("    " + mu(fmt.Sprintf("Retention is bounded at %d snapshots; older buckets fall off the back.", waitRingCap)) + "\n\n")

	b.WriteString("  " + styleHeader.Render(" list columns ") + "\n")
	b.WriteString("    " + mu("share of window · per-snapshot trend (sparkline, self-scaled) · wait class · gloss") + "\n")
	return b.String()
}
