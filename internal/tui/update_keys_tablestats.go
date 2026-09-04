package tui

// tblDescribeTarget resolves the table-overview row under the cursor to its TableStat for `d`.
func tblDescribeTarget(s *screen) (descTarget, bool) {
	curItem := s.currentItem
	switch s.level {
	case levelTableStats:
		// Generic-table rows carry the relation OID in statQueryID; resolve it
		// back to the loaded TableStat and describe by exact OID (no name lookup).
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		for i := range s.tbl.rows {
			if int64(s.tbl.rows[i].OID) == it.statQueryID {
				return descTarget{table: s.tbl.rows[i].AsTable()}, true
			}
		}
		return descTarget{}, false
	}
	return descTarget{}, false
}
