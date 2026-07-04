package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

func (m *Model) onTriageLoaded(msg triageLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTriage)
	if s == nil {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.triageResults = msg.results
	s.items = triageItems(msg.results)
	s.itemsRev++
	s.resetCursor()
	return nil
}

// triageItems flattens the (already severity-sorted) triage results into list
// rows. Green checks collapse into one trailing summary row so the eye lands
// on red first; crit/warn rows carry their TriageResult for the Enter drill.
func triageItems(results []pg.TriageResult) []item {
	items := make([]item, 0, len(results)+1)
	var okNames []string
	for _, r := range results {
		if r.Severity == pg.SevOK {
			okNames = append(okNames, r.Check)
			continue
		}
		items = append(items, item{name: r.Check, detail: r.Detail, hasChildren: true, data: r})
	}
	if len(okNames) > 0 {
		items = append(items, item{
			name:   fmt.Sprintf("%d check(s) ok", len(okNames)),
			detail: strings.Join(okNames, " · "),
		})
	}
	return items
}
