package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// The shared-memory map (levelShmem) renders pg_shmem_allocations: the whole
// Postgres shared-memory segment, not just the buffer pool. Each allocation is
// bucketed into a coarse subsystem category so the summary bar and the per-row
// colours tell the same story — "where does shared memory actually go".

type shmemCat int

const (
	catBuffer   shmemCat = iota // the buffer pool and its bookkeeping
	catWAL                      // WAL control / insert locks
	catXact                     // transaction status + SLRU caches (clog, multixact, subtrans, notify)
	catLocks                    // heavyweight + predicate lock tables
	catBackends                 // per-backend arrays, proc array, signalling
	catStats                    // cumulative stats + monitoring extensions
	catOther                    // everything else with a name
	catAnon                     // anonymous allocations (DSA, dynamic shmem)
	catFree                     // unused tail of the segment
	numShmemCats
)

// shmemCatOrder is the fixed left-to-right order of segments on the summary bar
// and entries in its legend. catFree is intentionally absent: it's the bar's
// muted ░ remainder (used = total − free), so it never gets a coloured segment.
var shmemCatOrder = []shmemCat{catBuffer, catWAL, catXact, catLocks, catBackends, catStats, catOther, catAnon}

func (c shmemCat) label() string {
	switch c {
	case catBuffer:
		return "buffer pool"
	case catWAL:
		return "WAL"
	case catXact:
		return "txn/SLRU"
	case catLocks:
		return "locks"
	case catBackends:
		return "backends"
	case catStats:
		return "stats"
	case catOther:
		return "other"
	case catAnon:
		return "anonymous"
	case catFree:
		return "free"
	}
	return ""
}

// shmemCatStyle colours a category. catFree returns styleMuted so its ░ tail and
// legend swatch match; the rest get distinct hues echoing the buffer palette.
func shmemCatStyle(c shmemCat) lipgloss.Style {
	switch c {
	case catBuffer:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("33")) // blue
	case catWAL:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	case catXact:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("99")) // violet
	case catLocks:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")) // red
	case catBackends:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("49")) // spring green
	case catStats:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("184")) // chartreuse
	case catOther:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // grey
	case catAnon:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // dim grey
	}
	return styleMuted
}

// shmemDisplayName is the human label for an allocation, naming the two special
// NULL-name rows so they read as more than a blank line.
func shmemDisplayName(a pg.ShmemAllocation) string {
	switch {
	case a.Anonymous:
		return "<anonymous>"
	case a.Free:
		return "<free>"
	default:
		return a.Name
	}
}

// shmemCatOf buckets one allocation. Order matters and is subtle:
//   - the per-backend regions are named "Backend … Buffer", so they'd be read as
//     the buffer pool unless matched first;
//   - "Buffer Blocks" contains the substring "lock" (b·lock·s), so the buffer
//     test must come before the lock test, not after;
//   - the lock tables in turn contain "PROC"/"PREDICATE", so locks precede the
//     broad "proc" backend test below.
func shmemCatOf(a pg.ShmemAllocation) shmemCat {
	if a.Anonymous {
		return catAnon
	}
	if a.Free {
		return catFree
	}
	n := strings.ToLower(a.Name)
	switch {
	case strings.HasPrefix(n, "backend ") || n == "shminvalbuffer":
		return catBackends
	case strings.HasPrefix(n, "checkpoint") || strings.Contains(n, "buffer"):
		return catBuffer
	case strings.Contains(n, "lock") || strings.Contains(n, "predicate") || strings.Contains(n, "serializable"):
		return catLocks
	case strings.HasPrefix(n, "xlog") || strings.Contains(n, "wal"):
		return catWAL
	case strings.Contains(n, "xact") || strings.Contains(n, "transaction") ||
		strings.Contains(n, "multixact") || strings.Contains(n, "commit_timestamp") ||
		strings.Contains(n, "notify") || strings.Contains(n, "async queue") ||
		strings.Contains(n, "knownassignedxids") || strings.Contains(n, "slru"):
		return catXact
	case strings.Contains(n, "proc") || strings.Contains(n, "signal") ||
		strings.Contains(n, "background worker"):
		return catBackends
	case strings.HasPrefix(n, "pg_stat") || strings.HasPrefix(n, "pg_qual") ||
		strings.Contains(n, "pg_wait_sampling") || strings.Contains(n, "shared memory stats") ||
		strings.Contains(n, "waitevent"):
		return catStats
	default:
		return catOther
	}
}

// itemShmemGroup extracts an allocation's category as its enum index, so
// sortByGroup clusters rows by subsystem in the catBuffer→catFree order rather
// than alphabetically. Non-shmem items report no value (sort last).
func itemShmemGroup(it item) (int64, bool) {
	a, ok := it.data.(pg.ShmemAllocation)
	if !ok {
		return 0, false
	}
	return int64(shmemCatOf(a)), true
}

// shmemCatTotals sums AllocatedSize per category over the loaded rows and
// returns the per-category totals plus the grand total of the whole segment.
func shmemCatTotals(items []item) (totals [numShmemCats]int64, grand int64) {
	for _, it := range items {
		a, ok := it.data.(pg.ShmemAllocation)
		if !ok {
			continue
		}
		totals[shmemCatOf(a)] += a.AllocatedSize
		grand += a.AllocatedSize
	}
	return totals, grand
}

