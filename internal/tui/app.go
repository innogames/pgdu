package tui

import (
	"slices"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pageinspect"
	"pgdu/internal/pg"
	"pgdu/internal/pgbouncer"
	"pgdu/internal/pglog"
	"pgdu/internal/prefs"
	"pgdu/internal/procfs"
)

type level int

const (
	levelTools level = iota
	levelDatabases
	levelSchemas
	levelTables
	levelParts
	levelBufferTables
	levelBufferDetail // single table: cache footprint + clock-sweep temperature histogram
	levelShmem        // whole shared-memory segment map (pg_shmem_allocations), grouped by subsystem
	levelColumns
	levelHeapPages
	levelHeapTuples
	levelTupleRow
	levelRelations
	levelIndexPages
	levelIndexTuples
	levelDescribe
	levelDiagnostics      // flat list of diagnostic queries (toolTools)
	levelDiagnosticResult // result table for a selected diagnostic query
	levelWAL              // WAL inspector overview: per-resource-manager stats
	levelWALRecords       // individual WAL records for one resource manager
	levelWALBlocks        // block references of one WAL record
	levelWALRelBlocks     // block references of one relation across the window
	levelWALBlockDetail   // one block reference with its payload: change data, decoded tuple, page image
	levelStatements       // pg_stat_statements top-queries table (toolQueries)
	levelStatementDetail  // single query: metrics, sample call, EXPLAIN
	levelStatementSamples // captured real predicate constants (pg_qualstats) for one query
	levelStatementResult  // rows returned by executing a query (psql-style result table)
	levelSnapshots        // on-disk top-queries snapshots browser (load as baseline / A→B)
	levelMaintenance      // server-health dashboard (toolMaintenance)
	levelSettings         // pg_settings browser (child of levelMaintenance)
	levelActivity         // live server activity from pg_stat_activity (toolActivity)
	levelLockTree         // blocking-chain forest from pg_locks (child of levelActivity)
	levelTableStats       // per-table statistics overview for one schema (toolTableStats)
	levelProgress         // live pg_stat_progress_* monitor (child of levelMaintenance)
	levelWaitProfile      // wait-event sampling profile ('W' on levelActivity)
	levelLogFiles         // log-analyzer file picker (toolLogs)
	levelLogs             // log-analyzer overview: aggregated groups ⇄ chronological timeline
	levelLogGroup         // the entries behind one aggregated log group
	levelLogEntry         // one full log record (message + DETAIL/HINT/STATEMENT/CONTEXT)
	levelPgBouncers       // pgbouncer instance list (toolPgBouncer)
	levelPgBouncer        // one instance: version/state/lists header + SHOW menu
	levelPgBouncerShow    // one SHOW table of an instance, parameterized by screen.pgbShow
)

// levelLast is the highest level value; tests iterate levelTools..levelLast.
const levelLast = levelPgBouncerShow

// tool identifies which top-level statistic the user is exploring.
// Propagated down the stack so each level knows which leaf to render.
type tool int

const (
	toolDisk tool = iota
	toolBuffers
	toolPageInspect
	toolTools       // diagnostic SQL query runner
	toolWAL         // write-ahead-log inspector
	toolQueries     // pg_stat_statements top-queries (powa-style)
	toolMaintenance // server-health dashboard + settings browser
	toolActivity    // live server activity (pg_stat_activity)
	toolTableStats  // per-table statistics overview (pg_stat_all_tables + sizes)
	toolLogs        // server-log analyzer (levelLogFiles → levelLogs)
	toolPgBouncer   // pgbouncer console browser (levelPgBouncers → levelPgBouncer → levelPgBouncerShow)
)

func (t tool) Name() string {
	switch t {
	case toolDisk:
		return "disk"
	case toolBuffers:
		return "buffers"
	case toolPageInspect:
		return "pageinspect"
	case toolTools:
		return "tools"
	case toolWAL:
		return "wal"
	case toolQueries:
		return "queries"
	case toolMaintenance:
		return "system overview"
	case toolActivity:
		return "activity"
	case toolTableStats:
		return "tables"
	case toolLogs:
		return "logs"
	case toolPgBouncer:
		return "pgbouncer"
	}
	return "?"
}

// item is the row data the renderer consumes; concrete payload is in `data`.
type item struct {
	name        string
	size        int64
	bloat       int64
	hasBloat    bool // true once bloat has been measured (even if zero)
	hasChildren bool // true when pressing Enter on this row drills into a submenu
	detail      string
	// detailStyled marks detail as carrying its own lipgloss styling (colored
	// segments). renderRow then prints it verbatim instead of wrapping it in
	// styleMuted, which would clobber the inner colors after their resets.
	detailStyled bool
	data         any

	// pageBuf is the shared-buffers state of a page-inspector row (heap or any
	// index AM), attached at item build when pg_buffercache data was loaded.
	// nil = page not cached, or no temperature data for this screen at all —
	// screen.pageBufs distinguishes the two for the renderer.
	pageBuf *pg.PageBuffer

	// typeTag is the kind label shown in the parts level's "type" column
	// ("heap"/"toast"/"btree"/"gist"/"brin"/"gin"/…). typeStyle tints it (and
	// only it) per kind, matching the relations level. Empty on other levels,
	// where the type column isn't rendered.
	typeTag   string
	typeStyle lipgloss.Style

	// Optional heap/index/toast breakdown for the tables level. When any are
	// non-zero, the bar is rendered as three coloured segments.
	heap, idx, toast int64

	// rows is the estimated row count; only meaningful when hasRows is true
	// (the tables level). Rendered as its own column on those rows.
	rows    int64
	hasRows bool

	// pages is the heap page count (BLCKSZ blocks). Rendered as its own
	// column on the page-inspector tables level so the user can see, before
	// drilling in, how big a window pg_buffercache-style scans will produce.
	pages    int64
	hasPages bool

	// tableCount is the number of tables in a schema; only meaningful when
	// hasTableCount is true (the schemas level). Rendered as its own column
	// between size and the schema name.
	tableCount    int64
	hasTableCount bool

	// statQueryID carries the pg_stat_statements queryid on levelStatements
	// rows (whose .data is []pg.DiagCell for the generic table renderer) so a
	// drill can look the full QueryStat back up from screen.statRows.
	statQueryID int64

	// stmtGroupKey is the full aggregation key of a grouped top-queries row
	// (Tab on levelStatements): the qualified pg.MainTable result or the
	// QueryKind tag. item.name carries the display form (public. stripped) so
	// the / filter matches what the user sees; Enter/d/u resolve by this key.
	stmtGroupKey string

	// diagRow is 1 + the row's index into screen.diagResult.Rows on
	// levelDiagnosticResult (0 = not a diagnostic row). It survives sorting
	// and lets actions read the full, unprojected row — item.data only holds
	// the C-picker's visible column subset.
	diagRow int

	// logIdx is 1 + the row's index into the log report's Entries on the log
	// timeline (whose .data is []pg.DiagCell for the generic renderer) and the
	// group-entries level, or into screen.logCands on the log-file picker;
	// 0 = not a log row.
	logIdx int

	// pgbIdx is 1 + the row's index into screen.pgbInsts on the pgbouncer
	// instance list; 0 = not an instance row.
	pgbIdx int

	// snapPath is the file path of the snapshot a levelSnapshots row represents,
	// so the load/delete actions can act on the highlighted file. The row's
	// SnapshotMeta is held in the parallel screen.statSnapMetas slice.
	snapPath string
}

