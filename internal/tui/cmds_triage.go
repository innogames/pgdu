package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// triageLoadedMsg delivers the health-triage battery's results. There is no
// err field on purpose: Triage degrades each failed check to a "could not
// evaluate" line instead of failing the whole report.
type triageLoadedMsg struct {
	results []pg.TriageResult
}

func (m *Model) loadTriageCmd() tea.Cmd {
	return query(func(ctx context.Context) tea.Msg {
		return triageLoadedMsg{results: m.client.Triage(ctx)}
	})
}
