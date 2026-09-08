package tui

import (
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// logView is which pane the levelLogs screen shows.
type logView int

const (
	logViewGroups   logView = iota // aggregated messages under section headers
	logViewTimeline                // every entry chronologically, generic table
	logViewSlow                    // only the duration: entries, slowest first
	logViewStats                   // pgbouncer's periodic stats lines, one metric per column
)

// table reports whether the pane is one of the generic-table views (timeline,
// slow), as opposed to the self-ordering groups pane.
func (v logView) table() bool { return v != logViewGroups }

// next cycles the panes on tab: groups → timeline → slow → (stats) → groups.
// The stats pane only exists when the report carries pgbouncer stats lines.
func (v logView) next(hasStats bool) logView {
	switch v {
	case logViewGroups:
		return logViewTimeline
	case logViewTimeline:
		return logViewSlow
	case logViewSlow:
		if hasStats {
			return logViewStats
		}
	}
	return logViewGroups
}

func (v logView) label() string {
	switch v {
	case logViewTimeline:
		return "timeline"
	case logViewSlow:
		return "slow queries"
	case logViewStats:
		return "pooler stats"
	}
	return ""
}

// logGroupBy is how the groups pane is sectioned.
type logGroupBy int

const (
	logGroupByCategory logGroupBy = iota
	logGroupByNone
)

func (g logGroupBy) label() string {
	if g == logGroupByNone {
		return "flat"
	}
	return "by category"
}

// logSection is the item.data payload of a section header row in the groups
// pane. Header rows are inert on Enter and skipped by the cursor-less filter.
type logSection struct {
	title   string
	groups  int
	entries int
}

// logScreen builds the levelLogs screen for one source, with the default view
// state: grouped by category, default window.
func (m *Model) logScreen(src pglog.Source) *screen {
	return &screen{
		level: levelLogs, title: "log", tool: toolLogs, db: m.client.DefaultDB(),
		log: logState{src: src, window: logDefaultWindow}, loading: true,
		sort: sortByCount, sortDesc: true}
}

// logParamMode is what the levelLogGroup screen lists: the sample entries, or
// the group's entries aggregated by the parameters their DETAIL line logged
// (log_parameter_max_length). The $1-only flavour is for statements whose
// trailing parameters vary per call (timestamps, offsets), where the full tuple
// would put every entry in its own row.
type logParamMode int

const (
	logParamsOff   logParamMode = iota
	logParamsAll                // one row per distinct parameter tuple
	logParamsFirst              // one row per distinct $1
)

func (p logParamMode) next() logParamMode {
	switch p {
	case logParamsOff:
		return logParamsAll
	case logParamsAll:
		return logParamsFirst
	}
	return logParamsOff
}

func (p logParamMode) label() string {
	switch p {
	case logParamsAll:
		return "by parameters"
	case logParamsFirst:
		return "by $1"
	}
	return "entries"
}

// logGroupScreen is the entries-of-one-group child; it renders from the
// parent's report, so it carries the group pointer and a back-reference via
// findLevel(levelLogs).
func (m *Model) logGroupScreen(parent *screen, g *pglog.Group) *screen {
	return &screen{
		level: levelLogGroup, title: "group", tool: toolLogs, db: parent.db,
		log:  logState{group: g, report: parent.log.report, src: parent.log.src},
		sort: sortByLast, sortDesc: true}
}

func (m *Model) logEntryScreen(parent *screen, e *pglog.Entry) *screen {
	return &screen{
		level: levelLogEntry, title: "entry", tool: toolLogs, db: parent.db,
		log: logState{entry: e, report: parent.log.report, src: parent.log.src}}
}

func (m *Model) onLogFilesLoaded(msg logFilesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelLogFiles)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.log.cands = msg.cands
	s.diagCols = logFileColumns()
	s.diagBarCol = -1
	// Newest first: the live log leads, then the rotations in age order.
	s.diagSortCol, s.sortDesc = 3, true
	s.items = logFileItems(msg.cands)
	s.diagMetricsDirty = true
	m.applySort(s)
	s.resetCursor()
	// The live server log is what people came for, but sorting by mtime can
	// put a chattier pgbouncer log above it — so pre-select the ● row instead
	// of whatever sorted first.
	for i, it := range s.items {
		if it.logIdx > 0 && it.logIdx <= len(msg.cands) && msg.cands[it.logIdx-1].Info.Current {
			s.cursor = i
			break
		}
	}
	// One readable candidate: skip the picker (it stays on the stack for o/Esc).
	if len(msg.cands) == 1 && m.top() == s {
		m.stack = append(m.stack, m.logScreen(msg.cands[0].Open()))
		return m.loadCurrent()
	}
	return nil
}

