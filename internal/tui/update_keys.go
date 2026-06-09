package tui

import (
	"math"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// reParam matches a pg_stat_statements placeholder ($1, $2, …) in a normalized
// query, used to decide whether a single captured constant maps unambiguously
// to the query's lone parameter.
var reParam = regexp.MustCompile(`\$\d+`)

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// pageStep is the cursor jump distance for PageUp/PageDown. Roughly the
// visible row count: terminal height minus header (3 lines), the inter-block
// blank, and the help row. Always at least 1 so a one-row jump still happens
// on tiny terminals.
func (m *Model) pageStep() int {
	step := m.height - 6
	if step < 1 {
		return 1
	}
	return step
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.top()
	// Any keypress clears the transient notice (e.g. the last export's path) so
	// it reads as a one-shot confirmation rather than lingering state.
	m.notice = ""
	// While the filter input has focus, route keys into the filter editor
	// instead of the list. Bypasses every other binding (so e.g. typing "s"
	// extends the query rather than cycling the sort).
	if s.filterFocused {
		return m.handleFilterKey(s, msg)
	}
	// When a reindex confirmation is armed, capture the next key here: `y`
	// (case-insensitive) executes; anything else cancels. Using y/n instead of
	// a second Enter avoids running REINDEX on an accidental double-tap.
	if s.pendingReindex != "" {
		if msg.String() == "y" || msg.String() == "Y" {
			idx := s.pendingReindex
			s.pendingReindex = ""
			s.reindexing = idx
			s.reindexErr = nil
			return m, m.reindexIndexCmd(s.table, idx)
		}
		s.pendingReindex = ""
		return m, nil
	}
	// Snapshot delete confirmation, same y/n arming as reindex.
	if m.pendingDeleteSnap != "" {
		path := m.pendingDeleteSnap
		m.pendingDeleteSnap = ""
		if msg.String() == "y" || msg.String() == "Y" {
			// A deleted base/end mark would dangle; clear it.
			if s.statMarkBase == path {
				s.statMarkBase = ""
			}
			return m, m.deleteSnapshotCmd(path, m.snapshotDir, s.db)
		}
		return m, nil
	}
	// The column-config overlay is modal: while open (only on the top-queries
	// table) it captures navigation and toggle keys instead of the normal list
	// bindings (Quit still quits).
	if m.showColumnConfig && s.level == levelStatements {
		return m, m.handleColumnConfigKey(s, msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		// On the buffer-tables level the bars carry a lot of semantics that
		// aren't obvious — use ? to toggle a dedicated reference overlay
		// instead of expanding the key list. Other levels keep the standard
		// help-expansion behaviour.
		if s.level == levelBufferTables || s.level == levelHeapPages || s.level == levelHeapTuples ||
			s.level == levelIndexPages || s.level == levelIndexTuples ||
			s.level == levelWAL || s.level == levelWALRecords || s.level == levelWALBlocks ||
			s.level == levelStatements || s.level == levelStatementDetail || s.level == levelSnapshots {
			m.showInfo = !m.showInfo
			break
		}
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Filter):
		s.filterFocused = true
	case key.Matches(msg, m.keys.Down):
		if s.level == levelStatementDetail {
			s.offset++ // clamped to the last screen by scrollWindow
			break
		}
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	case key.Matches(msg, m.keys.Up):
		if s.level == levelStatementDetail {
			s.offset = max(s.offset-1, 0)
			break
		}
		if s.cursor > 0 {
			s.cursor--
		}
	case key.Matches(msg, m.keys.PageDown):
		if s.level == levelStatementDetail {
			s.offset += m.pageStep() // clamped by scrollWindow
			break
		}
		// On levelHeapPages / levelIndexPages PageDown shifts the load
		// window instead of the cursor — within a window the cursor moves
		// with j/k. Clamps to the last full window so we never call
		// get_raw_page past EOF.
		if (s.level == levelHeapPages || s.level == levelIndexPages) && s.heapWindowCount > 0 && s.heapPageCount > s.heapWindowStart+s.heapWindowCount {
			s.heapWindowStart += s.heapWindowCount
			if s.heapWindowStart >= s.heapPageCount {
				s.heapWindowStart = max32(s.heapPageCount-s.heapWindowCount, 0)
			}
			s.cursor = 0
			s.offset = 0
			return m, m.loadCurrent()
		}
		s.cursor = max(min(s.cursor+m.pageStep(), s.visibleLen()-1), 0)
	case key.Matches(msg, m.keys.PageUp):
		if s.level == levelStatementDetail {
			s.offset = max(s.offset-m.pageStep(), 0)
			break
		}
		if (s.level == levelHeapPages || s.level == levelIndexPages) && s.heapWindowStart > 0 {
			s.heapWindowStart = max32(s.heapWindowStart-s.heapWindowCount, 0)
			s.cursor = 0
			s.offset = 0
			return m, m.loadCurrent()
		}
		s.cursor = max(s.cursor-m.pageStep(), 0)
	case key.Matches(msg, m.keys.Top):
		if s.level == levelStatementDetail {
			s.offset = 0
			break
		}
		s.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		if s.level == levelStatementDetail {
			s.offset = math.MaxInt32 // clamped to the last screen by scrollWindow
			break
		}
		s.cursor = max(s.visibleLen()-1, 0)
	case key.Matches(msg, m.keys.Sort):
		m.cycleSort(s)
	case key.Matches(msg, m.keys.ReverseSort):
		s.sortDesc = !s.sortDesc
		m.applySort(s)
	case key.Matches(msg, m.keys.Refresh):
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.ToggleBloat):
		m.fetchBloat = !m.fetchBloat
	case key.Matches(msg, m.keys.Install):
		return m, m.triggerInstall(s)
	case key.Matches(msg, m.keys.Rebaseline):
		// Restart the top-queries window: clear the baseline so the next
		// snapshot becomes the new "since" point. Also drops any loaded disk
		// snapshot (base→now or frozen A→B), returning to the live window.
		if s.level == levelStatements {
			s.statBaseline = nil
			s.statBaseSnap = nil
			s.statEndSnap = nil
			return m, m.loadCurrent()
		}
	case key.Matches(msg, m.keys.SaveSnapshot):
		// Dump the current pg_stat_statements counters to disk. Available from the
		// table and the detail view (both carry s.db).
		if s.level == levelStatements || s.level == levelStatementDetail {
			return m, m.saveSnapshotCmd(s.db)
		}
	case key.Matches(msg, m.keys.Snapshots):
		// Open the on-disk snapshots browser over the top-queries table.
		if s.level == levelStatements {
			next := &screen{level: levelSnapshots, title: "snapshots", tool: s.tool, db: s.db, loading: true}
			m.stack = append(m.stack, next)
			return m, m.loadCurrent()
		}
	case key.Matches(msg, m.keys.Columns):
		// Open the htop-style column picker over the top-queries table.
		if s.level == levelStatements {
			m.ensureStmtColsInit()
			m.showInfo = false // don't stack the picker under the ? reference overlay
			m.showColumnConfig = true
			m.colCfgCursor = 0
		}
	case key.Matches(msg, m.keys.MarkBase):
		// Mark/unmark the highlighted snapshot as the A-base for a frozen A→B diff.
		if s.level == levelSnapshots {
			if meta, ok := s.selectedSnapshot(); ok {
				if s.statMarkBase == meta.Path {
					s.statMarkBase = ""
				} else {
					s.statMarkBase = meta.Path
				}
			}
		}
	case key.Matches(msg, m.keys.DeleteSnapshot):
		// Arm the y/n delete confirmation for the highlighted snapshot.
		if s.level == levelSnapshots {
			if meta, ok := s.selectedSnapshot(); ok {
				m.pendingDeleteSnap = meta.Path
			}
		}
	case key.Matches(msg, m.keys.ToggleRefresh):
		// Pause/resume the live window's auto-refresh. Inert when refresh is
		// disabled by config (--queries-refresh 0) — there's nothing to pause.
		if (s.level == levelStatements || s.level == levelStatementDetail) && m.statRefresh > 0 {
			m.statPaused = !m.statPaused
			// Resuming restarts the self-rescheduling loop if it isn't running.
			if !m.statPaused && !m.statTicking {
				if tick := m.statementsTick(); tick != nil {
					m.statTicking = true
					return m, tick
				}
			}
			return m, nil
		}
	case key.Matches(msg, m.keys.Explain):
		// Re-run the (non-ANALYZE) plan for the detail view's query on demand —
		// real EXPLAIN when a pg_qualstats example is in hand, generic otherwise.
		if s.level == levelStatementDetail && s.statDetail != nil && !s.statExplaining &&
			pg.ExplainableQuery(s.statDetail.Query) {
			s.statExplaining = true
			s.statExplain = ""
			s.statExplainErr = nil
			s.statExplainAnalyze = false
			return m, m.statementPlanCmd(s)
		}
	case key.Matches(msg, m.keys.Params):
		// Browse the real values pg_qualstats captured for this query — only
		// meaningful when pg_qualstats is present (else there's nothing real to
		// show). Pushes levelStatementSamples and loads the captured constants.
		if s.level == levelStatementDetail && s.statDetail != nil && s.statQualstats {
			next := &screen{
				level: levelStatementSamples, title: "values", tool: s.tool,
				db: s.db, statDetail: s.statDetail,
				statSampleCall: s.statSampleCall, statSampleReal: s.statSampleReal,
				statQualstats: s.statQualstats, loading: true,
			}
			m.stack = append(m.stack, next)
			return m, m.loadStatementSamplesCmd(s.db, s.statDetail.QueryID)
		}
	case key.Matches(msg, m.keys.Export):
		// Write the current table/view to pgdu-<tool>-<datetime>.csv. Returns nil
		// (→ a hint) on screens with nothing tabular to export.
		if cmd := m.exportCSVCmd(s); cmd != nil {
			return m, cmd
		}
		m.notice = "nothing to export on this screen"
	case key.Matches(msg, m.keys.Describe):
		// Inert when already on a describe panel so `d` doesn't stack.
		if s.level == levelDescribe {
			break
		}
		t, ok := describeTarget(s)
		if !ok {
			break
		}
		next := &screen{
			level:   levelDescribe,
			title:   "describe",
			tool:    s.tool,
			db:      s.db,
			schema:  s.schema,
			loading: true,
		}
		m.stack = append(m.stack, next)
		if t.isIndex {
			return m, m.loadDescribeIndexCmd(t.db, t.indexOID, t.indexName)
		}
		if t.byName {
			return m, m.loadDescribeTableByNameCmd(t.db, t.tableName)
		}
		next.table = t.table
		return m, m.loadDescribeTableCmd(t.table)
	case key.Matches(msg, m.keys.DiskUsage):
		// From the top-queries views, jump to the main table's disk-usage (parts)
		// breakdown. Only meaningful for name-resolved targets (the statement
		// levels); a no-op elsewhere since those levels are already in the disk
		// tree or have no relation to point at. The relation is parsed/resolved
		// the same way as Describe, so the two stay consistent.
		t, ok := describeTarget(s)
		if !ok || !t.byName {
			break
		}
		// Push a loading parts screen now (spinner while we resolve), then resolve
		// the name; onDiskTableResolved fills in the table and loads its parts.
		next := &screen{
			level: levelParts, title: "disk usage", tool: toolDisk,
			db: t.db, loading: true,
			sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
		}
		m.stack = append(m.stack, next)
		return m, m.resolveDiskTableCmd(t.db, t.tableName)
	case key.Matches(msg, m.keys.Back):
		// Esc is shared with Back; when an overlay/filter is up, Esc closes
		// that instead of unwinding the stack. Other Back keys (←/h/
		// backspace) always navigate back so muscle memory for "go up a
		// level" is preserved.
		if msg.Type == tea.KeyEsc && m.showInfo {
			m.showInfo = false
			break
		}
		if msg.Type == tea.KeyEsc && s.filter != "" {
			s.filter = ""
			s.cursor = 0
			s.offset = 0
			break
		}
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
	case key.Matches(msg, m.keys.Enter):
		if cmd := m.handleReindexEnter(s); cmd != nil {
			return m, cmd
		}
		if s.level == levelStatementDetail {
			// The detail view doesn't drill further; Enter (not l/→) confirms an
			// EXPLAIN ANALYZE run on read-only queries.
			if msg.Type == tea.KeyEnter {
				return m, m.handleStatementAnalyze(s)
			}
			return m, nil
		}
		if s.level == levelStatementSamples {
			// The captured-values list doesn't drill further; Enter runs EXPLAIN
			// ANALYZE for the highlighted real value (read-only queries only).
			if msg.Type == tea.KeyEnter {
				return m, m.handleSampleAnalyze(s)
			}
			return m, nil
		}
		if s.level == levelParts && reindexCandidate(s) != "" {
			// First ENTER on a bloated index row → request confirmation;
			// don't drill (index rows don't drill anyway).
			return m, nil
		}
		return m, m.drillIn()
	}
	return m, nil
}

