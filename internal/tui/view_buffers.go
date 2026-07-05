package tui

import (
	"fmt"
	"sort"
	"strconv"
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
	sw := swatch
	var b strings.Builder
	mu := styleMuted.Render
	infoHeader(&b, "Bar reference")

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

	b.WriteString("  " + styleHeader.Render(" temperature ") + "  " +
		mu("clock-sweep usage counts — how reused each cached page is") + "\n")
	b.WriteString("    " + sw(usageHeatStyle(0)) + "  " + mu("cold (0)     evictable: not touched since the sweep last passed") + "\n")
	b.WriteString("    " + sw(usageHeatStyle(5)) + "  " + mu("hot  (5)     reused often, burned in (cap is 5)") + "\n")
	b.WriteString("    " + mu("a bar mostly cold means shared_buffers is bigger than the working set") + "\n")
	b.WriteString("    " + sw(styleDirty) + "  " + mu("dirty        pages modified in memory, not yet flushed to disk —") + "\n")
	b.WriteString("    " + mu("                 the checkpointer/bgwriter owes a write; high = write pressure") + "\n\n")

	b.WriteString("  " + mu("The top 10 tables by BufferedBytes each get a distinct palette hue;") + "\n")
	b.WriteString("  " + mu("their row bar matches the slice on the shared_buffers bar above.") + "\n")
	b.WriteString("  " + mu("Tables ranked 11+ use the default bar colour.  ") +
		styleBadge.Render("enter") + mu(" opens a per-table breakdown.") + "\n")

	return padInfo(&b, height)
}

// renderUsageHeatBar paints the cluster (or any) usagecount histogram as one
// stacked bar: one segment per count, width proportional to its buffer share,
// coloured cold→hot by usageHeatStyle. Rounding loss falls into the muted tail.
func renderUsageHeatBar(counts []pg.BufferUsageCount, width int) string {
	var total int64
	for _, u := range counts {
		total += u.Buffers
	}
	if total <= 0 {
		return paintBar(width)
	}
	segs := make([]barSegment, 0, len(counts))
	used := 0
	for _, u := range counts {
		c := max0(int(float64(width) * float64(u.Buffers) / float64(total)))
		if used+c > width {
			c = width - used
		}
		segs = append(segs, barSegment{cells: c, style: usageHeatStyle(u.Count)})
		used += c
	}
	return paintBar(width, segs...)
}

// renderUsageCountBar paints one usagecount bucket's bar in the detail view:
// fill is proportional to this bucket's buffers over the busiest bucket
// (maxBufs), so band sizes compare at a glance; the dirty portion of the fill
// is overlaid in styleDirty to show write pressure within the band.
func renderUsageCountBar(u pg.BufferUsageCount, maxBufs int64, width int) string {
	if maxBufs <= 0 {
		return paintBar(width)
	}
	fill := min(max0(int(float64(width)*float64(u.Buffers)/float64(maxBufs))), width)
	dirty := 0
	if u.Buffers > 0 {
		dirty = int(float64(fill) * float64(u.Dirty) / float64(u.Buffers))
	}
	if dirty > fill {
		dirty = fill
	}
	return paintBar(width,
		barSegment{cells: fill - dirty, style: usageHeatStyle(u.Count)},
		barSegment{cells: dirty, style: styleDirty},
	)
}

// bufferDetailBarWidth sizes the bars on the buffer-detail screen: wide enough
// to read, but leaving room for the count/percent/dirty columns to their right.
func bufferDetailBarWidth(termW int) int {
	w := termW - 44
	if w < barWidthMin {
		return barWidthMin
	}
	if w > summaryBarMax {
		return summaryBarMax
	}
	return w
}

