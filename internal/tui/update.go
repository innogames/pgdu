package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// Update is the top-level Bubble Tea dispatcher. Each msg case delegates to a
// handler that owns the per-message state mutation and returns the follow-up
// command (or nil). Handlers live in update_msgs.go; key dispatch lives in
// update_keys.go; navigation in update_drill.go; sort logic in update_sort.go.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case databasesLoadedMsg:
		return m, m.onDatabasesLoaded(msg)
	case schemasLoadedMsg:
		return m, m.onSchemasLoaded(msg)
	case tablesLoadedMsg:
		return m, m.onTablesLoaded(msg)
	case partsLoadedMsg:
		return m, m.onPartsLoaded(msg)
	case bufferStatsLoadedMsg:
		return m, m.onBufferStatsLoaded(msg)
	case bufferSummaryLoadedMsg:
		return m, m.onBufferSummaryLoaded(msg)
	case columnsLoadedMsg:
		return m, m.onColumnsLoaded(msg)
	case bloatFilledMsg:
		return m, m.onBloatFilled(msg)
	case extStatusMsg:
		return m, m.onExtStatus(msg)
	case extInstalledMsg:
		return m, m.onExtInstalled(msg)
	case reindexDoneMsg:
		return m, m.onReindexDone(msg)
	case heapPagesLoadedMsg:
		return m, m.onHeapPagesLoaded(msg)
	case heapTuplesLoadedMsg:
		return m, m.onHeapTuplesLoaded(msg)
	case tupleRowLoadedMsg:
		return m, m.onTupleRowLoaded(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// asMissingExt returns the underlying *pg.MissingExtensionError if err is one,
// or nil. errors.As handles wrapping so command callers can wrap freely.
func asMissingExt(err error) *pg.MissingExtensionError {
	var e *pg.MissingExtensionError
	if errors.As(err, &e) {
		return e
	}
	return nil
}