// handleColumnConfigKey drives the modal column-config overlay (C on the
// top-queries table): Up/Down/Top/Bottom move the cursor over the column
// registry, space/Enter toggle the highlighted column's visibility and rebuild
// the table from the cached window (no DB), and C/esc close it. The mandatory
// query column and columns unavailable under the current track_planning setting
// can't be toggled. Quit still quits.
func (m *Model) handleColumnConfigKey(s *screen, msg tea.KeyMsg) tea.Cmd {
	reg := stmtColumnRegistry()
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Columns), msg.Type == tea.KeyEsc:
		m.showColumnConfig = false
	case key.Matches(msg, m.keys.Up):
		if m.colCfgCursor > 0 {
			m.colCfgCursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.colCfgCursor < len(reg)-1 {
			m.colCfgCursor++
		}
	case key.Matches(msg, m.keys.Top):
		m.colCfgCursor = 0
	case key.Matches(msg, m.keys.Bottom):
		m.colCfgCursor = len(reg) - 1
	case key.Matches(msg, m.keys.Refresh), key.Matches(msg, m.keys.Enter):
		// Refresh is space — the natural htop toggle; Enter also toggles.
		if m.colCfgCursor < 0 || m.colCfgCursor >= len(reg) {
			break
		}
		d := reg[m.colCfgCursor]
		if d.mandatory {
			break
		}
		if d.available != nil && !d.available(stmtCtx{trackPlanning: s.statTrackPlanning}) {
			break // can't show a column that isn't collected (e.g. plan_ms with track_planning off)
		}
		m.ensureStmtColsInit()
		m.stmtColsVisible[d.id] = !m.stmtColEnabled(d.id, d.defaultOn)
		m.rebuildStatementItems(s)
	}
	return nil
}

