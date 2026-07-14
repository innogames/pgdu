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
	case key.Matches(msg, m.keys.Enter):
		// ENTER on a TOAST-pointer row jumps to that value's TOAST relation in
		// the page inspector; on any other row it just closes the overlay.
		if oid, chunk, ok := m.tupleLayoutToastUnderCursor(s); ok {
			return m.openToastChunkNav(s, oid, chunk)
		}
		m.closeTupleLayout(s)
	case key.Matches(msg, m.keys.Back):
		m.closeTupleLayout(s)
	case key.Matches(msg, m.keys.Describe):
		// Same describe target the list levels resolve (the inspected table);
		// close the overlay first so we don't return to it behind the panel.
		t, ok := describeTarget(s)
		if !ok {
			return nil
		}
		m.closeTupleLayout(s)
		next := &screen{
			level:   levelDescribe,
			title:   "describe",
			tool:    s.tool,
			db:      s.db,
			schema:  s.schema,
			loading: true,
			table:   t.table,
		}
		m.stack = append(m.stack, next)
		return m.loadDescribeTableCmd(t.table)
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

// tupleLayoutSegUnderCursor resolves the highlighted overlay segment, recomputing
// the same layout+sort mapping the renderer uses (view_tuple_layout.go): the
// cursor indexes the sorted legend, so order[cursor] gives the physical segment.
// Reports false when there's no tuple/segment under the cursor.
func (m *Model) tupleLayoutSegUnderCursor(s *screen) (tupleSeg, bool) {
	t := s.tupleByLP(s.tupleAttrsLP)
	if t == nil || len(s.tupleAttrs) == 0 {
		return tupleSeg{}, false
	}
	segs, _ := computeTupleLayout(*t, s.tupleAttrs)
	order := sortedTupleSegIdx(segs, m.tupleLayoutSort, m.tupleLayoutSortDesc)
	if m.tupleLayoutCursor < 0 || m.tupleLayoutCursor >= len(order) {
		return tupleSeg{}, false
	}
	return segs[order[m.tupleLayoutCursor]], true
}

// tupleLayoutToastUnderCursor reports the TOAST relation OID and chunk_id when
// the highlighted segment is a column storing an on-disk TOAST pointer.
func (m *Model) tupleLayoutToastUnderCursor(s *screen) (toastOID, chunkID uint32, ok bool) {
	seg, ok := m.tupleLayoutSegUnderCursor(s)
	if !ok || seg.kind != segColumn || seg.attr == nil {
		return 0, 0, false
	}
	chunkID, toastOID, ok = toastPointerRef(seg.attr.Value)
	return toastOID, chunkID, ok
}

// openToastChunkNav closes the overlay and pushes a loading heap-pages screen
// for the TOAST relation, then resolves the OID→Table and the chunk's block
// asynchronously (the pointer carries only the OID). The placeholder carries the
// toast OID up front so findLevel + the OID guard target it, not the original
// table's heap-pages screen deeper in the stack.
func (m *Model) openToastChunkNav(s *screen, toastOID, chunkID uint32) tea.Cmd {
	m.closeTupleLayout(s)
	next := &screen{
		level: levelHeapPages, title: "toast pages", tool: s.tool,
		db: s.db, schema: "pg_toast",
		table:           pg.Table{DB: s.db, OID: toastOID, Schema: "pg_toast"},
		loading:         true,
		heapWindowStart: 0, heapWindowCount: heapWindowDefault,
		sort: sortByBlkno, sortDesc: sortByBlkno.defaultDesc(),
	}
	m.stack = append(m.stack, next)
	return m.resolveToastTargetCmd(s.db, toastOID, chunkID)
}
