package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// Synthetic sentinel paths for the two timeline anchors in the L snapshots browser.
// They start with "@" so they can't collide with absolute file paths (which start
// with "/"). "@now" represents the live "now" end; "@reset" represents the
// cumulative origin (since the last pg_stat_statements reset).
const (
	snapNow     = "@now"
	snapReset   = "@reset"
	snapSession = "@session" // the in-memory baseline from when the tool was opened
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

// diskTableResolvedMsg carries the result of resolving a query's main table to a
// catalog relation, so the disk-usage jump (u in the top-queries views) can fill
// in the placeholder parts screen or report an unresolvable name.
type diskTableResolvedMsg struct {
	name  string
	table pg.Table
	err   error
}
type bloatFilledMsg struct {
	table pg.Table
	parts []pg.Part
	err   error
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

// reindexTickMsg drives the progress poll while a REINDEX is in flight.
type reindexTickMsg struct{}

// reindexProgressMsg carries one poll of pg_stat_progress_create_index for the
// reindexing table. row is nil when nothing is reporting (yet / any more).
type reindexProgressMsg struct {
	tableOID uint32
	row      *pg.ReindexProgress
}
type heapPagesLoadedMsg struct {
	table      pg.Table
	start      int32
	count      int32
	pages      []pg.HeapPageStat
	totalPages int32
	bufs       map[int64]pg.PageBuffer // per-block shared-buffers state (nil: no temp data)
	bufsErr    error                   // why bufs is nil; a MissingExtensionError drives the hint
	err        error
}
type toastTargetResolvedMsg struct {
	table   pg.Table
	block   int32
	chunkID uint32
	err     error
}
type heapTuplesLoadedMsg struct {
	tableOID uint32
	blkno    int32
	tuples   []pg.HeapTuple

	// pkCols names the table's primary-key columns, in key order, that each
	// tuple's PK value was projected from. Empty when the table has no primary
	// key (or the catalog lookup failed) — the pk column then isn't rendered.
	pkCols []string

	err error
}
type tupleRowLoadedMsg struct {
	tableOID uint32
	ctid     string
	cells    []pg.TupleCell
	err      error
}
type toastValueLoadedMsg struct {
	tableOID uint32
	chunkID  uint32
	cells    []pg.TupleCell
	err      error
}
type tupleAttrsLoadedMsg struct {
	tableOID uint32
	blkno    int32
	lp       int32
	attrs    []pg.TupleAttr
	err      error
}
type relationsLoadedMsg struct {
	db, schema string
	rels       []pg.Relation
	err        error
}
type indexPagesLoadedMsg struct {
	indexOID   uint32
	start      int32
	count      int32
	pages      []pg.IndexPageStat
	totalPages int32
	keyCols    []pg.IndexKeyColumn // index key/INCLUDE columns for the banner
	meta       *pg.BtreeMeta       // metapage banner (nil when bt_metap failed)
	bufs       map[int64]pg.PageBuffer
	bufsErr    error
	err        error
}
type btreeLevelsLoadedMsg struct {
	indexOID uint32
	counts   []pg.BtreeLevelCount
	err      error
}
type indexTuplesLoadedMsg struct {
	indexOID uint32
	blkno    int32
	pageType string
	level    int32 // btpo_level from the probe; -1 when not probed (see loadIndexTuplesCmd)
	tuples   []pg.IndexTuple
	err      error
}
type gistPagesLoadedMsg struct {
	indexOID   uint32
	start      int32
	count      int32
	pages      []pg.GistPageStat
	totalPages int32
	keyCols    []pg.IndexKeyColumn
	bufs       map[int64]pg.PageBuffer
	bufsErr    error
	err        error
}
type gistItemsLoadedMsg struct {
	indexOID uint32
	blkno    int32
	pageType string // "leaf" / "intr" / "del" (resolved when descending)
	items    []pg.GistItem
	err      error
}
type brinPagesLoadedMsg struct {
	indexOID   uint32
	start      int32
	count      int32
	pages      []pg.BrinPageStat
	totalPages int32
	keyCols    []pg.IndexKeyColumn
	meta       *pg.BrinMeta
	bufs       map[int64]pg.PageBuffer
	bufsErr    error
	err        error
}
type brinItemsLoadedMsg struct {
	indexOID uint32
	blkno    int32
	items    []pg.BrinItem
	err      error
}
type ginPagesLoadedMsg struct {
	indexOID   uint32
	start      int32
	count      int32
	pages      []pg.GinPageStat
	totalPages int32
	keyCols    []pg.IndexKeyColumn
	meta       *pg.GinMeta
	bufs       map[int64]pg.PageBuffer
	bufsErr    error
	err        error
}
type ginItemsLoadedMsg struct {
	indexOID uint32
	blkno    int32
	items    []pg.GinItem
	err      error
}
type describeLoadedMsg struct {
	oid  uint32
	desc *pg.Description
	err  error
	// table is the described relation for table describes (zero for indexes)
	// so a screen pushed by name still learns the pg.Table the page-inspector
	// jump (`p`) needs.
	table pg.Table
}
type describeBuffersLoadedMsg struct {
	db   string
	oid  uint32
	stat pg.TableBufferStat
	err  error
}
type diagnosticLoadedMsg struct {
	key    string // Diagnostic.Key for stale-message rejection
	result *pg.DiagResult
	err    error
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

// upgradeExtensionCmd runs ALTER EXTENSION ... UPDATE and reports completion via
// the same extInstalledMsg the install path uses — onExtInstalled clears the
// prompt and reloads the screen, which now reads the lifted version.
func (m *Model) upgradeExtensionCmd(db, ext string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		err := m.client.UpdateExtension(ctx, db, ext)
		return extInstalledMsg{db: db, ext: ext, err: err}
	})
}

func (m *Model) loadHeapPagesCmd(t pg.Table, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// Buffer temperature is best-effort decoration: without pg_buffercache
		// (or the privileges to read it) the temp column simply stays hidden.
		// It must be sampled BEFORE ListHeapPages — get_raw_page reads every
		// page in the window through shared buffers, so sampling afterwards
		// would report the inspector's own reads (every page cached at
		// usagecount 1), not the pre-inspection residency.
		bufs, bufsErr := m.client.PageBuffers(ctx, t.DB, t.OID, start, count)
		pages, err := m.client.ListHeapPages(ctx, t, start, count)
		if err != nil {
			return heapPagesLoadedMsg{table: t, start: start, count: count, err: err}
		}
		// RelPages failure is non-fatal: the page list still renders, only the
		// "pages N–M / total" status snippet shows ?/?? — much better than
		// dropping the whole load on a transient pg_class read error.
		rp, _ := m.client.RelPages(ctx, t)
		return heapPagesLoadedMsg{table: t, start: start, count: count, pages: pages, totalPages: rp, bufs: bufs, bufsErr: bufsErr}
	})
}

