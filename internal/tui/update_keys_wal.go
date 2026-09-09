package tui

import "pgdu/internal/pg"

// walDescribeTarget names the relation behind a WAL row for `d`: a by-relation
// row on the overview, or a block reference on the records / relation drill-
// downs. WAL only knows relations by (tablespace, relfilenode), so the target is
// resolved server-side in the row's database — the connection database for a
// shared relation (reldatabase 0). Rmgr rows, section lines and unresolved
// refs (relfilenode 0) have nothing to describe.
func walDescribeTarget(s *screen) (descTarget, bool) {
	it, ok := s.currentItem()
	if !ok {
		return descTarget{}, false
	}
	var db string
	var tablespace, filenode uint32
	switch v := it.data.(type) {
	case pg.WALRelStat:
		db, tablespace, filenode = v.DBName, v.RelTablespace, v.RelFileNode
	case pg.WALBlockRef:
		db, tablespace, filenode = v.DBName, v.RelTablespace, v.RelFileNode
	default:
		return descTarget{}, false
	}
	if filenode == 0 {
		return descTarget{}, false
	}
	if db == "" {
		db = s.db
	}
	return descTarget{byFilenode: true, db: db, tablespace: tablespace, filenode: filenode}, true
}