// handleFilterKey is the input handler while s.filterFocused is true. Esc
// clears + blurs, Enter blurs (keeps the query), Backspace deletes the last
// rune (and blurs if it empties the query), Up/Down navigate the filtered
// list live, and any printable input is appended to the query. Editing the
// query resets cursor/offset so the user always lands on the first match.
func (m *Model) handleFilterKey(s *screen, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		s.filter = ""
		s.filterFocused = false
		s.cursor = 0
		s.offset = 0
	case tea.KeyEnter:
		s.filterFocused = false
		s.clampCursor()
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
			s.cursor = 0
			s.offset = 0
		} else {
			s.filterFocused = false
		}
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
		}
	case tea.KeyDown:
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		if msg.Alt {
			return m, nil
		}
		s.filter += string(msg.Runes)
		s.cursor = 0
		s.offset = 0
	}
	return m, nil
}

// reindexCandidate returns the index name to reindex if the current row on a
// parts screen is an index part with bloat > reindexBloatThreshold. Returns
// "" when the row doesn't qualify (wrong level, wrong kind, bloat unknown or
// below threshold, or another reindex is already in flight on this screen).
func reindexCandidate(s *screen) string {
	if s.level != levelParts || s.reindexing != "" {
		return ""
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return ""
	}
	it := s.items[vis[s.cursor]]
	p, ok := it.data.(pg.Part)
	if !ok || p.Kind != pg.PartIndex {
		return ""
	}
	if !it.hasBloat || it.size <= 0 {
		return ""
	}
	if float64(it.bloat)/float64(it.size) <= reindexBloatThreshold {
		return ""
	}
	return p.Name
}

