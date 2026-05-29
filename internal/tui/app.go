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
)

// tool identifies which top-level statistic the user is exploring.
// Propagated down the stack so each level knows which leaf to render.
type tool int

const (
	toolDisk tool = iota
	toolBuffers
)

func (t tool) Name() string {
	switch t {
	case toolDisk:
		return "disk"
	case toolBuffers:
		return "buffers"
	}
	return "?"
}

type sortMode int

const (
	sortBySize sortMode = iota
	sortByName
	sortByHitRatio
	sortByRows
)

// defaultDesc is the natural direction for each sort column: bigger-first for
// numeric "more is more" columns, alphabetical for name, ascending for hit
// ratio so the worst-cached tables bubble to the top.
func (sm sortMode) defaultDesc() bool {
	switch sm {
	case sortBySize, sortByRows:
		return true
	case sortByName, sortByHitRatio:
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
}

// reindexBloatThreshold is the bloat % above which the parts view offers an
// inline REINDEX CONCURRENTLY action on an index row.
const reindexBloatThreshold = 0.05

// Extension names referenced by the TUI. Kept here so prompt text and the
// command that runs CREATE EXTENSION stay in sync if either is renamed.
const (
	extBufferCache = "pg_buffercache"
	extPgStatTuple = "pgstattuple"

	extPromptReasonBufferCache = "shared_buffers view requires the pg_buffercache extension"
	extPromptReasonPgStatTuple = "exact bloat measurements are available with pgstattuple"
)

// extPrompt is the per-screen "install this extension?" affordance.
type extPrompt struct {
	name        string // "pg_buffercache", "pgstattuple"
	db          string
	installable bool
	reason      string // human-readable explanation of why pgdu wants it
	blocking    bool   // when true, the screen content is replaced by the prompt
	err         error  // populated when a previous install attempt failed
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

	target string // host:port for header
}

func NewModel(client *pg.Client) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	m := &Model{
		client:     client,
		spinner:    sp,
		help:       help.New(),
		keys:       defaultKeys(),
		fetchBloat: true,
		target:     client.Target(),
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
		{name: "Shared buffers", detail: "browse tables by shared_buffers footprint and cache hit ratio", hasChildren: true, data: toolBuffers},
	}
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

func tableToItem(t pg.Table) item {
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
