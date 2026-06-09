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
type toastValueLoadedMsg struct {
	tableOID uint32
	chunkID  uint32
	cells    []pg.TupleCell
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
	err        error
}
type indexTuplesLoadedMsg struct {
	indexOID uint32
	blkno    int32
	pageType string
	tuples   []pg.IndexTuple
	err      error
}
type describeLoadedMsg struct {
	oid  uint32
	desc *pg.Description
	err  error
}
type diagnosticLoadedMsg struct {
	key    string // Diagnostic.Key for stale-message rejection
	result *pg.DiagResult
	err    error
}
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
type statementsLoadedMsg struct {
	db            string
	stats         []pg.QueryStat // raw cumulative snapshot; diffed against the baseline
	trackPlanning bool           // whether plan time is being collected
	statsReset    time.Time      // pg_stat_statements_info.stats_reset — guards a disk baseline
	err           error
}

// Snapshot persistence messages (levelStatements / levelSnapshots).
type snapshotSavedMsg struct {
	path string
	err  error
}
type snapshotsListedMsg struct {
	dir       string
	metas     []pg.SnapshotMeta
	liveReset time.Time // current pg_stat_statements stats_reset — drops invalidated snapshots
	err       error
}
type snapshotBaseLoadedMsg struct {
	snap *pg.Snapshot
	err  error
}
type snapshotFrozenLoadedMsg struct {
	base *pg.Snapshot
	end  *pg.Snapshot
	err  error
}

// statementsTickMsg drives the self-rescheduling refresh of the top-queries
// table so it behaves as a live "since you opened it" monitor.
type statementsTickMsg struct{}

type statementSampleLoadedMsg struct {
	db        string
	query     string // matches screen.statDetail.Query for stale-message rejection
	sample    string
	real      bool // sample is a real captured example (pg_qualstats), not synthesized
	fromData  bool // synthesized, but ≥1 placeholder was filled with a real value sampled from the live table
	qualstats bool // pg_qualstats is installed in db (drives the source hint / captured-values affordance)
	// installable is true when pg_qualstats is absent but already in
	// shared_preload_libraries, so a one-key CREATE EXTENSION would enable real
	// values. Drives the detail view's optional install hint.
	installable bool
	err         error
}
type statementExplainLoadedMsg struct {
	db      string
	query   string // matches screen.statDetail.Query for stale-message rejection
	plan    string
	err     error
	analyze bool // plan came from EXPLAIN ANALYZE rather than the generic plan
}
type statementSamplesLoadedMsg struct {
	db      string
	queryID int64 // matches screen.statDetail.QueryID for stale-message rejection
	samples []pg.QualSample
	err     error
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
		pages, err := m.client.ListIndexPages(ctx, r, start, count)
		if err != nil {
			return indexPagesLoadedMsg{indexOID: r.OID, start: start, count: count, err: err}
		}
		// RelPages errors are non-fatal: render the page list and accept "?"
		// for the totals, same approach as the heap-pages Cmd.
		rp, _ := m.client.RelPages(ctx, pg.Table{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
		return indexPagesLoadedMsg{indexOID: r.OID, start: start, count: count, pages: pages, totalPages: rp}
	})
}

func (m *Model) loadIndexTuplesCmd(r pg.Relation, blkno int32, pageType string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		tuples, err := m.client.ListIndexTuples(ctx, r, blkno, pageType)
		return indexTuplesLoadedMsg{indexOID: r.OID, blkno: blkno, pageType: pageType, tuples: tuples, err: err}
	})
}

func (m *Model) loadDescribeTableCmd(t pg.Table) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		d, err := m.client.DescribeTable(ctx, t)
		return describeLoadedMsg{oid: t.OID, desc: d, err: err}
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
		return describeLoadedMsg{oid: t.OID, desc: d, err: err}
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

func (m *Model) loadDiagnosticCmd(d pg.Diagnostic, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		result, err := m.client.RunDiagnostic(ctx, db, d)
		return diagnosticLoadedMsg{key: d.Key, result: result, err: err}
	})
}

// walWindowBytes is how much recent WAL the inspector analyses by default:
// one 16 MiB segment up to the current write head. Big enough to be
// interesting, small enough that pg_get_wal_stats / _records_info stay snappy
// under the 30 s query cap on a busy server.
const walWindowBytes int64 = 16 << 20

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

func (m *Model) loadStatementsCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		stats, err := m.client.StatementSnapshot(ctx, db)
		if err != nil {
			return statementsLoadedMsg{db: db, err: err}
		}
		tp, _ := m.client.TrackPlanning(ctx, db)     // best-effort column decoration
		reset, _ := m.client.StatementsInfo(ctx, db) // best-effort reset guard for disk baselines
		return statementsLoadedMsg{db: db, stats: stats, trackPlanning: tp, statsReset: reset}
	})
}

// saveSnapshotCmd captures the current pg_stat_statements counters for db and
// writes them to the snapshot directory, reporting the resulting path.
func (m *Model) saveSnapshotCmd(db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		snap, err := m.client.CaptureSnapshot(ctx, db)
		if err != nil {
			return snapshotSavedMsg{err: err}
		}
		path, err := pg.SaveSnapshot(m.snapshotDir, snap)
		return snapshotSavedMsg{path: path, err: err}
	})
}

// listSnapshotsCmd reads the snapshot directory for the browser, along with the
// current pg_stat_statements stats_reset for db so the handler can drop snapshots
// the live counters have since outgrown (a reset between capture and now).
func (m *Model) listSnapshotsCmd(dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		metas, err := pg.ListSnapshots(dir)
		reset, _ := m.client.StatementsInfo(ctx, db) // best-effort validity filter
		return snapshotsListedMsg{dir: dir, metas: metas, liveReset: reset, err: err}
	})
}