// logFileColumns is the picker's schema; logFileItems keeps its cells parallel.
func logFileColumns() []pg.DiagColumn {
	return []pg.DiagColumn{
		{Name: "file", Kind: pg.DiagText},
		{Name: "kind", Kind: pg.DiagText},
		{Name: "size", Kind: pg.DiagBytes},
		{Name: "modified", Kind: pg.DiagText},
		{Name: "age", Kind: pg.DiagDuration},
		{Name: "~lines", Kind: pg.DiagCount},
		{Name: "via", Kind: pg.DiagText},
		{Name: "current", Kind: pg.DiagText},
	}
}

// logFileItems renders the candidates as generic-table rows (sortable by any
// column); logIdx points back at the candidate for Enter.
func logFileItems(cands []pg.LogCandidate) []item {
	items := make([]item, 0, len(cands))
	for i, c := range cands {
		in := c.Info
		size := pg.DiagCell{Display: "—"}
		if in.Size >= 0 {
			size = pg.DiagCell{Display: humanize.Bytes(in.Size), Num: float64(in.Size), HasNum: true}
		}
		mod, age := pg.DiagCell{Display: "—"}, pg.DiagCell{Display: "—"}
		if !in.ModTime.IsZero() {
			mod = pg.DiagCell{Display: in.ModTime.Local().Format("2006-01-02 15:04")}
			ms := float64(time.Since(in.ModTime).Milliseconds())
			age = pg.DiagCell{Display: relativeAge(time.Since(in.ModTime)), Num: ms, HasNum: true}
		}
		lines := pg.DiagCell{Display: "—"}
		if in.Lines >= 0 {
			lines = pg.DiagCell{Display: "~" + fmtCount(int(in.Lines)), Num: float64(in.Lines), HasNum: true}
		}
		current := pg.DiagCell{}
		if in.Current {
			current = pg.DiagCell{Display: "●"}
		}
		cells := []pg.DiagCell{
			{Display: in.Path}, {Display: in.Kind}, size, mod, age, lines, {Display: c.Reason}, current,
		}
		parts := make([]string, len(cells))
		for j, cell := range cells {
			parts[j] = cell.Display
		}
		items = append(items, item{name: strings.Join(parts, " "), hasChildren: true, data: cells, logIdx: i + 1})
	}
	return items
}

func (m *Model) onLogLoaded(msg logLoadedMsg) tea.Cmd {
	s := m.findLevel(levelLogs)
	if s == nil || s.log.src == nil || s.log.src.Info().Path != msg.path {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.log.err = msg.err
		if !msg.refresh {
			s.log.report = nil
			s.items = nil
			s.itemsRev++
		}
		return nil
	}
	s.log.err = nil
	// Remember the highlighted group across a refresh so a live tail doesn't
	// yank the cursor when counts reshuffle the order.
	var keepKey string
	if msg.refresh && s.log.view == logViewGroups {
		if g := s.selectedLogGroup(); g != nil {
			keepKey = g.Key
		}
	}
	s.log.report = msg.report
	// Child screens hold the previous report pointer; refresh them too so a
	// group opened before a tick keeps showing live data.
	for _, sc := range m.stack {
		if sc.level == levelLogGroup || sc.level == levelLogEntry {
			sc.log.report = msg.report
		}
	}
	m.rebuildLogItems(s)
	if keepKey != "" {
		vis := s.visibleIndexes()
		for vi, idx := range vis {
			if g, ok := s.items[idx].data.(*pglog.Group); ok && g.Key == keepKey {
				s.cursor = vi
				break
			}
		}
	} else if !msg.refresh {
		s.resetCursor()
		// The groups pane opens on the first real row, not a section header.
		s.skipInertRow(1)
	}
	if top := m.top(); top != s && (top.level == levelLogGroup || top.level == levelLogEntry) {
		m.rebuildLogChild(top)
	}
	return m.logHostsCmd(s)
}

