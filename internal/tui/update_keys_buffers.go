package tui

import "pgdu/internal/pg"

// bufDescribeTarget reconstructs the pg.Table behind a buffer-tables row or the inspected table for `d`.
func bufDescribeTarget(s *screen) (descTarget, bool) {
	curItem := s.currentItem
	switch s.level {
	case levelBufferTables:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		st, ok := it.data.(pg.TableBufferStat)
		if !ok {
			return descTarget{}, false
		}
		// TableBufferStat has no pg.Table field; reconstruct from its own fields.
		return descTarget{table: pg.Table{
			DB: st.DB, Schema: st.Schema, Name: st.Name,
			OID: st.OID, TotalBytes: st.TotalBytes,
		}}, true
	case levelBufferDetail:
		// The inspected table is carried on the screen; same reconstruction as
		// the buffer-tables list row.
		if s.buf.detail == nil {
			return descTarget{}, false
		}
		st := s.buf.detail
		return descTarget{table: pg.Table{
			DB: st.DB, Schema: st.Schema, Name: st.Name,
			OID: st.OID, TotalBytes: st.TotalBytes,
		}}, true
	}
	return descTarget{}, false
}