// allDBsChoice is the item.data sentinel for the synthetic "(all databases)"
// row prepended to the database picker when choosing a target for a
// per-database diagnostic. Drilling into it runs the query across every
// connectable database (see RunDiagnosticAllDBs).
type allDBsChoice struct{}

type screen struct {
	level    level
	title    string
	items    []item
	cursor   int
	offset   int
	sort     sortMode
	sortDesc bool
	loaded   bool
	loading  bool
	err      error

	// Which top-level tool this screen belongs to. Inherited from the
	// parent screen when drilling in.
	tool tool

	// Context for loading & subsequent drills. db/schema are populated from
	// levelSchemas onward; table (and via it Name/OID) only at levelParts and
	// levelColumns.
	db     string
	schema string
	table  pg.Table

	// extPrompt, when set, asks the user whether to install a Postgres
	// extension. Blocking prompts hide the list (the screen is unusable
	// without the extension); non-blocking prompts render as a soft hint
	// above the list (the screen works without it but would do more if
	// the extension were present).
	extPrompt *extPrompt
	// installing is true while a CREATE EXTENSION request is in flight.
	installing bool

	// filter is the active fuzzy-match query against item names. Empty
	// means "no filter — show everything". filterFocused routes keypresses
	// into the filter input (typing edits the query) instead of the list
	// (typing triggers shortcuts).
	filter        string
	filterFocused bool

	// Filter-result cache. visibleIndexes/visibleLen run on every render frame,
	// and on a 3000-row table (top-queries) the per-row match dominates the frame
	// while scrolling — yet the filtered set only changes when the filter text or
	// the item list does, not when the cursor moves. Cache the computed slice and
	// reuse it until the key changes: the key is the filter plus itemsRev (bumped
	// whenever items are reordered/reloaded, see applySort — the choke point every
	// load funnels through) plus len(items) as a cheap guard for any setter that
	// bypasses applySort.
	visCache    []int
	visCacheKey visKey
	visCacheOK  bool
	itemsRev    uint64

	// Diagnostic-runner state (levelDiagnostics / levelDiagnosticResult).
	// diag is the selected query; diagCols is non-nil once the result is
	// loaded and switches the sort/render path to the generic table model.
	// diagSortCol is the index of the currently active sort column.
	diag        *pg.Diagnostic
	diagCols    []pg.DiagColumn
	diagBarCol  int  // headline bar column index, or -1
	diagSortCol int  // active sort column index for the generic table
	diagAllDBs  bool // true when this result runs the query across all databases (leading "database" column)

	// diagResult retains the full unprojected result on levelDiagnosticResult so
	// the C column picker can re-project the visible subset without re-running
	// the query. diagSortName tracks the active sort column by name (diagnostic
	// columns have no stable ids) so the sort survives a visibility rebuild.
	diagResult   *pg.DiagResult
	diagSortName string

	// diagFix is the suggested-fix overlay's state: the script built for the row
	// Enter was pressed on (Diagnostic.Fix), the database it targets, and the
	// confirm/run/output lifecycle once the user chooses to execute it.
	// Model.showDiagFix toggles the overlay itself.
	diagFix *diagFixRun

	// diagCatFilter restricts the levelDiagnostics list to one category
	// (f cycles all → index → table → …); "" shows every diagnostic.
	diagCatFilter string

	// diagTotalRow, when non-nil, is rendered as a pinned footer aggregating every
	// row of the table (whole-table, filter-independent). The top-queries and
	// table-overview load sites set it (sum for additive columns, pooled ratios /
	// means for the derived ones); every other diagnostic table leaves it nil.
	diagTotalRow []pg.DiagCell

	// Memoized per-column render metrics for renderDiagResult. These scan every
	// row (O(rows×cols), calling lipgloss.Width per cell) but depend only on the
	// loaded cell *values*, not on the cursor or sort order — so recomputing them
	// on every keypress is what made the table lag on busy servers (thousands of
	// pg_stat_statements rows). They're computed once per data load: item-load
	// sites set diagMetricsDirty, and renderDiagResult recomputes lazily.
	diagMetricsDirty bool
	diagColWBase     []int     // capped per-column display width (pre last-column grow)
	diagNaturalW     []int     // uncapped per-column display width
	diagBarMax       float64   // numeric max of the bar column, for bar scaling
	diagCostMax      []float64 // per-column numeric max for DiagCostGraded grading

	// Per-tool state lives in one sub-struct each (see the *State types below);
	// the generic table infra (diag*) and the list/nav core stay top-level.
	stat        stmtState
	act         actState
	wal         walState
	log         logState
	pgb         pgbState
	buf         bufState
	maintenance maintState
	desc        describeState
	reindex     reindexState
	tbl         tblState
	progress    progressState
	lock        lockState
	parts       partsState
	pages       pageState
}

