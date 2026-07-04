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

// handleInfoKey drives the modal ? reference overlay: scroll keys move
// infoOffset (clamped on render by scrollWindow), Help/Back/Quit close it (Quit
// still quits), and everything else is swallowed so the hidden list stays put.
func (m *Model) handleInfoKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Help), key.Matches(msg, m.keys.Back):
		m.showInfo = false
	case key.Matches(msg, m.keys.Up):
		m.infoOffset = max(m.infoOffset-1, 0)
	case key.Matches(msg, m.keys.Down):
		m.infoOffset++ // clamped by scrollWindow
	case key.Matches(msg, m.keys.PageUp):
		m.infoOffset = max(m.infoOffset-m.pageStep(), 0)
	case key.Matches(msg, m.keys.PageDown):
		m.infoOffset += m.pageStep() // clamped by scrollWindow
	case key.Matches(msg, m.keys.Top):
		m.infoOffset = 0
	case key.Matches(msg, m.keys.Bottom):
		m.infoOffset = math.MaxInt32 // clamped by scrollWindow
	}
	return nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.top()
	// Scope tool/level-specific bindings to the current screen so a disabled
	// binding never matches here (key.Matches honours Enabled), letting other
	// tools reuse the same physical key.
	m.keys.applyContext(s)
	// Any keypress clears the transient notice (e.g. the last export's path) so
	// it reads as a one-shot confirmation rather than lingering state.
	m.notice = ""
	// While the filter input has focus, route keys into the filter editor
	// instead of the list. Bypasses every other binding (so e.g. typing "s"
	// extends the query rather than cycling the sort).
	if s.filterFocused {
		return m.handleFilterKey(s, msg)
	}
	// While the seek input has focus, route keys into the seek editor (typing
	// builds the key value and live-jumps the cursor) instead of the list.
	if s.seekFocused {
		return m.handleSeekKey(s, msg)
	}
	// When a reindex confirmation is armed, capture the next key here: `y`
	// (case-insensitive) executes; anything else cancels. Using y/n instead of
	// a second Enter avoids running REINDEX on an accidental double-tap.
	if s.pendingReindex != "" {
		if msg.String() == "y" || msg.String() == "Y" {
			idx := s.pendingReindex
			s.pendingReindex = ""
			s.reindexing = idx
			s.reindexProg = nil
			s.reindexErr = nil
			// Run the (blocking) REINDEX and, alongside it, start polling
			// pg_stat_progress_create_index so the banner shows a live bar.
			return m, tea.Batch(m.reindexIndexCmd(s.table, idx), m.reindexTick())
		}
		s.pendingReindex = ""
		return m, nil
	}
	// VACUUM confirmation on levelParts: `v` armed it, y/Y executes, any other key cancels.
	if s.pendingVacuum {
		s.pendingVacuum = false
		if msg.String() == "y" || msg.String() == "Y" {
			return m, m.vacuumTableCmd(s.table)
		}
		return m, nil
	}
	// Maintenance stats reset confirmation — same y/n pattern as reindex.
	if s.pendingReset != "" {
		which := s.pendingReset
		s.pendingReset = ""
		if msg.String() == "y" || msg.String() == "Y" {
			switch which {
			case "statements":
				return m, m.resetStatementsCmd(s.db)
			case "qualstats":
				return m, m.resetQualstatsCmd(s.db)
			case "tablestats":
				return m, m.resetTableStatsCmd(s.db)
			}
		}
		return m, nil
	}
	// Snapshot delete confirmation, same y/n arming as reindex.
	if m.pendingDeleteSnap != "" {
		path := m.pendingDeleteSnap
		m.pendingDeleteSnap = ""
		if msg.String() == "y" || msg.String() == "Y" {
			return m, m.deleteSnapshotCmd(path, m.snapshotDir, s.db)
		}
		return m, nil
	}
	// Backend cancel/terminate confirmation — same y/n pattern as reindex.
	if s.pendingBackendAction != "" {
		action := s.pendingBackendAction
		pid := s.pendingBackendPID
		s.pendingBackendAction = ""
		s.pendingBackendPID = 0
		if msg.String() == "y" || msg.String() == "Y" {
			switch action {
			case "cancel":
				return m, m.cancelBackendCmd(s.db, pid)
			case "terminate":
				return m, m.terminateBackendCmd(s.db, pid)
			}
		}
		return m, nil
	}
	// The activity column-config overlay is modal — mirrors showColumnConfig for
	// the top-queries table.
	if m.showActColumnConfig && s.level == levelActivity {
		return m, m.handleActColumnConfigKey(s, msg)
	}
	// The column-config overlay is modal: while open (only on the top-queries
	// table) it captures navigation and toggle keys instead of the normal list
	// bindings (Quit still quits).
	if m.showColumnConfig && s.level == levelStatements {
		return m, m.handleColumnConfigKey(s, msg)
	}
	// Table overview column-config overlay — mirrors the two above.
	if m.showTblColumnConfig && s.level == levelTableStats {
		return m, m.handleTblColumnConfigKey(s, msg)
	}
	// Diagnostic-result column-config overlay — same modal pattern, but over the
	// result's dynamic column set instead of a static registry.
	if m.showDiagColumnConfig && s.level == levelDiagnosticResult {
		return m, m.handleDiagColumnConfigKey(s, msg)
	}
	// The SQL overlay (s on a diagnostic result) is modal: any key dismisses it
	// so the underlying table bindings don't fire while it's up. Quit still quits.
	if m.showDiagQuery {
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}
		m.showDiagQuery = false
		return m, nil
	}
	// The ? reference overlay is modal: while it's up, scroll keys move it and
	// the close keys dismiss it; nothing else fires, so the list hidden beneath
	// it never moves. Closing (Help/Back) is handled inside handleInfoKey.
	if m.showInfo && m.hasInfoOverlay(s) {
		return m, m.handleInfoKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		// On the buffer-tables level the bars carry a lot of semantics that
		// aren't obvious — use ? to toggle a dedicated reference overlay
		// instead of expanding the key list. Other levels keep the standard
		// help-expansion behaviour.
		if s.level == levelBufferTables || s.level == levelBufferDetail || s.level == levelShmem ||
			s.level == levelHeapPages || s.level == levelHeapTuples ||
			s.level == levelIndexPages || s.level == levelIndexTuples ||
			s.level == levelWAL || s.level == levelWALRecords || s.level == levelWALBlocks ||
			s.level == levelWALRelations || s.level == levelWALRelBlocks ||
			s.level == levelStatements || s.level == levelStatementDetail || s.level == levelSnapshots ||
			s.level == levelMaintenance || s.level == levelSettings ||
			s.level == levelActivity || s.level == levelTableStats || s.level == levelWaitProfile {
			m.showInfo = !m.showInfo
			if m.showInfo {
				m.infoOffset = 0 // always open scrolled to the top
			}
			break
		}
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Filter):
		s.filterFocused = true
	case key.Matches(msg, m.keys.Seek):
		// Seek is enabled only on levelIndexTuples (see applyContext). Open the
		// input fresh; mutually exclusive with the fuzzy filter.
		s.seekFocused = true
		s.seekQuery = ""
		s.seekStatus = ""
		s.filterFocused = false
	case key.Matches(msg, m.keys.Down):
		if s.level == levelStatementDetail {
			s.offset++ // clamped to the last screen by scrollWindow
			break
		}
		if s.level == levelMaintenance {
			// ↑↓ moves the capacity cursor (3 rows: statements, qualstats, table stats).
			s.maintCursor = min(s.maintCursor+1, 2)
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
		if s.level == levelMaintenance {
			s.maintCursor = max(s.maintCursor-1, 0)
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
		if s.level == levelMaintenance {
			s.offset += m.pageStep()
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
			s.resetCursor()
			return m, m.loadCurrent()
		}
		s.cursor = max(min(s.cursor+m.pageStep(), s.visibleLen()-1), 0)
	case key.Matches(msg, m.keys.PageUp):
		if s.level == levelStatementDetail {
			s.offset = max(s.offset-m.pageStep(), 0)
			break
		}
		if s.level == levelMaintenance {
			s.offset = max(s.offset-m.pageStep(), 0)
			break
		}
		if (s.level == levelHeapPages || s.level == levelIndexPages) && s.heapWindowStart > 0 {
			s.heapWindowStart = max32(s.heapWindowStart-s.heapWindowCount, 0)
			s.resetCursor()
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
	case key.Matches(msg, m.keys.ShowQuery):
		// Pop up the executed SQL for the current diagnostic so it can be
		// selected/copied. Enabled only on levelDiagnosticResult (applyContext);
		// sort cycling lives on the ←/→ arrows, so the two no longer share a key.
		if s.diag != nil {
			m.showDiagQuery = true
		}
	case key.Matches(msg, m.keys.JumpActivity):
		// System-overview cross-link: open the live Activity tool for the detail
		// behind the dashboard's connection/blocked/long-xact figures.
		m.stack = append(m.stack, m.toolEntryScreen(toolActivity))
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.JumpWAL):
		m.stack = append(m.stack, m.toolEntryScreen(toolWAL))
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.JumpReplication):
		// Open the replication-slots diagnostic against the default database.
		for i := range pg.Diagnostics {
			if pg.Diagnostics[i].Key == "replication_slots" {
				m.stack = append(m.stack, diagnosticResultScreen(&pg.Diagnostics[i], "", false))
				return m, m.loadCurrent()
			}
		}
	case key.Matches(msg, m.keys.WaitProfile):
		// Open the wait-event profile over the activity sample stream. No load
		// Cmd: it renders from Model.waitRing, which the activity tick keeps
		// feeding while the profile is on top (see onActivityTick).
		if s.level == levelActivity {
			next := &screen{
				level: levelWaitProfile, title: "wait profile", tool: toolActivity,
				db: s.db, loaded: true,
			}
			m.stack = append(m.stack, next)
			return m, nil
		}
	case key.Matches(msg, m.keys.Progress):
		// Open the live progress monitor over the pg_stat_progress_* views.
		// Enabled only on levelMaintenance, so it never shadows Params.
		next := &screen{
			level: levelProgress, title: "progress", tool: toolMaintenance,
			db: s.db, loading: true,
		}
		m.stack = append(m.stack, next)
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.SortNext):
		m.cycleSort(s, +1)
	case key.Matches(msg, m.keys.SortPrev):
		m.cycleSort(s, -1)
	case key.Matches(msg, m.keys.ReverseSort):
		s.sortDesc = !s.sortDesc
		m.applySort(s)
	case key.Matches(msg, m.keys.Refresh):
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.LockTree):
		// Open the blocking-chain tree over the activity table. Matched before
		// ToggleBloat since both use "b"; LockTree is enabled only on levelActivity.
		if s.level == levelActivity {
			next := &screen{
				level: levelLockTree, title: "lock tree", tool: toolActivity,
				db: s.db, loading: true,
			}
			m.stack = append(m.stack, next)
			return m, m.loadCurrent()
		}
	case key.Matches(msg, m.keys.ToggleBloat):
		m.fetchBloat = !m.fetchBloat
	case key.Matches(msg, m.keys.Install):
		return m, m.triggerInstall(s)
	case key.Matches(msg, m.keys.Rebaseline):
		// Restart the top-queries window: clear the baseline so the next
		// snapshot becomes the new "since" point. Also drops any loaded disk
		// snapshot (base→now or frozen A→B) and the cumulative flag.
		if s.level == levelStatements {
			s.statBaseline = nil
			s.statBaseSnap = nil
			s.statEndSnap = nil
			s.statCumulative = false
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
	case key.Matches(msg, m.keys.DeleteSnapshot):
		// Arm the y/n delete confirmation for the highlighted snapshot.
		if s.level == levelSnapshots {
			if meta, ok := s.selectedSnapshot(); ok {
				m.pendingDeleteSnap = meta.Path
			}
		}
	case key.Matches(msg, m.keys.ActivityFilter):
		if s.level == levelActivity {
			s.actFilter = s.actFilter.Next()
			// Reload immediately with the new filter.
			return m, m.loadActivityCmd(s.db, s.actFilter)
		}
		if s.level == levelDiagnostics {
			// Cycle the category filter (all → index → table → …) client-side.
			cats := diagCategories()
			next := ""
			for i, c := range cats {
				if c == s.diagCatFilter && i+1 < len(cats) {
					next = cats[i+1]
					break
				}
			}
			if s.diagCatFilter == "" && len(cats) > 0 {
				next = cats[0]
			}
			s.diagCatFilter = next
			s.items = diagnosticItems(s.diagCatFilter)
			s.itemsRev++
			s.resetCursor()
		}
	case key.Matches(msg, m.keys.CancelBackend):
		// Arm the two-step confirmation for pg_cancel_backend, from the activity
		// table or the lock tree.
		if pid := backendActionPID(s); pid != 0 {
			s.pendingBackendPID = pid
			s.pendingBackendAction = "cancel"
		}
	case key.Matches(msg, m.keys.TerminateBackend):
		if pid := backendActionPID(s); pid != 0 {
			s.pendingBackendPID = pid
			s.pendingBackendAction = "terminate"
		}
	case key.Matches(msg, m.keys.Columns):
		// Open the htop-style column picker — on the top-queries table or on
		// the activity table.
		if s.level == levelStatements {
			m.ensureStmtColsInit()
			m.showInfo = false
			m.showColumnConfig = true
			m.colCfgCursor = 0
		}
		if s.level == levelActivity {
			m.ensureActColsInit()
			m.showInfo = false
			m.showActColumnConfig = true
			m.actColCfgCursor = 0
		}
		if s.level == levelTableStats {
			m.ensureTblColsInit()
			m.showInfo = false
			m.showTblColumnConfig = true
			m.tblColCfgCursor = 0
		}
		if s.level == levelDiagnosticResult && s.diagResult != nil {
			m.showInfo = false
			m.showDiagColumnConfig = true
			m.diagColCfgCursor = 0
		}
	case key.Matches(msg, m.keys.ToggleRefresh):
		// Cycle the live window's auto-refresh cadence (activity: 500ms → 1s → 2s → 5s → 10s → off).
		if s.level == levelStatements || s.level == levelStatementDetail {
			m.cycleStatRefresh()
			// Cycling back on restarts the self-rescheduling loop if it stopped.
			if m.statRefresh > 0 && !m.statTicking {
				if tick := m.statementsTick(); tick != nil {
					m.statTicking = true
					return m, tick
				}
			}
			return m, nil
		}
		if s.level == levelActivity || s.level == levelProgress {
			m.cycleActivityRefresh()
			if m.activityRefresh > 0 && !m.activityTicking {
				if tick := m.activityTick(); tick != nil {
					m.activityTicking = true
					return m, tick
				}
			}
			return m, nil
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
	case key.Matches(msg, m.keys.Execute):
		// Execute the detail view's query for real and show its rows as a table.
		// Gated exactly like the EXPLAIN ANALYZE affordance (handleStatementAnalyze):
		// read-only statements only, and only once a literal sample call exists to
		// actually run (the normalized query has unbindable $n placeholders).
		if s.level == levelStatementDetail && s.statDetail != nil &&
			pg.ReadOnlyQuery(s.statDetail.Query) && s.statSampleCall != "" {
			next := &screen{
				level: levelStatementResult, title: "result", tool: s.tool,
				db: s.db, statDetail: s.statDetail, diagBarCol: -1, loading: true,
			}
			m.stack = append(m.stack, next)
			return m, m.loadStatementResultCmd(s.db, s.statDetail.Query, s.statSampleCall)
		}
	case key.Matches(msg, m.keys.Verbose):
		// Toggle the verbose detail view (parameter table + extra metric rows).
		// Scoped to the detail level; the body re-renders in place and reflows
		// through scrollWindow, so no offset reset is needed.
		if s.level == levelStatementDetail {
			s.statVerbose = !s.statVerbose
		}
		// On the parts level, `v` arms the VACUUM VERBOSE confirmation.
		if s.level == levelParts && s.table.OID != 0 {
			if m.vacuum.running {
				// A vacuum is already running; ignore.
				break
			}
			s.pendingVacuum = true
		}
		// On the activity table, `v` toggles visibility of evergreen auxiliary
		// backends (walwriter, checkpointer, launchers, io workers, …). The rebuild
		// uses the cached actRows so no DB round-trip is needed.
		if s.level == levelActivity {
			s.actVerbose = !s.actVerbose
			m.rebuildActivityItems(s)
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
		if t.indexByName {
			return m, m.loadDescribeIndexByNameCmd(t.db, t.indexName)
		}
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
	case key.Matches(msg, m.keys.WALByRelation):
		// From the rmgr overview, open the same window grouped by relation —
		// "what caused the change". Gated to levelWAL via applyContext; also
		// require the window to be resolved and no blocking prompt up.
		if s.level == levelWAL && s.loaded && (s.extPrompt == nil || !s.extPrompt.blocking) {
			next := &screen{
				level: levelWALRelations, title: "wal relations", tool: s.tool,
				db: s.db, walStart: s.walStart, walEnd: s.walEnd,
				sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
			}
			m.stack = append(m.stack, next)
			return m, m.loadCurrent()
		}
	case key.Matches(msg, m.keys.ShmemMap):
		// From the buffer-tables list, open the whole shared-memory map. Gated to
		// levelBufferTables via applyContext; require no blocking extension prompt.
		if s.level == levelBufferTables && (s.extPrompt == nil || !s.extPrompt.blocking) {
			next := &screen{
				level: levelShmem, title: "shmem", tool: toolBuffers, db: s.db,
				sort: sortBySize, sortDesc: sortBySize.defaultDesc(),
			}
			m.stack = append(m.stack, next)
			return m, m.loadCurrent()
		}
	case key.Matches(msg, m.keys.Back):
		// Esc is shared with Back; when an overlay/filter is up, Esc closes
		// that instead of unwinding the stack. `q` always navigates back so
		// muscle memory for "go up a level" is preserved.
		if msg.Type == tea.KeyEsc && m.showInfo {
			m.showInfo = false
			break
		}
		if msg.Type == tea.KeyEsc && s.filter != "" {
			s.filter = ""
			s.resetCursor()
			break
		}
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
	case key.Matches(msg, m.keys.Enter):
		if cmd := m.handleReindexEnter(s); cmd != nil {
			return m, cmd
		}
		if s.level == levelMaintenance {
			// Arm the reset confirmation for the highlighted extension capacity row.
			switch s.maintCursor {
			case 0:
				s.pendingReset = "statements"
			case 1:
				s.pendingReset = "qualstats"
			case 2:
				s.pendingReset = "tablestats"
			}
			return m, nil
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

// descTarget holds the resolved target for a describe action.
type descTarget struct {
	isIndex     bool
	byName      bool     // when the relation is known only by name (top-queries view)
	indexByName bool     // when only an index name is known (diagnostic result) — resolved via ResolveIndex
	table       pg.Table // when !isIndex && !byName
	db          string   // when isIndex || byName || indexByName
	tableName   string   // when byName — resolved server-side via ResolveTable
	indexOID    uint32   // when isIndex
	indexName   string   // when isIndex or indexByName
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

	case levelProgress:
		// Describe the operation's target relation by name (basebackup rows have
		// none). The name comes from regclass in the operation's own database, so
		// resolve it against the screen's db like the other by-name paths.
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		r, ok := it.data.(pg.ProgressRow)
		if !ok || r.Relation == "" {
			return descTarget{}, false
		}
		return descTarget{byName: true, db: s.db, tableName: r.Relation}, true

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

	case levelBufferDetail:
		// The inspected table is carried on the screen; same reconstruction as
		// the buffer-tables list row.
		if s.bufDetail == nil {
			return descTarget{}, false
		}
		st := s.bufDetail
		return descTarget{table: pg.Table{
			DB: st.DB, Schema: st.Schema, Name: st.Name,
			OID: st.OID, TotalBytes: st.TotalBytes,
		}}, true

	case levelTableStats:
		// Generic-table rows carry the relation OID in statQueryID; resolve it
		// back to the loaded TableStat and describe by exact OID (no name lookup).
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		for i := range s.tblRows {
			if int64(s.tblRows[i].OID) == it.statQueryID {
				return descTarget{table: s.tblRows[i].AsTable()}, true
			}
		}
		return descTarget{}, false

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
		case pg.RelBTreeIndex, pg.RelGist, pg.RelBrin, pg.RelGin:
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

	case levelActivity:
		// Describe the main table of the highlighted backend's query, in that
		// backend's database (which may differ from the screen's connection).
		pid := activitySelectedPID(s)
		if pid == 0 {
			return descTarget{}, false
		}
		for i := range s.actRows {
			if s.actRows[i].PID != pid {
				continue
			}
			name := pg.MainTable(s.actRows[i].Query)
			if name == "" {
				return descTarget{}, false
			}
			db := s.actRows[i].Database
			if db == "" {
				db = s.db
			}
			return descTarget{byName: true, db: db, tableName: name}, true
		}
		return descTarget{}, false

	case levelDiagnosticResult:
		// Generic diagnostic rows carry no pg.Table — resolve the relation by
		// the name in the row (server-side, like the top-queries view). Only
		// the table-shaped diagnostics expose a name column; the rest return
		// false here and `d` is a no-op.
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		cells, ok := it.data.([]pg.DiagCell)
		if !ok {
			return descTarget{}, false
		}
		// Prefer a table column; fall back to an index column (index-only
		// diagnostics: unused/duplicate/redundant/index-I/O), resolved via
		// ResolveIndex into a DescribeIndex panel.
		if name, ok := diagDescribeName(s.diagCols, cells); ok {
			return descTarget{byName: true, db: s.db, tableName: name}, true
		}
		if name, ok := diagDescribeIndexName(s.diagCols, cells); ok {
			return descTarget{indexByName: true, db: s.db, indexName: name}, true
		}
		return descTarget{}, false
	}

	return descTarget{}, false
}

// diagDescribeName extracts a relation name from a diagnostic result row so `d`
// can describe it. It finds a table-name column by the naming conventions the
// diagnostic SQL uses (relname / table / table_name / tablename), qualifying it
// with a sibling schema column when present and the value isn't already
// schema-qualified (regclass columns arrive pre-qualified, e.g. "public.foo").
// Returns ("", false) for diagnostics with no relation column — connections,
// database sizes, WAL, sequences, settings — so `d` stays inert there.
func diagDescribeName(cols []pg.DiagColumn, cells []pg.DiagCell) (string, bool) {
	tableIdx, schemaIdx := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c.Name) {
		case "relname", "table", "table_name", "tablename", "main_table_name":
			if tableIdx == -1 {
				tableIdx = i
			}
		case "schema", "schemaname", "schema_name", "table_schema":
			if schemaIdx == -1 {
				schemaIdx = i
			}
		}
	}
	if tableIdx < 0 || tableIdx >= len(cells) {
		return "", false
	}
	table := strings.TrimSpace(cells[tableIdx].Display)
	if table == "" || table == "—" {
		return "", false
	}
	// regclass-derived values are already schema-qualified and correctly quoted;
	// pass them straight through to to_regclass.
	if strings.Contains(table, ".") {
		return table, true
	}
	if schemaIdx >= 0 && schemaIdx < len(cells) {
		if schema := strings.TrimSpace(cells[schemaIdx].Display); schema != "" && schema != "—" {
			return quoteDiagIdent(schema) + "." + quoteDiagIdent(table), true
		}
	}
	// Quote even the bare name so a mixed-case relname survives to_regclass.
	return quoteDiagIdent(table), true
}

// diagDescribeIndexName extracts an index name from a diagnostic result row (the
// index-only diagnostics: unused / duplicate / redundant-prefix / index I/O).
// Qualified with a sibling schema column when present, the same way
// diagDescribeName handles tables. Returns ("", false) when no index column
// exists, so table describe stays the default.
func diagDescribeIndexName(cols []pg.DiagColumn, cells []pg.DiagCell) (string, bool) {
	idxIdx, schemaIdx := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c.Name) {
		case "index", "index_name", "indexname", "indexrelname", "redundant_index":
			if idxIdx == -1 {
				idxIdx = i
			}
		case "schema", "schemaname", "schema_name", "table_schema":
			if schemaIdx == -1 {
				schemaIdx = i
			}
		}
	}
	if idxIdx < 0 || idxIdx >= len(cells) {
		return "", false
	}
	name := strings.TrimSpace(cells[idxIdx].Display)
	if name == "" || name == "—" {
		return "", false
	}
	if strings.Contains(name, ".") {
		return name, true
	}
	if schemaIdx >= 0 && schemaIdx < len(cells) {
		if schema := strings.TrimSpace(cells[schemaIdx].Display); schema != "" && schema != "—" {
			return quoteDiagIdent(schema) + "." + quoteDiagIdent(name), true
		}
	}
	return quoteDiagIdent(name), true
}

