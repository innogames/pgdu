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
	Filter           key.Binding
	Help             key.Binding
	Quit             key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:      key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown:    key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		Top:         key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:      key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:       key.NewBinding(key.WithKeys("enter", "right", "l"), key.WithHelp("↵/l", "drill in")),
		Back:        key.NewBinding(key.WithKeys("left", "h", "esc", "backspace"), key.WithHelp("←/h", "back")),
		Sort:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort: cycle column")),
		ReverseSort: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse sort")),
		Refresh:     key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "refresh")),
		ToggleBloat: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "toggle bloat")),
		Install:     key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install extension")),
		Filter:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.Filter, k.Sort, k.ReverseSort, k.Refresh, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.Filter, k.Sort, k.ReverseSort},
		{k.Refresh, k.ToggleBloat, k.Install},
		{k.Help, k.Quit},
	}
}