// loadSnapshotBaseCmd loads one snapshot to use as the live window's baseline.
func (m *Model) loadSnapshotBaseCmd(path string) tea.Cmd {
	return query(func(context.Context) tea.Msg {
		snap, err := pg.LoadSnapshot(path)
		return snapshotBaseLoadedMsg{snap: snap, err: err}
	})
}

// loadSnapshotFrozenCmd loads two snapshots for a frozen A→B diff (base→end).
func (m *Model) loadSnapshotFrozenCmd(basePath, endPath string) tea.Cmd {
	return query(func(context.Context) tea.Msg {
		base, err := pg.LoadSnapshot(basePath)
		if err != nil {
			return snapshotFrozenLoadedMsg{err: err}
		}
		end, err := pg.LoadSnapshot(endPath)
		return snapshotFrozenLoadedMsg{base: base, end: end, err: err}
	})
}

// deleteSnapshotCmd removes a snapshot file then re-lists the directory so the
// browser refreshes; the listing carries any delete error.
func (m *Model) deleteSnapshotCmd(path, dir, db string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		if err := pg.DeleteSnapshot(path); err != nil {
			return snapshotsListedMsg{dir: dir, err: err}
		}
		metas, err := pg.ListSnapshots(dir)
		reset, _ := m.client.StatementsInfo(ctx, db)
		return snapshotsListedMsg{dir: dir, metas: metas, liveReset: reset, err: err}
	})
}

// statementsTick schedules the next top-queries re-sample, or returns nil when
// auto-refresh is off — either disabled by config (statRefresh <= 0) or paused
// at runtime (t key). Returning nil stops the self-rescheduling loop; a resume
// or re-entry into the tool restarts it.
func (m *Model) statementsTick() tea.Cmd {
	if m.statRefresh <= 0 || m.statPaused {
		return nil
	}
	return tea.Tick(m.statRefresh, func(time.Time) tea.Msg {
		return statementsTickMsg{}
	})
}

// loadStatementSampleCmd resolves the example call to show under a query. It
// prefers a *real* example query from pg_qualstats (real captured constants, so
// EXPLAIN reflects the plan a real call gets); when pg_qualstats is absent or
// has sampled nothing for this queryid yet, it falls back to synthesizing typed
// literals from the inferred $n types (BuildSampleCall). The qualstats flag is
// reported either way so the detail view can label the source and offer the
// captured-values list only when there's real data behind it.
func (m *Model) loadStatementSampleCmd(db string, queryID int64, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		qualstats := m.client.EnsureQualstats(ctx, db) == nil
		if qualstats {
			// pg_qualstats caps example queries at track_activity_query_size, so a
			// long statement comes back truncated mid-token — unusable for EXPLAIN.
			// Reject those and fall through to the synthesized (full-length) call.
			if ex, err := m.client.QualstatsExampleQuery(ctx, db, queryID); err == nil && ex != "" && pg.QualstatsExampleUsable(queryText, ex) {
				return statementSampleLoadedMsg{db: db, query: queryText, sample: ex, real: true, qualstats: true}
			}
		}
		// Absent but preloaded → a plain CREATE EXTENSION would enable real values;
		// surface that as an install hint rather than only synthesizing literals.
		installable := false
		if !qualstats {
			installable, _ = m.client.QualstatsPreloaded(ctx, db)
		}
		params, err := m.client.InferParams(ctx, db, queryText)
		if err != nil {
			return statementSampleLoadedMsg{db: db, query: queryText, err: err, qualstats: qualstats, installable: installable}
		}
		// Pull real values for the predicate columns from the live table so the
		// synthesized call uses constants that actually exist in the data; any
		// ordinal it can't resolve falls back to a generic typed literal.
		real := m.client.SampleParamValues(ctx, db, queryText, params)
		return statementSampleLoadedMsg{db: db, query: queryText, sample: pg.BuildSampleCall(queryText, params, real),
			fromData: len(real) > 0, qualstats: qualstats, installable: installable}
	})
}

func (m *Model) loadStatementExplainCmd(db, queryText string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainGeneric(ctx, db, queryText)
		return statementExplainLoadedMsg{db: db, query: queryText, plan: plan, err: err}
	})
}

// loadStatementSamplesCmd fetches the real predicate constants pg_qualstats
// captured for queryID — the captured-values list behind the detail view's `p`.
func (m *Model) loadStatementSamplesCmd(db string, queryID int64) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		if err := m.client.EnsureQualstats(ctx, db); err != nil {
			return statementSamplesLoadedMsg{db: db, queryID: queryID, err: err}
		}
		samples, err := m.client.QualstatsSamples(ctx, db, queryID)
		return statementSamplesLoadedMsg{db: db, queryID: queryID, samples: samples, err: err}
	})
}

// loadStatementExplainLiteralCmd runs a plain EXPLAIN (no GENERIC_PLAN, no
// ANALYZE) on sampleCall, a fully-literal real example query. matchQuery is the
// normalized text used only to reject stale messages. Used in place of the
// generic plan when a real pg_qualstats example is available, so the planner
// sees real values instead of $n.
func (m *Model) loadStatementExplainLiteralCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainLiteral(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, plan: plan, err: err}
	})
}

// loadStatementExplainAnalyzeCmd runs EXPLAIN ANALYZE on sampleCall (a fully
// literal query). matchQuery is the normalized query text used only to reject
// stale messages — sampleCall is what actually executes.
func (m *Model) loadStatementExplainAnalyzeCmd(db, matchQuery, sampleCall string) tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		plan, err := m.client.ExplainAnalyze(ctx, db, sampleCall)
		return statementExplainLoadedMsg{db: db, query: matchQuery, plan: plan, err: err, analyze: true}
	})
}
