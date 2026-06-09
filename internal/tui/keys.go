package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	Top, Bottom      key.Binding
	Enter, Back      key.Binding
	Sort             key.Binding
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
	Export           key.Binding
	SaveSnapshot     key.Binding
	Snapshots        key.Binding
	DeleteSnapshot   key.Binding
	Columns          key.Binding
	Filter           key.Binding
	Help             key.Binding
	Quit             key.Binding
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
		Sort:           key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort: cycle column")),
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
		Export:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export csv")),
		SaveSnapshot:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "save snapshot")),
		Snapshots:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "load snapshot")),
		DeleteSnapshot: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete snapshot")),
		Columns:        key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "configure columns")),
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
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

	// On a diagnostic result `s` shows the executed SQL (to copy out) instead of
	// cycling the sort column — the two share the physical "s" key, so scope them
	// to be mutually exclusive here rather than rely on switch ordering.
	k.ShowQuery.SetEnabled(diagResult)
	k.Sort.SetEnabled(!diagResult)

	k.Rebaseline.SetEnabled(stmtTable)
	k.Snapshots.SetEnabled(stmtTable)
	k.Columns.SetEnabled(stmtTable)
	k.ToggleRefresh.SetEnabled(stmtTable || stmtDetail)
	k.SaveSnapshot.SetEnabled(stmtTable || stmtDetail)
	k.DiskUsage.SetEnabled(stmtTable || stmtDetail)
	k.Params.SetEnabled(stmtDetail)
	k.Execute.SetEnabled(stmtDetail)
	k.DeleteSnapshot.SetEnabled(snapshots)
	// Install is only actionable when the screen offers an installable extension
	// (the prompt renders its own `i` hint); keep it out of the footer otherwise.
	k.Install.SetEnabled(s.extPrompt != nil && s.extPrompt.installable)
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.Sort, k.ReverseSort, k.Refresh}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.Filter, k.Sort, k.ShowQuery, k.ReverseSort},
		{k.Refresh, k.ToggleBloat, k.Install, k.Describe, k.DiskUsage},
		{k.Rebaseline, k.ToggleRefresh, k.Params, k.Execute, k.Export},
		{k.SaveSnapshot, k.Snapshots, k.DeleteSnapshot, k.Columns},
		{k.Help, k.Quit},
	}
}