// renderShmemSummary draws the grouped category bar plus its legend, shown
// pinned above the per-allocation list (mirrors renderBufferSummary).
func (m *Model) renderShmemSummary(s *screen) string {
	totals, grand := shmemCatTotals(s.items)
	if grand <= 0 {
		return "  " + styleMuted.Render("shared memory: unavailable")
	}
	barW := m.summaryBarWidth()

	// One coloured segment per non-empty category, biggest first within the fixed
	// order; the muted remainder is free memory (used = grand − free).
	segs := make([]barSegment, 0, len(shmemCatOrder))
	for _, c := range shmemCatOrder {
		if totals[c] <= 0 {
			continue
		}
		cells := max0(int(float64(barW) * float64(totals[c]) / float64(grand)))
		segs = append(segs, barSegment{cells: cells, style: shmemCatStyle(c)})
	}
	bar := paintBar(barW, segs...)

	muted := styleMuted.Render
	sw := func(style lipgloss.Style) string { return swatch(style) + " " }
	var stats strings.Builder
	stats.WriteString(muted(fmt.Sprintf("total %s  ·  ", humanize.Bytes(grand))))
	for _, c := range shmemCatOrder {
		if totals[c] <= 0 {
			continue
		}
		stats.WriteString(sw(shmemCatStyle(c)) + muted(fmt.Sprintf("%s %s  ·  ", c.label(), humanize.Bytes(totals[c]))))
	}
	stats.WriteString(muted("░ free " + humanize.Bytes(totals[catFree])))

	return summaryRow("shmem", bar) + "\n" + summaryStats(stats.String())
}

// Column widths for the shared-memory map list; kept beside the header/row
// renderers so they stay aligned.
const (
	shmemColSize  = 11 // "1023.99 MB"
	shmemColPct   = 7  // "100.0%"
	shmemColGroup = 11 // widest category label ("buffer pool")
)

func (m *Model) renderShmemList(s *screen, height int) string {
	vis := s.visibleIndexes()
	maxSz := maxItemSize(s.items, vis)
	_, grand := shmemCatTotals(s.items)
	barW := m.barWidth(s)

	return m.renderRowList(s, height, renderShmemHeader(s.sort, s.sortDesc, barW),
		func(it item, selected bool) string {
			a, _ := it.data.(pg.ShmemAllocation)
			return renderShmemRow(it, a, maxSz, grand, barW, selected)
		})
}

func renderShmemHeader(sort sortMode, sortDesc bool, barW int) string {
	line := headerIndent(barW) +
		padRight(sortMark("size", sort == sortBySize, sortDesc), shmemColSize) + "  " +
		padRight("share", shmemColPct) + "  " +
		padRight(sortMark("group", sort == sortByGroup, sortDesc), shmemColGroup) + "  " +
		sortMark("allocation", sort == sortByName, sortDesc)
	return styleMuted.Render(line)
}

func renderShmemRow(it item, a pg.ShmemAllocation, maxSize, grand int64, barW int, selected bool) string {
	cat := shmemCatOf(a)
	bar := renderSolidBar(it.size, maxSize, barW, shmemCatStyle(cat))
	cursor := selectedCursor(selected)

	pctStr := "—"
	if grand > 0 {
		pctStr = fmt.Sprintf("%.1f%%", float64(a.AllocatedSize)*100/float64(grand))
	}

	// The group column carries the category colour (matching the summary bar's
	// swatch); the allocation name follows it, highlighted only when selected.
	group := shmemCatStyle(cat).Render(padRight(cat.label(), shmemColGroup))
	name := highlightName(it.name, selected)

	return cursor + bar + "  " +
		padRight(humanize.Bytes(a.AllocatedSize), shmemColSize) + "  " +
		padRight(pctStr, shmemColPct) + "  " +
		group + "  " +
		name
}

// renderShmemInfo is the ? overlay for the shared-memory map: it explains what
// the view shows and what each category covers, sized to fill `height` lines.
func (m *Model) renderShmemInfo(height int) string {
	var b strings.Builder
	mu := styleMuted.Render
	sw := func(c shmemCat) string { return swatch(shmemCatStyle(c)) }
	infoHeader(&b, "Shared-memory map")

	b.WriteString("  " + mu("Every region of the Postgres shared-memory segment (pg_shmem_allocations),") + "\n")
	b.WriteString("  " + mu("not just the buffer pool. The bar groups allocations by subsystem; the muted") + "\n")
	b.WriteString("  " + mu("tail is unused (free) memory. Each row's size is its alignment-padded") + "\n")
	b.WriteString("  " + mu("allocated_size, and the shares sum to the whole segment.") + "\n\n")

	b.WriteString("  " + styleHeader.Render(" categories ") + "\n")
	rows := []struct {
		c    shmemCat
		desc string
	}{
		{catBuffer, "the buffer pool, its descriptors, IO condition vars, checkpoint state"},
		{catWAL, "WAL control and insert locks (XLOG Ctl, …)"},
		{catXact, "transaction status + SLRU caches: clog, multixact, subtrans, notify"},
		{catLocks, "heavyweight + predicate lock tables, fast-path locks"},
		{catBackends, "per-backend arrays, proc array, inter-process signalling"},
		{catStats, "cumulative statistics + monitoring extensions (pg_stat_*, pg_qualstats, …)"},
		{catOther, "named allocations that don't fall into the buckets above"},
		{catAnon, "anonymous allocations: DSA / dynamic shared memory with no index name"},
		{catFree, "the unused tail of the segment — headroom before shared memory is full"},
	}
	for _, r := range rows {
		b.WriteString("    " + sw(r.c) + "  " + mu(padRight(r.c.label(), 11)) + "  " + mu(r.desc) + "\n")
	}
	b.WriteString("\n  " + mu("Reading pg_shmem_allocations needs pg_read_all_stats / superuser; without it") + "\n")
	b.WriteString("  " + mu("the view shows a permission error instead of the map.") + "\n")

	return padInfo(&b, height)
}
