package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	Top, Bottom      key.Binding
	Enter, Back      key.Binding
	SortNext         key.Binding
	SortPrev         key.Binding
	ShowQuery        key.Binding
	ReverseSort      key.Binding
	Refresh          key.Binding
	Install          key.Binding
	Describe         key.Binding
	DiskUsage        key.Binding
	Rebaseline       key.Binding
	ToggleRefresh    key.Binding
	Params           key.Binding
	Execute          key.Binding
	Verbose          key.Binding
	Export           key.Binding
	SaveSnapshot     key.Binding
	Snapshots        key.Binding
	DeleteSnapshot   key.Binding
	Columns          key.Binding
	ResetCols        key.Binding // r: reset column visibility to defaults (inside the C picker)
	StmtView         key.Binding // tab (top queries): queries → by table → by type
	Filter           key.Binding
	Seek             key.Binding
	// GinNextLeaf jumps the GIN page list to the next posting-tree leaf page,
	// found server-side across the whole index (only those pages itemize).
	GinNextLeaf key.Binding
	Help        key.Binding
	Quit        key.Binding

	// Activity-tool-specific bindings.
	ActivityFilter   key.Binding // f: cycle backend filter mode
	CancelBackend    key.Binding // k: send pg_cancel_backend (SIGINT)
	TerminateBackend key.Binding // x: send pg_terminate_backend (SIGTERM)
	LockTree         key.Binding // b: open the blocking-chain lock tree

	// Shared-buffers-tool binding.
	ShmemMap key.Binding // m: open the shared-memory map (pg_shmem_allocations)

	// Disk-tool binding: jump from a heap/index/toast row on the parts level
	// straight into that object's page-inspector view.
	PageInspect key.Binding // p: open the page inspector for the selected part

	// Describe-panel binding: open the top-queries tool for the table's database
	// with the filter preset to the table name, so "who touches this table" is
	// one key away from its definition. The physical key is ToggleRefresh on the
	// live/queries levels, which are all off on levelDescribe.
	TopQueries key.Binding // t: → top queries, filtered to the described table
	// topQueriesInFooter advertises t on the describe panel, its only home.
	topQueriesInFooter bool

	// System-overview cross-links: jump from the maintenance dashboard into the
	// live tools that show the detail behind a summary row. Enabled only on
	// levelMaintenance (so they don't clash with r/reverse-sort and the log
	// analyzer's w/window elsewhere) and dispatched before those cases.
	JumpActivity    key.Binding // a: open the Activity tool
	JumpWAL         key.Binding // w: open the WAL inspector
	JumpReplication key.Binding // r: open the replication-slots diagnostic
	JumpIO          key.Binding // o: open the pg_stat_io diagnostic
	Progress        key.Binding // p: open the live progress monitor
	Settings        key.Binding // s: open the pg_settings browser

	// Wait-event profiler over the Activity tool's sample stream.
	WaitProfile key.Binding // W: open the wait-event profile

	// Log-analyzer bindings (levelLogs).
	LogGroupMode key.Binding // m: cycle section mode (category / severity / flat)
	LogPane      key.Binding // tab: groups → timeline → slow → groups
	LogWindow    key.Binding // w: widen the tail window
	LogJump      key.Binding // j: jump to this line in the chronological timeline
	LogParams    key.Binding // tab (group screen): entries → by parameters → by $1

	// Log-analyzer cross-link: the pgbouncer instance's logfile, or the current
	// server log — narrowed per level (logLinkFor).
	OpenLog key.Binding // l: → log analyzer

	// openLogInFooter advertises l where it is enabled, except on the system
	// overview whose header line lists it with the other jump keys.
	openLogInFooter bool

	// logJumpInFooter advertises j on the entry and group-rows levels.
	logJumpInFooter bool
	// logParamsInFooter advertises tab (parameter grouping) on the group screen.
	logParamsInFooter bool

	// logInFooter adds the log analyzer's cluster (tab/v/f/m/w/o) to the footer;
	// nothing else advertises those keys.
	logInFooter bool

	// waitProfileInFooter adds the W hint to the footer on the activity table.
	waitProfileInFooter bool

	// shmemInFooter adds the m (memory map) hint to the footer's short help on
	// the buffer-tables level, where it's the only advertisement for the view.
	shmemInFooter bool

	// pageInspectInFooter adds the p (pages) hint on the parts, buffer and table-describe levels, the only
	// place that advertises the cross-tool jump.
	pageInspectInFooter bool

	// columnsInFooter adds the C (configure columns) hint to the footer's short
	// help. Set per-screen by applyContext: the activity table has no other
	// advertisement for the picker, whereas the top-queries table already shows
	// "C columns" in its header, so it stays out of the footer there.
	columnsInFooter bool

	// showQueryInFooter adds the s (show SQL) hint to the footer on the
	// diagnostics list and a diagnostic result — the only advertisement for the
	// copy-the-SQL overlay.
	showQueryInFooter bool

	// toggleRefreshInFooter adds the t (refresh cadence) hint to the footer on
	// the live monitors (activity/progress), where the header shows the current
	// cadence but nothing advertises that t cycles it — including down to "off"
	// to freeze the view and read a query.
	toggleRefreshInFooter bool

	// lockTreeInFooter adds the b (lock tree) hint to the footer on the activity
	// table, its only advertisement for the blocking-chains view.
	lockTreeInFooter bool

	// progressInFooter adds the p (progress monitor) hint to the footer on the
	// activity table.
	progressInFooter bool

	// describeInFooter adds the d (describe) hint to the footer on the progress
	// monitor, where describing the operation's target relation is the only
	// drill action (Enter is a no-op there) and nothing else advertises it.
	describeInFooter bool
}

