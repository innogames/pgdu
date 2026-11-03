package tui

import (
	"time"

	"pgdu/internal/pg"
)

// growthView is the Tab-cycled scope of the growth table: the relation rows as
// captured, or one row per schema, database or partition tree folded from the
// same diffed set. Like the top-queries roll-ups these are pure re-projections
// of screen.growth.rows — no extra file read — so a scope switch is instant.
//
// Unlike those roll-ups, every scope here yields the same pg.GrowthRow, so all
// four share one column registry, one picker and one prefs key.
type growthView int

const (
	growthByTable growthView = iota
	growthBySchema
	growthByDatabase
	growthByPartTree
)

func (v growthView) next() growthView {
	switch v {
	case growthByTable:
		return growthBySchema
	case growthBySchema:
		return growthByDatabase
	case growthByDatabase:
		return growthByPartTree
	}
	return growthByTable
}

func (v growthView) grouped() bool { return v != growthByTable }

// label is the header badge and the footer hint for the view.
func (v growthView) label() string {
	switch v {
	case growthBySchema:
		return "by schema"
	case growthByDatabase:
		return "by database"
	case growthByPartTree:
		return "partition trees"
	}
	return "relations"
}

// noun is what one row is called in prose ("narrow to schema", "Σ 12 schemas").
func (v growthView) noun() string {
	switch v {
	case growthBySchema:
		return "schema"
	case growthByDatabase:
		return "database"
	case growthByPartTree:
		return "partition tree"
	}
	return "relation"
}

// plural is noun pluralised for the Σ footer; English is irregular enough that
// appending "s" to "schema" is right but to "database" is too, and only the
// partition tree needs care.
func (v growthView) plural() string {
	if v == growthByPartTree {
		return "partition trees"
	}
	return v.noun() + "s"
}

// scope maps the UI's view onto the data layer's fold. The view type lives
// here because the labels and the cycle order are UI concerns; pg owns only
// what a fold means.
func (v growthView) scope() pg.GrowthScope {
	switch v {
	case growthBySchema:
		return pg.GrowthBySchema
	case growthByDatabase:
		return pg.GrowthByDatabase
	case growthByPartTree:
		return pg.GrowthByPartition
	}
	return pg.GrowthByTable
}

// growthFilter narrows the leaf list to one roll-up row's members: the view the
// key was taken from, and the key itself. It is structural rather than textual
// because a schema or partition-tree group has no text the / filter could
// match.
type growthFilter struct {
	view growthView
	key  string
	// display is what the header shows for the narrowing, kept separate from
	// the key so a qualified key can read as a bare name.
	display string
}

func (f *growthFilter) matches(r pg.GrowthRow) bool {
	if f == nil {
		return true
	}
	return pg.ScopeKey(r, f.view.scope()) == f.key
}

// growthState is the size-history tool's per-screen state: the applied window,
// the diffed rows with their in-place scope views, and the sample browser.
type growthState struct {
	// --- the applied window (levelGrowth) ---

	// base and end are the window's two ends. endPath == growthNow marks a
	// live end, which loadCurrent re-captures on every refresh: sizes have no
	// in-memory baseline to diff against, so "now" is a real CaptureSizes.
	//
	// The paths are recorded when the window is picked rather than derived
	// back from CapturedAt the way the snapshot browser does it — a sample
	// knows where it came from, so there is nothing to search for.
	base, end         *pg.SizeSample
	basePath, endPath string
	// window carries the facts about the pair (skew, stats reset, identity,
	// floor) that the header banners, as opposed to any one row.
	window pg.GrowthWindow
	// rows is the diff at leaf scope — the ONE row set every scope
	// re-projects, so a scope switch never touches a file.
	rows []pg.GrowthRow
	// other is the relations both samples' size floors folded away, as one
	// row, so a total still reconciles.
	other pg.GrowthRow
	// cols is the projected column descriptors, parallel to screen.diagCols,
	// so cycleSort can map the renderer's column index back to a stable id.
	cols []growthColDesc

	// view is the Tab-cycled scope; group narrows the leaf list to one
	// roll-up row's members. showGone includes the dropped relations, hidden
	// by default because a negative delta reads as noise next to the growth
	// the tool is about.
	view     growthView
	group    *growthFilter
	showGone bool

	// --- the sample browser (levelGrowthWindow) ---

	// metas is the listing, aligned by index with the browser's items via
	// item.snapPath. live* is the current server's identity and size, used to
	// flag a sample from another cluster and to bar the @now row.
	metas        []pg.SizeSampleMeta
	liveOID      uint32
	liveSystemID int64
	liveTotal    int64
	// entry marks the browser pushed when the tool opens, before the table has
	// ever loaded: the pick is always the window's start, the @now row is
	// omitted, and Back leaves the tool rather than exposing the empty table.
	entry bool
	// capturing is an S capture in flight. A size capture stats every relation
	// and can run for minutes, so it must not stack.
	capturing bool

	// --- the long-term trend (the trend column) ---

	// trend is the sample history behind the trend column. It is loaded after
	// the window is already on screen, because reading up to a bucket's worth
	// of sample files is far slower than the two the window itself needs, and
	// the table is useful without it.
	trend growthTrend
}

// trendState is how far the trend column's history has got.
type trendState uint8

const (
	trendIdle    trendState = iota // not asked for yet
	trendLoading                   // a read is in flight
	trendReady                     // series loaded (possibly empty)
	trendNone                      // too little history, or the read failed
)

// growthTrend is the sample history projected onto the current scope.
//
// series is keyed by pg.ScopeKey for the scope it was folded at, so it is
// rebuilt whenever the scope changes; scope records which, so a stale fold is
// never handed to a cell. paths is the sample set it was built from, which is
// what keeps the @now refresh — which re-captures on every tick — from
// re-reading the whole history each time.
type growthTrend struct {
	state  trendState
	hist   *pg.SizeSeries
	series map[string][]float64
	scope  pg.GrowthScope
	paths  []string
	note   string // why there is no trace, when there is none
}

// shown reports whether the trend column should appear at all. It is present
// while loading so the column does not jump into the layout when the history
// lands, and absent when there is no history to draw.
func (t *growthTrend) shown() bool {
	return t.state == trendLoading || (t.state == trendReady && t.hist != nil)
}

// fold re-projects the history onto a scope, memoised: a Tab between scopes
// re-folds in memory and never re-reads a file.
func (t *growthTrend) fold(rows []pg.GrowthRow, by pg.GrowthScope) {
	if t.hist == nil {
		t.series, t.scope = nil, by
		return
	}
	t.series = pg.FoldSizeSeries(t.hist, rows, by)
	t.scope = by
}

// visibleRows applies the gone-row and narrowing filters. It runs BEFORE any
// roll-up, so the Σ footer always reconciles with what is on screen at every
// scope; filtering only the leaf list would make the schema total disagree
// with the relation total the moment v is pressed.
func (g *growthState) visibleRows() []pg.GrowthRow {
	out := make([]pg.GrowthRow, 0, len(g.rows))
	for _, r := range g.rows {
		if !g.showGone && r.Tag == pg.GrowthGone {
			continue
		}
		if !g.group.matches(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// duration is the applied window's length, or 0 when no window is applied —
// which the rate columns render as "—" rather than as zero growth.
func (g *growthState) duration() time.Duration { return g.window.Duration }
