package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
)

// handleSeekKey is the input handler while s.seekFocused is true. It mirrors
// handleFilterKey: Esc clears + blurs, Enter blurs (keeping the cursor where it
// landed), Backspace edits (and blurs when it empties the query), Up/Down nudge
// the cursor, and printable input extends the query.
//
// On the per-page views every edit re-runs the seek, so the cursor jumps live
// to the covering entry — the whole page is already loaded. The page lists
// (levelHeapPages/levelIndexPages) instead seek on Enter only: landing on a
// block outside the loaded window means re-reading a window's worth of pages
// from the server, which is not something to do once per keystroke.
func (m *Model) handleSeekKey(s *screen, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	live := !seeksWindow(s)
	switch msg.Code {
	case tea.KeyEsc:
		s.pages.seekFocused = false
		s.pages.seekQuery = ""
		s.pages.seekStatus = ""
	case tea.KeyEnter:
		s.pages.seekFocused = false
		if !live {
			return m, m.seekToPage(s)
		}
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(s.pages.seekQuery); len(r) > 0 {
			s.pages.seekQuery = string(r[:len(r)-1])
			if live {
				seekApply(s)
			}
		} else {
			s.pages.seekFocused = false
			s.pages.seekStatus = ""
		}
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
		}
	case tea.KeyDown:
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	default:
		if msg.Text == "" || msg.Mod.Contains(tea.ModAlt) {
			return m, nil
		}
		s.pages.seekQuery += msg.Text
		if live {
			seekApply(s)
		}
	}
	return m, nil
}

// seeksWindow reports whether this screen's seek moves the loaded page window
// (and so costs a server round-trip) rather than only the cursor.
func seeksWindow(s *screen) bool {
	return s.level == levelHeapPages || s.level == levelIndexPages
}

// seekApply routes a seek edit to the right scan for the index access method:
// BRIN seeks by heap block number, everything else (B-tree) by key value.
func seekApply(s *screen) {
	if s.pages.index.AccessMethod == "brin" {
		seekToBlock(s)
		return
	}
	seekToKey(s)
}

// seekToPage jumps either page list to a block number. A block already in the
// loaded window only moves the cursor; anything else slides the window to the
// grid-aligned span holding it (the alignment drillBrinItem uses, so a seek and
// a BRIN drill to the same block land on the same window) and reloads, with
// focusBlkno asking that load to put the cursor on the block itself.
//
// The clamps mirror PgUp/PgDn: never below 0, never past the last full window,
// because get_raw_page errors hard past EOF. heapPageCount is the relation's
// real block count, so a block beyond it cannot be reached and says so.
func (m *Model) seekToPage(s *screen) tea.Cmd {
	s.pages.seekStatus = ""
	q := strings.TrimSpace(s.pages.seekQuery)
	if q == "" {
		return nil
	}
	blk, err := strconv.ParseInt(q, 10, 64)
	if err != nil || blk < 0 {
		s.pages.seekStatus = "enter a block number"
		return nil
	}
	if total := int64(s.pages.heapPageCount); total > 0 && blk >= total {
		s.pages.seekStatus = fmt.Sprintf("block %d is past the end (%d pages)", blk, total)
		return nil
	}

	want := int32(blk)
	if cur, ok := blockInWindow(s, want); ok {
		s.cursor = cur
		s.clampCursor()
		s.pages.seekStatus = fmt.Sprintf("→ block %d", want)
		return nil
	}

	step := s.pages.heapWindowCount
	if step <= 0 {
		step = heapWindowDefault
	}
	start := (want / step) * step
	if s.pages.heapPageCount > 0 && start+step > s.pages.heapPageCount {
		start = max32(s.pages.heapPageCount-step, 0)
	}
	s.pages.heapWindowStart = start
	s.pages.focusBlkno = want
	s.pages.seekStatus = fmt.Sprintf("→ block %d", want)
	s.resetCursor()
	return m.loadCurrent()
}

// blockInWindow finds a block among the currently visible rows, so a seek
// inside the loaded window costs nothing. Visible, not loaded: a seek honours
// an active filter the same way seekTargets does.
func blockInWindow(s *screen, blk int32) (int, bool) {
	for visPos, idx := range s.visibleIndexes() {
		if got, ok := itemBlkno(s.items[idx]); ok && got == int64(blk) {
			return visPos, true
		}
	}
	return 0, false
}