func (m *Model) resolveToastTargetCmd(db string, toastOID, chunkID uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		t, blk, err := m.client.ToastChunkLocation(ctx, db, toastOID, chunkID)
		return toastTargetResolvedMsg{table: t, block: blk, chunkID: chunkID, err: err}
	})
}

func (m *Model) loadHeapTuplesCmd(t pg.Table, blkno int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		tuples, pkCols, err := m.client.ListHeapTuples(ctx, t, blkno)
		return heapTuplesLoadedMsg{tableOID: t.OID, blkno: blkno, tuples: tuples, pkCols: pkCols, err: err}
	})
}

func (m *Model) loadTupleRowCmd(t pg.Table, ctid string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		cells, err := m.client.ListTupleRow(ctx, t, ctid)
		return tupleRowLoadedMsg{tableOID: t.OID, ctid: ctid, cells: cells, err: err}
	})
}

func (m *Model) loadTupleAttrsCmd(t pg.Table, blkno, lp int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		attrs, err := m.client.ListTupleAttrs(ctx, t, blkno, lp)
		return tupleAttrsLoadedMsg{tableOID: t.OID, blkno: blkno, lp: lp, attrs: attrs, err: err}
	})
}

func (m *Model) loadToastValueCmd(t pg.Table, chunkID uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		cells, err := m.client.ReadToastValue(ctx, t, chunkID)
		return toastValueLoadedMsg{tableOID: t.OID, chunkID: chunkID, cells: cells, err: err}
	})
}