// stmtState: Top-queries state: the live/diffed statement window, snapshot anchors and the detail/sample/explain pane.
type stmtState struct {
	// cols is the projected top-queries column descriptors, parallel to
	// diagCols (same length/order). Non-nil only on levelStatements; it maps the
	// renderer's column index (diagSortCol) back to a stable column id so the
	// cycle-sort can record the active column by identity (see m.stmtTable.sortColID).
	cols []stmtColDesc

	// view is the table's Tab-cycled projection of the same window: the
	// per-statement list or a roll-up by main table / command type. group
	// narrows the per-statement list to one roll-up row's members (Enter on a
	// grouped row; Esc widens) — a structured filter, since a type group has
	// no text the / filter could match. groupCols is the grouped views'
	// counterpart of cols (exactly one of the two is non-nil after a rebuild,
	// so cycleSort knows which picker's sort memory to write).
	view      stmtView
	group     *stmtGroupFilter
	groupCols []stmtGroupColDesc

	// Top-queries state (levelStatements). baseline is the snapshot taken
	// when the tool was entered (or last re-baselined); every refresh diffs
	// the live counters against it so the table shows the window "since you
	// opened it" — pg_stat_statements has no time axis of its own. rows is
	// the current set of window deltas (used to resolve a drilled-into row back
	// to its full QueryStat). windowExecMs is the summed exec time across
	// the window, the denominator for the time% column. baselineAt /
	// sampledAt drive the window-status header.
	baseline      map[int64]pg.QueryStat
	rows          []pg.QueryStat
	windowExecMs  float64
	baselineAt    time.Time
	sampledAt     time.Time
	trackPlanning bool // pg_stat_statements.track_planning — gates the plan_ms column
	liveCount     int  // distinct queries in the last live sample — sizes the "now" anchor bar

	// Session anchor: the very first in-memory baseline taken when the tool was
	// entered, preserved unchanged even after a disk/cumulative baseline replaces
	// baseline. The "session start" row in the L browser restores this window.
	sessionBaseline map[int64]pg.QueryStat
	sessionStart    time.Time

	// Snapshot baseline state (levelStatements). baseSnap is non-nil when the
	// window's baseline was loaded from a disk snapshot rather than the live
	// auto-baseline: the header then reads "since <CapturedAt> (snapshot)".
	// endSnap is non-nil for a *frozen* A→B diff between two snapshots — the
	// window then doesn't re-sample live (endSnap is the "now").
	// cumulative is true when the baseline is an empty map (diff against nothing),
	// yielding raw cumulative counters since the last pg_stat_statements reset.
	baseSnap   *pg.Snapshot
	endSnap    *pg.Snapshot
	cumulative bool

	// Snapshots-browser state (levelSnapshots). snapMetas is aligned by index
	// with items (one meta per row).
	snapMetas []pg.SnapshotMeta
	liveReset time.Time // live pg_stat_statements stats_reset — dates the "since last reset" anchor
	// entry marks the browser pushed when the tool opens, before the table has
	// ever loaded: the user picks the window's base there (session start by
	// default, a saved snapshot, or since last reset). No window is applied yet,
	// so the end is always "now", the "now" row is omitted and Back leaves the
	// tool instead of exposing the still-empty table underneath.
	entry bool

	// Query-detail state (levelStatementDetail). detail is the window-delta
	// QueryStat for the drilled-into query; sampleCall is the synthesized
	// example call (or "" with sampleErr set when params couldn't be
	// inferred); explain holds the EXPLAIN output, run automatically on
	// entry (generic plan) and re-runnable via x, or replaced by EXPLAIN
	// ANALYZE on Enter. explainAnalyze flags which of the two the current
	// explain text is.
	detail         *pg.QueryStat
	sampleCall     string
	sampleParams   []pg.SampleParam // per-$n breakdown behind statSampleCall (verbose table)
	sampleReal     bool             // statSampleCall is a real pg_qualstats example, not synthesized
	sampleFromData bool             // statSampleCall is synthesized but uses real values sampled from the live table
	sampleFromQual bool             // statSampleCall is synthesized but ≥1 placeholder uses a per-predicate pg_qualstats constant
	qualstats      bool             // pg_qualstats is installed in db (drives source hint + captured-values key)
	sampleErr      error
	explain        string
	explainErr     error
	explaining     bool
	explainAnalyze bool
	verbose        bool // v toggles the verbose detail view (parameter table + extra metric rows)
	// hotStats holds the main table's cumulative HOT-update counters
	// (pg_stat_user_tables), fetched async on entry and rendered next to the
	// parsed table name. nil until loaded or when the table didn't resolve;
	// hotErr records a fetch failure (kept quiet — the row is just omitted).
	hotStats *pg.TableHotStats
	hotErr   error
}

// actState: Activity tool state: the last pg_stat_activity sample plus its filters and the armed cancel/terminate.
type actState struct {
	// ── Activity tool (levelActivity) ────────────────────────────────────────
	// rows is the last fetched pg_stat_activity snapshot.
	// err is non-nil when the load failed (shown instead of the list).
	// hosts maps client_addr → resolved hostname (built incrementally by the
	// background resolver and merged into items on arrival).
	// filter is the current backend-filter mode (active+waiting / non-idle / all).
	// verbose shows all backends including evergreen auxiliary processes when
	// true; false hides walwriter/checkpointer/launchers/io workers/etc by default.
	// cols is the projected column descriptor slice, kept so the C picker and
	// sort cycling can map column indices back to stable actColIDs.
	// toast maps db+relname (see toastKey) → owning-table display name for the
	// TOAST relations named in autovacuum rows, built incrementally by the
	// background resolver and merged into the table column on arrival; "" means
	// resolution was attempted but yielded nothing.
	rows    []pg.ActivityRow
	summary pg.ActivitySummary // server-wide counts + max_connections for the header
	err     error
	hosts   map[string]string
	toast   map[string]string
	filter  pg.ActivityFilter
	verbose bool
	cols    []actColDesc
	// progressPct is the pid → clamped pg_stat_progress_* percent for the
	// inline "active - 63%" state cell — the same monotonic high-water clamp as
	// progressPctMax on the progress screen (see that field for the rationale).
	progressPct map[int32]progressMark

	// pendingAction is the PID of the backend the user pressed k/x/^k on,
	// waiting for a y/Y confirmation.  action is "cancel" or "terminate".  The
	// query text is captured at arm time so an auto-refresh between arm and
	// confirm can't swap what the banner shows.
	pendingPID    int32
	pendingAction string // "cancel" | "terminate" | ""
	pendingQuery  string
}

// walState: WAL inspector state.
type walState struct {
	// WAL-inspector state. summary is the header snapshot rendered above
	// the rmgr list on levelWAL (nil until loaded; summaryErr non-nil when
	// the privilege-gated header sources failed but the list still works).
	// start/end are the resolved LSN window the overview was computed
	// over; they're carried down to levelWALRecords so every level analyses
	// the same window. rmgr names the resource manager whose records a
	// levelWALRecords screen lists; recLSN is the start LSN of the record
	// a levelWALBlocks screen drilled into.
	summary    *pg.WALSummary
	summaryErr error
	start      string
	end        string
	rmgr       string
	recLSN     string // start LSN of the drilled-into record
	recEnd     string // its end LSN — the upper bound for pg_get_wal_block_info
	// recTypeStats is the per-record-type byte/count breakdown rendered as
	// a summary table above the levelWALRecords list. Populated alongside the
	// record rows; nil/empty until loaded.
	recTypeStats []pg.WALRmgrStat
	// checkpoint is the best-effort checkpoint context rendered in the
	// levelWAL header (nil until loaded / when the privilege-gated sources
	// failed). Loaded independently of summary so each degrades on its own.
	checkpoint *pg.WALCheckpointInfo
	// relFilenode / relLabel identify the relation a levelWALRelBlocks
	// screen lists (its block references across the carried window).
	relFilenode uint32
	relLabel    string
	// blockRef is the block reference a levelWALBlockDetail screen shows (its
	// record LSNs + block_id key the payload fetch); detail is the loaded
	// payload, nil until it lands.
	blockRef *pg.WALBlockRef
	detail   *pg.WALBlockDetail
	// rmgrs and rels are the two tables the levelWAL screen stacks, both over
	// start…end: the resource-manager rows and, beneath them, the same window
	// re-aggregated per relation. They are the source of truth — applySort
	// rebuilds s.items from them (buildWALItems) because the list interleaves
	// the two tables with inert section rows that a plain sort would shuffle.
	// The relation scan (pg_get_wal_block_info) is slower than the rmgr stats
	// and needs the resolved window, so it is chained off the overview load:
	// relsLoading is true in between and relsErr holds a failure of that
	// second query alone — the rmgr rows still show.
	rmgrs       []pg.WALRmgrStat
	rels        []pg.WALRelStat
	relsErr     error
	relsLoading bool
}