// handleReindexEnter arms the y/n reindex confirmation when Enter lands on a
// qualifying bloated index row. The execute path lives in handleKey, which
// intercepts the next keystroke. Returns nil when the press isn't part of the
// reindex flow, so the caller can fall through to the normal drill-in.
func (m *Model) handleReindexEnter(s *screen) tea.Cmd {
	if s.level != levelParts {
		return nil
	}
	cand := reindexCandidate(s)
	if cand == "" {
		return nil
	}
	s.pendingReindex = cand
	s.reindexErr = nil
	return nil
}

// handleStatementAnalyze runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) for the
// detail view's query. ANALYZE executes the query for real, so it's gated to
// read-only SELECT shapes (ReadOnlyQuery) and needs the sample call — the
// query with synthesized literals filling its $n — to be ready. Returns nil
// (a no-op) when any of those don't hold.
func (m *Model) handleStatementAnalyze(s *screen) tea.Cmd {
	if s.statDetail == nil || s.statExplaining {
		return nil
	}
	if !pg.ReadOnlyQuery(s.statDetail.Query) || s.statSampleCall == "" {
		return nil
	}
	s.statExplaining = true
	s.statExplain = ""
	s.statExplainErr = nil
	s.statExplainAnalyze = true
	return m.loadStatementExplainAnalyzeCmd(s.db, s.statDetail.Query, s.statSampleCall)
}