func (m *Model) loadRelationsCmd(db, schema string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		rs, err := m.client.ListRelations(ctx, db, schema)
		return relationsLoadedMsg{db: db, schema: schema, rels: rs, err: err}
	})
}

func (m *Model) loadIndexPagesCmd(r pg.Relation, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// Sampled before the page walk for the same reason as loadHeapPagesCmd:
		// bt_page_stats pulls every window page into shared buffers.
		bufs, bufsErr := m.client.PageBuffers(ctx, r.DB, r.OID, start, count)
		pages, err := m.client.ListIndexPages(ctx, r, start, count)
		if err != nil {
			return indexPagesLoadedMsg{indexOID: r.OID, start: start, count: count, err: err}
		}
		// RelPages errors are non-fatal: render the page list and accept "?"
		// for the totals, same approach as the heap-pages Cmd.
		rp, _ := m.client.RelPages(ctx, pg.Table{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
		// Banner data is best-effort decoration over the page list — a metap
		// or key-column read may fail (privileges, a redefined index) without
		// dropping the load. keyCols nil → no keys banner; meta nil → no
		// metapage line.
		keyCols, _ := m.client.IndexKeyColumns(ctx, r)
		var meta *pg.BtreeMeta
		if bm, err := m.client.BtreeMeta(ctx, r); err == nil {
			meta = &bm
		}
		return indexPagesLoadedMsg{indexOID: r.OID, start: start, count: count, pages: pages, totalPages: rp, keyCols: keyCols, meta: meta, bufs: bufs, bufsErr: bufsErr}
	})
}

// btreeCensusTimeout bounds the whole-index level census separately from
// queryTimeout: reading every page of a large index is legitimately slower
// than any other read in the TUI, and a late failure only costs a banner
// line, so it gets more rope than the interactive queries.
const btreeCensusTimeout = 2 * time.Minute

func (m *Model) loadBtreeLevelCountsCmd(r pg.Relation) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), btreeCensusTimeout)
		defer cancel()
		counts, err := m.client.BtreeLevelCounts(ctx, r)
		return btreeLevelsLoadedMsg{indexOID: r.OID, counts: counts, err: err}
	}
}

func (m *Model) loadIndexTuplesCmd(r pg.Relation, blkno int32, pageType string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// A downlink descent pushes the child screen with an unknown type;
		// probe it so the decode-vs-raw choice and further downlink navigation
		// stay correct as the user walks toward the leaves. Best-effort: on
		// failure the empty type just takes the raw path.
		// level stays -1 (unknown) unless the probe below runs; the direct drill
		// from levelIndexPages already set it on the screen from bt_page_stats.
		level := int32(-1)
		if pageType == "" {
			if pt, lv, err := m.client.BtreePageType(ctx, r, blkno); err == nil {
				pageType = pt
				level = lv
			}
		}
		tuples, err := m.client.ListIndexTuples(ctx, r, blkno, pageType)
		return indexTuplesLoadedMsg{indexOID: r.OID, blkno: blkno, pageType: pageType, level: level, tuples: tuples, err: err}
	})
}

