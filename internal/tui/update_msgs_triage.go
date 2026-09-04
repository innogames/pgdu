package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// triagePending marks a placeholder row for a check that has not reported yet.
type triagePending struct{}

// onTriageStarted resets the report for a fresh run: no results, every check
// pending. The spinner screen is dropped right away — the placeholder rows
// carry the progress from here on.
func (m *Model) onTriageStarted(msg triageStartedMsg) tea.Cmd {
	s := m.findLevel(levelTriage)
	if s == nil || msg.gen != s.triage.gen {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.triage.results = nil
	s.triage.names = msg.names
	s.triage.pending = slices.Clone(msg.names)
	s.items = triageItems(s.triage.results, s.triage.pending)
	s.itemsRev++
	s.resetCursor()
	return waitTriageCmd(msg.gen, msg.ch)
}

// onTriageCheck slots one finished check into the report, keeping the
// severity-sorted order stable (battery order within a severity) so rows do
// not jump around under the cursor as the rest of the battery lands.
func (m *Model) onTriageCheck(msg triageCheckMsg) tea.Cmd {
	s := m.findLevel(levelTriage)
	if s == nil || msg.gen != s.triage.gen {
		return nil
	}
	s.triage.results = append(s.triage.results, msg.result)
	pg.SortTriage(s.triage.results, s.triage.names)
	s.triage.pending = slices.DeleteFunc(s.triage.pending, func(n string) bool { return n == msg.result.Check })
	s.items = triageItems(s.triage.results, s.triage.pending)
	s.itemsRev++
	s.clampCursor()
	return waitTriageCmd(msg.gen, msg.ch)
}

func (m *Model) onTriageLoaded(msg triageLoadedMsg) tea.Cmd {
	s := m.findLevel(levelTriage)
	if s == nil || msg.gen != s.triage.gen {
		return nil
	}
	// Every check has reported; anything still listed as pending would be a
	// bookkeeping slip, so drop it rather than spin forever.
	s.triage.pending = nil
	s.items = triageItems(s.triage.results, nil)
	s.itemsRev++
	s.clampCursor()
	return nil
}

// triageItems flattens the (already severity-sorted) triage results into list
// rows. Green checks collapse into one trailing summary row so the eye lands
// on red first; crit/warn rows carry their TriageResult for the Enter drill.
// Checks still running follow as inert placeholder rows.
func triageItems(results []pg.TriageResult, pending []string) []item {
	items := make([]item, 0, len(results)+len(pending)+1)
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
	for _, n := range pending {
		items = append(items, item{name: n, detail: "checking…", data: triagePending{}})
	}
	return items
}
