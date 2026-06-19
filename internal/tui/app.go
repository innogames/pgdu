package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"pgdu/internal/pg"
	"pgdu/internal/prefs"
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
	levelWALRelations     // WAL window aggregated per relation (what caused the change)
	levelWALRelBlocks     // block references of one relation across the window
	levelStatements       // pg_stat_statements top-queries table (toolQueries)
	levelStatementDetail  // single query: metrics, sample call, EXPLAIN
	levelStatementSamples // captured real predicate constants (pg_qualstats) for one query
	levelStatementResult  // rows returned by executing a query (psql-style result table)
	levelSnapshots        // on-disk top-queries snapshots browser (load as baseline / A→B)
	levelMaintenance      // server-health dashboard (toolMaintenance)
	levelSettings         // pg_settings browser (child of levelMaintenance)
	levelActivity         // live server activity from pg_stat_activity (toolActivity)
)

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

	// snapPath is the file path of the snapshot a levelSnapshots row represents,
	// so the load/delete actions can act on the highlighted file. The row's
	// SnapshotMeta is held in the parallel screen.statSnapMetas slice.
	snapPath string
}

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

	// Populated on the levelBufferTables screen alongside the row data.
	bufferSummary    *pg.BufferCacheSummary
	bufferSummaryErr error

	// levelBufferDetail state: bufDetail is the table being inspected (carried
	// from the parent row, so the overview figures render immediately); bufUsage
	// is its clock-sweep temperature histogram, loaded asynchronously.
	bufDetail    *pg.TableBufferStat
	bufUsage     []pg.BufferUsageCount
	bufBlockSize int64 // cluster block_size, for expressing the histogram in bytes
	bufUsageErr  error

	// bloatScanning is true while a FillBloat command for this parts screen
	// is in flight. The bloat fetch is one-shot (all parts in one call), so
	// the progress display is "scanning…" / "ready" rather than incremental.
	bloatScanning bool

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

	// Seek-to-key state (levelIndexTuples only). seekFocused routes keypresses
	// into the seek input; seekQuery is the typed leading-key value; seekStatus
	// is the one-line result hint ("→ #0008" / "no match"). Distinct from the
	// fuzzy filter: seek jumps the cursor to the B-tree entry whose key range
	// covers the value rather than narrowing the list.
	seekFocused bool
	seekQuery   string
	seekStatus  string

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

	// pendingReindex holds the index name the user pressed ENTER on (parts
	// level, index row with bloat > 5%). Pressing `y` confirms and runs
	// REINDEX INDEX CONCURRENTLY; any other key clears it.
	pendingReindex string
	// reindexing is the index currently being rebuilt (empty when idle).
	reindexing string
	// reindexErr is the last REINDEX failure, shown until the next attempt.
	reindexErr error

	// Page-inspector state. levelHeapPages renders a window of the heap's
	// page array; PgUp/PgDn moves the window in heapWindowCount-sized
	// steps. heapPageCount comes from pg_class.relpages and clamps the
	// upper bound — required since get_raw_page errors past EOF.
	heapWindowStart int32
	heapWindowCount int32
	heapPageCount   int32

	// levelHeapTuples: which page we drilled into.
	heapPageBlkno int32

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

	// Deep-dive context for the index page/tuple views, loaded alongside the
	// page list (best-effort, so a privilege/redefinition failure just hides the
	// banner). indexKeyCols drives the "keys: (…) include: (…)" banner;
	// btreeMeta/brinMeta/ginMeta drive the per-AM metapage banner (GiST has no
	// metapage). indexPageType is reused generically across access methods to
	// carry the current page's role string (btree l/r/i/d; gist leaf/intr/del;
	// brin meta/regular/revmap; gin opaque flags).
	indexKeyCols []pg.IndexKeyColumn
	btreeMeta    *pg.BtreeMeta
	brinMeta     *pg.BrinMeta
	ginMeta      *pg.GinMeta

	// describe holds the loaded \d-style description for levelDescribe screens.
	// Nil until the async load completes.
	describe *pg.Description
	// descBuf is the cache-footprint stat for the describe-table screen's
	// shared-buffers section, loaded asynchronously and independently of
	// describe (nil until loaded; descBufErr non-nil on a non-extension error).
	// A missing pg_buffercache is carried by extPrompt instead.
	descBuf    *pg.TableBufferStat
	descBufErr error

	// WAL-inspector state. walSummary is the header snapshot rendered above
	// the rmgr list on levelWAL (nil until loaded; walSummaryErr non-nil when
	// the privilege-gated header sources failed but the list still works).
	// walStart/walEnd are the resolved LSN window the overview was computed
	// over; they're carried down to levelWALRecords so every level analyses
	// the same window. walRmgr names the resource manager whose records a
	// levelWALRecords screen lists; walRecLSN is the start LSN of the record
	// a levelWALBlocks screen drilled into.
	walSummary    *pg.WALSummary
	walSummaryErr error
	walStart      string
	walEnd        string
	walRmgr       string
	walRecLSN     string // start LSN of the drilled-into record
	walRecEnd     string // its end LSN — the upper bound for pg_get_wal_block_info
	// walRecTypeStats is the per-record-type byte/count breakdown rendered as
	// a summary table above the levelWALRecords list. Populated alongside the
	// record rows; nil/empty until loaded.
	walRecTypeStats []pg.WALRmgrStat
	// walCheckpoint is the best-effort checkpoint context rendered in the
	// levelWAL header (nil until loaded / when the privilege-gated sources
	// failed). Loaded independently of walSummary so each degrades on its own.
	walCheckpoint *pg.WALCheckpointInfo
	// walRelFilenode / walRelLabel identify the relation a levelWALRelBlocks
	// screen lists (its block references across the carried window).
	walRelFilenode uint32
	walRelLabel    string

	// Diagnostic-runner state (levelDiagnostics / levelDiagnosticResult).
	// diag is the selected query; diagCols is non-nil once the result is
	// loaded and switches the sort/render path to the generic table model.
	// diagSortCol is the index of the currently active sort column.
	diag        *pg.Diagnostic
	diagCols    []pg.DiagColumn
	diagBarCol  int // headline bar column index, or -1
	diagSortCol int // active sort column index for the generic table

	// stmtCols is the projected top-queries column descriptors, parallel to
	// diagCols (same length/order). Non-nil only on levelStatements; it maps the
	// renderer's column index (diagSortCol) back to a stable column id so the
	// cycle-sort can record the active column by identity (see m.stmtSortColID).
	stmtCols []stmtColDesc

	// diagTotalRow, when non-nil, is rendered as a pinned footer summing every
	// row of the table (whole-table, filter-independent). Only the top-queries
	// load sites set it; every other diagnostic table leaves it nil.
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

	// Top-queries state (levelStatements). statBaseline is the snapshot taken
	// when the tool was entered (or last re-baselined); every refresh diffs
	// the live counters against it so the table shows the window "since you
	// opened it" — pg_stat_statements has no time axis of its own. statRows is
	// the current set of window deltas (used to resolve a drilled-into row back
	// to its full QueryStat). statWindowExecMs is the summed exec time across
	// the window, the denominator for the time% column. statBaselineAt /
	// statSampledAt drive the window-status header.
	statBaseline      map[int64]pg.QueryStat
	statRows          []pg.QueryStat
	statWindowExecMs  float64
	statBaselineAt    time.Time
	statSampledAt     time.Time
	statTrackPlanning bool // pg_stat_statements.track_planning — gates the plan_ms column
	statLiveCount     int  // distinct queries in the last live sample — sizes the "now" anchor bar

	// Session anchor: the very first in-memory baseline taken when the tool was
	// entered, preserved unchanged even after a disk/cumulative baseline replaces
	// statBaseline. The "session start" row in the L browser restores this window.
	statSessionBaseline map[int64]pg.QueryStat
	statSessionStart    time.Time

	// Snapshot baseline state (levelStatements). statBaseSnap is non-nil when the
	// window's baseline was loaded from a disk snapshot rather than the live
	// auto-baseline: the header then reads "since <CapturedAt> (snapshot)".
	// statEndSnap is non-nil for a *frozen* A→B diff between two snapshots — the
	// window then doesn't re-sample live (statEndSnap is the "now").
	// statCumulative is true when the baseline is an empty map (diff against nothing),
	// yielding raw cumulative counters since the last pg_stat_statements reset.
	statBaseSnap   *pg.Snapshot
	statEndSnap    *pg.Snapshot
	statCumulative bool

	// Snapshots-browser state (levelSnapshots). statSnapMetas is aligned by index
	// with items (one meta per row).
	statSnapMetas []pg.SnapshotMeta
	statLiveReset time.Time // live pg_stat_statements stats_reset — dates the "since last reset" anchor

	// Query-detail state (levelStatementDetail). statDetail is the window-delta
	// QueryStat for the drilled-into query; statSampleCall is the synthesized
	// example call (or "" with statSampleErr set when params couldn't be
	// inferred); statExplain holds the EXPLAIN output, run automatically on
	// entry (generic plan) and re-runnable via x, or replaced by EXPLAIN
	// ANALYZE on Enter. statExplainAnalyze flags which of the two the current
	// statExplain text is.
	statDetail         *pg.QueryStat
	statSampleCall     string
	statSampleParams   []pg.SampleParam // per-$n breakdown behind statSampleCall (verbose table)
	statSampleReal     bool             // statSampleCall is a real pg_qualstats example, not synthesized
	statSampleFromData bool             // statSampleCall is synthesized but uses real values sampled from the live table
	statSampleFromQual bool             // statSampleCall is synthesized but ≥1 placeholder uses a per-predicate pg_qualstats constant
	statQualstats      bool             // pg_qualstats is installed in db (drives source hint + captured-values key)
	statSampleErr      error
	statExplain        string
	statExplainErr     error
	statExplaining     bool
	statExplainAnalyze bool
	statVerbose        bool // v toggles the verbose detail view (parameter table + extra metric rows)
	// statHotStats holds the main table's cumulative HOT-update counters
	// (pg_stat_user_tables), fetched async on entry and rendered next to the
	// parsed table name. nil until loaded or when the table didn't resolve;
	// statHotErr records a fetch failure (kept quiet — the row is just omitted).
	statHotStats *pg.TableHotStats
	statHotErr   error

	// ── Activity tool (levelActivity) ────────────────────────────────────────
	// actRows is the last fetched pg_stat_activity snapshot.
	// actErr is non-nil when the load failed (shown instead of the list).
	// actHosts maps client_addr → resolved hostname (built incrementally by the
	// background resolver and merged into items on arrival).
	// actFilter is the current backend-filter mode (active+waiting / non-idle / all).
	// actVerbose shows all backends including evergreen auxiliary processes when
	// true; false hides walwriter/checkpointer/launchers/io workers/etc by default.
	// actCols is the projected column descriptor slice, kept so the C picker and
	// sort cycling can map column indices back to stable actColIDs.
	actRows    []pg.ActivityRow
	actSummary pg.ActivitySummary // server-wide counts + max_connections for the header
	actErr     error
	actHosts   map[string]string
	actFilter  pg.ActivityFilter
	actVerbose bool
	actCols    []actColDesc

	// pendingBackendAction is the PID of the backend the user pressed k/x on,
	// waiting for a y/Y confirmation.  action is "cancel" or "terminate".
	pendingBackendPID    int32
	pendingBackendAction string // "cancel" | "terminate" | ""

	// ── Maintenance dashboard (levelMaintenance) ─────────────────────────────
	// maint is the loaded snapshot; maintErr is non-nil when the load failed.
	maint    *pg.MaintenanceInfo
	maintErr error
	// maintCursor is the row within the extension-capacity section that ↑↓ move
	// over (0 = pg_stat_statements, 1 = pg_qualstats).
	maintCursor int
	// pendingReset is set by Enter on a capacity row; y confirms the reset.
	pendingReset string
	// settingRows is the full pg_settings list for levelSettings.
	settingRows []pg.SettingRow

	// ── Table maintenance panel (levelParts) ──────────────────────────────────
	// tableStats is the maintenance snapshot for the current table, loaded
	// asynchronously alongside the parts list.
	tableStats    *pg.TableMaintStats
	tableStatsErr error

	// pendingVacuum is true when the user has armed the vacuum confirm flow
	// (pressed `v` on levelParts); y executes, any other key cancels.
	pendingVacuum bool
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

	// when true, bloat is fetched on entering the parts view.
	fetchBloat bool

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

	// Top-queries column configuration (C key on levelStatements). stmtColsVisible
	// is the per-column-id visibility set (nil = registry defaults, so a fresh run
	// shows the historical columns; lazily filled on first use). stmtSortColID
	// tracks the active sort column by identity so it survives a visibility change
	// — the projected index (screen.diagSortCol) is recomputed each rebuild.
	// showColumnConfig toggles the htop-style picker overlay; colCfgCursor is its
	// row cursor over the column registry.
	stmtColsVisible  map[stmtColID]bool
	stmtSortColID    stmtColID
	showColumnConfig bool
	colCfgCursor     int

	// Activity tool column configuration (C key on levelActivity). actColsVisible
	// is the per-column-id visibility set (nil = registry defaults). actSortColID
	// tracks the active sort column by stable id across visibility rebuilds.
	// actColCfgCursor is the C-picker row cursor; showActColumnConfig opens it.
	actColsVisible      map[actColID]bool
	actSortColID        actColID
	showActColumnConfig bool
	actColCfgCursor     int

	// actProcPrev holds the previous /proc sample per PID, used to compute CPU%
	// and I/O byte-rate deltas between consecutive samples.
	actProcPrev map[int32]procRaw
	// actProcStats holds the derived per-PID display values (RSS, CPU%, read/s,
	// write/s) from the most recent sample pair. nil = not yet sampled.
	actProcStats map[int32]procDerived

	// activityTicking is true while a self-rescheduling refresh tick is running
	// for the Activity tool, so re-entering levelActivity doesn't spawn a second
	// loop.
	activityTicking bool

	// activityRefresh is the Activity tool auto-refresh cadence. Cycled by the t
	// key: 2s → 10s → off → 2s.
	activityRefresh time.Duration

	// statTicking is true while a self-rescheduling refresh tick is running for
	// the top-queries tool, so re-entering levelStatements doesn't spawn a
	// second tick loop.
	statTicking bool

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

	target string // host:port for header

	// vacuum holds the state for the streaming VACUUM output pane on levelParts.
	// It is a value type so the pane's scrollWindow can update its offset in
	// place; vacuumPaneVisible(s) gates whether the pane is rendered at all.
	vacuum vacuumState
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
	for _, t := range []tool{toolDisk, toolBuffers, toolPageInspect, toolTools, toolWAL, toolQueries, toolMaintenance, toolActivity} {
		if t.Name() == name {
			return t, true
		}
	}
	return 0, false
}

