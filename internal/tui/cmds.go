package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
	"pgdu/internal/sysmem"
)

// --- messages ---

type databasesLoadedMsg struct {
	dbs []pg.Database
	err error
}
type schemasLoadedMsg struct {
	db      string
	schemas []pg.Schema
	err     error
}
type tablesLoadedMsg struct {
	db, schema string
	tables     []pg.Table
	err        error
}
type partsLoadedMsg struct {
	table pg.Table
	parts []pg.Part
	err   error
}
type bloatFilledMsg struct {
	table pg.Table
	parts []pg.Part
	err   error
}
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
type columnsLoadedMsg struct {
	tableOID uint32
	columns  []pg.Column
	err      error
}
type extStatusMsg struct {
	db     string
	ext    string
	status pg.ExtensionStatus
	err    error
}
type extInstalledMsg struct {
	db  string
	ext string
	err error
}
type reindexDoneMsg struct {
	tableOID  uint32
	indexName string
	err       error
}
type heapPagesLoadedMsg struct {
	table      pg.Table
	start      int32
	count      int32
	pages      []pg.HeapPageStat
	totalPages int32
	err        error
}
type heapTuplesLoadedMsg struct {
	tableOID uint32
	blkno    int32
	tuples   []pg.HeapTuple
	err      error
}
type tupleRowLoadedMsg struct {
	tableOID uint32
	ctid     string
	cells    []pg.TupleCell
	err      error
}

// --- commands ---

// queryTimeout caps every read-side query. Big enough that an honestly slow
// catalog scan completes; small enough that a hung connection doesn't wedge
// the TUI. Write commands (REINDEX) intentionally skip this — they can run
// for minutes against a large index.
const queryTimeout = 30 * time.Second

// query wraps a read-side client call with a bounded context. Returns a Cmd
// that runs fn under a fresh context with queryTimeout and propagates its
// result message.
func query(fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		return fn(ctx)
	}
}

func (m *Model) loadDatabasesCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		dbs, err := m.client.ListDatabases(ctx)
		return databasesLoadedMsg{dbs: dbs, err: err}
	})
}

func (m *Model) loadSchemasCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		ss, err := m.client.ListSchemas(ctx, db)
		return schemasLoadedMsg{db: db, schemas: ss, err: err}
	})
}

func (m *Model) loadTablesCmd(db, schema string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		ts, err := m.client.ListTables(ctx, db, schema)
		return tablesLoadedMsg{db: db, schema: schema, tables: ts, err: err}
	})
}

func (m *Model) loadPartsCmd(t pg.Table) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		parts, err := m.client.TableParts(ctx, t)
		return partsLoadedMsg{table: t, parts: parts, err: err}
	})
}

func (m *Model) fillBloatCmd(t pg.Table, parts []pg.Part) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.FillBloat(ctx, t, parts)
		return bloatFilledMsg{table: t, parts: parts, err: err}
	})
}

func (m *Model) loadColumnsCmd(t pg.Table) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		cols, err := m.client.ListColumns(ctx, t)
		return columnsLoadedMsg{tableOID: t.OID, columns: cols, err: err}
	})
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

func (m *Model) probeExtensionCmd(db, ext string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		st, err := m.client.ProbeExtension(ctx, db, ext)
		return extStatusMsg{db: db, ext: ext, status: st, err: err}
	})
}

func (m *Model) installExtensionCmd(db, ext string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.CreateExtension(ctx, db, ext)
		return extInstalledMsg{db: db, ext: ext, err: err}
	})
}

func (m *Model) loadHeapPagesCmd(t pg.Table, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		pages, err := m.client.ListHeapPages(ctx, t, start, count)
		if err != nil {
			return heapPagesLoadedMsg{table: t, start: start, count: count, err: err}
		}
		// RelPages failure is non-fatal: the page list still renders, only the
		// "pages N–M / total" status snippet shows ?/?? — much better than
		// dropping the whole load on a transient pg_class read error.
		rp, _ := m.client.RelPages(ctx, t)
		return heapPagesLoadedMsg{table: t, start: start, count: count, pages: pages, totalPages: rp}
	})
}

func (m *Model) loadHeapTuplesCmd(t pg.Table, blkno int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		tuples, err := m.client.ListHeapTuples(ctx, t, blkno)
		return heapTuplesLoadedMsg{tableOID: t.OID, blkno: blkno, tuples: tuples, err: err}
	})
}

func (m *Model) loadTupleRowCmd(t pg.Table, ctid string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		cells, err := m.client.ListTupleRow(ctx, t, ctid)
		return tupleRowLoadedMsg{tableOID: t.OID, ctid: ctid, cells: cells, err: err}
	})
}

func (m *Model) reindexIndexCmd(t pg.Table, indexName string) tea.Cmd {
	// REINDEX CONCURRENTLY can take a long time on big indexes — pgxpool will
	// honour Postgres' own statement_timeout if set. No client-side cap.
	return func() tea.Msg {
		err := m.client.ReindexIndex(context.Background(), t, indexName)
		return reindexDoneMsg{tableOID: t.OID, indexName: indexName, err: err}
	}
}
