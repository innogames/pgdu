package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// drillWAL dispatches Enter on the WAL overview by which of its two tables the
// row belongs to: a resource manager opens its records, a relation its block
// references. Section rows (titles, Σ) carry neither payload and stay inert.
func (m *Model) drillWAL(s *screen, cur item) tea.Cmd {
	if _, ok := cur.data.(pg.WALRelStat); ok {
		return m.drillWALRelation(s, cur)
	}
	return m.drillWALRmgr(s, cur)
}

// drillWALRmgr lists the records of the highlighted resource manager.
func (m *Model) drillWALRmgr(s *screen, cur item) tea.Cmd {
	st, ok := cur.data.(pg.WALRmgrStat)
	if !ok || st.Count == 0 {
		return nil
	}
	next := &screen{
		level: levelWALRecords, title: "wal records", tool: s.tool,
		db: s.db, wal: walState{rmgr: st.Name, start: s.wal.start, end: s.wal.end},
		sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillWALRecord lists the block references of the highlighted record.
func (m *Model) drillWALRecord(s *screen, cur item) tea.Cmd {
	r, ok := cur.data.(pg.WALRecord)
	if !ok {
		return nil
	}
	next := &screen{
		level: levelWALBlocks, title: "wal blocks", tool: s.tool,
		db: s.db, wal: walState{rmgr: s.wal.rmgr, recLSN: r.StartLSN, recEnd: r.EndLSN},
		sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillWALRelation lists the block references touching the highlighted relation.
func (m *Model) drillWALRelation(s *screen, cur item) tea.Cmd {
	st, ok := cur.data.(pg.WALRelStat)
	if !ok || st.RecCount == 0 {
		return nil
	}
	// The block-refs list is keyed on relfilenode; carry a human label for
	// the breadcrumb/status row (resolved name, or the numeric fallback).
	label := st.RelName
	if label == "" {
		label = fmt.Sprintf("relfilenode %d", st.RelFileNode)
	}
	if st.IsToast {
		label += " (toast)"
	}
	next := &screen{
		level: levelWALRelBlocks, title: "wal rel blocks", tool: s.tool,
		db: s.db, wal: walState{start: s.wal.start, end: s.wal.end, relFilenode: st.RelFileNode, relLabel: label},
		sort: sortBySize, sortDesc: sortBySize.defaultDesc()}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}

// drillWALBlock opens the payload of the highlighted block reference: what the
// record actually wrote for that page (change data, decoded tuple, page image).
func (m *Model) drillWALBlock(s *screen, cur item) tea.Cmd {
	b, ok := cur.data.(pg.WALBlockRef)
	if !ok || b.StartLSN == "" {
		return nil
	}
	ref := b
	next := &screen{
		level: levelWALBlockDetail, title: "wal block", tool: s.tool,
		db: s.db, wal: walState{start: s.wal.start, end: s.wal.end, rmgr: s.wal.rmgr, blockRef: &ref},
		sort: sortByName, sortDesc: false}
	m.stack = append(m.stack, next)
	return m.loadCurrent()
}