func NewModel(client *pg.Client, queriesRefresh time.Duration, snapshotDir string, colPrefs *prefs.Prefs, initialTool string) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &Model{
		client:          client,
		spinner:         sp,
		help:            help.New(),
		keys:            defaultKeys(),
		fetchBloat:      true,
		statRefresh:     queriesRefresh,
		activityRefresh: 2 * time.Second,
		snapshotDir:     snapshotDir,
		colPrefs:        colPrefs,
		target:          client.Target(),
	}
	// Seed in-memory column visibility from persisted selections. A partial map
	// is fine: actColEnabled/stmtColEnabled fall back to registry defaults for any
	// id the user never touched, so columns added in a later build still appear.
	if colPrefs != nil {
		if v := colPrefs.Columns(colPrefsActivity); len(v) > 0 {
			m.actColsVisible = colVisFromStrings[actColID](v)
		}
		if v := colPrefs.Columns(colPrefsQueries); len(v) > 0 {
			m.stmtColsVisible = colVisFromStrings[stmtColID](v)
		}
	}
	root := &screen{
		level:    levelTools,
		title:    "tools",
		sort:     sortByName,
		sortDesc: sortByName.defaultDesc(),
	}
	m.stack = []*screen{root}
	// --<tool> shortcut: open the requested tool directly, but keep the picker as
	// the stack root so Back/Esc still returns to it. The root is pre-populated
	// synchronously (toolItems is pure) since Init only loads the top screen.
	if t, ok := toolByName(initialTool); ok {
		root.items = toolItems()
		root.loaded = true
		m.stack = append(m.stack, m.toolEntryScreen(t))
	}
	return m
}

