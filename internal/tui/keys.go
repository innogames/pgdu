package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up, Down    key.Binding
	Top, Bottom key.Binding
	Enter, Back key.Binding
	SortSize    key.Binding
	SortName    key.Binding
	Refresh     key.Binding
	ToggleBloat key.Binding
	Help        key.Binding
	Quit        key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Top:         key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:      key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:       key.NewBinding(key.WithKeys("enter", "right", "l"), key.WithHelp("↵/l", "drill in")),
		Back:        key.NewBinding(key.WithKeys("left", "h", "esc", "backspace"), key.WithHelp("←/h", "back")),
		SortSize:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort: size")),
		SortName:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "sort: name")),
		Refresh:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		ToggleBloat: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "toggle bloat")),
		Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Back, k.SortSize, k.SortName, k.Refresh, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Enter, k.Back},
		{k.SortSize, k.SortName, k.Refresh, k.ToggleBloat},
		{k.Help, k.Quit},
	}
}