// logState: Log analyzer state; the levelLogs screen owns report, children re-point to it on refresh.
type logState struct {
	// ── Log analyzer (levelLogFiles / levelLogs / levelLogGroup / levelLogEntry) ──
	// cands is the picker's candidate list; src the opened file and
	// report the parsed window (both live on the levelLogs screen — the
	// child levels read them from that parent via findLevel). err replaces
	// s.err so the header block still renders around a failed reload.
	cands  []pg.LogCandidate
	src    pglog.Source
	report *pglog.Report
	err    error
	// View state, all client-side over report: which pane (groups,
	// timeline or slow), whether groups are sectioned by category, and the requested
	// tail window (0 = whole file).
	view    logView
	groupBy logGroupBy
	window  int64
	// group/entry are what levelLogGroup and levelLogEntry show; cols
	// is the projected timeline column set (parallel to diagCols).
	group *pglog.Group
	entry *pglog.Entry
	cols  []logColDesc
	// params is the group screen's tab state: entries, or one row per bound
	// parameter tuple (or per $1). paramKey narrows a group screen to the
	// entries behind one such row (Enter on it); paramFirst records which
	// key flavour it was built with.
	params     logParamMode
	paramKey   string
	paramFirst bool
	// hosts caches reverse-DNS results (IP → hostname) for the timeline's
	// opt-in hostname column, filled asynchronously like actHosts.
	hosts map[string]string
	// collapsed holds the groups-pane sections folded with Enter on their
	// header, keyed by category so the fold survives refreshes, re-sorts and
	// pane switches (the rows rebuild from the report every time).
	collapsed map[pglog.Category]bool
	// cat narrows both panes to one category while catOn is set. f cycles it;
	// the cross-links that open the analyzer for one kind of line (the WAL
	// inspector's checkpoint log) arrive with it set. Distinct from the /
	// filter, which matches text. On the picker, autoOpen asks discovery to
	// open the current server log as soon as it lands, so a cross-link needs
	// no pick.
	cat      pglog.Category
	catOn    bool
	autoOpen bool
}

// pgbState: PgBouncer tool state.
type pgbState struct {
	// PgBouncer tool state. insts/probes are the instance list (parallel
	// slices, levelPgBouncers); inst is the instance an overview or SHOW
	// screen belongs to; show selects the SHOW at levelPgBouncerShow, whose
	// result rides in diagResult like a diagnostic's. err is the last load
	// error, rendered in the header instead of failing the screen so a paused
	// or restarting pooler keeps its place on the stack.
	insts       []pgbouncer.Instance
	probes      []pgbouncer.Probe
	inst        *pgbouncer.Instance
	overview    *pgbouncer.Overview
	show        pgbShow
	err         error
	autoDrilled bool // the single-instance auto-drill already happened once
}

// bufState: Shared-buffers tool state (levelBufferTables / levelBufferDetail).
type bufState struct {
	// Populated on the levelBufferTables screen alongside the row data.
	summary    *pg.BufferCacheSummary
	summaryErr error

	// levelBufferDetail state: detail is the table being inspected (carried
	// from the parent row, so the overview figures render immediately); usage
	// is its clock-sweep temperature histogram, loaded asynchronously.
	detail    *pg.TableBufferStat
	usage     []pg.BufferUsageCount
	blockSize int64 // cluster block_size, for expressing the histogram in bytes
	usageErr  error
}

// maintState: Maintenance / system overview and settings browser state.
type maintState struct {
	// ── Maintenance dashboard (levelMaintenance) ─────────────────────────────
	// info is the loaded snapshot; err is non-nil when the load failed.
	info *pg.MaintenanceInfo
	err  error
	// prev is the snapshot info replaced on the last successful load, the
	// other half of the two-sample rates (nil until the second load). It lives
	// on the screen, not the Model, so a Back to the tool menu forgets the
	// window and a stale sample from another db is impossible.
	prev *pg.MaintenanceInfo
	// first is the sample taken when the screen was opened, the base of the
	// since-open rates; same lifetime as prev.
	first *pg.MaintenanceInfo
	// cursor is the action row ↑↓ move over: the four extension-capacity
	// reset rows (maintResetRows) followed by the recommendations (actionRows).
	// cursorKey is that row's identity (reset name or Advice.Key) so a reload
	// that reshuffles the recommendations puts the cursor back on the same
	// finding; cursorLine is the line it was rendered on (-1 unknown) and
	// follow asks the next render to scroll it into view.
	cursor     int
	cursorKey  string
	cursorLine int
	follow     bool
	// pendingReset is set by Enter on a capacity row; y confirms the reset.
	pendingReset string
	// advice is MaintAdvice over info and schema, cached on every load so the
	// action rows, the inline notes and the panel are one list.
	advice pg.AdviceSet
	// schema is the per-database catalog sweep behind the schema-health
	// section; it loads separately (loadMaintSchemaCmd) on open and on manual
	// refresh only, never on the auto-refresh tick. schemaLoading marks a sweep
	// in flight (the previous one stays on screen meanwhile).
	schema        *pg.SchemaHealth
	schemaLoading bool
	// settingRows is the full pg_settings list for levelSettings.
	settingRows []pg.SettingRow
}

// describeState: Describe pane (d) state.
type describeState struct {
	// info holds the loaded \d-style description for levelDescribe screens.
	// Nil until the async load completes.
	info *pg.Description
	// detail toggles the info panel's detail mode (`d` on the panel):
	// cache footprint, per-index usage, tuple churn, scans and maintenance.
	// Off by default so the plain view stays psql-lean and never pays the
	// pg_buffercache scan.
	detail bool
	// buf is the cache-footprint stat for the info-table screen's
	// shared-buffers section, loaded asynchronously and independently of
	// info (nil until loaded; bufErr non-nil on a non-extension error).
	// A missing pg_buffercache is carried by extPrompt instead.
	// bufLoading is set while a load is in flight so `d` toggles and
	// refreshes don't stack concurrent full-pool scans (each one is a
	// multi-second pg_buffercache walk on a big shared_buffers).
	buf        *pg.TableBufferStat
	bufErr     error
	bufLoading bool
}