// renderBufferDetail draws the single-table drill-down: the cache-footprint
// figures carried from the parent row (rendered synchronously), then the
// clock-sweep temperature histogram for this table's buffers (loaded async).
func (m *Model) renderBufferDetail(s *screen, height int) string {
	mu := styleMuted.Render
	var b strings.Builder
	st := s.bufDetail
	if st == nil {
		for range height {
			b.WriteString("\n")
		}
		return b.String()
	}

	b.WriteString("\n")
	b.WriteString("  " + styleSelected.Render(st.Schema+"."+st.Name) +
		mu("  ·  shared-buffers detail") + "\n\n")

	barW := bufferDetailBarWidth(m.width)

	// The buffer-volatile figures (buffered/dirty/avg usage) are recomputed from
	// the fresh histogram (s.bufUsage) rather than the older list-load snapshot
	// in st, so the footprint and the bars below always agree — pg_buffercache is
	// sampled live, and a table can be dirtied/warmed between the two reads. Only
	// table size (on-disk) and hit ratio (cumulative) come from st. On a histogram
	// error we fall back to st's snapshot since fresh data is unavailable.
	var totBufs, totDirty, maxBufs, weighted int64
	for _, u := range s.bufUsage {
		totBufs += u.Buffers
		totDirty += u.Dirty
		weighted += int64(u.Count) * u.Buffers
		if u.Buffers > maxBufs {
			maxBufs = u.Buffers
		}
	}
	bs := s.bufBlockSize
	if bs <= 0 {
		bs = 8192 // defensive: standard BLCKSZ if the block_size read failed
	}
	bufferedBytes, dirtyBytes, avgUsage := totBufs*bs, totDirty*bs, 0.0
	if totBufs > 0 {
		avgUsage = float64(weighted) / float64(totBufs)
	}
	if s.bufUsageErr != nil {
		bufferedBytes, dirtyBytes, avgUsage = st.BufferedBytes, st.DirtyBytes, st.UsageAvg
	}

	// --- cache footprint ---
	b.WriteString("  " + styleHeader.Render(" cache footprint ") + "\n")
	cachedVal := "—"
	if st.TotalBytes > 0 {
		pct := float64(bufferedBytes) / float64(st.TotalBytes) * 100
		cachedVal = percentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct)) +
			"  " + renderSolidBar(bufferedBytes, st.TotalBytes, barW, percentStyle(pct))
	}
	hitVal := "—"
	if hr := st.HitRatio(); hr >= 0 {
		pct := hr * 100
		hitVal = gradedPercentStyle(pct).Render(fmt.Sprintf("%.1f%%", pct))
	}
	// Grade the dirty figure by its share of this table's buffered pages: a
	// mostly-dirty footprint means checkpoint/bgwriter flush pressure ahead.
	dirtyVal := humanize.Bytes(dirtyBytes)
	if dirtyBytes > 0 && bufferedBytes > 0 {
		pct := float64(dirtyBytes) / float64(bufferedBytes) * 100
		dirtyVal = bloatPercentStyle(int(pct)).Render(fmt.Sprintf("%s (%.1f%% of buffered)", humanize.Bytes(dirtyBytes), pct))
	}
	rows := [][2]string{
		{"buffered", humanize.Bytes(bufferedBytes)},
		{"table size", humanize.Bytes(st.TotalBytes)},
		{"cached", cachedVal},
		{"hit ratio", hitVal},
		{"dirty", dirtyVal},
		{"avg usage", fmt.Sprintf("%.1f / 5", avgUsage)},
	}
	labelW := 0
	for _, kv := range rows {
		if n := len(kv[0]); n > labelW {
			labelW = n
		}
	}
	for _, kv := range rows {
		b.WriteString("    " + mu(padRight(kv[0], labelW)) + "  " + kv[1] + "\n")
	}

	// --- clock-sweep temperature histogram ---
	b.WriteString("\n  " + styleHeader.Render(" buffer temperature ") + "\n")
	switch {
	case s.bufUsageErr != nil:
		b.WriteString("    " + styleErr.Render(s.bufUsageErr.Error()) + "\n")
	default:
		if totBufs == 0 {
			b.WriteString("    " + mu("not currently in shared_buffers") + "\n")
		} else {
			for _, u := range s.bufUsage {
				word := ""
				switch u.Count {
				case 0:
					word = "cold"
				case len(usageHeatPalette) - 1:
					word = "hot"
				}
				pct := float64(u.Buffers) * 100 / float64(totBufs)
				line := "  " + mu(padRight(word, 4)) + " " +
					usageHeatStyle(u.Count).Render(strconv.Itoa(u.Count)) + "  " +
					renderUsageCountBar(u, maxBufs, barW) + "  " +
					padRight(formatRows(u.Buffers), 6) + "  " +
					padRight(fmt.Sprintf("%.1f%%", pct), 6)
				if u.Dirty > 0 {
					line += "  " + styleDirty.Render("dirty "+formatRows(u.Dirty))
				}
				if u.Pinned > 0 {
					line += "  " + styleBarAlt.Render("pinned "+formatRows(u.Pinned))
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("\n  " + mu("0 = cold (evictable) → 5 = hot (frequently reused)  ·  ") +
				swatch(styleDirty) + mu(" dirty (modified, awaiting flush)") + "\n")
		}
	}

	return padInfo(&b, height)
}

// renderBufferDetailInfo is the ? overlay for the per-table buffer-detail
// screen: it explains the cache-footprint figures and the clock-sweep
// temperature histogram, sized to fill `height` lines.
func (m *Model) renderBufferDetailInfo(height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	sw := swatch
	infoHeader(&b, "Buffer detail reference")

	b.WriteString("  " + styleHeader.Render(" cache footprint ") + "  " +
		mu("how much of this table lives in shared_buffers") + "\n")
	b.WriteString("    " + mu("buffered     bytes of this table (heap + toast + all indexes) cached") + "\n")
	b.WriteString("    " + mu("table size   on-disk pg_total_relation_size, for context") + "\n")
	b.WriteString("    " + mu("cached       buffered ÷ table size — how much of the table fits in cache") + "\n")
	b.WriteString("    " + mu("hit ratio    cumulative shared-buffer hits ÷ (hits + disk reads)") + "\n")
	b.WriteString("    " + mu("dirty        bytes modified in memory, not yet flushed to disk") + "\n")
	b.WriteString("    " + mu("avg usage    mean clock-sweep usage count of this table's pages (0–5)") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" buffer temperature ") + "  " +
		mu("this table's pages grouped by clock-sweep usage count") + "\n")
	b.WriteString("    " + sw(usageHeatStyle(0)) + " " + sw(usageHeatStyle(2)) + " " + sw(usageHeatStyle(5)) +
		"  " + mu("cold (0) → hot (5): each access bumps a page's count up to 5;") + "\n")
	b.WriteString("    " + mu("           the clock-sweep eviction scan ticks it back down. A page is") + "\n")
	b.WriteString("    " + mu("           only evicted once it reaches 0, so count = reuse / staying power.") + "\n")
	b.WriteString("    " + sw(styleDirty) + "  " + mu("dirty        the modified-but-unflushed slice of each band — pages the") + "\n")
	b.WriteString("    " + mu("                 checkpointer still owes a write; lots of hot+dirty = write pressure") + "\n")
	b.WriteString("    " + mu("each row's bar is scaled to the table's busiest band, so band sizes compare.") + "\n")

	return padInfo(&b, height)
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
		sw := func(style lipgloss.Style) string { return swatch(style) + " " }
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
	sw := func(style lipgloss.Style) string { return swatch(style) + " " }
	sbStats := usedStr + muted("  ·  ") +
		sw(styleBar) + muted(fmt.Sprintf("this db %s  ·  ", humanize.Bytes(sum.ThisDBBytes))) +
		sw(styleBarAlt) + muted(fmt.Sprintf("other %s  ·  ", humanize.Bytes(sum.OtherDBBytes))) +
		muted(fmt.Sprintf("░ free %s  ·  total %s",
			humanize.Bytes(sum.FreeBytes()),
			humanize.Bytes(sum.TotalBytes)))
	lines = append(lines, summaryRow("shared_buffers", sbBar))
	lines = append(lines, summaryStats(sbStats))

	// Temperature bar: the cluster-wide clock-sweep usagecount distribution.
	// Cold (0) pages are evictable, hot (5) pages are burned in; a bar dominated
	// by cold means shared_buffers is bigger than the working set.
	if line := m.bufferTemperatureLines(sum, barW); line != "" {
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n"), rankBuffersByOID(s.items)
}

// bufferTemperatureLines renders the "temperature" summary bar + stats line
// from the cluster usagecount histogram, or "" when it's unavailable.
func (m *Model) bufferTemperatureLines(sum *pg.BufferCacheSummary, barW int) string {
	if len(sum.UsageCounts) == 0 {
		return ""
	}
	var totBufs, totDirty, totPinned, weighted int64
	for _, u := range sum.UsageCounts {
		totBufs += u.Buffers
		totDirty += u.Dirty
		totPinned += u.Pinned
		weighted += int64(u.Count) * u.Buffers
	}
	if totBufs == 0 {
		return ""
	}
	blockSize := sum.TotalBytes / totBufs
	avg := float64(weighted) / float64(totBufs)
	cold := sum.UsageCounts[0].Buffers
	hot := sum.UsageCounts[len(sum.UsageCounts)-1].Buffers
	muted := styleMuted.Render
	sw := func(style lipgloss.Style) string { return swatch(style) + " " }
	stats := muted(fmt.Sprintf("avg %.1f/5  ·  ", avg)) +
		sw(usageHeatStyle(0)) + muted(fmt.Sprintf("cold %.0f%%  ·  ", float64(cold)*100/float64(totBufs))) +
		sw(usageHeatStyle(len(usageHeatPalette)-1)) + muted(fmt.Sprintf("hot %.0f%%  ·  ", float64(hot)*100/float64(totBufs))) +
		sw(styleDirty) + muted(fmt.Sprintf("dirty %s  ·  pinned %d", humanize.Bytes(totDirty*blockSize), totPinned))
	bar := renderUsageHeatBar(sum.UsageCounts, barW)
	return summaryRow("temperature", bar) + "\n" + summaryStats(stats)
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