func (m *Model) onLogHosts(msg logHostsMsg) tea.Cmd {
	s := m.findLevel(levelLogs)
	if s == nil {
		return nil
	}
	if s.log.hosts == nil {
		s.log.hosts = make(map[string]string)
	}
	maps.Copy(s.log.hosts, msg.hosts)
	if s.log.view.table() && s.log.report != nil {
		m.rebuildLogItems(s)
	}
	return nil
}

func (m *Model) onLogTick() tea.Cmd {
	top := m.top()
	if top.level != levelLogs && top.level != levelLogGroup && top.level != levelLogEntry {
		m.logTicking = false
		return nil
	}
	next := m.logTick()
	if next == nil {
		m.logTicking = false
		return nil
	}
	s := m.findLevel(levelLogs)
	if s == nil || s.loading {
		return next
	}
	return tea.Batch(m.loadLogCmd(s, true), next)
}

// selectedLogGroup returns the group under the cursor in the groups pane.
func (s *screen) selectedLogGroup() *pglog.Group {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return nil
	}
	g, _ := s.items[vis[s.cursor]].data.(*pglog.Group)
	return g
}

// rebuildLogItems regenerates the levelLogs rows for the current pane. The groups pane is ordered here (sections + per-section sort), so
// applySort leaves it alone; the timeline and slow panes go through the generic
// diagnostic-table path (diagCols + []pg.DiagCell rows) and its sort — the
// slow pane is the timeline restricted to CatSlowQuery rows.
func (m *Model) rebuildLogItems(s *screen) {
	r := s.log.report
	if r == nil {
		s.items = nil
		s.diagCols = nil
		s.itemsRev++
		return
	}
	if s.log.view.table() {
		m.rebuildLogTimeline(s)
		return
	}
	s.diagCols = nil
	s.log.cols = nil
	s.items = m.buildLogGroupItems(s)
	s.itemsRev++
	s.clampCursor()
}

// buildLogGroupItems orders the groups into sections. Within a
// section rows follow s.sort (count / last seen / title); sections follow
// pglog.Categories (signal first, chatter last), or there is a single unnamed
// one in flat mode.
func (m *Model) buildLogGroupItems(s *screen) []item {
	r := s.log.report
	type section struct {
		key     int
		title   string
		groups  []*pglog.Group
		entries int
	}
	secs := map[int]*section{}
	var order []int
	add := func(key int, title string, g *pglog.Group) {
		sec := secs[key]
		if sec == nil {
			sec = &section{key: key, title: title}
			secs[key] = sec
			order = append(order, key)
		}
		sec.groups = append(sec.groups, g)
		sec.entries += g.Count
	}
	for i := range r.Groups {
		g := &r.Groups[i]
		switch s.log.groupBy {
		case logGroupByNone:
			add(0, "", g)
		default:
			pos := 0
			for j, c := range pglog.Categories {
				if c == g.Category {
					pos = j
				}
			}
			add(pos, g.Category.Label(), g)
		}
	}
	sort.Ints(order)

	less := func(a, b *pglog.Group) bool {
		var l bool
		switch s.sort {
		case sortByLast:
			l = a.Last.Before(b.Last)
		case sortByName:
			l = a.Title < b.Title
		default:
			if a.Count != b.Count {
				l = a.Count < b.Count
			} else {
				l = a.Last.Before(b.Last)
			}
		}
		if s.sortDesc {
			return !l
		}
		return l
	}
	var items []item
	for _, key := range order {
		sec := secs[key]
		sort.SliceStable(sec.groups, func(i, j int) bool { return less(sec.groups[i], sec.groups[j]) })
		if s.log.groupBy != logGroupByNone {
			items = append(items, item{
				name: sec.title,
				data: logSection{title: sec.title, groups: len(sec.groups), entries: sec.entries},
			})
		}
		for _, g := range sec.groups {
			items = append(items, item{
				name:        g.Title,
				size:        int64(g.Count),
				hasChildren: true,
				data:        g,
			})
		}
	}
	return items
}