func (m *Model) loadGistPagesCmd(r pg.Relation, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// Sampled before the page walk for the same reason as loadHeapPagesCmd:
		// get_raw_page pulls every window page into shared buffers.
		bufs, bufsErr := m.client.PageBuffers(ctx, r.DB, r.OID, start, count)
		pages, err := m.client.ListGistPages(ctx, r, start, count)
		if err != nil {
			return gistPagesLoadedMsg{indexOID: r.OID, start: start, count: count, err: err}
		}
		rp, _ := m.client.RelPages(ctx, pg.Table{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
		keyCols, _ := m.client.IndexKeyColumns(ctx, r) // best-effort banner
		return gistPagesLoadedMsg{indexOID: r.OID, start: start, count: count, pages: pages, totalPages: rp, keyCols: keyCols, bufs: bufs, bufsErr: bufsErr}
	})
}

func (m *Model) loadGistItemsCmd(r pg.Relation, blkno int32, pageType string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// On a downlink descent the child's role is unknown; probe its opaque
		// flags so the decode/drill path is correct. Best-effort.
		if pageType == "" {
			if leaf, deleted, err := m.client.GistPageFlags(ctx, r, blkno); err == nil {
				pageType = gistPageRole(leaf, deleted)
			}
		}
		items, err := m.client.ListGistItems(ctx, r, blkno)
		return gistItemsLoadedMsg{indexOID: r.OID, blkno: blkno, pageType: pageType, items: items, err: err}
	})
}

func (m *Model) loadBrinPagesCmd(r pg.Relation, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// Sampled before the page walk for the same reason as loadHeapPagesCmd:
		// get_raw_page pulls every window page into shared buffers.
		bufs, bufsErr := m.client.PageBuffers(ctx, r.DB, r.OID, start, count)
		pages, err := m.client.ListBrinPages(ctx, r, start, count)
		if err != nil {
			return brinPagesLoadedMsg{indexOID: r.OID, start: start, count: count, err: err}
		}
		rp, _ := m.client.RelPages(ctx, pg.Table{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
		keyCols, _ := m.client.IndexKeyColumns(ctx, r)
		var meta *pg.BrinMeta
		if bm, err := m.client.BrinMeta(ctx, r); err == nil {
			meta = &bm
		}
		return brinPagesLoadedMsg{indexOID: r.OID, start: start, count: count, pages: pages, totalPages: rp, keyCols: keyCols, meta: meta, bufs: bufs, bufsErr: bufsErr}
	})
}

func (m *Model) loadBrinItemsCmd(r pg.Relation, blkno int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		items, err := m.client.ListBrinItems(ctx, r, blkno)
		return brinItemsLoadedMsg{indexOID: r.OID, blkno: blkno, items: items, err: err}
	})
}

func (m *Model) loadGinPagesCmd(r pg.Relation, start, count int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		// Sampled before the page walk for the same reason as loadHeapPagesCmd:
		// get_raw_page pulls every window page into shared buffers.
		bufs, bufsErr := m.client.PageBuffers(ctx, r.DB, r.OID, start, count)
		pages, err := m.client.ListGinPages(ctx, r, start, count)
		if err != nil {
			return ginPagesLoadedMsg{indexOID: r.OID, start: start, count: count, err: err}
		}
		rp, _ := m.client.RelPages(ctx, pg.Table{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
		keyCols, _ := m.client.IndexKeyColumns(ctx, r)
		var meta *pg.GinMeta
		if gm, err := m.client.GinMeta(ctx, r); err == nil {
			meta = &gm
		}
		return ginPagesLoadedMsg{indexOID: r.OID, start: start, count: count, pages: pages, totalPages: rp, keyCols: keyCols, meta: meta, bufs: bufs, bufsErr: bufsErr}
	})
}

func (m *Model) loadGinItemsCmd(r pg.Relation, blkno int32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		items, err := m.client.ListGinItems(ctx, r, blkno)
		return ginItemsLoadedMsg{indexOID: r.OID, blkno: blkno, items: items, err: err}
	})
}

