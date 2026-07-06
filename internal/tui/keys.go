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
	ToggleBloat      key.Binding
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
	Filter           key.Binding
	Seek             key.Binding
	Help             key.Binding
	Quit             key.Binding

	// Activity-tool-specific bindings.
	ActivityFilter   key.Binding // f: cycle backend filter mode
	CancelBackend    key.Binding // k: send pg_cancel_backend (SIGINT)
	TerminateBackend key.Binding // x: send pg_terminate_backend (SIGTERM)
	LockTree         key.Binding // b: open the blocking-chain lock tree

	// WAL-inspector binding.
	WALByRelation key.Binding // w: open the by-relation breakdown of the window

	// Shared-buffers-tool binding.
	ShmemMap key.Binding // m: open the shared-memory map (pg_shmem_allocations)

	// System-overview cross-links: jump from the maintenance dashboard into the
	// live tools that show the detail behind a summary row. Enabled only on
	// levelMaintenance (so they don't clash with r/reverse-sort and w/by-relation
	// elsewhere) and dispatched before those cases.
	JumpActivity    key.Binding // a: open the Activity tool
	JumpWAL         key.Binding // w: open the WAL inspector
	JumpReplication key.Binding // r: open the replication-slots diagnostic
	Progress        key.Binding // p: open the live progress monitor

	// Wait-event profiler over the Activity tool's sample stream.
	WaitProfile key.Binding // W: open the wait-event profile

	// waitProfileInFooter adds the W hint to the footer on the activity table.
	waitProfileInFooter bool

	// shmemInFooter adds the m (memory map) hint to the footer's short help on
	// the buffer-tables level, where it's the only advertisement for the view.
	shmemInFooter bool

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

	// describeInFooter adds the d (describe) hint to the footer on the progress
	// monitor, where describing the operation's target relation is the only
	// drill action (Enter is a no-op there) and nothing else advertises it.
	describeInFooter bool
}

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
		ToggleBloat:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "toggle bloat")),
		Install:        key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install extension")),
		Describe:       key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "describe")),
		DiskUsage:      key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "disk usage")),
		Rebaseline:     key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset window")),
		ToggleRefresh:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "refresh cadence")),
		Params:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "captured values")),
		Execute:        key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "execute query")),
		Verbose:        key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "verbose")),
		Export:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export csv")),
		SaveSnapshot:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "save snapshot")),
		Snapshots:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "load snapshot")),
		DeleteSnapshot: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete snapshot")),
		Columns:        key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "configure columns")),
		ResetCols:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reset to defaults")),
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Seek:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "seek to key")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),

		ActivityFilter:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cycle filter")),
		CancelBackend:    key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "cancel backend")),
		TerminateBackend: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "terminate backend")),
		LockTree:         key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "lock tree")),

		WALByRelation: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "by relation")),
		ShmemMap:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "memory map")),

		JumpActivity:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "activity")),
		JumpWAL:         key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "wal")),
		JumpReplication: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "replication")),
		Progress:        key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "progress")),

		WaitProfile: key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "wait profile")),
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

	// `s` shows the SQL (to copy out) on a diagnostic result and previews the
	// highlighted query's SQL on the diagnostics list. Sort cycling moved to the
	// ←/→ arrows (SortNext/SortPrev), which stay globally enabled — so even the
	// diagnostic-result table is sortable, with no clash against the show-SQL key.
	diagList := s.level == levelDiagnostics
	k.ShowQuery.SetEnabled(diagResult || diagList)
	k.showQueryInFooter = diagResult || diagList

	k.Rebaseline.SetEnabled(stmtTable)
	k.Snapshots.SetEnabled(stmtTable)
	// C (Columns) is the column-config picker on the top-queries table, the
	// activity table, the table overview and diagnostic results. The picker is
	// hard to find without a header hint, so surface it in the footer everywhere
	// but the top-queries table, whose header already advertises it.
	k.Columns.SetEnabled(stmtTable || activity || tableStats || diagResult)
	k.columnsInFooter = activity || tableStats || diagResult
	// t (ToggleRefresh) cycles the auto-refresh cadence on top-queries levels and
	// on the live activity/progress levels. Surface it in the footer on the pure
	// live monitors, whose header shows the cadence but not the key that changes
	// it; the top-queries levels keep it to the ? help to avoid a crowded footer.
	k.ToggleRefresh.SetEnabled(stmtTable || stmtDetail || activity || s.level == levelProgress)
	k.toggleRefreshInFooter = activity || s.level == levelProgress
	k.SaveSnapshot.SetEnabled(stmtTable || stmtDetail)
	k.DiskUsage.SetEnabled(stmtTable || stmtDetail)
	k.Params.SetEnabled(stmtDetail)
	k.Execute.SetEnabled(stmtDetail)
	// v is the verbose toggle on statement detail, the VACUUM trigger on parts,
	// and the auxiliary-backend visibility toggle on the activity table.
	k.Verbose.SetEnabled(stmtDetail || s.level == levelParts || activity)
	k.DeleteSnapshot.SetEnabled(snapshots)
	// Install is only actionable when the screen offers an installable extension
	// (the prompt renders its own `i` hint); keep it out of the footer otherwise.
	k.Install.SetEnabled(s.extPrompt != nil && s.extPrompt.installable)

	// f cycles the backend filter on the activity table and the category filter
	// on the diagnostics list.
	k.ActivityFilter.SetEnabled(activity || s.level == levelDiagnostics)
	// Cancel/terminate act on the selected backend from both the activity table
	// and its lock-tree child.
	k.CancelBackend.SetEnabled(activity || s.level == levelLockTree)
	k.TerminateBackend.SetEnabled(activity || s.level == levelLockTree)
	// b opens the lock tree from the activity table.
	k.LockTree.SetEnabled(activity)
	k.lockTreeInFooter = activity

	// w opens the by-relation WAL breakdown — only from the rmgr overview, so
	// the physical key stays free for reuse on every other level.
	k.WALByRelation.SetEnabled(s.level == levelWAL)

	// m opens the shared-memory map from the buffer-tables list; surface it in
	// the footer there since nothing else advertises it.
	k.ShmemMap.SetEnabled(s.level == levelBufferTables)
	k.shmemInFooter = s.level == levelBufferTables

	// System-overview cross-links only exist on the maintenance dashboard; gating
	// them here keeps r/w free for reverse-sort and WAL-by-relation everywhere else.
	maint := s.level == levelMaintenance
	k.JumpActivity.SetEnabled(maint)
	k.JumpWAL.SetEnabled(maint)
	k.JumpReplication.SetEnabled(maint)
	// p opens the live progress monitor; gated to the dashboard so the physical
	// key stays free for Params (captured values) on statement detail.
	k.Progress.SetEnabled(maint)

	// The progress monitor orders rows in SQL (pct DESC, pid) with no user sort,
	// and its rows don't drill (Enter is a no-op) — describing the target relation
	// via d is the only action. Drop the misleading drill/sort hints from its
	// footer and surface d instead, so the footer matches what the level does.
	progress := s.level == levelProgress
	k.Enter.SetEnabled(!progress)
	k.SortPrev.SetEnabled(!progress)
	k.SortNext.SetEnabled(!progress)
	k.ReverseSort.SetEnabled(!progress)
	k.describeInFooter = progress

	// W opens the wait-event profile over the activity table's sample stream.
	k.WaitProfile.SetEnabled(activity)
	k.waitProfileInFooter = activity

	// s seeks on the index-tuples view: a key value on B-tree, a heap block
	// number on BRIN. GiST/GIN keys have no total order, so seek is disabled
	// there (use the / filter). The physical key is otherwise ShowQuery
	// (diagnostic-result only), so the two never overlap.
	k.Seek.SetEnabled(s.level == levelIndexTuples &&
		(s.index.AccessMethod == "btree" || s.index.AccessMethod == "brin"))
}

