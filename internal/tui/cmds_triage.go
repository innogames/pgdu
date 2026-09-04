package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// triageStartedMsg opens a streaming triage run: names lists every check in
// battery order so the screen can show a placeholder row per check, and gen
// ties the run to the screen state so a refresh started meanwhile can drop
// the stale run's late results.
type triageStartedMsg struct {
	gen   uint64
	names []string
	ch    <-chan pg.TriageResult
}

// triageCheckMsg delivers one finished check. There is no err field on
// purpose: TriageStream degrades each failed check to a "could not evaluate"
// line instead of failing the report.
type triageCheckMsg struct {
	gen    uint64
	result pg.TriageResult
	ch     <-chan pg.TriageResult
}

// triageLoadedMsg marks the run complete: every check has reported.
type triageLoadedMsg struct {
	gen uint64
}

// loadTriageCmd starts the battery in its own goroutine and hands the UI the
// channel its results arrive on, so fast checks render while the slow ones
// (bloat estimates, the shared MaintenanceInfo fetch) are still running. The
// whole run shares one queryTimeout budget like any other read; each check
// additionally has its own sub-budget inside TriageStream.
func (m *Model) loadTriageCmd(gen uint64) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan pg.TriageResult, 32)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			m.client.TriageStream(ctx, func(r pg.TriageResult) { ch <- r })
			close(ch)
		}()
		return triageStartedMsg{gen: gen, names: m.client.TriageCheckNames(), ch: ch}
	}
}

// waitTriageCmd waits for the next check result (or the end of the run).
func waitTriageCmd(gen uint64, ch <-chan pg.TriageResult) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return triageLoadedMsg{gen: gen}
		}
		return triageCheckMsg{gen: gen, result: r, ch: ch}
	}
}
