package tui

import "pgdu/internal/pg"

// actDescribeTarget names the main table of the highlighted backend's query, in that backend's database.
func actDescribeTarget(s *screen) (descTarget, bool) {
	switch s.level {
	case levelActivity:
		// Describe the main table of the highlighted backend's query, in that
		// backend's database (which may differ from the screen's connection).
		pid := activitySelectedPID(s)
		if pid == 0 {
			return descTarget{}, false
		}
		for i := range s.act.rows {
			if s.act.rows[i].PID != pid {
				continue
			}
			name := pg.MainTable(s.act.rows[i].Query)
			if name == "" {
				return descTarget{}, false
			}
			db := s.act.rows[i].Database
			if db == "" {
				db = s.db
			}
			return descTarget{byName: true, db: db, tableName: name}, true
		}
		return descTarget{}, false
	}
	return descTarget{}, false
}
