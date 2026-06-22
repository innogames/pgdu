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
	Filter           key.Binding
	Seek             key.Binding
	Help             key.Binding
	Quit             key.Binding

	// Activity-tool-specific bindings.
	ActivityFilter   key.Binding // f: cycle backend filter mode
	CancelBackend    key.Binding // k: send pg_cancel_backend (SIGINT)
	TerminateBackend key.Binding // x: send pg_terminate_backend (SIGTERM)

	// WAL-inspector binding.
	WALByRelation key.Binding // w: open the by-relation breakdown of the window

	// Shared-buffers-tool binding.
	ShmemMap key.Binding // m: open the shared-memory map (pg_shmem_allocations)

	// shmemInFooter adds the m (memory map) hint to the footer's short help on
	// the buffer-tables level, where it's the only advertisement for the view.
	shmemInFooter bool

	// columnsInFooter adds the C (configure columns) hint to the footer's short
	// help. Set per-screen by applyContext: the activity table has no other
	// advertisement for the picker, whereas the top-queries table already shows
	// "C columns" in its header, so it stays out of the footer there.
	columnsInFooter bool
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
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Seek:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "seek to key")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),

		ActivityFilter:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "cycle filter")),
		CancelBackend:    key.NewBinding(key.WithKeys("k"), key.WithHelp("k", "cancel backend")),
		TerminateBackend: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "terminate backend")),

		WALByRelation: key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "by relation")),
		ShmemMap:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "memory map")),
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

	// `s` shows the executed SQL (to copy out) only on a diagnostic result. Sort
	// cycling moved to the ←/→ arrows (SortNext/SortPrev), which stay globally
	// enabled — so even the diagnostic-result table is sortable, with no clash
	// against the show-SQL key.
	k.ShowQuery.SetEnabled(diagResult)

	k.Rebaseline.SetEnabled(stmtTable)
	k.Snapshots.SetEnabled(stmtTable)
	// C (Columns) is the column-config picker on the top-queries table and on the
	// activity table. The picker is hard to find on activity (no header hint), so
	// surface it in the footer there; the top-queries header already advertises it.
	k.Columns.SetEnabled(stmtTable || activity)
	k.columnsInFooter = activity
	// t (ToggleRefresh) cycles the auto-refresh cadence on top-queries levels and
	// on the activity level.
	k.ToggleRefresh.SetEnabled(stmtTable || stmtDetail || activity)
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

	k.ActivityFilter.SetEnabled(activity)
	k.CancelBackend.SetEnabled(activity)
	k.TerminateBackend.SetEnabled(activity)

	// w opens the by-relation WAL breakdown — only from the rmgr overview, so
	// the physical key stays free for reuse on every other level.
	k.WALByRelation.SetEnabled(s.level == levelWAL)

	// m opens the shared-memory map from the buffer-tables list; surface it in
	// the footer there since nothing else advertises it.
	k.ShmemMap.SetEnabled(s.level == levelBufferTables)
	k.shmemInFooter = s.level == levelBufferTables

	// s seeks on the index-tuples view: a key value on B-tree, a heap block
	// number on BRIN. GiST/GIN keys have no total order, so seek is disabled
	// there (use the / filter). The physical key is otherwise ShowQuery
	// (diagnostic-result only), so the two never overlap.
	k.Seek.SetEnabled(s.level == levelIndexTuples &&
		(s.index.AccessMethod == "btree" || s.index.AccessMethod == "brin"))
}

func (k keyMap) ShortHelp() []key.Binding {
	b := []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.SortPrev, k.SortNext, k.ReverseSort, k.Refresh}
	if k.columnsInFooter {
		b = append(b, k.Columns)
	}
	if k.shmemInFooter {
		b = append(b, k.ShmemMap)
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
		{k.SaveSnapshot, k.Snapshots, k.DeleteSnapshot, k.Columns, k.WALByRelation, k.ShmemMap},
		{k.Help, k.Quit},
	}
}