// rebuildLogTimeline projects every entry onto the generic table; the slow
// pane keeps only duration: lines, the stats pane only pgbouncer stats lines
// (with its own column registry).
func (m *Model) rebuildLogTimeline(s *screen) {
	r := s.log.report
	descs := logSpec(s.log.view).visibleCols(m.logTableFor(s.log.view), logCtx{})
	ctx := logCtx{multiDay: !r.Window.From.IsZero() && r.Window.To.Sub(r.Window.From) > 24*time.Hour, hosts: s.log.hosts}
	items := make([]item, 0, len(r.Entries))
	for i := range r.Entries {
		e := &r.Entries[i]
		if s.log.view == logViewSlow && e.Category != pglog.CatSlowQuery {
			continue
		}
		if s.log.view == logViewStats && e.PoolerStats == nil {
			continue
		}
		cells := make([]pg.DiagCell, len(descs))
		parts := make([]string, len(descs))
		for j, d := range descs {
			cells[j] = d.cell(e, ctx)
			parts[j] = cells[j].Display
		}
		items = append(items, item{name: strings.Join(parts, " "), hasChildren: true, data: cells, logIdx: i + 1})
	}
	s.log.cols = descs
	s.diagCols = diagColumnsFrom(descs)
	s.diagBarCol = -1
	m.syncLogSort(s, descs)
	s.items = items
	s.diagMetricsDirty = true
	m.applySort(s)
}

// rebuildLogChild fills a levelLogGroup screen's rows from its group's sample
// entries (newest first); levelLogEntry has no rows.
func (m *Model) rebuildLogChild(s *screen) {
	if s.level != levelLogGroup || s.log.group == nil || s.log.report == nil {
		return
	}
	r := s.log.report
	// The group pointer may belong to an older report after a refresh; re-find
	// it by key so the rows track live data.
	g := s.log.group
	for i := range r.Groups {
		if r.Groups[i].Key == g.Key {
			g = &r.Groups[i]
			s.log.group = g
			break
		}
	}
	if s.log.params != logParamsOff {
		m.rebuildLogParamRows(s, g)
		return
	}
	s.diagCols = nil
	var items []item
	if s.log.paramKey != "" {
		// A parameter drill-down lists every member with that key, walking
		// the whole report rather than the capped Samples.
		gi := groupIndex(r, g)
		for i := range r.Entries {
			e := &r.Entries[i]
			if int(e.Group) == gi && pglog.ParamKey(e, s.log.paramFirst) == s.log.paramKey {
				items = append(items, item{name: e.FirstLine(), hasChildren: true, data: e, logIdx: i + 1})
			}
		}
	} else {
		items = make([]item, 0, len(g.Samples))
		for _, idx := range g.Samples {
			if idx < 0 || idx >= len(r.Entries) {
				continue
			}
			e := &r.Entries[idx]
			items = append(items, item{name: e.FirstLine(), hasChildren: true, data: e, logIdx: idx + 1})
		}
	}
	s.items = items
	s.itemsRev++
	m.applySort(s)
}

// groupIndex locates g in r.Groups (the value Entry.Group carries).
func groupIndex(r *pglog.Report, g *pglog.Group) int {
	for i := range r.Groups {
		if &r.Groups[i] == g || r.Groups[i].Key == g.Key {
			return i
		}
	}
	return -1
}