// Help wording: a binding that opens another screen or tool reads "→ <where>"
// (PageInspect "→ pages", LockTree "→ lock tree"); a binding that acts in place
// (sort, filter, refresh, toggles, cancel/kill, export) names the action. The
// footer and the ? help then tell jumps from actions at a glance; Enter's own
// destination comes from enterLabel (keys_enter.go).
func defaultKeys() keyMap {
	return keyMap{
		Up:             key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:           key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		PageUp:         key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown:       key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		Top:            key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:         key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "drill in")),
		Back:           key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("q/esc", "back")),
		SortNext:       key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "next column")),
		SortPrev:       key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "prev column")),
		ShowQuery:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "show SQL")),
		ReverseSort:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse sort")),
		Refresh:        key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "refresh")),
		Install:        key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install extension")),
		Describe:       key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "→ describe")),
		DiskUsage:      key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "→ disk usage")),
		Rebaseline:     key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset window")),
		ToggleRefresh:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "refresh cadence")),
		Params:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "→ captured values")),
		Execute:        key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "execute query")),
		Verbose:        key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "verbose")),
		Export:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export csv")),
		SaveSnapshot:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "save snapshot")),
		Snapshots:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "→ snapshots")),
		DeleteSnapshot: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete snapshot")),
		Columns:        key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "configure columns")),
		StmtView:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "queries/by table/by type")),
		ResetCols:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reset to defaults")),
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Seek:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "seek to key")),
		GinNextLeaf:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next data-leaf page")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),

		ActivityFilter:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cycle filter")),
		CancelBackend:    key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "cancel backend")),
		TerminateBackend: key.NewBinding(key.WithKeys("x", "ctrl+k"), key.WithHelp("x/^k", "kill backend")),
		LockTree:         key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "→ lock tree")),

		ShmemMap:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "→ memory map")),
		PageInspect: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "→ pages")),
		TopQueries:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "→ top queries")),

		JumpActivity:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "→ activity")),
		JumpWAL:         key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "→ wal")),
		JumpReplication: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "→ replication")),
		JumpIO:          key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "→ i/o")),
		Progress:        key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "→ progress")),
		Settings:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "→ settings")),

		WaitProfile: key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "→ wait profile")),

		LogGroupMode: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "section mode")),
		LogPane:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "groups/timeline/slow/stats")),
		LogWindow:    key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "widen window")),
		LogJump:      key.NewBinding(key.WithKeys("j"), key.WithHelp("j", "→ timeline")),
		LogParams:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "entries/params/$1")),

		OpenLog: key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "→ log analyzer")),
	}
}