// quoteDiagIdent double-quotes a single SQL identifier so it round-trips through
// to_regclass unchanged regardless of case or special characters.
func quoteDiagIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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

// backendActionPID resolves the PID that k/x should act on for the current
// screen: the activity table's selected row or the lock tree's selected node.
// Returns 0 on any other level or when nothing is selected.
func backendActionPID(s *screen) int32 {
	switch s.level {
	case levelActivity:
		return activitySelectedPID(s)
	case levelLockTree:
		return lockTreeSelectedPID(s)
	}
	return 0
}

// activitySelectedPID returns the PID of the currently highlighted backend in
// the Activity table, or 0 when no row is selected or the data doesn't carry a
// PID. item.statQueryID reuses the queryid field; the PID lives in the first
// DiagCell of the row (the "pid" column, actColPID, which is always first in
// the registry and always enabled since it's rendered first).
func activitySelectedPID(s *screen) int32 {
	if s.level != levelActivity || len(s.items) == 0 {
		return 0
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return 0
	}
	it := s.items[vis[s.cursor]]
	cells, ok := it.data.([]pg.DiagCell)
	if !ok || len(cells) == 0 {
		return 0
	}
	// The pid column is the first one in actColumnRegistry and is always
	// visible (defaultOn=true), but we can't assume index 0 = pid without
	// checking the column config. Look for a numeric cell whose Num fits
	// int32 range — since PID is the only DiagInt column with HasNum=true
	// that's always first. Simpler: match by screen.actRows by QueryID
	// stored in statQueryID, which may be 0, so fall back to parsing the
	// pid column display value.
	if !cells[0].HasNum {
		return 0
	}
	return int32(cells[0].Num)
}