// statementPlanCmd issues the right (non-ANALYZE) EXPLAIN for the detail view:
// a plain EXPLAIN on the real example call when one is available (real captured
// values from pg_qualstats), otherwise the generic plan on the normalized query.
// The caller is responsible for setting statExplaining / clearing prior output.
func (m *Model) statementPlanCmd(s *screen) tea.Cmd {
	if s.statSampleReal && s.statSampleCall != "" {
		return m.loadStatementExplainLiteralCmd(s.db, s.statDetail.Query, s.statSampleCall)
	}
	return m.loadStatementExplainCmd(s.db, s.statDetail.Query)
}

// handleSampleAnalyze runs EXPLAIN (ANALYZE, …) for the highlighted captured
// value on the samples level. Reconstruction is only reliable when the
// normalized query has exactly one placeholder — then the captured constant is
// unambiguously that $1, and we substitute it for a true per-value plan. For
// multi-parameter queries we can't map one captured constant to one of several
// placeholders, so we fall back to the representative real example query
// (statSampleCall). Gated to read-only shapes since ANALYZE executes.
func (m *Model) handleSampleAnalyze(s *screen) tea.Cmd {
	if s.statDetail == nil || s.statExplaining || !pg.ReadOnlyQuery(s.statDetail.Query) {
		return nil
	}
	sm, ok := s.selectedSample()
	if !ok {
		return nil
	}
	q := sampleAnalyzeQuery(s.statDetail.Query, s.statSampleCall, sm)
	if q == "" {
		return nil
	}
	s.statExplaining = true
	s.statExplain = ""
	s.statExplainErr = nil
	s.statExplainAnalyze = true
	return m.loadStatementExplainAnalyzeCmd(s.db, s.statDetail.Query, q)
}

// selectedSnapshot resolves the snapshot meta under the cursor on the snapshots
// level, honouring the active filter via visibleIndexes.
func (s *screen) selectedSnapshot() (pg.SnapshotMeta, bool) {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return pg.SnapshotMeta{}, false
	}
	path := s.items[vis[s.cursor]].snapPath
	return metaByPath(s.statSnapMetas, path)
}

// selectedSample resolves the captured value under the cursor on the samples
// level, honouring the active filter via visibleIndexes.
func (s *screen) selectedSample() (pg.QualSample, bool) {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return pg.QualSample{}, false
	}
	sm, ok := s.items[vis[s.cursor]].data.(pg.QualSample)
	return sm, ok
}

// sampleAnalyzeQuery builds the literal query to EXPLAIN ANALYZE for a captured
// value: a clean $1 substitution for single-parameter queries, else the real
// example query. Returns "" when neither is usable.
func sampleAnalyzeQuery(normalized, example string, sm pg.QualSample) string {
	if uniqueParams(normalized) == 1 && sm.ConstValue != "" {
		return strings.ReplaceAll(normalized, "$1", sm.ConstValue)
	}
	return example
}

// uniqueParams counts the distinct $n placeholders in a normalized query.
func uniqueParams(query string) int {
	seen := map[string]struct{}{}
	for _, p := range reParam.FindAllString(query, -1) {
		seen[p] = struct{}{}
	}
	return len(seen)
}