// seekToBlock jumps the cursor to the BRIN summary range covering the heap block
// number typed into the seek input. Ranges are ordered ascending by their
// starting block (each spanning pages-per-range blocks), so the covering range is
// the last whose BlockNum <= the query. Computed over the visible (filtered)
// rows; lands on the first row of the covering range.
func seekToBlock(s *screen) {
	s.pages.seekStatus = ""
	q := strings.TrimSpace(s.pages.seekQuery)
	if q == "" {
		return
	}
	blk, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		s.pages.seekStatus = "enter a heap block number"
		return
	}
	type cand struct {
		visPos int
		start  int64
	}
	var cands []cand
	for visPos, idx := range s.visibleIndexes() {
		t, ok := s.items[idx].data.(pg.BrinItem)
		if !ok {
			continue
		}
		cands = append(cands, cand{visPos: visPos, start: t.BlockNum})
	}
	if len(cands) == 0 {
		s.pages.seekStatus = "no ranges to seek"
		return
	}
	// Stable: a BRIN range has one item per indexed attribute, so BlockNum is
	// not unique and an unstable sort would pick an arbitrary one of them.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].start < cands[j].start })
	chosen := 0
	for i, c := range cands {
		if c.start > blk {
			break
		}
		chosen = i
	}
	coverStart := cands[chosen].start
	tgt := cands[chosen]
	for _, c := range cands { // land on the first row of the covering range
		if c.start == coverStart {
			tgt = c
			break
		}
	}
	s.cursor = tgt.visPos
	s.clampCursor()

	upper := "+∞"
	if s.pages.brinMeta != nil && s.pages.brinMeta.PagesPerRange > 0 {
		upper = strconv.FormatInt(coverStart+int64(s.pages.brinMeta.PagesPerRange)-1, 10)
	}
	s.pages.seekStatus = fmt.Sprintf("→ blk %d  (range %d…%s)", blk, coverStart, upper)
}

// seekTarget is one candidate entry for a seek: its position in the visible list
// (so the cursor can land on it), the page slot it sits at, the decoded leading
// key column, and whether it carries a key at all (the minus-infinity leftmost
// downlink does not).
type seekTarget struct {
	visPos  int
	off     int32
	leading string
	hasKey  bool
}

// seekTargets returns the page's seekable entries in B-tree key order — sorted
// by item offset, with the page high key excluded so the remaining entries are
// strictly ascending by key. The high key is the keyed pivot at offset 1 of a
// non-rightmost page (leaf or internal); the rightmost minus-infinity downlink
// is a pivot at offset 1 too but carries no key, so it stays a target. Computed
// over the visible (filtered) list so seek respects an active filter, and over
// offset order regardless of the active display sort.
func seekTargets(s *screen) []seekTarget {
	cols := s.pages.indexKeyCols
	pageType := s.pages.indexPageType
	vis := s.visibleIndexes()
	out := make([]seekTarget, 0, len(vis))
	for visPos, idx := range vis {
		t, ok := s.items[idx].data.(pg.IndexTuple)
		if !ok {
			continue
		}
		leading, hasKey := pageinspect.LeadingKeyValue(t, cols)
		if t.ItemOffset == 1 && hasKey && classifyIndexTuple(t, pageType) == idxTuplePivot {
			continue // page high key — the page's own upper bound, not a child range
		}
		out = append(out, seekTarget{visPos: visPos, off: t.ItemOffset, leading: leading, hasKey: hasKey})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].off < out[j].off })
	return out
}

// seekToKey jumps the cursor to the entry whose key range covers s.seekQuery —
// the last entry whose leading key value is <= the query (the same child a
// B-tree descent would pick). A keyless minus-infinity downlink covers anything
// below the first real key; a query past the last key lands on the last entry.
// It only moves the cursor; it never drills in.
func seekToKey(s *screen) {
	s.pages.seekStatus = ""
	q := strings.TrimSpace(s.pages.seekQuery)
	if q == "" {
		return
	}
	targets := seekTargets(s)
	hasAnyKey := false
	for _, e := range targets {
		if e.hasKey {
			hasAnyKey = true
			break
		}
	}
	if !hasAnyKey {
		// No decodable keys to compare against (e.g. the key-column types failed
		// to load). Leave the cursor where it is rather than jumping blindly.
		s.pages.seekStatus = "no key to seek (types unavailable)"
		return
	}

	chosen := -1
	for i, e := range targets {
		if !e.hasKey {
			chosen = i // minus-infinity child: covers everything below the first key
			continue
		}
		if pageinspect.KeyValueLess(q, e.leading) {
			break // e is past the query — the previous target covers it
		}
		chosen = i
	}
	if chosen < 0 {
		chosen = 0 // query sorts before every key with no minus-infinity entry
	}

	tgt := targets[chosen]
	s.cursor = tgt.visPos
	s.clampCursor()

	status := fmt.Sprintf("→ #%04d", tgt.off)
	if s.pages.indexPageType == "i" {
		lower := "−∞"
		if tgt.hasKey {
			lower = tgt.leading
		}
		upper := "+∞"
		if chosen+1 < len(targets) {
			upper = targets[chosen+1].leading
		}
		status += fmt.Sprintf("  (%s…%s)", lower, upper)
	}
	s.pages.seekStatus = status
}
