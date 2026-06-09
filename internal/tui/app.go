package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

type level int

const (
	levelTools level = iota
	levelDatabases
	levelSchemas
	levelTables
	levelParts
	levelBufferTables
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
	levelStatements       // pg_stat_statements top-queries table (toolQueries)
	levelStatementDetail  // single query: metrics, sample call, EXPLAIN
	levelStatementSamples // captured real predicate constants (pg_qualstats) for one query
	levelSnapshots        // on-disk top-queries snapshots browser (load as baseline / A→B)
)

// tool identifies which top-level statistic the user is exploring.
// Propagated down the stack so each level knows which leaf to render.
type tool int

const (
	toolDisk tool = iota
	toolBuffers
	toolPageInspect
	toolTools   // diagnostic SQL query runner
	toolWAL     // write-ahead-log inspector
	toolQueries // pg_stat_statements top-queries (powa-style)
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
	}
	return "?"
}

type sortMode int

const (
	sortBySize sortMode = iota
	sortByName
	sortByHitRatio
	sortByCached
	sortByTotal
	sortByRows
	sortByBlkno
	sortByDeadRatio
	sortByFreeSpace
	sortByLP
	sortByLevel
	sortByCount // WAL: record count per resource manager
	sortByFPI   // WAL: full-page-image bytes
)

// defaultDesc is the natural direction for each sort column: bigger-first for
// numeric "more is more" columns, alphabetical for name, ascending for hit
// ratio so the worst-cached tables bubble to the top.
func (sm sortMode) defaultDesc() bool {
	switch sm {
	case sortBySize, sortByRows, sortByCached, sortByTotal, sortByDeadRatio, sortByFreeSpace, sortByCount, sortByFPI:
		return true
	case sortByName, sortByHitRatio, sortByBlkno, sortByLP, sortByLevel:
		return false
	}
	return false
}

// name is the short column label used in the status row and column headers.
func (sm sortMode) name() string {
	switch sm {
	case sortBySize:
		return "size"
	case sortByRows:
		return "~rows"
	case sortByHitRatio:
		return "hit"
	case sortByCached:
		return "cached"
	case sortByTotal:
		return "total"
	case sortByBlkno:
		return "blkno"
	case sortByDeadRatio:
		return "dead%"
	case sortByFreeSpace:
		return "free"
	case sortByLP:
		return "lp"
	case sortByLevel:
		return "level"
	case sortByCount:
		return "count"
	case sortByFPI:
		return "fpi"
	default:
		return "name"
	}
}

// label is name plus an arrow indicating the current sort direction.
func (sm sortMode) label(desc bool) string {
	arrow := "↑"
	if desc {
		arrow = "↓"
	}
	return sm.name() + arrow
}