// applyContext enables exactly the bindings valid on screen s and disables the
// rest, so both key.Matches (dispatch in handleKey) and the help view honour the
// current tool/level. Navigation, filter, sort, refresh, export, install,
// describe, bloat, help and quit stay global; the queries/snapshots cluster is
// scoped to its levels so a future tool can reuse those physical keys without a
// switch-ordering collision. Bindings keep their finer in-handler guards
// (statRefresh, statDetail, qualstats, …) on top of this level scoping.
func (k *keyMap) applyContext(s *screen) {
	stmtTable := s.level == levelStatements
	stmtDetail := s.level == levelStatementDetail
	snapshots := s.level == levelSnapshots
	diagResult := s.level == levelDiagnosticResult
	activity := s.level == levelActivity
	tableStats := s.level == levelTableStats
	logs := s.level == levelLogs
	logAny := logs || s.level == levelLogGroup || s.level == levelLogEntry || s.level == levelLogFiles
	pgbShow := s.level == levelPgBouncerShow
	pgbAny := pgbShow || s.level == levelPgBouncers || s.level == levelPgBouncer

	// `s` shows the SQL (to copy out) on a diagnostic result and previews the
	// highlighted query's SQL on the diagnostics list. Sort cycling moved to the
	// ←/→ arrows (SortNext/SortPrev), which stay globally enabled — so even the
	// diagnostic-result table is sortable, with no clash against the show-SQL key.
	diagList := s.level == levelDiagnostics
	k.ShowQuery.SetEnabled(diagResult || diagList)
	k.showQueryInFooter = diagResult || diagList

	k.Rebaseline.SetEnabled(stmtTable)
	k.Snapshots.SetEnabled(stmtTable)
	// tab cycles the top-queries table through its roll-ups (by table / by
	// type) and back; the physical key is the log analyzer's pane cycle
	// elsewhere, gated to its own levels. Named for where it leads next so the
	// footer reads like a jump.
	k.StmtView.SetEnabled(stmtTable)
	if stmtTable {
		k.StmtView.SetHelp("tab", s.stat.view.next().label())
	}
	// C (Columns) is the column-config picker on the top-queries table, the
	// activity table, the table overview, diagnostic results and the heap
	// tuple list (where it picks the relation's own columns; TOAST relations
	// have nothing worth picking). The picker is hard to find without a header
	// hint, so surface it in the footer everywhere but the top-queries table,
	// whose header already advertises it.
	heapTuples := s.level == levelHeapTuples && s.table.Schema != "pg_toast"
	k.Columns.SetEnabled(stmtTable || activity || tableStats || diagResult || (logs && s.log.view.table()) || pgbShow || heapTuples)
	k.columnsInFooter = activity || tableStats || diagResult || (logs && s.log.view.table()) || pgbShow || heapTuples
	// t (ToggleRefresh) cycles the auto-refresh cadence on top-queries levels and
	// on the live activity/progress levels. Surface it in the footer on the pure
	// live monitors, whose header shows the cadence but not the key that changes
	// it; the top-queries levels keep it to the ? help to avoid a crowded footer.
	k.ToggleRefresh.SetEnabled(stmtTable || stmtDetail || activity || s.level == levelProgress || logAny || pgbAny || s.level == levelMaintenance)
	k.toggleRefreshInFooter = activity || s.level == levelProgress || logs || pgbAny
	// l opens the highlighted instance's logfile from the pgbouncer list and its
	// overview (the SHOW tables have no row-level log to open), and elsewhere
	// the current server log as logLinkFor says: checkpoint lines from the WAL
	// overview, slow-query lines from top queries, the whole log from the system
	// overview. The overview's header line already lists its jump keys, so the
	// footer stays as it is there.
	link, linked := logLinkFor(s)
	k.OpenLog.SetEnabled((pgbAny && !pgbShow) || linked)
	k.openLogInFooter = k.OpenLog.Enabled() && s.level != levelMaintenance
	if linked {
		k.OpenLog.SetHelp("l", link.help())
	} else {
		k.OpenLog.SetHelp("l", "→ log analyzer")
	}
	k.SaveSnapshot.SetEnabled(stmtTable || stmtDetail)
	k.DiskUsage.SetEnabled(stmtTable || stmtDetail)
	k.Params.SetEnabled(stmtDetail)
	k.Execute.SetEnabled(stmtDetail)
	// v is the parameter-source toggle on statement detail, the VACUUM trigger on parts,
	// the auxiliary-backend visibility toggle on the activity table and the
	// every-row toggle on the system overview.
	k.Verbose.SetEnabled(stmtDetail || s.level == levelParts || activity || s.level == levelMaintenance)
	k.DeleteSnapshot.SetEnabled(snapshots)
	// Install is only actionable when the screen offers an installable extension
	// (the prompt renders its own `i` hint); keep it out of the footer otherwise.
	k.Install.SetEnabled(s.extPrompt != nil && s.extPrompt.installable)

	// f cycles the backend filter on the activity table and the category filter
	// on the diagnostics list and the log analyzer's overview.
	k.ActivityFilter.SetEnabled(activity || s.level == levelDiagnostics || logs)
	// Cancel/terminate act on the selected backend from both the activity table
	// and its lock-tree child.
	k.CancelBackend.SetEnabled(activity || s.level == levelLockTree)
	k.TerminateBackend.SetEnabled(activity || s.level == levelLockTree)
	// b opens the lock tree from the activity table.
	k.LockTree.SetEnabled(activity)
	k.lockTreeInFooter = activity

	// Log analyzer cluster: m/tab/w only on the overview, o from every log level.
	k.LogGroupMode.SetEnabled(logs)
	k.LogPane.SetEnabled(logs)
	k.LogWindow.SetEnabled(logs)
	k.logInFooter = logs
	logRow := s.level == levelLogGroup || s.level == levelLogEntry
	k.LogJump.SetEnabled(logRow)
	k.logJumpInFooter = logRow
	k.LogParams.SetEnabled(s.level == levelLogGroup)
	k.logParamsInFooter = s.level == levelLogGroup

	// m opens the shared-memory map from the buffer-tables list; surface it in
	// the footer there since nothing else advertises it.
	k.ShmemMap.SetEnabled(s.level == levelBufferTables)
	k.shmemInFooter = s.level == levelBufferTables

	// p jumps from a parts row, a buffer-tables row, the buffer detail or a
	// table's describe panel into the page inspector for that object. The
	// physical key is Params (statement detail) and Progress (dashboard/activity)
	// elsewhere; none of those levels overlaps, so no dispatch collision.
	pageInspect := s.level == levelParts || s.level == levelBufferTables || s.level == levelBufferDetail ||
		describeHasHeap(s)
	k.PageInspect.SetEnabled(pageInspect)
	k.pageInspectInFooter = pageInspect
	// t opens the top-queries tool filtered to the described table. Gated to
	// the describe panel of a table, where ToggleRefresh (the other t) is off.
	topQueries := describeHasHeap(s)
	k.TopQueries.SetEnabled(topQueries)
	k.topQueriesInFooter = topQueries

	// System-overview cross-links only exist on the maintenance dashboard; gating
	// them here keeps r/w free for reverse-sort and the log window everywhere else.
	maint := s.level == levelMaintenance
	k.JumpActivity.SetEnabled(maint)
	k.JumpWAL.SetEnabled(maint)
	k.JumpReplication.SetEnabled(maint)
	k.JumpIO.SetEnabled(maint)
	// s opens the pg_settings browser. Enter can't: on the dashboard it acts on
	// the cursor's action row (arms a stats reset, opens a recommendation's
	// screen). The physical key is ShowQuery (diagnostics) and Seek (index
	// tuples) elsewhere, both off here.
	k.Settings.SetEnabled(maint)
	// p opens the live progress monitor, from the dashboard and the activity table
	// (running operations are backends, so it's a natural cross-link from activity);
	// gated off elsewhere so the physical key stays free for Params (captured
	// values) on statement detail. Surface it in the activity footer, where nothing
	// else advertises it — the dashboard lists it among its jump keys.
	k.Progress.SetEnabled(maint || activity)
	k.progressInFooter = activity

	// The progress monitor orders rows in SQL (pct DESC, pid) with no user sort,
	// and its rows don't drill (Enter is a no-op) — describing the target relation
	// via d is the only action. Drop the misleading drill/sort hints from its
	// footer and surface d instead, so the footer matches what the level does.
	progress := s.level == levelProgress
	// Enter's hint names where it leads on this level (and row), and the key is
	// disabled outright on leaf levels and inert rows so the footer never
	// advertises a dead key. enterLabel (keys_enter.go) is the single table.
	label, drills := enterLabel(s)
	k.Enter.SetEnabled(drills)
	if drills {
		k.Enter.SetHelp("↵", label)
	}
	k.SortPrev.SetEnabled(!progress)
	k.SortNext.SetEnabled(!progress)
	k.ReverseSort.SetEnabled(!progress)
	// … and on the log entry / group-rows levels, where d describes the main
	// table of the statement behind the row — the only path from a slow query
	// to its relation. The WAL views advertise it only while the cursor is on a
	// relation / block-ref row, since their rmgr rows and section lines have
	// no relation to describe.
	_, walRel := walDescribeTarget(s)
	k.describeInFooter = progress || logRow || walRel

	// W opens the wait-event profile over the activity table's sample stream.
	k.WaitProfile.SetEnabled(activity)
	k.waitProfileInFooter = activity

	// s seeks on the index-tuples view: a key value on B-tree, a heap block
	// number on BRIN. GiST/GIN keys have no total order, so seek is disabled
	// there (use the / filter). The physical key is otherwise ShowQuery
	// (diagnostic-result only), so the two never overlap.
	k.Seek.SetEnabled(s.level == levelIndexTuples &&
		(s.pages.index.AccessMethod == "btree" || s.pages.index.AccessMethod == "brin"))
	// n on the GIN page list hops to the next data-leaf page across the whole
	// index. Sorting by type only reorders the loaded window, and a GIN's few
	// posting-tree pages are usually nowhere near the first one — so the hop is
	// the only practical way to reach the pages that drill. Footer-advertised
	// since nothing else hints at it.
	k.GinNextLeaf.SetEnabled(s.level == levelIndexPages && s.pages.index.AccessMethod == "gin")
}

