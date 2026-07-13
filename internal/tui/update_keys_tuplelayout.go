package tui

import (
	"math"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// tupleByLP finds the loaded HeapTuple a byte-layout overlay is keyed to. The
// overlay swallows list navigation while open, so the item set can only change
// under it via a stale async reload — returning nil then blanks the overlay
// instead of showing another tuple's bytes.
func (s *screen) tupleByLP(lp int32) *pg.HeapTuple {
	for i := range s.items {
		if t, ok := s.items[i].data.(pg.HeapTuple); ok && t.LP == lp {
			return &t
		}
	}
	return nil
}

// openTupleLayout arms the byte-layout modal for one line pointer and kicks
// off its attr-split load. Called from the Enter drill on levelHeapTuples.
func (m *Model) openTupleLayout(s *screen, lp int32) tea.Cmd {
	m.showInfo = false
	m.showTupleLayout = true
	m.tupleLayoutCursor, m.tupleLayoutOffset = 0, 0
	m.tupleLayoutSort, m.tupleLayoutSortDesc = tlSortOffset, false
	return m.reloadTupleAttrs(s, lp)
}

// reloadTupleAttrs resets the screen's attr-split state to "loading lp" and
// issues the load — the one place the tupleAttrs* fields are armed, shared by
// the overlay's open and space-reload paths.
func (m *Model) reloadTupleAttrs(s *screen, lp int32) tea.Cmd {
	s.tupleAttrsLP = lp
	s.tupleAttrs = nil
	s.tupleAttrsErr = nil
	s.tupleAttrsLoading = true
	return m.loadTupleAttrsCmd(s.table, s.heapPageBlkno, lp)
}

// closeTupleLayout dismisses the modal and drops the loaded split so a stale
// tupleAttrsLoadedMsg can't repopulate a closed overlay.
func (m *Model) closeTupleLayout(s *screen) {
	m.showTupleLayout = false
	s.tupleAttrsLP = 0
	s.tupleAttrs = nil
	s.tupleAttrsErr = nil
	s.tupleAttrsLoading = false
}

// handleTupleLayoutKey drives the modal tuple byte-layout overlay (Enter on a
// heap tuple): Up/Down/PgUp/PgDn/Top/Bottom move the legend cursor, ←/→ and r
// control the sort, space reloads the split, ? toggles the reference overlay,
// and enter/esc/q close. Everything else is swallowed so the list beneath
// never moves. Quit still quits. Cursor moves may overshoot — the renderer
// clamps to the segment count (same contract as handleInfoKey/scrollWindow).
func (m *Model) handleTupleLayoutKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	// The ? reference sits on top of the overlay: scroll keys move it and
	// ?/esc dismiss it (back to the layout), exactly like the level infos.
	if m.showInfo {
		return m.handleInfoKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showInfo = true
		m.infoOffset = 0
	case key.Matches(msg, m.keys.Enter), key.Matches(msg, m.keys.Back):
		m.closeTupleLayout(s)
	case key.Matches(msg, m.keys.Up):
		m.tupleLayoutCursor = max(m.tupleLayoutCursor-1, 0)
	case key.Matches(msg, m.keys.Down):
		m.tupleLayoutCursor++ // clamped by the renderer
	case key.Matches(msg, m.keys.PageUp):
		m.tupleLayoutCursor = max(m.tupleLayoutCursor-m.pageStep(), 0)
	case key.Matches(msg, m.keys.PageDown):
		m.tupleLayoutCursor += m.pageStep() // clamped by the renderer
	case key.Matches(msg, m.keys.Top):
		m.tupleLayoutCursor = 0
	case key.Matches(msg, m.keys.Bottom):
		m.tupleLayoutCursor = math.MaxInt32 // clamped by the renderer
	case key.Matches(msg, m.keys.SortNext):
		m.tupleLayoutSort = (m.tupleLayoutSort + 1) % tlSortCount
		m.tupleLayoutSortDesc = m.tupleLayoutSort.defaultDesc()
	case key.Matches(msg, m.keys.SortPrev):
		m.tupleLayoutSort = (m.tupleLayoutSort + tlSortCount - 1) % tlSortCount
		m.tupleLayoutSortDesc = m.tupleLayoutSort.defaultDesc()
	case key.Matches(msg, m.keys.ReverseSort):
		m.tupleLayoutSortDesc = !m.tupleLayoutSortDesc
	case key.Matches(msg, m.keys.Refresh):
		return m.reloadTupleAttrs(s, s.tupleAttrsLP)
	}
	return nil
}