// toolItems is the static list shown on the root tool-picker screen.
func toolItems() []item {
	return []item{
		{name: "Disk usage", detail: "browse tables by total relation size on disk", hasChildren: true, data: toolDisk},
		{name: "Top queries", detail: "powa-style top queries from pg_stat_statements — calls, time, I/O; EXPLAIN and sample params on Enter", hasChildren: true, data: toolQueries},
		{name: "Current Activity", detail: "live server activity (pg_stat_activity): active queries, waits, client IPs; cancel / terminate backends", hasChildren: true, data: toolActivity},
		{name: "Shared buffers", detail: "browse tables by shared_buffers footprint and cache hit ratio", hasChildren: true, data: toolBuffers},
		{name: "Page inspector", detail: "drill into heap pages and tuple line pointers using pageinspect", hasChildren: true, data: toolPageInspect},
		{name: "WAL inspector", detail: "drill into recent write-ahead-log: bytes per resource manager, records, block refs (pg_walinspect)", hasChildren: true, data: toolWAL},
		{name: "System overview", detail: "server health dashboard: connections, transactions, I/O, replication, autovacuum, WAL, PgBouncer", hasChildren: true, data: toolMaintenance},
		{name: "Other Tools", detail: "run diagnostic queries — index / table / vacuum / activity / wal / server health", hasChildren: true, data: toolTools},
	}
}

// diagnosticItems builds the static list of available diagnostic queries shown
// at levelDiagnostics. Each item carries the Diagnostic value as its .data so
// drillIn can type-assert it and push a result screen.
func diagnosticItems() []item {
	items := make([]item, len(pg.Diagnostics))
	for i, d := range pg.Diagnostics {
		items[i] = item{
			name:        d.Title,
			detail:      "[" + d.Category + "]  " + d.Description,
			hasChildren: true,
			data:        d,
		}
	}
	return items
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCurrent())
}

// --- screen-stack helpers ---

func (m *Model) top() *screen { return m.stack[len(m.stack)-1] }

func (m *Model) findLevel(l level) *screen {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i].level == l {
			return m.stack[i]
		}
	}
	return nil
}
