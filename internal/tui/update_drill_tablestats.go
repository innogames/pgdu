package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// drillTableStat drills from the table overview into the disk parts view of the table under the cursor.
func (m *Model) drillTableStat(s *screen, cur item) tea.Cmd {
	// Drill into the disk "parts" view (heap/index/toast + per-index bloat)
	// for the table under the cursor. The row carries its OID in statQueryID;
	// resolve it back to the loaded TableStat (sort-order independent) and
	// reconstruct a pg.Table. The parts screen is stamped toolDisk — the only
	// tool that drives levelParts — so it behaves exactly like a disk drill.
	var ts *pg.TableStat
	for i := range s.tbl.rows {
		if int64(s.tbl.rows[i].OID) == cur.statQueryID {
			ts = &s.tbl.rows[i]
			break
		}
	}
	if ts == nil {
		return nil
	}
	t := ts.AsTable()
	if !m.vacuum.running {
		m.vacuum = vacuumState{}
	}
	next := &screen{
		level: levelParts, title: "parts", tool: toolDisk,
		db: t.DB, schema: t.Schema, table: t,
		sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}
