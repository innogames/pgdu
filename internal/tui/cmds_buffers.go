package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
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
	oid       uint32 // the detail's TableBufferStat.Key
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
		stats, err := m.client.RelationBufferStats(ctx, db, schema)
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

// loadBufferDetailCmd fetches the temperature histogram of a buffers-list
// row: the whole table for a per-table row, the one relation for a split-view
// row.
func (m *Model) loadBufferDetailCmd(db string, st pg.TableBufferStat) tea.Cmd {
	oid, whole := st.Key(), st.Rel == nil
	return query(func(ctx context.Context) tea.Msg {
		counts, blockSize, err := m.client.TableBufferUsageCounts(ctx, db, oid, whole)
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

// bufferEvictedMsg carries the outcome of a pg_buffercache_evict_* call.
type bufferEvictedMsg struct {
	db     string
	report string
	err    error
}

// evictRelationCmd drops one relation's unpinned shared buffers. Not wrapped in
// query()'s read timeout: eviction walks the whole buffer pool, which on a
// large shared_buffers takes longer than a read is allowed to.
func (m *Model) evictRelationCmd(db string, oid uint32) tea.Cmd {
	return func() tea.Msg {
		report, err := m.client.EvictRelation(context.Background(), db, oid)
		return bufferEvictedMsg{db: db, report: report, err: err}
	}
}

// evictAllCmd drops every unpinned shared buffer in the cluster.
func (m *Model) evictAllCmd(db string) tea.Cmd {
	return func() tea.Msg {
		report, err := m.client.EvictAllBuffers(context.Background(), db)
		return bufferEvictedMsg{db: db, report: report, err: err}
	}
}