// reindexState: REINDEX flow on levelParts: the armed confirm, the running rebuild and its progress.
type reindexState struct {
	// pending holds the index name the user pressed ENTER on (parts
	// level, index row with bloat > 5%). Pressing `y` confirms and runs
	// REINDEX INDEX CONCURRENTLY; any other key clears it.
	pending string
	// running is the index currently being rebuilt (empty when idle).
	running string
	// prog is the last-polled live progress of the running REINDEX from
	// pg_stat_progress_create_index; nil until the first sample lands (or
	// between phases where the view reports no counters).
	prog *pg.ReindexProgress
	// pctMax is the high-water mark of prog.OverallPct() across
	// polls. Phase totals are estimates and briefly read 0 on transitions, so
	// the banner renders this clamp instead of the raw sample — the overall
	// bar only ever moves forward.
	pctMax float64
	// err is the last REINDEX failure, shown until the next attempt.
	err error
}

// tblState: Table overview (levelTableStats) state.
type tblState struct {
	// ── Table overview tool (levelTableStats) ────────────────────────────────
	// rows is the last fetched per-table stats snapshot for this schema (the
	// source of truth for drill-in / describe, looked up by OID). cols is the
	// projected column descriptor slice, kept so the C picker and sort cycling
	// can map column indices back to stable tblColIDs.
	rows []pg.TableStat
	cols []tblColDesc
	// statsReset dates the cumulative pg_stat counters shown here (the
	// database's stats_reset); zero when unknown / never reset. Shown in the
	// status line so "since when" is never ambiguous.
	statsReset time.Time
}

// progressState: Progress view (pg_stat_progress_*) state.
type progressState struct {
	// ── Progress monitor (levelProgress) ──────────────────────────────────────
	// rows is the last fetched set of running operations from the
	// pg_stat_progress_* views; err is non-nil when the load failed.
	rows []pg.ProgressRow
	err  error
	// pctMax is the per-operation high-water mark of OverallPct(),
	// keyed by pid — the same monotonic clamp reindexPctMax applies to the
	// REINDEX banner, so VACUUM's repeated index passes and transiently-zero
	// totals hold the bar instead of snapping it back. Entries are rebuilt on
	// every refresh, so a finished operation's mark is dropped with its row.
	pctMax map[int32]progressMark
}

// lockState: Lock tree state.
type lockState struct {
	// ── Lock tree (levelLockTree) ─────────────────────────────────────────────
	// nodes is the last fetched set of blocking-chain backends; err is
	// non-nil when the load failed. The items list is the forest flattened in
	// DFS order (item.data = lockTreeRow carrying the node and its indent depth).
	nodes []pg.LockNode
	err   error
}

// partsState: levelParts extras: bloat scan progress, the per-table maintenance stats and the armed VACUUM.
type partsState struct {
	// bloatScanning is true while a FillBloat command for this parts screen
	// is in flight. The bloat fetch is one-shot (all parts in one call), so
	// the progress display is "scanning…" / "ready" rather than incremental.
	bloatScanning bool

	// ── Table maintenance panel (levelParts) ──────────────────────────────────
	// tableStats is the maintenance snapshot for the current table, loaded
	// asynchronously alongside the parts list.
	tableStats    *pg.TableMaintStats
	tableStatsErr error

	// pendingVacuum is true when the user has armed the vacuum confirm flow
	// (pressed `v` on levelParts); y executes, any other key cancels.
	pendingVacuum bool
}

// pageState: Page inspector state: heap window, tuple/index detail, AM metadata and the seek input. Field names are unchanged.
type pageState struct {
	// Seek-to-key state (levelIndexTuples only). seekFocused routes keypresses
	// into the seek input; seekQuery is the typed leading-key value; seekStatus
	// is the one-line result hint ("→ #0008" / "no match"). Distinct from the
	// fuzzy filter: seek jumps the cursor to the B-tree entry whose key range
	// covers the value rather than narrowing the list.
	seekFocused bool
	seekQuery   string
	seekStatus  string

	// Page-inspector state. levelHeapPages renders a window of the heap's
	// page array; PgUp/PgDn moves the window in heapWindowCount-sized
	// steps. heapPageCount comes from pg_class.relpages and clamps the
	// upper bound — required since get_raw_page errors past EOF.
	heapWindowStart int32
	heapWindowCount int32
	heapPageCount   int32

	// pageBufs is the current window's block → shared-buffers state map from
	// pg_buffercache (best-effort side load). Non-nil means temperature data
	// is available and the page lists render their "temp" column; nil hides
	// it (extension missing, insufficient privileges, or load not done).
	pageBufs map[int64]pg.PageBuffer

	// levelHeapTuples: which page we drilled into.
	heapPageBlkno int32
	// focusLP, when non-zero on a levelHeapTuples screen, names the line
	// pointer to land the cursor on — and open the byte-layout overlay for —
	// once the page's tuples arrive. Set by the index-entry drills, which
	// arrive here through a ctid rather than a cursor pick; consumed by the
	// first load so a later reload of the page doesn't re-open the overlay.
	focusLP int32

	// tuplePKCols names the table's primary-key columns, in key order, as of
	// the last tuple load. Non-empty names the key in the expanded row; empty
	// means the table has no primary key, so there is nothing to project.
	tuplePKCols []string

	// The tuple list's table-column picker (C on levelHeapTuples). tupleCols is
	// every live column of the relation (the picker's rows), tupleShown the
	// indexes into it the list currently renders as value columns — both from
	// the last load. tuplePick is what the user asked for: nil means "the
	// default", i.e. the primary key, and is materialised to the shown names
	// after each load so the picker reflects what the server resolved. Each
	// toggle reloads the page, since the projection is part of the query. The
	// pick lives here on purpose: a page inspection is a one-off look, so the
	// choice is not persisted.
	tupleCols  []pg.HeapColumn
	tupleShown []int
	tuplePick  []string

	// Tuple byte-layout overlay (Enter on levelHeapTuples): the per-attribute
	// split of the selected tuple, loaded async when the overlay opens.
	// tupleAttrsLP names the line pointer the data belongs to (0 = none) so
	// a stale load or a cursor move can't show another tuple's layout.
	tupleAttrs        []pg.TupleAttr
	tupleAttrsLP      int32
	tupleAttrsLoading bool
	tupleAttrsErr     error

	// levelTupleRow: the ctid we're showing. Carries (block,offset) text so
	// the SQL bind doesn't have to re-derive it from heapPageBlkno + LP —
	// the line pointer might be a REDIRECT pointing at a different page.
	tupleCtid string
	// toastChunkID, when non-zero on a levelTupleRow screen, means we are
	// displaying the fully-assembled TOAST value for this chunk_id rather
	// than a single-row ctid projection. Mutually exclusive with tupleCtid.
	toastChunkID uint32

	// Index page-inspector state. index identifies which B-tree we're
	// looking at on levelIndexPages / levelIndexTuples. The window-state
	// fields (heapWindowStart / heapWindowCount / heapPageCount) are
	// shared with the heap page-inspector — generic page-array bookkeeping,
	// not heap-specific. indexPageBlkno records the block the user drilled
	// into on levelIndexTuples; indexPageType carries that block's
	// bt_page_stats type ('l'/'r'/'i'/'d') so the per-item loader knows
	// whether to decode keys against the heap, and the drill handler
	// knows whether ENTER should open a heap row.
	index          pg.Relation
	indexPageBlkno int32
	indexPageType  string
	// indexPageLevel is the B-tree depth (btpo_level, 0 = leaf) of the page
	// drilled into on levelIndexTuples, shown as "L2" in the status line. nil
	// when unknown (mid-descent, before the loader probes it) or not a B-tree.
	indexPageLevel *int32

	// Deep-dive context for the index page/tuple views, loaded alongside the
	// page list (best-effort, so a privilege/redefinition failure just hides the
	// banner). indexKeyCols drives the "keys: (…) include: (…)" banner;
	// btreeMeta/brinMeta/ginMeta drive the per-AM metapage banner (GiST has no
	// metapage). indexPageType is reused generically across access methods to
	// carry the current page's role string (btree l/r/i/d; gist leaf/intr/del;
	// brin meta/regular/revmap; gin opaque flags).
	indexKeyCols []pg.IndexKeyColumn
	// indexTuples is the last loaded B-tree page as returned by the client, kept
	// so the row list can be rebuilt without a reload when a posting-list tuple
	// is expanded or collapsed. postingOpen records which posting tuples (by
	// item offset) currently show their member tids; members are heavy (a page
	// can pack over a thousand) so they stay folded until asked for.
	indexTuples []pg.IndexTuple
	postingOpen map[int32]bool
	btreeMeta   *pg.BtreeMeta
	brinMeta    *pg.BrinMeta
	ginMeta     *pg.GinMeta

	// btreeLevels is the whole-tree page census (pages per level) behind the
	// "levels:" banner line on the B-tree page list. The scan reads every page
	// of the index, so it runs once per screen — window moves and refreshes
	// reuse the cached counts. btreeLevelsLoading suppresses duplicate scans
	// while one is in flight; btreeLevelsDone marks it finished, with the
	// failure (if any) kept in btreeLevelsErr so the banner can say why the
	// counts are missing instead of silently dropping the line.
	btreeLevels        []pg.BtreeLevelCount
	btreeLevelsLoading bool
	btreeLevelsDone    bool
	btreeLevelsErr     error
}

