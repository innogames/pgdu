package tui

import "pgdu/internal/pg"

// toggleBufSplit flips the buffers list between per-table and per-relation
// rows, re-projected from the loaded snapshot, and keeps the cursor on the
// table it was on (its first row in the new order).
func (m *Model) toggleBufSplit(s *screen) {
	var oid uint32
	if it, ok := s.currentItem(); ok {
		if st, ok := it.data.(pg.TableBufferStat); ok {
			oid = st.OID
		}
	}
	s.buf.byRel = !s.buf.byRel
	s.items = buildBufferItems(s)
	m.applySort(s)
	s.resetCursor()
	for i, vi := range s.visibleIndexes() {
		if st, ok := s.items[vi].data.(pg.TableBufferStat); ok && st.OID == oid {
			s.cursor = i
			break
		}
	}
}

// bufStatAt is the buffers stat `d` and `p` act on: the highlighted list row,
// or the stat the detail screen inspects.
func bufStatAt(s *screen) (pg.TableBufferStat, bool) {
	switch s.level {
	case levelBufferTables:
		it, ok := s.currentItem()
		if !ok {
			return pg.TableBufferStat{}, false
		}
		st, ok := it.data.(pg.TableBufferStat)
		return st, ok
	case levelBufferDetail:
		if s.buf.detail == nil {
			return pg.TableBufferStat{}, false
		}
		return *s.buf.detail, true
	}
	return pg.TableBufferStat{}, false
}

// bufStatTable reconstructs the owning pg.Table of a buffers stat, which has
// no pg.Table field of its own.
func bufStatTable(st pg.TableBufferStat) pg.Table {
	return pg.Table{DB: st.DB, Schema: st.Schema, Name: st.Name, OID: st.OID, TotalBytes: st.TotalBytes}
}

// bufDescribeTarget names what `d` describes on the buffer screens: a split-view
// index row's index, else the (owning) table.
func bufDescribeTarget(s *screen) (descTarget, bool) {
	st, ok := bufStatAt(s)
	if !ok {
		return descTarget{}, false
	}
	if st.Rel != nil && st.Rel.Kind == pg.PartIndex {
		return descTarget{isIndex: true, db: st.DB, indexOID: st.Rel.OID, indexName: st.Schema + "." + st.Rel.Name}, true
	}
	return descTarget{table: bufStatTable(st)}, true
}
