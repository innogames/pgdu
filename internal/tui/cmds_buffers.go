package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"pgdu/internal/pg"
	"pgdu/internal/sysmem"
)

type bufferStatsLoadedMsg struct {
	db, schema string
	stats      []pg.TableBufferStat
	err        error
}

type bufferSummaryLoadedMsg struct {
	db      string
	summary pg.BufferCacheSummary
	err     error
}

type bufferDetailLoadedMsg struct {
	db        string
	oid       uint32
	counts    []pg.BufferUsageCount
	blockSize int64
	err       error
}

type shmemLoadedMsg struct {
	db     string
	allocs []pg.ShmemAllocation
	err    error
}

func (m *Model) loadBufferStatsCmd(db, schema string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		stats, err := m.client.TableBufferStats(ctx, db, schema)
		return bufferStatsLoadedMsg{db: db, schema: schema, stats: stats, err: err}
	})
}

func (m *Model) loadBufferSummaryCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		sum, err := m.client.BufferCacheSummary(ctx, db)
		if err == nil {
			mem := sysmem.Read()
			sum.ServerMemBytes = mem.Total
			sum.ServerMemAvailableBytes = mem.Available
			sum.ServerMemFreeBytes = mem.Free
		}
		return bufferSummaryLoadedMsg{db: db, summary: sum, err: err}
	})
}

func (m *Model) loadBufferDetailCmd(db string, oid uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		counts, blockSize, err := m.client.TableBufferUsageCounts(ctx, db, oid)
		return bufferDetailLoadedMsg{db: db, oid: oid, counts: counts, blockSize: blockSize, err: err}
	})
}

func (m *Model) loadShmemCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		allocs, err := m.client.ShmemAllocations(ctx, db)
		return shmemLoadedMsg{db: db, allocs: allocs, err: err}
	})
}

// loadDescribeBuffersCmd fetches the single-table cache-footprint stat for the
// describe-table view. Runs separately from the describe load so a missing
// pg_buffercache (or any buffer error) never breaks the columns panel.
func (m *Model) loadDescribeBuffersCmd(db string, oid uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		stat, err := m.client.TableBufferStatByOID(ctx, db, oid)
		return describeBuffersLoadedMsg{db: db, oid: oid, stat: stat, err: err}
	})
}
