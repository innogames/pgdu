package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
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
)

// item is the row data the renderer consumes; concrete payload is in `data`.
type item struct {
	name   string
	size   int64
	bloat  int64
	detail string
	data   any
}

type screen struct {
	level   level
	title   string
	items   []item
	cursor  int
	offset  int
	sort    sortMode
	loaded  bool
	loading bool
	err     error

	// Which top-level tool this screen belongs to. Inherited from the
	// parent screen when drilling in.
	tool tool

	// Context for loading & subsequent drills.
	db        string
	schema    string
	tableName string
	tableOID  uint32
	table     pg.Table
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
		level: levelTools,
		title: "tools",
		sort:  sortByName,
	}}
	return m
}

// toolItems is the static list shown on the root tool-picker screen.
func toolItems() []item {
	return []item{
		{name: "Disk usage", detail: "browse tables by total relation size on disk", data: toolDisk},
		{name: "Shared buffers", detail: "browse tables by shared_buffers footprint and cache hit ratio", data: toolBuffers},
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCurrent())
}

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
type columnsLoadedMsg struct {
	tableOID uint32
	columns  []pg.Column
	err      error
}

// --- commands ---

func (m *Model) loadCurrent() tea.Cmd {
	s := m.top()
	switch s.level {
	case levelTools:
		s.items = toolItems()
		s.loading = false
		s.loaded = true
		return nil
	}
	s.loading = true
	s.loaded = false
	switch s.level {
	case levelDatabases:
		return m.loadDatabasesCmd()
	case levelSchemas:
		return m.loadSchemasCmd(s.db)
	case levelTables:
		return m.loadTablesCmd(s.db, s.schema)
	case levelBufferTables:
		return m.loadBufferStatsCmd(s.db, s.schema)
	case levelParts:
		return m.loadPartsCmd(s.table)
	case levelColumns:
		return m.loadColumnsCmd(s.db, s.tableOID)
	}
	return nil
}

func (m *Model) loadDatabasesCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		dbs, err := m.client.ListDatabases(ctx)
		return databasesLoadedMsg{dbs: dbs, err: err}
	}
}

func (m *Model) loadSchemasCmd(db string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ss, err := m.client.ListSchemas(ctx, db)
		return schemasLoadedMsg{db: db, schemas: ss, err: err}
	}
}

func (m *Model) loadTablesCmd(db, schema string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ts, err := m.client.ListTables(ctx, db, schema)
		return tablesLoadedMsg{db: db, schema: schema, tables: ts, err: err}
	}
}

func (m *Model) loadPartsCmd(t pg.Table) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		parts, err := m.client.TableParts(ctx, t)
		return partsLoadedMsg{table: t, parts: parts, err: err}
	}
}

func (m *Model) fillBloatCmd(t pg.Table, parts []pg.Part) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := m.client.FillBloat(ctx, t, parts)
		return bloatFilledMsg{table: t, parts: parts, err: err}
	}
}

func (m *Model) loadColumnsCmd(db string, oid uint32) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cols, err := m.client.ListColumns(ctx, db, oid)
		return columnsLoadedMsg{tableOID: oid, columns: cols, err: err}
	}
}

func (m *Model) loadBufferStatsCmd(db, schema string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stats, err := m.client.TableBufferStats(ctx, db, schema)
		return bufferStatsLoadedMsg{db: db, schema: schema, stats: stats, err: err}
	}
}

