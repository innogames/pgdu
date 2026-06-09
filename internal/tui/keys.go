package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	Top, Bottom      key.Binding
	Enter, Back      key.Binding
	Sort             key.Binding
	ReverseSort      key.Binding
	Refresh          key.Binding
	ToggleBloat      key.Binding
	Install          key.Binding
	Describe         key.Binding
	Rebaseline       key.Binding
	ToggleRefresh    key.Binding
	Explain          key.Binding
	Params           key.Binding
	Export           key.Binding
	SaveSnapshot     key.Binding
	Snapshots        key.Binding
	MarkBase         key.Binding
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
		ReverseSort:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse sort")),
		Refresh:        key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "refresh")),
		ToggleBloat:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "toggle bloat")),
		Install:        key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install extension")),
		Describe:       key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "describe")),
		Rebaseline:     key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset window")),
		ToggleRefresh:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "toggle refresh")),
		Explain:        key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "explain")),
		Params:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "captured values")),
		Export:         key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export csv")),
		SaveSnapshot:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "save snapshot")),
		Snapshots:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "load snapshot")),
		MarkBase:       key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark base (A→B)")),
		DeleteSnapshot: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete snapshot")),
		Columns:        key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "configure columns")),
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:           key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.Sort, k.ReverseSort, k.Refresh}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.Filter, k.Sort, k.ReverseSort},
		{k.Refresh, k.ToggleBloat, k.Install, k.Describe},
		{k.Rebaseline, k.ToggleRefresh, k.Explain, k.Params, k.Export},
		{k.SaveSnapshot, k.Snapshots, k.MarkBase, k.DeleteSnapshot, k.Columns},
		{k.Help, k.Quit},
	}
}
