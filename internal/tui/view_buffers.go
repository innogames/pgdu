package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// renderBufferInfo draws a static explainer for the server-memory and
// shared_buffers bars, sized to fill `height` lines so the help row stays
// pinned to the bottom. Shown when the user toggles `?` on
// levelBufferTables.
func (m *Model) renderBufferInfo(height int) string {
	sw := func(style lipgloss.Style) string { return style.Render("▇") }
	var b strings.Builder
	mu := styleMuted.Render
	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render("Bar reference") + mu("  ·  press ") +
		styleBadge.Render("?") + mu(" or ") + styleBadge.Render("esc") + mu(" to dismiss") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" server memory ") + "  " +
		mu("the whole host's RAM (MemTotal) — the superset") + "\n")
	b.WriteString("    " + sw(styleBar) + "  " + mu("sb used      pages of shared_buffers actively caching data") + "\n")
	b.WriteString("    " + sw(styleSBFree) + "  " + mu("sb free      empty pages reserved by Postgres but not yet used") + "\n")
	b.WriteString("    " + sw(styleOtherUsed) + "  " + mu("other        memory used outside shared_buffers (other procs, kernel)") + "\n")
	b.WriteString("    " + sw(styleCache) + "  " + mu("cache        reclaimable kernel buffers + page cache (≈ MemAvailable − MemFree)") + "\n")
	b.WriteString("    " + mu("░  free         truly unallocated memory (MemFree)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" shared_buffers ") + "  " +
		mu("the Postgres-only subset of server memory — a slice of the bar above") + "\n")
	b.WriteString("    " + sw(styleBar) + "  " + mu("this db      buffered pages belonging to the current database") + "\n")
	b.WriteString("    " + sw(styleBarAlt) + "  " + mu("other dbs    buffered pages from other databases (and shared catalogs)") + "\n")
	b.WriteString("    " + mu("░  free         empty pages within shared_buffers") + "\n\n")

	b.WriteString("  " + mu("The top 10 tables by BufferedBytes each get a distinct palette hue;") + "\n")
	b.WriteString("  " + mu("their row bar matches the slice on the shared_buffers bar above.") + "\n")
	b.WriteString("  " + mu("Tables ranked 11+ use the default bar colour.") + "\n")

	rendered := strings.Count(b.String(), "\n")
	for i := rendered; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// summaryBarWidth picks the bar width for the two stacked summary bars.
// Wider than the per-row bars by design — there's nothing to align against
// once the stats text moved onto its own line.
func (m *Model) summaryBarWidth() int {
	// "  " indent + label + "  [" prefix + "]" suffix + 2 cells slack.
	reserve := 2 + summaryLabelWidth + 3 + 1 + 2
	w := m.width - reserve
	if w < barWidthMin {
		return barWidthMin
	}
	if w > summaryBarMax {
		return summaryBarMax
	}
	return w
}

// renderBufferSummary draws the multi-line summary block: an optional
// server-memory bar with stats, then the shared_buffers bar with stats.
// The biggest tables are painted as slices on the shared_buffers bar from
// bufferSlicePalette; the returned rankByOID map ranks every buffered
// table so list rows below can pick the same palette colour by rank.
func (m *Model) renderBufferSummary(s *screen) (string, map[uint32]int) {
	if s.bufferSummaryErr != nil {
		return "  " + styleMuted.Render("shared buffers: ") +
			styleErr.Render(s.bufferSummaryErr.Error()), nil
	}
	sum := s.bufferSummary
	if sum == nil || sum.TotalBytes <= 0 {
		return "  " + styleMuted.Render("shared_buffers: unavailable"), nil
	}

	barW := m.summaryBarWidth()
	lines := make([]string, 0, 4)

	// Server-memory bar (suppressed when we have no host RAM info, or
	// when MemAvailable / MemFree are unavailable — without both we can't
	// split cache out from truly-free memory).
	if sum.ServerMemBytes > 0 && sum.ServerMemAvailableBytes > 0 && sum.ServerMemFreeBytes > 0 {
		sbUsed := sum.ThisDBBytes + sum.OtherDBBytes
		sbFree := sum.FreeBytes()
		sbTotal := sum.TotalBytes
		// "Other used" is non-shared_buffers memory that isn't reclaimable
		// (= total - available - SB). The cache (= reclaimable buffers +
		// page cache) is what `free -w` calls "buff/cache"; we approximate
		// it as MemAvailable - MemFree, which is close enough for a bar.
		// Clamp all derived values to >=0 in case of rounding races.
		otherUsed := max(sum.ServerMemBytes-sum.ServerMemAvailableBytes-sbTotal, 0)
		cache := max(sum.ServerMemAvailableBytes-sum.ServerMemFreeBytes, 0)
		bar := renderServerMemBar(sbUsed, sbFree, otherUsed, cache, sum.ServerMemBytes, barW)
		muted := styleMuted.Render
		sw := func(style lipgloss.Style) string { return style.Render("▇") + " " }
		stats := muted(fmt.Sprintf("shared buffer %s (", humanize.Bytes(sbTotal))) +
			sw(styleBar) + muted(fmt.Sprintf("used %s / ", humanize.Bytes(sbUsed))) +
			sw(styleSBFree) + muted(fmt.Sprintf("free %s)  ·  ", humanize.Bytes(sbFree))) +
			sw(styleOtherUsed) + muted(fmt.Sprintf("other %s  ·  ", humanize.Bytes(otherUsed))) +
			sw(styleCache) + muted(fmt.Sprintf("cache %s  ·  ", humanize.Bytes(cache))) +
			muted(fmt.Sprintf("░ free %s  ·  total %s",
				humanize.Bytes(sum.ServerMemFreeBytes),
				humanize.Bytes(sum.ServerMemBytes)))
		lines = append(lines, summaryRow("server memory", bar))
		lines = append(lines, summaryStats(stats))
	}

	// Shared-buffers bar. Slice count is dynamic — fit as many distinct
	// tables as we have palette colours for, dropping the trailing ones
	// whose proportion would round to a sub-cell slice (invisible).
	slices := topBufferSlices(s.items, sum.TotalBytes, barW)
	var sliceTotal int64
	for _, sl := range slices {
		sliceTotal += sl.bytes
	}
	remainder := max(sum.ThisDBBytes-sliceTotal, 0)
	sbBar := renderBufferBar(slices, remainder, sum.OtherDBBytes, sum.TotalBytes, barW)
	usedPct := float64(sum.ThisDBBytes+sum.OtherDBBytes) * 100 / float64(sum.TotalBytes)
	usedStr := percentStyle(usedPct).Render(fmt.Sprintf("%.1f%% used", usedPct))
	muted := styleMuted.Render
	sw := func(style lipgloss.Style) string { return style.Render("▇") + " " }
	sbStats := usedStr + muted("  ·  ") +
		sw(styleBar) + muted(fmt.Sprintf("this db %s  ·  ", humanize.Bytes(sum.ThisDBBytes))) +
		sw(styleBarAlt) + muted(fmt.Sprintf("other %s  ·  ", humanize.Bytes(sum.OtherDBBytes))) +
		muted(fmt.Sprintf("░ free %s  ·  total %s",
			humanize.Bytes(sum.FreeBytes()),
			humanize.Bytes(sum.TotalBytes)))
	lines = append(lines, summaryRow("shared_buffers", sbBar))
	lines = append(lines, summaryStats(sbStats))

	return strings.Join(lines, "\n"), rankBuffersByOID(s.items)
}

// summaryRow is one "  <label>  [bar]" line. label is padded so multiple
// rows' opening brackets line up.
func summaryRow(label, bar string) string {
	return "  " + styleMuted.Render(padRight(label, summaryLabelWidth)) + "  " + bar
}

// summaryStats is a stats line sitting under a summary bar. It's indented
// to align with the bar's content (after the opening bracket) so the eye
// can pair stats to the bar above.
func summaryStats(stats string) string {
	indent := strings.Repeat(" ", 2+summaryLabelWidth+3)
	return indent + stats
}

// topBufferSlices picks the biggest items in the screen by BufferedBytes
// and returns them as bufferSlice values coloured from bufferSlicePalette.
// The cap is the palette size, further trimmed to drop trailing entries
// whose proportion would round below 1 cell on the bar (invisible). The
// selection is by absolute size — independent of the current sort — so
// the bar always shows the biggest cache users.
func topBufferSlices(items []item, total int64, width int) []bufferSlice {
	type pair struct {
		oid   uint32
		name  string
		bytes int64
	}
	pairs := make([]pair, 0, len(items))
	for _, it := range items {
		st, ok := it.data.(pg.TableBufferStat)
		if !ok || st.BufferedBytes <= 0 {
			continue
		}
		pairs = append(pairs, pair{oid: st.OID, name: st.Schema + "." + st.Name, bytes: st.BufferedBytes})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].bytes > pairs[j].bytes })
	if cap := len(bufferSlicePalette); len(pairs) > cap {
		pairs = pairs[:cap]
	}
	if total > 0 && width > 0 {
		minBytes := int64(float64(total) / float64(width))
		for len(pairs) > 0 && pairs[len(pairs)-1].bytes < minBytes {
			pairs = pairs[:len(pairs)-1]
		}
	}
	out := make([]bufferSlice, len(pairs))
	for i, p := range pairs {
		out[i] = bufferSlice{oid: p.oid, name: p.name, bytes: p.bytes, style: bufferSliceStyle(i)}
	}
	return out
}

// rankBuffersByOID assigns every buffer-stat item a rank (0 = biggest
// BufferedBytes) so row bars in the list can pick a palette colour by
// rank. The same rank is used for the top-N slices on the summary bar,
// so a row's colour matches its slice exactly for tables in the top-N.
func rankBuffersByOID(items []item) map[uint32]int {
	type entry struct {
		oid   uint32
		bytes int64
	}
	es := make([]entry, 0, len(items))
	for _, it := range items {
		st, ok := it.data.(pg.TableBufferStat)
		if !ok || st.BufferedBytes <= 0 {
			continue
		}
		es = append(es, entry{oid: st.OID, bytes: st.BufferedBytes})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].bytes > es[j].bytes })
	out := make(map[uint32]int, len(es))
	for i, e := range es {
		out[e.oid] = i
	}
	return out
}