func (k keyMap) ShortHelp() []key.Binding {
	b := []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.SortPrev, k.SortNext, k.ReverseSort, k.Refresh}
	if k.logInFooter {
		b = append(b, k.LogPane, k.LogGroupMode, k.LogWindow)
	}
	if k.StmtView.Enabled() {
		b = append(b, k.StmtView)
	}
	if k.logJumpInFooter {
		b = append(b, k.LogJump)
	}
	if k.logParamsInFooter {
		b = append(b, k.LogParams)
	}
	if k.toggleRefreshInFooter {
		b = append(b, k.ToggleRefresh)
	}
	if k.openLogInFooter {
		b = append(b, k.OpenLog)
	}
	if k.columnsInFooter {
		b = append(b, k.Columns)
	}
	if k.shmemInFooter {
		b = append(b, k.ShmemMap)
	}
	if k.pageInspectInFooter {
		b = append(b, k.PageInspect)
	}
	if k.topQueriesInFooter {
		b = append(b, k.TopQueries)
	}
	if k.lockTreeInFooter {
		b = append(b, k.LockTree)
	}
	if k.waitProfileInFooter {
		b = append(b, k.WaitProfile)
	}
	if k.progressInFooter {
		b = append(b, k.Progress)
	}
	if k.showQueryInFooter {
		b = append(b, k.ShowQuery)
	}
	if k.describeInFooter {
		b = append(b, k.Describe)
	}
	if k.GinNextLeaf.Enabled() {
		b = append(b, k.GinNextLeaf)
	}
	return b
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.Filter, k.Seek, k.GinNextLeaf, k.SortPrev, k.SortNext, k.ShowQuery, k.ReverseSort},
		{k.Refresh, k.Install, k.Describe, k.DiskUsage},
		{k.Rebaseline, k.ToggleRefresh, k.Params, k.Execute, k.Verbose, k.Export},
		{k.ActivityFilter, k.CancelBackend, k.TerminateBackend, k.LockTree, k.WaitProfile},
		{k.StmtView, k.SaveSnapshot, k.Snapshots, k.DeleteSnapshot, k.Columns, k.ShmemMap, k.PageInspect, k.TopQueries},
		{k.JumpActivity, k.JumpWAL, k.JumpReplication, k.JumpIO, k.Progress, k.Settings},
		{k.LogPane, k.LogGroupMode, k.LogWindow, k.LogJump, k.LogParams, k.OpenLog},
		{k.Help, k.Quit},
	}
}
