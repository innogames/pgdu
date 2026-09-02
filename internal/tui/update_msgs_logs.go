package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// logView is which pane the levelLogs screen shows.
type logView int

const (
	logViewGroups   logView = iota // aggregated messages under section headers
	logViewTimeline                // every entry chronologically, generic table
)

// logGroupBy is how the groups pane is sectioned.
type logGroupBy int

const (
	logGroupByCategory logGroupBy = iota
	logGroupBySeverity
	logGroupByNone
)

func (g logGroupBy) label() string {
	switch g {
	case logGroupBySeverity:
		return "by severity"
	case logGroupByNone:
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
// state: grouped by category, every category shown (v hides the spam ones),
// no severity floor, default window.
func (m *Model) logScreen(src pg.LogSource) *screen {
	return &screen{
		level: levelLogs, title: "log", tool: toolLogs, db: m.client.DefaultDB(),
		logSrc: src, logWindow: logDefaultWindow, loading: true, logShowSpam: true,
		sort: sortByCount, sortDesc: true,
	}
}

// logGroupScreen is the entries-of-one-group child; it renders from the
// parent's report, so it carries the group pointer and a back-reference via
// findLevel(levelLogs).
func (m *Model) logGroupScreen(parent *screen, g *pg.LogGroup) *screen {
	return &screen{
		level: levelLogGroup, title: "group", tool: toolLogs, db: parent.db,
		logGroup: g, logReport: parent.logReport, logSrc: parent.logSrc,
		sort: sortByLast, sortDesc: true,
	}
}

func (m *Model) logEntryScreen(parent *screen, e *pg.LogEntry) *screen {
	return &screen{
		level: levelLogEntry, title: "entry", tool: toolLogs, db: parent.db,
		logEntry: e, logReport: parent.logReport, logSrc: parent.logSrc,
	}
}

func (m *Model) onLogFilesLoaded(msg logFilesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelLogFiles)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.logCands = msg.cands
	s.items = logFileItems(msg.cands)
	s.itemsRev++
	s.resetCursor()
	// One readable candidate: skip the picker (it stays on the stack for o/Esc).
	if len(msg.cands) == 1 && m.top() == s {
		m.stack = append(m.stack, m.logScreen(msg.cands[0].Open()))
		return m.loadCurrent()
	}
	return nil
}

// logFileItems renders the candidates as picker rows.
func logFileItems(cands []pg.LogCandidate) []item {
	items := make([]item, 0, len(cands))
	for _, c := range cands {
		var parts []string
		parts = append(parts, c.Info.Kind)
		if c.Info.Size >= 0 {
			parts = append(parts, humanize.Bytes(c.Info.Size))
		}
		if !c.Info.ModTime.IsZero() {
			parts = append(parts, c.Info.ModTime.Local().Format("2006-01-02 15:04")+" ("+relativeAge(time.Since(c.Info.ModTime))+")")
		}
		parts = append(parts, "via "+c.Reason)
		if c.Info.Current {
			parts = append(parts, "current")
		}
		items = append(items, item{
			name:        c.Info.Path,
			detail:      strings.Join(parts, "  ·  "),
			hasChildren: true,
			size:        max(c.Info.Size, 0),
			data:        c,
		})
	}
	return items
}

func (m *Model) onLogLoaded(msg logLoadedMsg) tea.Cmd {
	s := m.findLevel(levelLogs)
	if s == nil || s.logSrc == nil || s.logSrc.Info().Path != msg.path {
		return nil
	}
	s.loading = false
	s.loaded = true
	if msg.err != nil {
		s.logErr = msg.err
		if !msg.refresh {
			s.logReport = nil
			s.items = nil
			s.itemsRev++
		}
		return nil
	}
	s.logErr = nil
	// Remember the highlighted group across a refresh so a live tail doesn't
	// yank the cursor when counts reshuffle the order.
	var keepKey string
	if msg.refresh && s.logView == logViewGroups {
		if g := s.selectedLogGroup(); g != nil {
			keepKey = g.Key
		}
	}
	s.logReport = msg.report
	// Child screens hold the previous report pointer; refresh them too so a
	// group opened before a tick keeps showing live data.
	for _, sc := range m.stack {
		if sc.level == levelLogGroup || sc.level == levelLogEntry {
			sc.logReport = msg.report
		}
	}
	m.rebuildLogItems(s)
	if keepKey != "" {
		vis := s.visibleIndexes()
		for vi, idx := range vis {
			if g, ok := s.items[idx].data.(*pg.LogGroup); ok && g.Key == keepKey {
				s.cursor = vi
				break
			}
		}
	} else if !msg.refresh {
		s.resetCursor()
		// The groups pane opens on the first real row, not a section header.
		m.skipLogHeader(s, 1)
	}
	if top := m.top(); top != s && (top.level == levelLogGroup || top.level == levelLogEntry) {
		m.rebuildLogChild(top)
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
func (s *screen) selectedLogGroup() *pg.LogGroup {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return nil
	}
	g, _ := s.items[vis[s.cursor]].data.(*pg.LogGroup)
	return g
}

// skipLogHeader nudges the cursor off a section-header row in direction dir
// (+1 down, -1 up) so ↑/↓ never rest on an inert line.
func (m *Model) skipLogHeader(s *screen, dir int) {
	vis := s.visibleIndexes()
	for s.cursor >= 0 && s.cursor < len(vis) {
		if _, hdr := s.items[vis[s.cursor]].data.(logSection); !hdr {
			return
		}
		next := s.cursor + dir
		if next < 0 || next >= len(vis) {
			// Nothing beyond the header in that direction: bounce back.
			dir = -dir
			next = s.cursor + dir
			if next < 0 || next >= len(vis) {
				return
			}
		}
		s.cursor = next
	}
}

// logGroupVisible applies the view filters (spam, severity floor) to a group.
func (s *screen) logGroupVisible(g *pg.LogGroup) bool {
	if !s.logShowSpam && g.Category.IsSpam() {
		return false
	}
	return g.Severity >= s.logMinSev
}

// logEntryVisible is the per-entry counterpart for the timeline.
func (s *screen) logEntryVisible(e *pg.LogEntry) bool {
	if !s.logShowSpam && e.Category.IsSpam() {
		return false
	}
	return e.Severity >= s.logMinSev
}

// rebuildLogItems regenerates the levelLogs rows for the current pane and
// filters. The groups pane is ordered here (sections + per-section sort), so
// applySort leaves it alone; the timeline goes through the generic
// diagnostic-table path (diagCols + []pg.DiagCell rows) and its sort.
func (m *Model) rebuildLogItems(s *screen) {
	r := s.logReport
	if r == nil {
		s.items = nil
		s.diagCols = nil
		s.itemsRev++
		return
	}
	if s.logView == logViewTimeline {
		m.rebuildLogTimeline(s)
		return
	}
	s.diagCols = nil
	s.logCols = nil
	s.items = m.buildLogGroupItems(s)
	s.itemsRev++
	s.clampCursor()
}

// buildLogGroupItems orders the visible groups into sections. Within a
// section rows follow s.sort (count / last seen / title); sections follow
// pg.LogCategories (signal first, spam last) or severity high→low.
func (m *Model) buildLogGroupItems(s *screen) []item {
	r := s.logReport
	type section struct {
		key     int
		title   string
		groups  []*pg.LogGroup
		entries int
	}
	secs := map[int]*section{}
	var order []int
	add := func(key int, title string, g *pg.LogGroup) {
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
		if !s.logGroupVisible(g) {
			continue
		}
		switch s.logGroupBy {
		case logGroupBySeverity:
			add(int(pg.SevPanic-g.Severity), g.Severity.String(), g)
		case logGroupByNone:
			add(0, "", g)
		default:
			pos := 0
			for j, c := range pg.LogCategories {
				if c == g.Category {
					pos = j
				}
			}
			add(pos, g.Category.Label(), g)
		}
	}
	sort.Ints(order)

	less := func(a, b *pg.LogGroup) bool {
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
		if s.logGroupBy != logGroupByNone {
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

// rebuildLogTimeline projects the visible entries onto the generic table.
func (m *Model) rebuildLogTimeline(s *screen) {
	r := s.logReport
	descs := m.visibleLogCols()
	ctx := logCtx{multiDay: !r.Window.From.IsZero() && r.Window.To.Sub(r.Window.From) > 24*time.Hour}
	items := make([]item, 0, len(r.Entries))
	for i := range r.Entries {
		e := &r.Entries[i]
		if !s.logEntryVisible(e) {
			continue
		}
		cells := make([]pg.DiagCell, len(descs))
		parts := make([]string, len(descs))
		for j, d := range descs {
			cells[j] = d.cell(e, ctx)
			parts[j] = cells[j].Display
		}
		items = append(items, item{name: strings.Join(parts, " "), data: cells, logIdx: i + 1})
	}
	s.logCols = descs
	s.diagCols = logDiagColumnsFrom(descs)
	s.diagBarCol = -1
	m.syncLogSort(s, descs)
	s.items = items
	s.diagMetricsDirty = true
	m.applySort(s)
}

// rebuildLogChild fills a levelLogGroup screen's rows from its group's sample
// entries (newest first); levelLogEntry has no rows.
func (m *Model) rebuildLogChild(s *screen) {
	if s.level != levelLogGroup || s.logGroup == nil || s.logReport == nil {
		return
	}
	r := s.logReport
	// The group pointer may belong to an older report after a refresh; re-find
	// it by key so the rows track live data.
	g := s.logGroup
	for i := range r.Groups {
		if r.Groups[i].Key == g.Key {
			g = &r.Groups[i]
			s.logGroup = g
			break
		}
	}
	items := make([]item, 0, len(g.Samples))
	for _, idx := range g.Samples {
		if idx < 0 || idx >= len(r.Entries) {
			continue
		}
		e := &r.Entries[idx]
		items = append(items, item{name: e.FirstLine(), data: e, logIdx: idx + 1})
	}
	s.items = items
	s.itemsRev++
	m.applySort(s)
}

// logEntryOf resolves the entry a row refers to, on any log level.
func (s *screen) logEntryOf(it item) *pg.LogEntry {
	if e, ok := it.data.(*pg.LogEntry); ok {
		return e
	}
	if it.logIdx > 0 && s.logReport != nil && it.logIdx <= len(s.logReport.Entries) {
		return &s.logReport.Entries[it.logIdx-1]
	}
	return nil
}

// logSourceLabel is the compact "file · kind · size" used in the header and
// the picker.
func logSourceLabel(info pg.LogSourceInfo) string {
	parts := []string{filepath.Base(info.Path), info.Kind}
	if info.Size >= 0 {
		parts = append(parts, humanize.Bytes(info.Size))
	}
	return strings.Join(parts, "  ·  ")
}

// logWindowLabel describes how much of the file the report covers.
func logWindowLabel(w pg.LogWindow) string {
	switch {
	case w.FileSize < 0:
		return fmt.Sprintf("last %s", humanize.Bytes(w.Bytes))
	case !w.Truncated:
		return fmt.Sprintf("whole file (%s)", humanize.Bytes(w.FileSize))
	default:
		return fmt.Sprintf("tail %s of %s", humanize.Bytes(w.Bytes), humanize.Bytes(w.FileSize))
	}
}