// reindexBloatThreshold is the bloat % above which the parts view offers an
// inline REINDEX CONCURRENTLY action on an index row.
const reindexBloatThreshold = 0.05

// Extension names referenced by the TUI. Kept here so prompt text and the
// command that runs CREATE EXTENSION stay in sync if either is renamed.
const (
	extBufferCache    = "pg_buffercache"
	extPgStatTuple    = "pgstattuple"
	extPageInspect    = "pageinspect"
	extWALInspect     = "pg_walinspect"
	extStatStatements = "pg_stat_statements"
	extQualstats      = "pg_qualstats"

	extPromptReasonBufferCache    = "shared_buffers view requires the pg_buffercache extension"
	extPromptReasonPgStatTuple    = "exact bloat measurements are available with pgstattuple"
	extPromptReasonPageInspect    = "Page inspector requires the pageinspect extension"
	extPromptReasonPageTemp       = "per-page buffer temperature is available with pg_buffercache"
	extPromptReasonWALPageImage   = "decoding the full-page image requires the pageinspect extension"
	extPromptReasonWALInspect     = "WAL inspector requires the pg_walinspect extension (and a superuser / pg_read_server_files role to read WAL)"
	extPromptReasonStatStatements = "Top queries requires the pg_stat_statements extension (also needs it in shared_preload_libraries + a restart to collect)"
	extPromptReasonQualstats      = "real EXPLAIN values are available with pg_qualstats (already in shared_preload_libraries here)"
)

// extPrompt is the per-screen "install this extension?" affordance. It doubles
// as an "upgrade this extension?" prompt when upgrade is set (the extension is
// installed but too old — see OutdatedExtensionError): the `i` key then runs
// ALTER EXTENSION ... UPDATE instead of CREATE EXTENSION.
type extPrompt struct {
	name        string // "pg_buffercache", "pgstattuple"
	db          string
	installable bool
	reason      string // human-readable explanation of why pgdu wants it
	blocking    bool   // when true, the screen content is replaced by the prompt
	err         error  // populated when a previous install attempt failed

	// Set for the outdated-extension (upgrade) variant of the prompt.
	upgrade   bool
	installed string // currently installed version, e.g. "1.6"
	available string // version an UPDATE would install, e.g. "1.11"
	required  string // minimum version pgdu needs, e.g. "1.8"
}