// less returns true when item a should come before item b *ignoring* the
// direction flag — applySort applies direction by flipping the result.
// Items missing the comparator's payload (no rows estimate, no hit ratio)
// sort below items that have one, so "unknown" stays a distinct bucket from
// "small".
func (sm sortMode) less(a, b item) bool {
	switch sm {
	case sortBySize:
		return a.size < b.size
	case sortByRows:
		ai, oka := itemRows(a)
		bi, okb := itemRows(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByHitRatio:
		ai, oka := itemHitRatio(a)
		bi, okb := itemHitRatio(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByCached:
		ai, oka := itemCachedRatio(a)
		bi, okb := itemCachedRatio(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByTotal:
		ai, oka := itemTotalBytes(a)
		bi, okb := itemTotalBytes(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByBlkno:
		ai, oka := itemBlkno(a)
		bi, okb := itemBlkno(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByDeadRatio:
		ai, oka := itemDeadRatio(a)
		bi, okb := itemDeadRatio(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByFreeSpace:
		ai, oka := itemFreeSpace(a)
		bi, okb := itemFreeSpace(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByLP:
		ai, oka := itemLP(a)
		bi, okb := itemLP(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByLevel:
		ai, oka := itemTreeLevel(a)
		bi, okb := itemTreeLevel(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByCount:
		ai, oka := itemWALCount(a)
		bi, okb := itemWALCount(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByFPI:
		ai, oka := itemWALFPI(a)
		bi, okb := itemWALFPI(b)
		if oka != okb {
			return okb
		}
		if !oka {
			return false
		}
		return ai < bi
	case sortByName:
		return false
	}
	return false
}

// item is the row data the renderer consumes; concrete payload is in `data`.
type item struct {
	name        string
	size        int64
	bloat       int64
	hasBloat    bool // true once bloat has been measured (even if zero)
	hasChildren bool // true when pressing Enter on this row drills into a submenu
	detail      string
	data        any

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

	// describe holds the loaded \d-style description for levelDescribe screens.
	// Nil until the async load completes.
	describe *pg.Description

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
	// the window, the denominator for the %time column. statBaselineAt /
	// statSampledAt drive the window-status header.
	statBaseline      map[int64]pg.QueryStat
	statRows          []pg.QueryStat
	statWindowExecMs  float64
	statBaselineAt    time.Time
	statSampledAt     time.Time
	statTrackPlanning bool // pg_stat_statements.track_planning — gates the plan_ms column

	// Snapshot baseline state (levelStatements). statBaseSnap is non-nil when the
	// window's baseline was loaded from a disk snapshot rather than the live
	// auto-baseline: the header then reads "since <CapturedAt> (snapshot)".
	// statEndSnap is non-nil for a *frozen* A→B diff between two snapshots — the
	// window then doesn't re-sample live (statEndSnap is the "now").
	statBaseSnap *pg.Snapshot
	statEndSnap  *pg.Snapshot

	// Snapshots-browser state (levelSnapshots). statSnapMetas is aligned by index
	// with items (one meta per row). statMarkBase is the path of the snapshot
	// marked as the A-base (m); when set, Enter on another row builds a frozen
	// A→B diff instead of loading the row as a live baseline.
	statSnapMetas []pg.SnapshotMeta
	statMarkBase  string

	// Query-detail state (levelStatementDetail). statDetail is the window-delta
	// QueryStat for the drilled-into query; statSampleCall is the synthesized
	// example call (or "" with statSampleErr set when params couldn't be
	// inferred); statExplain holds the EXPLAIN output, run automatically on
	// entry (generic plan) and re-runnable via x, or replaced by EXPLAIN
	// ANALYZE on Enter. statExplainAnalyze flags which of the two the current
	// statExplain text is.
	statDetail         *pg.QueryStat
	statSampleCall     string
	statSampleReal     bool // statSampleCall is a real pg_qualstats example, not synthesized
	statSampleFromData bool // statSampleCall is synthesized but uses real values sampled from the live table
	statQualstats      bool // pg_qualstats is installed in db (drives source hint + captured-values key)
	statSampleErr      error
	statExplain        string
	statExplainErr     error
	statExplaining     bool
	statExplainAnalyze bool
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
	// explainer for the server-memory and shared_buffers bars.
	showInfo bool

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

	// statTicking is true while a self-rescheduling refresh tick is running for
	// the top-queries tool, so re-entering levelStatements doesn't spawn a
	// second tick loop.
	statTicking bool

	// statRefresh is the top-queries re-sample cadence (from --queries-refresh /
	// PGDU_QUERIES_REFRESH). Zero disables auto-refresh entirely. statPaused is
	// the runtime toggle (t key) layered on top of it.
	statRefresh time.Duration
	statPaused  bool

	// notice is a transient one-line status shown in the header (e.g. the path
	// a CSV export was written to). Cleared on the next keypress.
	notice string

	// snapshotDir is where top-queries snapshots are saved (S) and listed (L).
	snapshotDir string

	// pendingDeleteSnap holds the path of the snapshot the user pressed D on in
	// the browser; the next key confirms (y/Y) or cancels — mirrors pendingReindex.
	pendingDeleteSnap string

	target string // host:port for header
}

func NewModel(client *pg.Client, queriesRefresh time.Duration, snapshotDir string) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &Model{
		client:      client,
		spinner:     sp,
		help:        help.New(),
		keys:        defaultKeys(),
		fetchBloat:  true,
		statRefresh: queriesRefresh,
		snapshotDir: snapshotDir,
		target:      client.Target(),
	}
	m.stack = []*screen{{
		level:    levelTools,
		title:    "tools",
		sort:     sortByName,
		sortDesc: sortByName.defaultDesc(),
	}}
	return m
}

// toolItems is the static list shown on the root tool-picker screen.
func toolItems() []item {
	return []item{
		{name: "Disk usage", detail: "browse tables by total relation size on disk", hasChildren: true, data: toolDisk},
		{name: "Top queries", detail: "powa-style top queries from pg_stat_statements — calls, time, I/O; EXPLAIN and sample params on Enter", hasChildren: true, data: toolQueries},
		{name: "Shared buffers", detail: "browse tables by shared_buffers footprint and cache hit ratio", hasChildren: true, data: toolBuffers},
		{name: "Page inspector", detail: "drill into heap pages and tuple line pointers using pageinspect", hasChildren: true, data: toolPageInspect},
		{name: "WAL inspector", detail: "drill into recent write-ahead-log: bytes per resource manager, records, block refs (pg_walinspect)", hasChildren: true, data: toolWAL},
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

// --- item builders (db rows → tui rows) ---

func schemaDetail(sc pg.Schema) string {
	return fmt.Sprintf("%d tables", sc.TableCount)
}

func tableToItem(t pg.Table, tl tool) item {
	// In the page-inspector flow only the heap is browsable — indexes and
	// toast aren't reachable through this drill path. Sizing the row by
	// total-relation-size (and showing the heap/idx/toast breakdown) would
	// suggest otherwise, so we show heap-only stats and surface the page
	// count instead — that's the figure the user actually navigates next.
	if tl == toolPageInspect {
		pages := t.HeapBytes / heapPageBlockSize
		if t.HeapBytes%heapPageBlockSize != 0 {
			pages++
		}
		return item{
			name: t.Name, size: t.HeapBytes, hasChildren: true,
			data: t,
			rows: t.EstRows, hasRows: true,
			pages: pages, hasPages: true,
		}
	}
	// Tables with a tiny TOAST relation (empty or a handful of out-of-line
	// values) clutter the detail line with a near-zero figure. Hide TOAST
	// below 1 MiB — the colored bar segment is already 0-width at that scale.
	const toastShowThreshold = 1 << 20
	parts := []string{
		"heap " + humanize.Bytes(t.HeapBytes),
		"idx " + humanize.Bytes(t.IndexesBytes),
	}
	if t.ToastBytes >= toastShowThreshold {
		parts = append(parts, "toast "+humanize.Bytes(t.ToastBytes))
	}
	return item{
		name: t.Name, size: t.TotalBytes, hasChildren: true,
		detail: strings.Join(parts, " · "), data: t,
		heap: t.HeapBytes, idx: t.IndexesBytes, toast: t.ToastBytes,
		rows: t.EstRows, hasRows: true,
	}
}

// heapPageBlockSize is the standard PostgreSQL page size. pgdu doesn't talk
// to clusters with non-default BLCKSZ; if it ever needs to, this becomes a
// per-connection setting read from current_setting('block_size').
const heapPageBlockSize int64 = 8192

func heapPageToItem(p pg.HeapPageStat) item {
	// Used bytes scale the bar against a fixed BLCKSZ so every row in the
	// heap-pages view shares the same horizontal scale — the eye can
	// compare occupancy across pages without re-reading the numbers.
	used := max(heapPageBlockSize-int64(p.FreeBytes), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		data: p,
	}
}

func heapTupleToItem(t pg.HeapTuple) item {
	// hasChildren is set only for NORMAL line pointers — DEAD/UNUSED have
	// no row to fetch, and REDIRECT points at a target on (potentially)
	// another page that we'd need to chase, which the row-detail view
	// doesn't currently do.
	return item{
		name:        fmt.Sprintf("#%04d", t.LP),
		size:        int64(t.LPLen),
		hasChildren: t.LPFlags == pg.LPNormal && t.Ctid != nil,
		data:        t,
	}
}

func tupleCellToItem(c pg.TupleCell) item {
	v := "NULL"
	if c.Value != nil {
		v = *c.Value
	}
	return item{
		name:   c.Name,
		detail: v,
		data:   c,
	}
}

// itemBlkno extracts the block number from a heap- or index-page item.
// Returns (0, false) for items lacking page-summary data so they sort
// below pages we can rank.
func itemBlkno(it item) (int64, bool) {
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		return p.Blkno, true
	case pg.IndexPageStat:
		return int64(p.Blkno), true
	}
	return 0, false
}

// itemDeadRatio is dead/(live+dead) for heap- or index-page items; second
// return is false for empty pages so they don't dominate the dead% sort.
func itemDeadRatio(it item) (float64, bool) {
	var r float64
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		r = p.DeadFrac()
	case pg.IndexPageStat:
		r = p.DeadFrac()
	default:
		return 0, false
	}
	if r < 0 {
		return 0, false
	}
	return r, true
}

// itemFreeSpace returns the per-page free bytes; second return is false for
// items lacking page-summary data.
func itemFreeSpace(it item) (int64, bool) {
	switch p := it.data.(type) {
	case pg.HeapPageStat:
		return int64(p.FreeBytes), true
	case pg.IndexPageStat:
		return int64(p.FreeSize), true
	}
	return 0, false
}

// itemLP extracts the line-pointer index for heap-tuple items, or the
// itemoffset for index-tuple items (same concept — a per-page slot index).
func itemLP(it item) (int64, bool) {
	switch t := it.data.(type) {
	case pg.HeapTuple:
		return int64(t.LP), true
	case pg.IndexTuple:
		return int64(t.ItemOffset), true
	}
	return 0, false
}

// itemTreeLevel returns btpo_level for B-tree page items (0 = leaf).
// Second return is false for non-index-page items.
func itemTreeLevel(it item) (int64, bool) {
	p, ok := it.data.(pg.IndexPageStat)
	if !ok {
		return 0, false
	}
	return int64(p.BtpoLevel), true
}

// relationToItem builds the levelRelations row for one mixed relation entry.
// hasChildren is always true: both tables and B-tree indexes drill into a
// page-inspector view. The detail string is left empty — the dedicated
// renderRelationsList paints the parent name in muted text on index rows
// without a separate detail column.
func relationToItem(r pg.Relation) item {
	pages := max(int64(r.Pages), 0)
	return item{
		name:        r.Name,
		size:        r.SizeBytes,
		hasChildren: true,
		data:        r,
		rows:        r.EstRows,
		hasRows:     true,
		pages:       pages,
		hasPages:    true,
	}
}

func indexPageToItem(p pg.IndexPageStat) item {
	// Used bytes mirror the heap-page item: BLCKSZ minus free. The bar
	// reads as "how packed is this page" at a uniform scale.
	used := max(heapPageBlockSize-int64(p.FreeSize), 0)
	return item{
		name: fmt.Sprintf("page #%07d", p.Blkno),
		size: used,
		data: p,
	}
}

func indexTupleToItem(t pg.IndexTuple) item {
	// hasChildren is set only when a live heap row was projected (Decoded
	// non-nil) — that's the same gate the drill handler uses, so the "+"
	// marker tracks what ENTER will actually do. Internal-page downlinks
	// and entries whose heap row is gone don't drill.
	return item{
		name:        fmt.Sprintf("#%04d", t.ItemOffset),
		size:        int64(t.ItemLen),
		hasChildren: t.Decoded != nil && t.Ctid != nil,
		data:        t,
	}
}

// walRmgrToItem builds one levelWAL row from a resource-manager stat. size is
// the combined byte total (record data + FPI) so the shared bar scales the
// rmgr against its siblings; the FPI split and counts render as their own
// columns / bar segment in renderWALList.
func walRmgrToItem(s pg.WALRmgrStat) item {
	return item{
		name:        s.Name,
		size:        s.CombinedSize,
		hasChildren: s.Count > 0,
		data:        s,
	}
}

// walRecordToItem builds one levelWALRecords row. size is the combined byte
// total (record_length + fpi_length). hasChildren is always true: every
// record can be drilled into for its block references (the list may turn out
// empty on PG 15 where pg_get_wal_block_info is absent — surfaced then).
func walRecordToItem(r pg.WALRecord) item {
	return item{
		name:        r.RecordType,
		size:        r.CombinedSize(),
		detail:      r.Description,
		hasChildren: true,
		data:        r,
	}
}

// walBlockToItem builds one levelWALBlocks row. size is the FPI length — the
// bar reads as "how much full-page-image write amplification did this block
// reference cost". Block refs are leaves (no further drill).
func walBlockToItem(b pg.WALBlockRef) item {
	// Prefer the resolved relation name; fall back to the raw relfilenode when
	// the relation lives in another database or has been dropped.
	target := fmt.Sprintf("%d", b.RelFileNode)
	if b.RelName != "" {
		target = b.RelName
	}
	if b.IsToast {
		target += " (toast)"
	}
	return item{
		name: fmt.Sprintf("rel %s/%s blk %d", target, b.ForkName(), b.BlockNumber),
		size: int64(b.FPILength),
		data: b,
	}
}

// itemWALCount / itemWALFPI extract the record count and FPI bytes from a
// levelWAL rmgr-stat item. Second return is false for items without that
// payload so they sort below rows we can rank.
func itemWALCount(it item) (int64, bool) {
	s, ok := it.data.(pg.WALRmgrStat)
	if !ok {
		return 0, false
	}
	return s.Count, true
}

func itemWALFPI(it item) (int64, bool) {
	switch v := it.data.(type) {
	case pg.WALRmgrStat:
		return v.FPISize, true
	case pg.WALRecord:
		return int64(v.FPILength), true
	}
	return 0, false
}

func bufferStatToItem(s pg.TableBufferStat) item {
	// detail is left empty: the per-row figures (table size, cached %, hit %)
	// are rendered as their own columns in renderBufferList.
	return item{
		name: s.Schema + "." + s.Name,
		size: s.BufferedBytes,
		data: s,
	}
}

func columnToItem(col pg.Column) item {
	nullPart := ""
	if col.NullFrac > 0.005 {
		nullPart = fmt.Sprintf(" · %.0f%% null", col.NullFrac*100)
	}
	toastMark := ""
	// 🍞 flags columns whose values are likely actually in TOAST. Capability
	// (extended/external storage on a table with a TOAST relation) isn't enough:
	// PostgreSQL only externalizes values that push the row past
	// TOAST_TUPLE_THRESHOLD (~2 KB). avg_width here is pg_column_size-derived,
	// so a column averaging at/above that threshold is almost certainly being
	// compressed and/or externalized.
	const toastAvgWidthThreshold = 2048
	if col.Toastable && col.AvgWidth >= toastAvgWidthThreshold {
		toastMark = "🍞 "
	}
	detail := fmt.Sprintf("%s%s · avg %s%s", toastMark, col.Type, humanize.Bytes(int64(col.AvgWidth)), nullPart)
	return item{
		name:   col.Name,
		size:   col.EstBytes,
		detail: detail,
		data:   col,
	}
}

func partToItem(p pg.Part) item {
	detail := ""
	switch p.Kind {
	case pg.PartHeap:
		detail = heapDetail(p.HeapStats)
	case pg.PartToast:
		detail = "TOAST storage"
		if p.ToastName != "" {
			detail += " · " + p.ToastName
		}
	case pg.PartIndex:
		var tags []string
		if p.IsPrimary {
			tags = append(tags, "primary")
		}
		if p.IsUnique && !p.IsPrimary {
			tags = append(tags, "unique")
		}
		tags = append(tags, p.AccessMethod)
		detail = "index · " + strings.Join(tags, " · ")
	}
	return item{
		name:        p.Name,
		size:        p.SizeBytes,
		bloat:       p.WastedBytes,
		hasBloat:    p.HasBloat,
		hasChildren: p.Kind == pg.PartHeap, // only heap drills into per-column view
		detail:      detail,
		data:        p,
	}
}

// heapDetail builds the inline status string shown on the heap row at the
// parts level: dead-tuple % and "last vacuum" age. Falls back to "table heap"
// when stats aren't available (e.g. matviews or stats never collected).
func heapDetail(h *pg.HeapStats) string {
	if h == nil {
		return "table heap"
	}
	parts := []string{"heap"}
	if frac := h.DeadFrac(); frac >= 0 && h.NDead > 0 {
		parts = append(parts, fmt.Sprintf("%s dead (%.0f%%)", formatRows(h.NDead), frac*100))
	}
	if last := h.LastVacuumed(); last != nil {
		parts = append(parts, "vac "+relativeAge(time.Since(*last)))
	} else if h.NLive+h.NDead > 0 {
		parts = append(parts, "never vacuumed")
	}
	if last := h.LastAnalyzed(); last != nil {
		parts = append(parts, "ana "+relativeAge(time.Since(*last)))
	}
	return strings.Join(parts, " · ")
}

// relativeAge formats a duration as a short human-readable age suffix such as
// "3h ago" or "12d ago". Negative durations (clock skew) read as "0s ago".
func relativeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
}

// positionLabel reports the cursor's position within the list, e.g.
// "12/438". Returns "0 items" for empty lists so the status line never
// shows the misleading "0/0". When a filter is active, the visible count
// is shown alongside the total ("12/45 of 438") so the user can tell at a
// glance how many rows were hidden.
func positionLabel(s *screen) string {
	total := len(s.items)
	if total == 0 {
		return "0 items"
	}
	vis := s.visibleLen()
	if vis == 0 {
		return fmt.Sprintf("0/0 of %d", total)
	}
	if s.filter != "" {
		return fmt.Sprintf("%d/%d of %d", s.cursor+1, vis, total)
	}
	return fmt.Sprintf("%d/%d", s.cursor+1, vis)
}

// bloatScanLabel returns a short status indicator for the bloat fetch on
// the parts level. FillBloat is a single round trip that covers every
// part, so the states are "scanning…" (in flight) or "ready" (done) —
// any partial scanned count comes from individual rows whose bloat could
// not be measured (e.g. unsupported index access methods).
func bloatScanLabel(s *screen) string {
	if s.level != levelParts || len(s.items) == 0 {
		return ""
	}
	if s.bloatScanning {
		return "bloat: scanning…"
	}
	scanned := 0
	for _, it := range s.items {
		if it.hasBloat {
			scanned++
		}
	}
	if scanned == 0 {
		return ""
	}
	if scanned == len(s.items) {
		return "bloat: ready"
	}
	return fmt.Sprintf("bloat: %d/%d scanned", scanned, len(s.items))
}

// --- formatting helpers ---

func levelLabel(l level) string {
	switch l {
	case levelTools:
		return "tools"
	case levelDatabases:
		return "databases"
	case levelSchemas:
		return "schemas"
	case levelTables:
		return "tables"
	case levelBufferTables:
		return "buffer-tables"
	case levelParts:
		return "parts"
	case levelColumns:
		return "columns"
	case levelHeapPages:
		return "heap-pages"
	case levelHeapTuples:
		return "heap-tuples"
	case levelTupleRow:
		return "tuple-row"
	case levelRelations:
		return "relations"
	case levelIndexPages:
		return "index-pages"
	case levelIndexTuples:
		return "index-tuples"
	case levelDescribe:
		return "describe"
	case levelDiagnostics:
		return "diagnostics"
	case levelDiagnosticResult:
		return "diag-result"
	case levelWAL:
		return "wal"
	case levelWALRecords:
		return "wal-records"
	case levelWALBlocks:
		return "wal-blocks"
	case levelStatements:
		return "queries"
	case levelStatementDetail:
		return "query-detail"
	case levelSnapshots:
		return "snapshots"
	}
	return "?"
}

func formatRows(n int64) string {
	if n < 0 {
		return "?"
	}
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
