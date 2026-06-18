package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// handleSeekKey is the input handler while s.seekFocused is true (levelIndexTuples
// only). It mirrors handleFilterKey: Esc clears + blurs, Enter blurs (keeping the
// cursor where it landed), Backspace edits (and blurs when it empties the query),
// Up/Down nudge the cursor, and printable input extends the query. Every edit
// re-runs the seek so the cursor jumps live to the covering entry.
func (m *Model) handleSeekKey(s *screen, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		s.seekFocused = false
		s.seekQuery = ""
		s.seekStatus = ""
	case tea.KeyEnter:
		s.seekFocused = false
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(s.seekQuery); len(r) > 0 {
			s.seekQuery = string(r[:len(r)-1])
			seekApply(s)
		} else {
			s.seekFocused = false
			s.seekStatus = ""
		}
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
		}
	case tea.KeyDown:
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		if msg.Alt {
			return m, nil
		}
		s.seekQuery += string(msg.Runes)
		seekApply(s)
	}
	return m, nil
}

// seekApply routes a seek edit to the right scan for the index access method:
// BRIN seeks by heap block number, everything else (B-tree) by key value.
func seekApply(s *screen) {
	if s.index.AccessMethod == "brin" {
		seekToBlock(s)
		return
	}
	seekToKey(s)
}

// seekToBlock jumps the cursor to the BRIN summary range covering the heap block
// number typed into the seek input. Ranges are ordered ascending by their
// starting block (each spanning pages-per-range blocks), so the covering range is
// the last whose BlockNum <= the query. Computed over the visible (filtered)
// rows; lands on the first row of the covering range.
func seekToBlock(s *screen) {
	s.seekStatus = ""
	q := strings.TrimSpace(s.seekQuery)
	if q == "" {
		return
	}
	blk, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		s.seekStatus = "enter a heap block number"
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
		s.seekStatus = "no ranges to seek"
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].start < cands[j].start })
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
	if s.brinMeta != nil && s.brinMeta.PagesPerRange > 0 {
		upper = fmt.Sprintf("%d", coverStart+int64(s.brinMeta.PagesPerRange)-1)
	}
	s.seekStatus = fmt.Sprintf("→ blk %d  (range %d…%s)", blk, coverStart, upper)
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
	cols := s.indexKeyCols
	pageType := s.indexPageType
	vis := s.visibleIndexes()
	out := make([]seekTarget, 0, len(vis))
	for visPos, idx := range vis {
		t, ok := s.items[idx].data.(pg.IndexTuple)
		if !ok {
			continue
		}
		leading, hasKey := leadingKeyValue(t, cols)
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
	s.seekStatus = ""
	q := strings.TrimSpace(s.seekQuery)
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
		s.seekStatus = "no key to seek (types unavailable)"
		return
	}

	chosen := -1
	for i, e := range targets {
		if !e.hasKey {
			chosen = i // minus-infinity child: covers everything below the first key
			continue
		}
		if keyValueLess(q, e.leading) {
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
	if s.indexPageType == "i" {
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
	s.seekStatus = status
}