type Model struct {
	client  *pg.Client
	stack   []*screen
	width   int
	height  int
	spinner spinner.Model
	help    help.Model
	keys    keyMap

	// showInfo toggles the buffer-tables info overlay (? key) — a static
	// explainer for the server-memory and shared_buffers bars. infoOffset is the
	// scroll position within that overlay (some references, e.g. maintenance, are
	// taller than the screen); it's reset to 0 each time the overlay is opened and
	// clamped on render by scrollWindow.
	showInfo   bool
	infoOffset int

	// showDiagQuery toggles the overlay that prints the executed SQL of the
	// current diagnostic (s key on levelDiagnosticResult) so it can be copied.
	showDiagQuery bool

	// showDiagFix toggles the suggested-fix overlay (Enter on a diagnostic
	// result row with a Fix builder); its state lives on screen.diagFix. Modal
	// like showDiagQuery, but with its own key handling (handleDiagFixKey):
	// Enter arms a y/n confirm that runs the script, and while it runs or after
	// it finished the overlay stays up to show the output.
	showDiagFix bool

	// Column-picker state of the registry-backed tables (C key): the per-column
	// visibility set (nil = registry defaults, so a fresh run shows the
	// historical columns), the active sort column by stable id — it survives a
	// visibility change; the projected index screen.diagSortCol is recomputed
	// each rebuild — and the modal picker overlay flag + cursor. The static
	// half (registry, prefs key, sort fallback) is each table's colSpec.
	stmtTable      colTable[stmtColID]
	stmtGroupTable colTable[stmtColID] // the by-table / by-type roll-ups (Tab on levelStatements)
	actTable       colTable[actColID]
	tblTable       colTable[tblColID]

	// Tuple byte-layout overlay (Enter on levelHeapTuples). The cursor walks the
	// legend rows; the offset is the legend's scroll window start. The loaded
	// attrs live on the screen (tupleAttrs*) — this is just the modal state.
	// Sorting is the overlay's own (pageinspect.SegSort): the legend can't ride the shared
	// sortMode machinery since it isn't a screen item list.
	showTupleLayout     bool
	tupleLayoutCursor   int
	tupleLayoutOffset   int
	tupleLayoutSort     pageinspect.SegSort
	tupleLayoutSortDesc bool

	// Table-column picker on the tuple list (C on levelHeapTuples). The column
	// set is the relation's pg_attribute rows (screen.pages.tupleCols), so like
	// the diagnostic picker it can't ride a static colTable registry.
	showTupleColumnConfig bool
	tupleColCfgCursor     int

	// Diagnostic-result column configuration (C on levelDiagnosticResult).
	// Diagnostic columns are dynamic (server field descriptions), so unlike the
	// registry-backed colTables, visibility is kept per diagnostic key and
	// by column name. A missing inner map (or missing name) means visible;
	// entries are lazily seeded from prefs by diagVis. diagColCfgCursor is the
	// C-picker row cursor; showDiagColumnConfig opens it.
	diagColsVisible      map[string]map[string]bool
	showDiagColumnConfig bool
	diagColCfgCursor     int

	// actProcPrev holds the previous /proc sample per PID, used to compute CPU%
	// and I/O byte-rate deltas between consecutive samples.
	actProcPrev map[int32]procfs.PIDStats
	// actProcStats holds the derived per-PID display values (RSS, CPU%, read/s,
	// write/s) from the most recent sample pair. nil = not yet sampled.
	actProcStats map[int32]procDerived

	// waitRing accumulates per-tick wait-event samples from every activity
	// refresh (see pushWaitBucket). Model-level so the histogram survives
	// screen pushes/pops and already has history when the profile opens;
	// lazily allocated on the first activity snapshot.
	waitRing *waitRing

	// activityTicking is true while a self-rescheduling refresh tick is running
	// for the Activity tool, so re-entering levelActivity doesn't spawn a second
	// loop.
	activityTicking bool

	// activityRefresh is the Activity tool auto-refresh cadence. Cycled by the t
	// key: 2s → 10s → off → 2s.
	activityRefresh time.Duration

	// maintTicking/maintRefresh are the system overview's auto-refresh loop,
	// off by default (0): each tick reloads the snapshot, which is what turns the
	// cumulative counters into per-minute rates. Cycled by t.
	maintTicking bool
	maintRefresh time.Duration

	// statTicking is true while a self-rescheduling refresh tick is running for
	// the top-queries tool, so re-entering levelStatements doesn't spawn a
	// second tick loop.
	statTicking bool

	// logTicking/logRefresh are the log analyzer's live-tail loop: off by
	// default (a log is usually read after the fact), t cycles 5s → 15s → 60s → off.
	logTicking bool
	logRefresh time.Duration

	// pgbTicking/pgbRefresh are the pgbouncer tool's live-refresh loop (t cycles
	// 1s → 2s → 5s → 10s → off), shared by the instance list, overview and
	// SHOW tables.
	pgbTicking bool
	pgbRefresh time.Duration
	// pgbAvailable gates the PgBouncer entry on the root tool picker: it is
	// hidden until discovery (fired from Init) has actually found an instance,
	// so hosts without a pooler don't advertise a tool that can only say "no
	// pgbouncer instance found". --pgbouncer bypasses the
	// menu and keeps working regardless.
	pgbAvailable bool

	// logFile is the --log-file override: when set the analyzer skips the picker
	// and opens it directly.
	logFile string

	// Log table pickers: the timeline and slow panes share logTable's visibility
	// set (the slow pane remembers its own sort column), the pooler-stats pane
	// renders a different registry and so has its own table.
	logTable         colTable[logColID]
	logStatsTable    colTable[logColID]
	logSlowSortColID logColID

	// statRefresh is the top-queries re-sample cadence (from --queries-refresh /
	// PGDU_QUERIES_REFRESH). Zero disables auto-refresh entirely. The t key cycles
	// it through the 2s default, a calmer 60s, then off (see cycleStatRefresh).
	statRefresh time.Duration

	// notice is a transient one-line status shown in the header (e.g. the path
	// a CSV export was written to). Cleared on the next keypress.
	notice string

	// snapshotDir is where top-queries snapshots are saved (S) and listed (L).
	snapshotDir string

	// colPrefs persists per-user column-picker selections across sessions. It is
	// always non-nil (prefs.Load never fails); a save error is best-effort.
	colPrefs *prefs.Prefs

	// pendingDeleteSnap holds the path of the snapshot the user pressed D on in
	// the browser; the next key confirms (y/Y) or cancels — mirrors pendingReindex.
	pendingDeleteSnap string

	target    string // host:port; keys snapshot compatibility, so it never changes shape
	hostLabel string // root crumb of the breadcrumb (cli.Config.HostLabel)

	// vacuum holds the state for the streaming VACUUM output pane on levelParts.
	// It is a value type so the pane's scrollWindow can update its offset in
	// place; vacuumPaneVisible(s) gates whether the pane is rendered at all.
	vacuum vacuumState
}

// diagFixRun is one suggested-fix script and its execution on a diagnostic
// result screen. pending is the armed y/n confirm; running/finished/err track
// the RunFix call, whose streamed lines land in buf. Like vacuumState, the
// output pane scrolls through scrollWindow with tail-follow.
type diagFixRun struct {
	sql, db  string
	pending  bool
	running  bool
	started  time.Time
	finished time.Time
	err      error
	buf      []string
	offset   int
	follow   bool
}

// vacuumState holds the live and completed output of a streaming VACUUM run.
type vacuumState struct {
	table    pg.Table
	started  time.Time
	finished time.Time
	running  bool
	err      error
	buf      []string // lines received via OnNotice
	offset   int      // scrollWindow offset into buf
	follow   bool     // whether to tail-follow new lines
}

// vacuumPaneVisible returns true when the vacuum output pane should be shown
// for the given parts screen: either a vacuum is running, or one has finished
// for this exact table.
func (m *Model) vacuumPaneVisible(s *screen) bool {
	if s.level != levelParts {
		return false
	}
	return (m.vacuum.running || !m.vacuum.finished.IsZero()) &&
		m.vacuum.table.OID == s.table.OID
}

// toolByName maps a canonical tool name (as produced by tool.Name and accepted
// by the --<tool> CLI flags) back to the tool enum. The bool is false for an
// unknown/empty name so the caller can fall back to the tool picker.
func toolByName(name string) (tool, bool) {
	for _, t := range []tool{toolDisk, toolBuffers, toolPageInspect, toolTools, toolWAL, toolQueries, toolMaintenance, toolActivity, toolTableStats, toolLogs, toolPgBouncer} {
		if t.Name() == name {
			return t, true
		}
	}
	return 0, false
}

