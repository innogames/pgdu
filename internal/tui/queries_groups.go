package tui

import (
	"pgdu/internal/pg"
)

// stmtView is the Tab-cycled projection of the top-queries window: the
// per-statement list, or one row per main table / command type built from the
// same window deltas. The roll-ups are pure re-projections of screen.stat.rows —
// no extra SQL — so they refresh with every live tick like the list does.
type stmtView int

const (
	stmtViewQueries stmtView = iota
	stmtViewByTable
	stmtViewByType
)

func (v stmtView) next() stmtView {
	switch v {
	case stmtViewQueries:
		return stmtViewByTable
	case stmtViewByTable:
		return stmtViewByType
	}
	return stmtViewQueries
}

func (v stmtView) grouped() bool { return v != stmtViewQueries }

// label is the header badge and the footer hint for the view.
func (v stmtView) label() string {
	switch v {
	case stmtViewByTable:
		return "by table"
	case stmtViewByType:
		return "by type"
	}
	return "queries"
}

// colName heads the key column of the grouped table (and names what Enter
// narrows to): the same labels the per-statement list uses for the parsed
// table and the command-type tag, so the two views read alike.
func (v stmtView) colName() string {
	if v == stmtViewByType {
		return "T"
	}
	return "table"
}

// noun is what one group is called in prose ("212 tables", "narrow to type").
func (v stmtView) noun() string {
	if v == stmtViewByType {
		return "type"
	}
	return "table"
}

// stmtNoTable is the bucket for statements without a resolvable main table
// (VALUES, BEGIN/COMMIT, a leading subquery…). It is a real group so the Σ
// footer still reconciles with the window total; d/u skip it.
const stmtNoTable = "(no table)"

// key returns a statement's aggregation key for the view and its display form.
// For tables the key is the qualified pg.MainTable result (what d/u resolve by
// name) and the display strips public. like the list's table column does.
func (v stmtView) key(q pg.QueryStat) (key, display string) {
	switch v {
	case stmtViewByTable:
		key = pg.MainTable(q.Query)
		if key == "" {
			return stmtNoTable, stmtNoTable
		}
		return key, mainTableDisplay(q.Query)
	case stmtViewByType:
		key = pg.QueryKind(q.Query)
		return key, key
	}
	return "", ""
}

// stmtGroupFilter narrows the per-statement list to one roll-up row's members:
// the view the key was taken from and the key itself.
type stmtGroupFilter struct {
	view stmtView
	key  string
}

// matches reports whether q belongs to the narrowed group.
func (f *stmtGroupFilter) matches(q pg.QueryStat) bool {
	if f == nil {
		return true
	}
	k, _ := f.view.key(q)
	return k == f.key
}

// stmtGroup is one roll-up row: its key, the member count and the summed
// counters. Running the metric cell builders over sum yields pooled ratios
// (mean_ms = Σtotal÷Σcalls, weighted hit%) exactly like the Σ footer.
type stmtGroup struct {
	key, display string
	n            int
	sum          pg.QueryStat
}

// groupQueryStats folds the window rows by v.key in first-seen order; the
// order is irrelevant since applySort orders the items afterwards.
func groupQueryStats(rows []pg.QueryStat, v stmtView) []stmtGroup {
	idx := make(map[string]int)
	var out []stmtGroup
	for _, q := range rows {
		k, disp := v.key(q)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, stmtGroup{key: k, display: disp})
		}
		out[i].n++
		addQueryStat(&out[i].sum, q)
	}
	return out
}

// addQueryStat adds every additive counter of q into dst (identity fields and
// the non-additive extrema/stddev are left alone). Summing all counters — not
// just those any single column reads today — keeps roll-ups and the footer
// correct as opt-in columns are enabled or new ones added to the registry.
func addQueryStat(dst *pg.QueryStat, q pg.QueryStat) {
	dst.Calls += q.Calls
	dst.Rows += q.Rows
	dst.TotalExecTime += q.TotalExecTime
	dst.Plans += q.Plans
	dst.TotalPlanTime += q.TotalPlanTime
	dst.SharedBlksHit += q.SharedBlksHit
	dst.SharedBlksRead += q.SharedBlksRead
	dst.SharedBlksDirtied += q.SharedBlksDirtied
	dst.SharedBlksWritten += q.SharedBlksWritten
	dst.LocalBlksHit += q.LocalBlksHit
	dst.LocalBlksRead += q.LocalBlksRead
	dst.LocalBlksDirtied += q.LocalBlksDirtied
	dst.LocalBlksWritten += q.LocalBlksWritten
	dst.TempBlksRead += q.TempBlksRead
	dst.TempBlksWritten += q.TempBlksWritten
	dst.SharedBlkReadTime += q.SharedBlkReadTime
	dst.SharedBlkWriteTime += q.SharedBlkWriteTime
	dst.LocalBlkReadTime += q.LocalBlkReadTime
	dst.LocalBlkWriteTime += q.LocalBlkWriteTime
	dst.TempBlkReadTime += q.TempBlkReadTime
	dst.TempBlkWriteTime += q.TempBlkWriteTime
	dst.WALRecords += q.WALRecords
	dst.WALFPI += q.WALFPI
	dst.WALBytes += q.WALBytes
}

// Column ids the grouped views add to the metric set they share with the list.
const (
	colQueries stmtColID = "queries"
	colGroup   stmtColID = "group"
)

// stmtGroupColDesc is a column of the grouped views; the ids are the list's
// stmtColIDs so the metric columns keep one identity across both pickers.
type stmtGroupColDesc = colDesc[stmtColID, stmtGroup, stmtCtx]

