package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"pgdu/internal/pg"
)

type walOverviewLoadedMsg struct {
	db    string
	start string // resolved window start LSN
	end   string // resolved window end LSN
	stats []pg.WALRmgrStat
	err   error
}

type walSummaryLoadedMsg struct {
	db      string
	summary pg.WALSummary
	err     error
}

type walRecordsLoadedMsg struct {
	db        string
	rmgr      string
	records   []pg.WALRecord
	typeStats []pg.WALRmgrStat // per-record-type breakdown for the summary table
	err       error
}

type walBlocksLoadedMsg struct {
	db     string
	recLSN string
	blocks []pg.WALBlockRef
	err    error
}

type walCheckpointLoadedMsg struct {
	db   string
	info pg.WALCheckpointInfo
	err  error
}

type walRelationsLoadedMsg struct {
	db    string
	start string
	end   string
	rels  []pg.WALRelStat
	err   error
}

type walRelBlocksLoadedMsg struct {
	db          string
	relfilenode uint32
	blocks      []pg.WALBlockRef
	err         error
}

func (m *Model) loadWALOverviewCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		start, end, err := m.client.WALWindow(ctx, db, walWindowBytes)
		if err != nil {
			return walOverviewLoadedMsg{db: db, err: err}
		}
		stats, err := m.client.WALRmgrStats(ctx, db, start, end)
		return walOverviewLoadedMsg{db: db, start: start, end: end, stats: stats, err: err}
	})
}

func (m *Model) loadWALSummaryCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		sum, err := m.client.WALOverview(ctx, db)
		return walSummaryLoadedMsg{db: db, summary: sum, err: err}
	})
}

func (m *Model) loadWALRecordsCmd(db, start, end, rmgr string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		recs, err := m.client.WALRecords(ctx, db, start, end, rmgr)
		if err != nil {
			return walRecordsLoadedMsg{db: db, rmgr: rmgr, err: err}
		}
		// Best-effort: the per-type summary is decoration over the record
		// list, so a failure here shouldn't drop the whole load.
		stats, _ := m.client.WALRecordTypeStats(ctx, db, start, end, rmgr)
		return walRecordsLoadedMsg{db: db, rmgr: rmgr, records: recs, typeStats: stats}
	})
}

func (m *Model) loadWALBlocksCmd(db, recLSN, recEnd string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		blocks, err := m.client.WALBlocks(ctx, db, recLSN, recEnd)
		return walBlocksLoadedMsg{db: db, recLSN: recLSN, blocks: blocks, err: err}
	})
}

func (m *Model) loadWALCheckpointCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		info, err := m.client.WALCheckpoint(ctx, db)
		return walCheckpointLoadedMsg{db: db, info: info, err: err}
	})
}

func (m *Model) loadWALRelationsCmd(db, start, end string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rels, err := m.client.WALRelStats(ctx, db, start, end)
		return walRelationsLoadedMsg{db: db, start: start, end: end, rels: rels, err: err}
	})
}

func (m *Model) loadWALRelBlocksCmd(db, start, end string, relfilenode uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		blocks, err := m.client.WALRelBlocks(ctx, db, start, end, relfilenode)
		return walRelBlocksLoadedMsg{db: db, relfilenode: relfilenode, blocks: blocks, err: err}
	})
}