func NewModel(client *pg.Client, queriesRefresh time.Duration, snapshotDir string, colPrefs *prefs.Prefs, initialTool, logFile string) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &Model{
		client:          client,
		spinner:         sp,
		help:            help.New(),
		keys:            defaultKeys(),
		statRefresh:     queriesRefresh,
		activityRefresh: 2 * time.Second,
		pgbRefresh:      2 * time.Second,
		snapshotDir:     snapshotDir,
		colPrefs:        colPrefs,
		target:          client.Target(),
		hostLabel:       client.HostLabel(),
		logFile:         logFile,
	}
	// Seed in-memory column visibility from persisted selections. A partial map
	// is fine: actColEnabled/stmtColEnabled fall back to registry defaults for any
	// id the user never touched, so columns added in a later build still appear.
	if colPrefs != nil {
		if v := colPrefs.Columns(colPrefsActivity); len(v) > 0 {
			m.actTable.visible = colVisFromStrings[actColID](v)
		}
		if v := colPrefs.Columns(colPrefsQueries); len(v) > 0 {
			m.stmtTable.visible = colVisFromStrings[stmtColID](v)
		}
		if v := colPrefs.Columns(colPrefsQueryGroups); len(v) > 0 {
			m.stmtGroupTable.visible = colVisFromStrings[stmtColID](v)
		}
		if v := colPrefs.Columns(colPrefsTableStats); len(v) > 0 {
			m.tblTable.visible = colVisFromStrings[tblColID](v)
		}
		if v := colPrefs.Columns(colPrefsLogs); len(v) > 0 {
			m.logTable.visible = colVisFromStrings[logColID](v)
		}
		if v := colPrefs.Columns(colPrefsLogStats); len(v) > 0 {
			m.logStatsTable.visible = colVisFromStrings[logColID](v)
		}
	}
	root := &screen{
		level:    levelTools,
		title:    "tools",
		sort:     sortByName,
		sortDesc: sortByName.defaultDesc()}
	m.stack = []*screen{root}
	// --<tool> shortcut: open the requested tool directly, but keep the picker as
	// the stack root so Back/Esc still returns to it. The root is pre-populated
	// synchronously (toolItems is pure) since Init only loads the top screen.
	if t, ok := toolByName(initialTool); ok {
		root.items = toolItems(m.pgbAvailable)
		root.loaded = true
		m.stack = append(m.stack, m.toolEntryScreen(t))
	}
	return m
}

// toolItems is the list shown on the root tool-picker screen; pgBouncer
// controls whether the PgBouncer entry is included (see Model.pgbAvailable).
func toolItems(pgBouncer bool) []item {
	items := []item{
		{name: "System overview", detail: "server health on one screen: recommendations that open the finding behind them, connections, transactions, I/O, replication, autovacuum, WAL, schema health", hasChildren: true, data: toolMaintenance},
		{name: "Disk usage", detail: "browse tables by total relation size on disk", hasChildren: true, data: toolDisk},
		{name: "Top queries", detail: "powa-style top queries from pg_stat_statements — calls, time, I/O; EXPLAIN and sample params on Enter", hasChildren: true, data: toolQueries},
		{name: "Current Activity", detail: "live server activity (pg_stat_activity): active queries, waits, client IPs; cancel / terminate backends", hasChildren: true, data: toolActivity},
		{name: "Table overview", detail: "per-table stats for a schema: size, write/scan activity, cache hit ratios, bloat, vacuum age, storage options — sortable, customizable columns", hasChildren: true, data: toolTableStats},
		{name: "Log analyzer", detail: "parse the server log (current, rotated, .gz): errors, slow statements, checkpoints, temp files, locks — grouped and searchable, live tail", hasChildren: true, data: toolLogs},
		{name: "PgBouncer", detail: "pgbouncer console browser: auto-discovered instances (/proc, /etc/pgbouncer), pools, per-second stats, clients, servers, databases, config", hasChildren: true, data: toolPgBouncer},
		{name: "Shared buffers", detail: "browse tables by shared_buffers footprint and cache hit ratio", hasChildren: true, data: toolBuffers},
		{name: "Page inspector", detail: "drill into heap pages and tuple line pointers using pageinspect", hasChildren: true, data: toolPageInspect},
		{name: "WAL inspector", detail: "drill into recent write-ahead-log: bytes per resource manager, records, block refs (pg_walinspect)", hasChildren: true, data: toolWAL},
		{name: "Other Tools", detail: "run diagnostic queries — index / table / vacuum / activity / wal / server health", hasChildren: true, data: toolTools},
	}
	if !pgBouncer {
		items = slices.DeleteFunc(items, func(it item) bool { return it.data == toolPgBouncer })
	}
	return items
}

// setPgbAvailable records that at least one pgbouncer instance exists and, if
// the root picker is already populated, re-inserts the PgBouncer entry while
// keeping the cursor on the tool it was on.
func (m *Model) setPgbAvailable() {
	if m.pgbAvailable {
		return
	}
	m.pgbAvailable = true
	root := m.stack[0]
	if root.level != levelTools || !root.loaded {
		return
	}
	var cur tool
	if root.cursor < len(root.items) {
		cur, _ = root.items[root.cursor].data.(tool)
	}
	root.items = toolItems(true)
	root.itemsRev++ // doesn't go through applySort; invalidate the filter cache
	for i, it := range root.items {
		if it.data == cur {
			root.cursor = i
			break
		}
	}
}

// diagnosticItems builds the list of available diagnostic queries shown at
// levelDiagnostics, restricted to one category when cat is non-empty (the f
// cycle). Each item carries the Diagnostic value as its .data so drillIn can
// type-assert it and push a result screen; the category renders as a coloured
// badge in renderDiagnosticList.
func diagnosticItems(cat string) []item {
	items := make([]item, 0, len(pg.Diagnostics))
	for _, d := range pg.Diagnostics {
		if cat != "" && d.Category != cat {
			continue
		}
		items = append(items, item{
			name:        d.Title,
			detail:      d.Description,
			hasChildren: true,
			data:        d,
		})
	}
	return items
}

// diagCategories returns the distinct diagnostic categories in registry order —
// the f key cycles through them (prefixed by "" = all).
func diagCategories() []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range pg.Diagnostics {
		if !seen[d.Category] {
			seen[d.Category] = true
			out = append(out, d.Category)
		}
	}
	return out
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCurrent(), m.pgbAvailableCmd())
}

// --- screen-stack helpers ---

func (m *Model) top() *screen { return m.stack[len(m.stack)-1] }

func (m *Model) findLevel(l level) *screen {
	for _, v := range slices.Backward(m.stack) {
		if v.level == l {
			return v
		}
	}
	return nil
}
