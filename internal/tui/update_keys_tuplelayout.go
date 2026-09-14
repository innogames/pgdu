package tui

import (
	"math"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pageinspect"
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
	m.tupleLayoutSort, m.tupleLayoutSortDesc = pageinspect.SortOffset, false
	m.showTupleValue = false
	return m.reloadTupleAttrs(s, lp)
}

// reloadTupleAttrs resets the screen's attr-split state to "loading lp" and
// issues the load — the one place the tupleAttrs* fields are armed, shared by
// the overlay's open and space-reload paths.
func (m *Model) reloadTupleAttrs(s *screen, lp int32) tea.Cmd {
	s.pages.tupleAttrsLP = lp
	s.pages.tupleAttrs = nil
	s.pages.tupleAttrsErr = nil
	s.pages.tupleAttrsLoading = true
	s.pages.toastVal = nil
	return m.loadTupleAttrsCmd(s.table, s.pages.heapPageBlkno, lp)
}

// closeTupleLayout dismisses the modal and drops the loaded split so a stale
// tupleAttrsLoadedMsg can't repopulate a closed overlay.
func (m *Model) closeTupleLayout(s *screen) {
	m.showTupleLayout = false
	m.showTupleValue = false
	s.pages.tupleAttrsLP = 0
	s.pages.tupleAttrs = nil
	s.pages.tupleAttrsErr = nil
	s.pages.tupleAttrsLoading = false
	s.pages.toastVal = nil
}

// handleTupleLayoutKey drives the modal tuple byte-layout overlay (Enter on a
// heap tuple): Up/Down/PgUp/PgDn/Top/Bottom move the legend cursor, ←/→ and r
// control the sort, space reloads the split, ? toggles the reference overlay,
// enter opens the value pane on a column (see handleTupleValueKey) and
// esc/q close. Everything else is swallowed so the list beneath
// never moves. Quit still quits. Cursor moves may overshoot — the renderer
// clamps to the segment count (same contract as handleInfoKey/scrollWindow).
func (m *Model) handleTupleLayoutKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	// The ? reference sits on top of the overlay: scroll keys move it and
	// ?/esc dismiss it (back to the layout), exactly like the level infos.
	if m.showInfo {
		return m.handleInfoKey(msg)
	}
	// The value pane sits between the two: it captures the scroll keys, and the
	// layout reference still opens on top of it.
	if m.showTupleValue {
		return m.handleTupleValueKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showInfo = true
		m.infoOffset = 0
	case key.Matches(msg, m.keys.Enter):
		// ENTER on a TOAST-pointer row jumps to that value's TOAST relation in
		// the page inspector; on a TOAST chunk's chunk_data it opens the value
		// pane over the whole reassembled value; on any other column holding
		// bytes it opens the value pane, since the legend row only has room for
		// a prefix of a long value; on any other row it just closes the overlay.
		switch oid, chunk, ok := m.tupleLayoutToastUnderCursor(s); {
		case ok:
			return m.openToastChunkNav(s, oid, chunk)
		case m.tupleLayoutValueUnderCursor(s):
			m.showTupleValue = true
			m.tupleValueOffset = 0
			if chunkID, ok := m.tupleLayoutToastValueUnderCursor(s); ok {
				return m.loadToastValue(s, chunkID)
			}
		default:
			m.closeTupleLayout(s)
		}
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
			table:   t.table}
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
		m.tupleLayoutSort = (m.tupleLayoutSort + 1) % pageinspect.SortCount
		m.tupleLayoutSortDesc = m.tupleLayoutSort.DefaultDesc()
	case key.Matches(msg, m.keys.SortPrev):
		m.tupleLayoutSort = (m.tupleLayoutSort + pageinspect.SortCount - 1) % pageinspect.SortCount
		m.tupleLayoutSortDesc = m.tupleLayoutSort.DefaultDesc()
	case key.Matches(msg, m.keys.ReverseSort):
		m.tupleLayoutSortDesc = !m.tupleLayoutSortDesc
	case key.Matches(msg, m.keys.Refresh):
		return m.reloadTupleAttrs(s, s.pages.tupleAttrsLP)
	}
	return nil
}

// tupleLayoutSegUnderCursor resolves the highlighted overlay segment, recomputing
// the same layout+sort mapping the renderer uses (view_tuple_layout.go): the
// cursor indexes the sorted legend, so order[cursor] gives the physical segment.
// Reports false when there's no tuple/segment under the cursor.
func (m *Model) tupleLayoutSegUnderCursor(s *screen) (pageinspect.Seg, bool) {
	t := s.tupleByLP(s.pages.tupleAttrsLP)
	if t == nil || len(s.pages.tupleAttrs) == 0 {
		return pageinspect.Seg{}, false
	}
	segs, _ := pageinspect.Layout(*t, s.pages.tupleAttrs)
	order := pageinspect.SortedIdx(segs, m.tupleLayoutSort, m.tupleLayoutSortDesc)
	if m.tupleLayoutCursor < 0 || m.tupleLayoutCursor >= len(order) {
		return pageinspect.Seg{}, false
	}
	return segs[order[m.tupleLayoutCursor]], true
}