// --- Update ---

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case databasesLoadedMsg:
		s := m.findLevel(levelDatabases)
		if s == nil {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, d := range msg.dbs {
			s.items = append(s.items, item{name: d.Name, size: d.SizeBytes, data: d})
		}
		m.applySort(s)

	case schemasLoadedMsg:
		s := m.findLevel(levelSchemas)
		if s == nil || s.db != msg.db {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, sc := range msg.schemas {
			detail := fmt.Sprintf("%d tables", sc.TableCount)
			s.items = append(s.items, item{name: sc.Name, size: sc.SizeBytes, detail: detail, data: sc})
		}
		m.applySort(s)

	case tablesLoadedMsg:
		s := m.findLevel(levelTables)
		if s == nil || s.db != msg.db || s.schema != msg.schema {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, t := range msg.tables {
			detail := fmt.Sprintf("heap %s · idx %s · toast %s · ~%s rows",
				humanize.Bytes(t.HeapBytes), humanize.Bytes(t.IndexesBytes),
				humanize.Bytes(t.ToastBytes), formatRows(t.EstRows))
			s.items = append(s.items, item{name: t.Name, size: t.TotalBytes, detail: detail, data: t})
		}
		m.applySort(s)

	case partsLoadedMsg:
		s := m.findLevel(levelParts)
		if s == nil || s.tableOID != msg.table.OID {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, p := range msg.parts {
			s.items = append(s.items, partToItem(p))
		}
		m.applySort(s)
		if m.fetchBloat && msg.err == nil {
			return m, m.fillBloatCmd(msg.table, msg.parts)
		}

	case bufferStatsLoadedMsg:
		s := m.findLevel(levelBufferTables)
		if s == nil || s.db != msg.db || s.schema != msg.schema {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, st := range msg.stats {
			s.items = append(s.items, bufferStatToItem(st))
		}
		m.applySort(s)

	case columnsLoadedMsg:
		s := m.findLevel(levelColumns)
		if s == nil || s.tableOID != msg.tableOID {
			return m, nil
		}
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.items = s.items[:0]
		for _, col := range msg.columns {
			s.items = append(s.items, columnToItem(col))
		}
		m.applySort(s)

	case bloatFilledMsg:
		s := m.findLevel(levelParts)
		if s == nil || s.tableOID != msg.table.OID {
			return m, nil
		}
		if msg.err != nil {
			s.err = msg.err
			return m, nil
		}
		for i, p := range msg.parts {
			if i < len(s.items) {
				s.items[i].bloat = p.WastedBytes
			}
		}
		m.applySort(s)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.top()
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Down):
		if s.cursor < len(s.items)-1 {
			s.cursor++
		}
	case key.Matches(msg, m.keys.Up):
		if s.cursor > 0 {
			s.cursor--
		}
	case key.Matches(msg, m.keys.Top):
		s.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		s.cursor = len(s.items) - 1
		if s.cursor < 0 {
			s.cursor = 0
		}
	case key.Matches(msg, m.keys.SortSize):
		s.sort = sortBySize
		m.applySort(s)
	case key.Matches(msg, m.keys.SortName):
		s.sort = sortByName
		m.applySort(s)
	case key.Matches(msg, m.keys.Refresh):
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.ToggleBloat):
		m.fetchBloat = !m.fetchBloat
	case key.Matches(msg, m.keys.Back):
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
	case key.Matches(msg, m.keys.Enter):
		return m, m.drillIn()
	}
	return m, nil
}

func (m *Model) drillIn() tea.Cmd {
	s := m.top()
	if !s.loaded || len(s.items) == 0 {
		return nil
	}
	cur := s.items[s.cursor]
	switch s.level {
	case levelTools:
		t := cur.data.(tool)
		next := &screen{level: levelDatabases, title: "databases", tool: t, sort: sortBySize}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelDatabases:
		d := cur.data.(pg.Database)
		next := &screen{level: levelSchemas, title: "schemas", tool: s.tool, db: d.Name, sort: sortBySize}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelSchemas:
		sc := cur.data.(pg.Schema)
		var next *screen
		switch s.tool {
		case toolBuffers:
			next = &screen{level: levelBufferTables, title: "buffers", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize}
		default:
			next = &screen{level: levelTables, title: "tables", tool: s.tool, db: sc.DB, schema: sc.Name, sort: sortBySize}
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelTables:
		t := cur.data.(pg.Table)
		next := &screen{
			level: levelParts, title: "parts", tool: s.tool,
			db: t.DB, schema: t.Schema, tableName: t.Name, tableOID: t.OID,
			table: t, sort: sortBySize,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	case levelParts:
		// Only the heap row drills further — into per-column space estimates.
		// Toast and index rows have no meaningful sub-breakdown.
		p, ok := cur.data.(pg.Part)
		if !ok || p.Kind != pg.PartHeap {
			return nil
		}
		next := &screen{
			level: levelColumns, title: "columns", tool: s.tool,
			db: s.db, schema: s.schema, tableName: s.tableName, tableOID: s.tableOID,
			table: s.table, sort: sortBySize,
		}
		m.stack = append(m.stack, next)
		return m.loadCurrent()
	}
	return nil
}

// --- View ---

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	s := m.top()

	var b strings.Builder
	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	contentHeight := m.height - 4 // header + blank + help
	if contentHeight < 3 {
		contentHeight = 3
	}

	switch {
	case s.loading || !s.loaded:
		b.WriteString(fmt.Sprintf("  %s loading %s…\n", m.spinner.View(), s.title))
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case s.err != nil:
		b.WriteString(styleErr.Render("  error: "+s.err.Error()) + "\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	case len(s.items) == 0:
		b.WriteString("  (no items)\n")
		for i := 1; i < contentHeight; i++ {
			b.WriteString("\n")
		}
	default:
		if s.level == levelTools {
			b.WriteString(m.renderToolPicker(s, contentHeight))
		} else {
			b.WriteString(m.renderList(s, contentHeight))
		}
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render(m.help.View(m.keys)))
	return b.String()
}

func (m *Model) renderHeader() string {
	s := m.top()
	mode := m.bloatBadge()
	left := styleHeader.Render(" pgdu ") + " " + styleMuted.Render(m.target) + " " + mode
	crumbs := m.breadcrumb()
	return left + "    " + crumbs + "\n" + styleMuted.Render(strings.Repeat("─", maxInt(m.width-1, 1))) + "\n" +
		fmt.Sprintf("  sort: %s  ·  %d items  ·  level: %s", sortLabel(s.sort), len(s.items), levelLabel(s.level))
}

func (m *Model) bloatBadge() string {
	// Bloat is only meaningful on the disk tool; suppress the badge elsewhere
	// to keep the header clean.
	top := m.top()
	if top.level == levelTools || top.tool != toolDisk {
		return ""
	}
	if !m.fetchBloat {
		return styleMuted.Render("[bloat off]")
	}
	return styleBadge.Render("[bloat on]")
}

func (m *Model) breadcrumb() string {
	parts := []string{"server"}
	for _, sc := range m.stack {
		switch sc.level {
		case levelTools:
		case levelDatabases:
			parts = append(parts, sc.tool.Name())
		case levelSchemas:
			parts = append(parts, sc.db)
		case levelTables, levelBufferTables:
			parts = append(parts, sc.schema)
		case levelParts:
			parts = append(parts, sc.tableName)
		case levelColumns:
			parts = append(parts, "heap")
		}
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		if i == len(parts)-1 {
			out[i] = styleCrumbActive.Render(p)
		} else {
			out[i] = styleBreadcrumb.Render(p)
		}
	}
	return strings.Join(out, styleBreadcrumb.Render(" ▸ "))
}

func (m *Model) renderToolPicker(s *screen, height int) string {
	var b strings.Builder
	for i, it := range s.items {
		cursor := "  "
		name := it.name
		if i == s.cursor {
			cursor = styleSelected.Render("▶ ")
			name = styleSelected.Render(name)
		}
		b.WriteString(cursor)
		b.WriteString(padRight(name, 20))
		b.WriteString("  ")
		b.WriteString(styleMuted.Render(it.detail))
		b.WriteString("\n")
	}
	for i := len(s.items); i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func (m *Model) renderList(s *screen, height int) string {
	var max int64
	for _, it := range s.items {
		if it.size > max {
			max = it.size
		}
	}
	// Maintain viewport so cursor is visible.
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+height {
		s.offset = s.cursor - height + 1
	}
	end := s.offset + height
	if end > len(s.items) {
		end = len(s.items)
	}
	var b strings.Builder
	for i := s.offset; i < end; i++ {
		it := s.items[i]
		b.WriteString(renderRow(row{
			size: it.size, bloat: it.bloat, maxSize: max,
			name: it.name, detail: it.detail, selected: i == s.cursor,
		}))
		b.WriteString("\n")
	}
	// Pad to fixed height so help line stays put.
	for i := end - s.offset; i < height; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// --- helpers ---

func (m *Model) top() *screen { return m.stack[len(m.stack)-1] }

func (m *Model) findLevel(l level) *screen {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i].level == l {
			return m.stack[i]
		}
	}
	return nil
}

func (m *Model) applySort(s *screen) {
	switch s.sort {
	case sortBySize:
		sort.SliceStable(s.items, func(i, j int) bool {
			if s.items[i].size != s.items[j].size {
				return s.items[i].size > s.items[j].size
			}
			return s.items[i].name < s.items[j].name
		})
	case sortByName:
		sort.SliceStable(s.items, func(i, j int) bool { return s.items[i].name < s.items[j].name })
	}
	if s.cursor >= len(s.items) {
		s.cursor = len(s.items) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func bufferStatToItem(s pg.TableBufferStat) item {
	hr := s.HitRatio()
	var hitStr string
	if hr < 0 {
		hitStr = "no I/O yet"
	} else {
		hitStr = fmt.Sprintf("hit %.1f%%", hr*100)
	}
	cachedPct := ""
	if s.TotalBytes > 0 {
		pct := float64(s.BufferedBytes) / float64(s.TotalBytes) * 100
		cachedPct = fmt.Sprintf(" · cached %.1f%%", pct)
	}
	detail := fmt.Sprintf("%s · table %s%s", hitStr, humanize.Bytes(s.TotalBytes), cachedPct)
	return item{
		name:   s.Schema + "." + s.Name,
		size:   s.BufferedBytes,
		detail: detail,
		data:   s,
	}
}

func columnToItem(col pg.Column) item {
	nullPart := ""
	if col.NullFrac > 0.005 {
		nullPart = fmt.Sprintf(" · %.0f%% null", col.NullFrac*100)
	}
	detail := fmt.Sprintf("%s · avg %s%s", col.Type, humanize.Bytes(int64(col.AvgWidth)), nullPart)
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
		detail = "table heap"
	case pg.PartToast:
		detail = "TOAST storage"
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
		name:   p.Name,
		size:   p.SizeBytes,
		bloat:  p.WastedBytes,
		detail: detail,
		data:   p,
	}
}

func sortLabel(s sortMode) string {
	if s == sortBySize {
		return "size↓"
	}
	return "name"
}

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