// rebuildLogParamRows turns the group screen into a generic table with one
// row per parameter key. Duration columns are only offered when the members
// carry one (slow queries, lock waits); the time columns render as text whose
// lexical order is chronological within a window. logIdx points at the row's
// newest entry so j (jump) and Enter (drill into the key's entries) work.
func (m *Model) rebuildLogParamRows(s *screen, g *pglog.Group) {
	r := s.log.report
	first := s.log.params == logParamsFirst
	rows := pglog.GroupParams(r, groupIndex(r, g), first)
	hasDur := false
	for i := range rows {
		hasDur = hasDur || rows[i].HasDur
	}
	cols := []pg.DiagColumn{
		{Name: "parameters", Kind: pg.DiagText},
		{Name: "count", Kind: pg.DiagCount},
		{Name: "share", Kind: pg.DiagPercent},
	}
	if hasDur {
		cols = append(cols,
			pg.DiagColumn{Name: "total", Kind: pg.DiagDuration},
			pg.DiagColumn{Name: "avg", Kind: pg.DiagDuration},
			pg.DiagColumn{Name: "p95", Kind: pg.DiagDuration},
			pg.DiagColumn{Name: "max", Kind: pg.DiagDuration})
	}
	cols = append(cols, pg.DiagColumn{Name: "first", Kind: pg.DiagText}, pg.DiagColumn{Name: "last", Kind: pg.DiagText})
	dur := func(ms float64) pg.DiagCell {
		return pg.DiagCell{Display: fmtAge(ms), Num: ms, HasNum: true}
	}
	ts := func(t time.Time) pg.DiagCell {
		if t.IsZero() {
			return pg.DiagCell{Display: "—"}
		}
		return pg.DiagCell{Display: t.Format("01-02 15:04:05")}
	}
	items := make([]item, 0, len(rows))
	for i := range rows {
		p := &rows[i]
		share := 0.0
		if g.Count > 0 {
			share = 100 * float64(p.Count) / float64(g.Count)
		}
		cells := []pg.DiagCell{
			{Display: collapseWS(pglog.FormatParamKey(p.Key, first), 300)},
			{Display: fmtCount(p.Count), Num: float64(p.Count), HasNum: true},
			{Display: fmt.Sprintf("%.1f%%", share), Num: share, HasNum: true},
		}
		if hasDur {
			cells = append(cells, dur(p.SumMs), dur(p.AvgMs()), dur(p.P95Ms), dur(p.MaxMs))
		}
		cells = append(cells, ts(p.First), ts(p.Last))
		parts := make([]string, len(cells))
		for j, c := range cells {
			parts[j] = c.Display
		}
		items = append(items, item{name: strings.Join(parts, " "), hasChildren: true, data: cells, logIdx: p.Sample + 1})
	}
	// Keep the user's column choice across refreshes; a fresh table (or one
	// whose column set changed) opens count-descending.
	if s.diagCols == nil || len(s.diagCols) != len(cols) {
		s.diagSortCol, s.sortDesc = 1, true
	}
	s.diagCols = cols
	s.diagBarCol = -1
	s.items = items
	s.itemsRev++
	s.diagMetricsDirty = true
	m.applySort(s)
}

// logEntryOf resolves the entry a row refers to, on any log level.
func (s *screen) logEntryOf(it item) *pglog.Entry {
	if e, ok := it.data.(*pglog.Entry); ok {
		return e
	}
	if it.logIdx > 0 && s.log.report != nil && it.logIdx <= len(s.log.report.Entries) {
		return &s.log.report.Entries[it.logIdx-1]
	}
	return nil
}

// logSourceLabel is the compact "file · kind · size" used in the header and
// the picker.
func logSourceLabel(info pglog.SourceInfo) string {
	parts := []string{filepath.Base(info.Path), info.Kind}
	if info.Size >= 0 {
		parts = append(parts, humanize.Bytes(info.Size))
	}
	return strings.Join(parts, "  ·  ")
}

// logWindowLabel describes how much of the file the report covers.
func logWindowLabel(w pglog.Window) string {
	switch {
	case w.FileSize < 0:
		return "last " + humanize.Bytes(w.Bytes)
	case !w.Truncated:
		return fmt.Sprintf("whole file (%s)", humanize.Bytes(w.FileSize))
	default:
		return fmt.Sprintf("tail %s of %s", humanize.Bytes(w.Bytes), humanize.Bytes(w.FileSize))
	}
}