// stmtGroupDefaultOn is the roll-ups' leaner default column set: a table row
// pools dozens of statements, so per-call shape metrics (blk/row, miss, plan
// time) say less there than the shares and totals do. Everything else in the
// list's registry is one C toggle away.
var stmtGroupDefaultOn = map[stmtColID]bool{
	colTotalMs: true, colPctTime: true, colMeanMs: true, colCalls: true, colRows: true,
	colHitPct: true, colIOms: true, colWAL: true,
}

// stmtGroupColumnRegistry derives the grouped views' columns from the list's
// registry: every metric column wrapped to read the group's summed counters,
// then the member count and the key column (named for the view: table / T),
// last so the no-bar grow lands on the name.
func stmtGroupColumnRegistry(v stmtView) []stmtGroupColDesc {
	var out []stmtGroupColDesc
	for _, d := range stmtColumnRegistry() {
		switch d.id {
		case colMainTable, colType, colQuery:
			continue
		}
		cell := d.cell
		out = append(out, stmtGroupColDesc{
			id: d.id, name: d.name, kind: d.kind, desc: d.desc, available: d.available,
			defaultOn: stmtGroupDefaultOn[d.id],
			cell:      func(g stmtGroup, ctx stmtCtx) pg.DiagCell { return cell(g.sum, ctx) },
		})
	}
	out = append(out,
		stmtGroupColDesc{id: colQueries, name: "queries", kind: pg.DiagInt, defaultOn: true,
			desc: "distinct statements rolled up into this row",
			cell: func(g stmtGroup, _ stmtCtx) pg.DiagCell { return diagNum(formatRows(int64(g.n)), float64(g.n)) }},
		stmtGroupColDesc{id: colGroup, name: v.colName(), kind: pg.DiagText, defaultOn: true, mandatory: true,
			desc: "the " + v.noun() + " the statements were grouped by",
			cell: func(g stmtGroup, _ stmtCtx) pg.DiagCell { return pg.DiagCell{Display: g.display} }},
	)
	return out
}

// stmtGroupSpec binds a grouped view's registry to the roll-ups' shared picker
// state (Model.stmtGroupTable) and prefs key; the two views differ only in the
// key column's header, so they share visibility and sort memory.
func stmtGroupSpec(v stmtView) colSpec[stmtColID, stmtGroup, stmtCtx] {
	return colSpec[stmtColID, stmtGroup, stmtCtx]{
		registry:    func() []stmtGroupColDesc { return stmtGroupColumnRegistry(v) },
		prefsKey:    colPrefsQueryGroups,
		defaultSort: colTotalMs,
		title:       "choose which columns the " + v.label() + " roll-up shows — opt-in metrics are off by default",
		unavailNote: "track_planning off",
	}
}

// labelStmtGroupFooter turns the summed row into the pinned "← Sum" footer of a
// grouped view: the label in the key column, the whole window's statement
// count in queries. Located by column id so it holds whichever columns show.
func labelStmtGroupFooter(descs []stmtGroupColDesc, total []pg.DiagCell, nQueries int) {
	for i, d := range descs {
		switch d.id {
		case colGroup:
			total[i].Display = "← Sum"
		case colQueries:
			total[i] = diagNum(formatRows(int64(nQueries)), float64(nQueries))
		}
	}
}

// buildStatementGroupItems rolls the window rows up by the view's key into
// generic-table rows (item.data = []pg.DiagCell). windowMs is the whole
// window's exec time (the time% denominator, so a group's share reads against
// the window). Returns the items, the projected descriptors and the Σ footer
// (nil without rows).
func (m *Model) buildStatementGroupItems(v stmtView, rows []pg.QueryStat, windowMs float64, trackPlanning bool) ([]item, []stmtGroupColDesc, []pg.DiagCell) {
	ctx := stmtCtx{windowMs: windowMs, trackPlanning: trackPlanning}
	descs := stmtGroupSpec(v).visibleCols(&m.stmtGroupTable, ctx)
	groups := groupQueryStats(rows, v)
	items := make([]item, 0, len(groups))
	var sum stmtGroup
	for _, g := range groups {
		items = append(items, item{
			name:         g.display,
			data:         cellsFor(descs, g, ctx),
			hasChildren:  true, // Enter → the list narrowed to this group
			stmtGroupKey: g.key,
		})
		sum.n += g.n
		addQueryStat(&sum.sum, g.sum)
	}
	if len(groups) == 0 {
		return items, descs, nil
	}
	total := cellsFor(descs, sum, ctx)
	labelStmtGroupFooter(descs, total, sum.n)
	return items, descs, total
}

// syncStmtBar points the generic renderer's bar at the sort column on the
// grouped views — "which tables are heavy in this metric" is the question they
// answer — and at nothing on the list, where the width goes to the query text.
// A text sort column (the key) has no magnitude to bar. The width memo is keyed
// on the dirty flag, not the bar column, so a move must invalidate it.
func (m *Model) syncStmtBar(s *screen) {
	bar := -1
	if s.stat.view.grouped() && s.diagSortCol >= 0 && s.diagSortCol < len(s.diagCols) &&
		numericDiagKind(s.diagCols[s.diagSortCol].Kind) {
		bar = s.diagSortCol
	}
	if bar != s.diagBarCol {
		s.diagBarCol = bar
		s.diagMetricsDirty = true
	}
}

// numericDiagKind reports the column kinds that carry a magnitude (the ones
// cycleSort defaults to descending).
func numericDiagKind(k pg.DiagColumnKind) bool {
	switch k {
	case pg.DiagInt, pg.DiagFloat, pg.DiagPercent, pg.DiagBytes, pg.DiagPercentGraded,
		pg.DiagCostGraded, pg.DiagDuration, pg.DiagPercentBad, pg.DiagCount:
		return true
	}
	return false
}
