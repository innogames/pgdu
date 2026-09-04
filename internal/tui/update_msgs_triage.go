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

// triageOKSummary marks the "N check(s) ok" row; Enter on it folds/unfolds the
// green checks (see toggleTriageOK).
type triageOKSummary struct{}

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
	m.rebuildTriageItems(s)
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
	m.rebuildTriageItems(s)
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
	m.rebuildTriageItems(s)
	s.clampCursor()
	return nil
}

// rebuildTriageItems re-derives the rows from the report and invalidates the
// filter cache; callers position the cursor.
func (m *Model) rebuildTriageItems(s *screen) {
	s.items = triageItems(s.triage.results, s.triage.pending, s.triage.showOK)
	s.itemsRev++
}

// toggleTriageOK folds or unfolds the green checks in place, keeping the
// cursor on the same row (by name) so the list doesn't jump.
func (m *Model) toggleTriageOK(s *screen) {
	var name string
	if cur, ok := s.currentItem(); ok {
		name = cur.name
	}
	s.triage.showOK = !s.triage.showOK
	m.rebuildTriageItems(s)
	s.clampCursor()
	for pos, idx := range s.visibleIndexes() {
		if s.items[idx].name == name {
			s.cursor = pos
			break
		}
	}
}

// triageItems flattens the (already severity-sorted) triage results into list
// rows. Green checks collapse into one trailing summary row so the eye lands
// on red first — or, with showOK, follow that row one per check so each can be
// read and drilled like a finding. Every check row carries its TriageResult
// for the Enter drill. Checks still running follow as inert placeholder rows.
func triageItems(results []pg.TriageResult, pending []string, showOK bool) []item {
	items := make([]item, 0, len(results)+len(pending)+1)
	var ok []pg.TriageResult
	for _, r := range results {
		if r.Severity == pg.SevOK {
			ok = append(ok, r)
			continue
		}
		items = append(items, item{name: r.Check, detail: r.Detail, hasChildren: true, data: r})
	}
	if len(ok) > 0 {
		sum := item{name: fmt.Sprintf("%d check(s) ok", len(ok)), hasChildren: true, data: triageOKSummary{}}
		if !showOK {
			names := make([]string, len(ok))
			for i, r := range ok {
				names[i] = r.Check
			}
			sum.detail = strings.Join(names, " · ")
		}
		items = append(items, sum)
		if showOK {
			for _, r := range ok {
				items = append(items, item{name: r.Check, detail: r.Detail, hasChildren: true, data: r})
			}
		}
	}
	for _, n := range pending {
		items = append(items, item{name: n, detail: "checking…", data: triagePending{}})
	}
	return items
}