func (k keyMap) ShortHelp() []key.Binding {
	b := []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.SortPrev, k.SortNext, k.ReverseSort, k.Refresh}
	if k.toggleRefreshInFooter {
		b = append(b, k.ToggleRefresh)
	}
	if k.columnsInFooter {
		b = append(b, k.Columns)
	}
	if k.shmemInFooter {
		b = append(b, k.ShmemMap)
	}
	if k.lockTreeInFooter {
		b = append(b, k.LockTree)
	}
	if k.waitProfileInFooter {
		b = append(b, k.WaitProfile)
	}
	if k.showQueryInFooter {
		b = append(b, k.ShowQuery)
	}
	if k.describeInFooter {
		b = append(b, k.Describe)
	}
	return b
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.Filter, k.Seek, k.SortPrev, k.SortNext, k.ShowQuery, k.ReverseSort},
		{k.Refresh, k.ToggleBloat, k.Install, k.Describe, k.DiskUsage},
		{k.Rebaseline, k.ToggleRefresh, k.Params, k.Execute, k.Verbose, k.Export},
		{k.ActivityFilter, k.CancelBackend, k.TerminateBackend, k.LockTree, k.WaitProfile},
		{k.SaveSnapshot, k.Snapshots, k.DeleteSnapshot, k.Columns, k.WALByRelation, k.ShmemMap},
		{k.Help, k.Quit},
	}
}