func (m *Model) loadDescribeTableCmd(t pg.Table) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		d, err := m.client.DescribeTable(ctx, t)
		return describeLoadedMsg{oid: t.OID, desc: d, err: err, table: t}
	})
}

// loadDescribeTableByNameCmd resolves a relation name (parsed out of a query in
// the top-queries view) to its catalog metadata, then describes it — both in one
// round-trip so the describe panel opens with a single Cmd like the others.
func (m *Model) loadDescribeTableByNameCmd(db, name string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		t, err := m.client.ResolveTable(ctx, db, name)
		if err != nil {
			return describeLoadedMsg{err: err}
		}
		d, err := m.client.DescribeTable(ctx, t)
		return describeLoadedMsg{oid: t.OID, desc: d, err: err, table: t}
	})
}

// loadDescribeIndexByNameCmd resolves an index name (from a diagnostic result
// row that carries only the index name) to its OID, then describes it — the
// index analogue of loadDescribeTableByNameCmd.
func (m *Model) loadDescribeIndexByNameCmd(db, name string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		oid, qualified, err := m.client.ResolveIndex(ctx, db, name)
		if err != nil {
			return describeLoadedMsg{err: err}
		}
		d, err := m.client.DescribeIndex(ctx, db, oid, qualified)
		return describeLoadedMsg{oid: oid, desc: d, err: err}
	})
}

// resolveDiskTableCmd resolves a relation name (parsed out of a query in the
// top-queries view) to its catalog metadata so the caller can open the
// disk-usage (parts) view for it. Only the resolve step runs here; a placeholder
// parts screen is already on the stack and onDiskTableResolved fills in the
// table and fires the parts load via loadCurrent.
func (m *Model) resolveDiskTableCmd(db, name string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		t, err := m.client.ResolveTable(ctx, db, name)
		return diskTableResolvedMsg{name: name, table: t, err: err}
	})
}

func (m *Model) loadDescribeIndexCmd(db string, oid uint32, name string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		d, err := m.client.DescribeIndex(ctx, db, oid, name)
		return describeLoadedMsg{oid: oid, desc: d, err: err}
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

// reindexProgressInterval is how often the REINDEX banner re-polls
// pg_stat_progress_create_index — slow enough not to load the server, fast
// enough that the bar visibly moves on a multi-minute rebuild.
const reindexProgressInterval = 500 * time.Millisecond

func (m *Model) reindexTick() tea.Cmd {
	return tea.Tick(reindexProgressInterval, func(time.Time) tea.Msg { return reindexTickMsg{} })
}

func (m *Model) loadReindexProgressCmd(db string, tableOID uint32) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		r, ok, err := m.client.ReindexProgress(ctx, db, tableOID)
		if err != nil || !ok {
			return reindexProgressMsg{tableOID: tableOID, row: nil}
		}
		return reindexProgressMsg{tableOID: tableOID, row: &r}
	})
}

func (m *Model) loadDiagnosticCmd(d pg.Diagnostic, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		result, err := m.client.RunDiagnostic(ctx, db, d)
		return diagnosticLoadedMsg{key: d.Key, result: result, err: err}
	})
}

// loadDiagnosticAllDBsCmd runs a per-database diagnostic against every
// connectable database and merges the results (leading "database" column). The
// same diagnosticLoadedMsg/onDiagnosticLoaded path renders the generic table.
func (m *Model) loadDiagnosticAllDBsCmd(d pg.Diagnostic) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		result, err := m.client.RunDiagnosticAllDBs(ctx, d)
		return diagnosticLoadedMsg{key: d.Key, result: result, err: err}
	})
}

// walWindowBytes is how much recent WAL the inspector analyses by default:
// one 16 MiB segment up to the current write head. Big enough to be
// interesting, small enough that pg_get_wal_stats / _records_info stay snappy
// under the 30 s query cap on a busy server.
const walWindowBytes int64 = 16 << 20