// descTarget holds the resolved target for a describe action.
type descTarget struct {
	isIndex   bool
	byName    bool     // when the relation is known only by name (top-queries view)
	table     pg.Table // when !isIndex && !byName
	db        string   // when isIndex || byName
	tableName string   // when byName — resolved server-side via ResolveTable
	indexOID  uint32   // when isIndex
	indexName string   // when isIndex
}

// describeTarget resolves what `d` should describe given the top screen. It
// reuses the same cursor-resolution guard as drillIn (visibleIndexes bounds
// check). Returns (descTarget{}, false) when the current level or row is not
// describable (e.g. tools/databases/schemas, heap/toast rows, non-btree index).
func describeTarget(s *screen) (descTarget, bool) {
	// Helper: resolve the item under the cursor (same as drillIn).
	curItem := func() (item, bool) {
		vis := s.visibleIndexes()
		if s.cursor < 0 || s.cursor >= len(vis) {
			return item{}, false
		}
		return s.items[vis[s.cursor]], true
	}

	switch s.level {
	case levelStatements:
		// item.name is the flattened statement text; parse out its main table and
		// describe it by name (resolved server-side, since we have no OID here).
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		name := pg.MainTable(it.name)
		if name == "" {
			return descTarget{}, false
		}
		return descTarget{byName: true, db: s.db, tableName: name}, true

	case levelStatementDetail, levelStatementSamples:
		if s.statDetail == nil {
			return descTarget{}, false
		}
		name := pg.MainTable(s.statDetail.Query)
		if name == "" {
			return descTarget{}, false
		}
		return descTarget{byName: true, db: s.db, tableName: name}, true

	case levelTables:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		t, ok := it.data.(pg.Table)
		if !ok {
			return descTarget{}, false
		}
		return descTarget{table: t}, true

	case levelBufferTables:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		st, ok := it.data.(pg.TableBufferStat)
		if !ok {
			return descTarget{}, false
		}
		// TableBufferStat has no pg.Table field; reconstruct from its own fields.
		return descTarget{table: pg.Table{
			DB: st.DB, Schema: st.Schema, Name: st.Name,
			OID: st.OID, TotalBytes: st.TotalBytes,
		}}, true

	case levelColumns:
		// The table being described is always s.table at these levels.
		return descTarget{table: s.table}, true

	case levelParts:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		p, ok := it.data.(pg.Part)
		if !ok {
			return descTarget{}, false
		}
		if p.Kind == pg.PartIndex {
			return descTarget{
				isIndex:   true,
				db:        s.db,
				indexOID:  p.OID,
				indexName: p.Name,
			}, true
		}
		// Heap or toast row: describe the table.
		return descTarget{table: s.table}, true

	case levelRelations:
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		r, ok := it.data.(pg.Relation)
		if !ok {
			return descTarget{}, false
		}
		switch r.Kind {
		case pg.RelTable, pg.RelToast:
			return descTarget{table: pg.Table{
				DB: r.DB, Schema: r.Schema, OID: r.OID, Name: r.Name,
				TotalBytes: r.SizeBytes, EstRows: r.EstRows,
			}}, true
		case pg.RelBTreeIndex:
			return descTarget{
				isIndex:   true,
				db:        r.DB,
				indexOID:  r.OID,
				indexName: r.Qualified(),
			}, true
		}
		return descTarget{}, false

	case levelHeapPages, levelHeapTuples, levelTupleRow:
		return descTarget{table: s.table}, true

	case levelIndexPages, levelIndexTuples:
		return descTarget{
			isIndex:   true,
			db:        s.db,
			indexOID:  s.index.OID,
			indexName: s.index.Qualified(),
		}, true
	}

	return descTarget{}, false
}

// triggerInstall is a no-op unless the current screen has an extPrompt
// that's still installable. Sets `installing` so the view can show a
// spinner, and dispatches CREATE EXTENSION — or, for the outdated-extension
// (upgrade) variant, ALTER EXTENSION ... UPDATE.
func (m *Model) triggerInstall(s *screen) tea.Cmd {
	if s.extPrompt == nil || !s.extPrompt.installable || s.installing {
		return nil
	}
	s.installing = true
	s.extPrompt.err = nil
	if s.extPrompt.upgrade {
		return m.upgradeExtensionCmd(s.extPrompt.db, s.extPrompt.name)
	}
	return m.installExtensionCmd(s.extPrompt.db, s.extPrompt.name)
}