// tupleLayoutToastUnderCursor reports the TOAST relation OID and chunk_id when
// the highlighted segment is a column storing an on-disk TOAST pointer.
func (m *Model) tupleLayoutToastUnderCursor(s *screen) (toastOID, chunkID uint32, ok bool) {
	seg, ok := m.tupleLayoutSegUnderCursor(s)
	if !ok || seg.Kind != pageinspect.SegColumn || seg.Attr == nil {
		return 0, 0, false
	}
	chunkID, toastOID, ok = pageinspect.ToastPointerRef(seg.Attr.Value)
	return toastOID, chunkID, ok
}

// tupleLayoutToastValueUnderCursor reports the chunk_id when the highlighted
// segment is a TOAST chunk row's chunk_data — the one column whose bytes are a
// slice of a bigger value, so its pane shows the whole value rather than the
// stored slice alone. Keyed on the attribute name because a TOAST relation's
// descriptor is fixed (chunk_id, chunk_seq, chunk_data); the schema and the
// tuple's own chunk_id guard against a user table that merely names a column
// chunk_data.
func (m *Model) tupleLayoutToastValueUnderCursor(s *screen) (chunkID uint32, ok bool) {
	if s.table.Schema != "pg_toast" {
		return 0, false
	}
	t := s.tupleByLP(s.pages.tupleAttrsLP)
	if t == nil || t.ChunkID == nil {
		return 0, false
	}
	seg, ok := m.tupleLayoutSegUnderCursor(s)
	if !ok || seg.Kind != pageinspect.SegColumn || seg.Attr == nil || seg.Attr.Name != "chunk_data" {
		return 0, false
	}
	return *t.ChunkID, true
}

// loadToastValue arms the chunk_data pane's value load, unless the overlay
// already holds (or is fetching) this very chunk — re-entering the pane after
// esc must not refetch a value that hasn't changed under us.
func (m *Model) loadToastValue(s *screen, chunkID uint32) tea.Cmd {
	if tv := s.pages.toastVal; tv != nil && tv.chunkID == chunkID {
		return nil
	}
	s.pages.toastVal = &toastValueState{chunkID: chunkID, loading: true}
	return m.loadToastValueCmd(s.table, chunkID)
}

// handleTupleValueKey drives the value pane nested in the layout overlay: the
// scroll keys move its window (scrollWindow clamps, same contract as
// handleInfoKey), ? opens the layout reference on top, enter/esc return to the
// legend. Everything else is swallowed so neither the legend nor the list
// beneath it moves. Quit still quits.
func (m *Model) handleTupleValueKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.showInfo = true
		m.infoOffset = 0
	case key.Matches(msg, m.keys.Enter), key.Matches(msg, m.keys.Back):
		m.showTupleValue = false
	case key.Matches(msg, m.keys.Up):
		m.tupleValueOffset = max(m.tupleValueOffset-1, 0)
	case key.Matches(msg, m.keys.Down):
		m.tupleValueOffset++ // clamped by scrollWindow
	case key.Matches(msg, m.keys.PageUp):
		m.tupleValueOffset = max(m.tupleValueOffset-m.pageStep(), 0)
	case key.Matches(msg, m.keys.PageDown):
		m.tupleValueOffset += m.pageStep() // clamped by scrollWindow
	case key.Matches(msg, m.keys.Top):
		m.tupleValueOffset = 0
	case key.Matches(msg, m.keys.Bottom):
		m.tupleValueOffset = math.MaxInt32 // clamped by scrollWindow
	}
	return nil
}

// tupleLayoutValueUnderCursor reports whether the highlighted segment has
// content a pane of its own can show more of: a column with stored bytes, whose
// decoded value and hex the legend row can only spell a prefix of. Header
// fields, padding and the null bitmap are already spelled out in place, so
// ENTER keeps closing the overlay there.
func (m *Model) tupleLayoutValueUnderCursor(s *screen) bool {
	seg, ok := m.tupleLayoutSegUnderCursor(s)
	return ok && tupleSegDrills(seg)
}

// tupleSegDrills is the legend's drill predicate — what paints the ↵ in front
// of a row and what Enter acts on there: a column with stored bytes opens the
// value pane (or, for a TOAST pointer, the chunk pages). Kept as one function
// so the mark and the key can't disagree.
func tupleSegDrills(seg pageinspect.Seg) bool {
	return seg.Kind == pageinspect.SegColumn && seg.Attr != nil && len(seg.Attr.Value) > 0
}

// openToastChunkNav closes the overlay and pushes a loading heap-pages screen
// for the TOAST relation, then resolves the OID→Table and the chunk's block and
// line pointer asynchronously (the pointer carries only the OID);
// onToastTargetResolved pushes the chunk's page on top and opens its layout.
// The placeholder carries the toast OID up front so findLevel + the OID guard
// target it, not the original table's heap-pages screen deeper in the stack.
func (m *Model) openToastChunkNav(s *screen, toastOID, chunkID uint32) tea.Cmd {
	m.closeTupleLayout(s)
	next := &screen{
		level: levelHeapPages, title: "toast pages", tool: s.tool,
		db: s.db, schema: "pg_toast",
		table:   pg.Table{DB: s.db, OID: toastOID, Schema: "pg_toast"},
		loading: true,
		pages:   pageState{heapWindowStart: 0, heapWindowCount: heapWindowDefault},
		sort:    sortByBlkno, sortDesc: sortByBlkno.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.resolveToastTargetCmd(s.db, toastOID, chunkID)
}
