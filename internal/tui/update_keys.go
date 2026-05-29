package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// pageStep is the cursor jump distance for PageUp/PageDown. Roughly the
// visible row count: terminal height minus header (3 lines), the inter-block
// blank, and the help row. Always at least 1 so a one-row jump still happens
// on tiny terminals.
func (m *Model) pageStep() int {
	step := m.height - 6
	if step < 1 {
		return 1
	}
	return step
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.top()
	// While the filter input has focus, route keys into the filter editor
	// instead of the list. Bypasses every other binding (so e.g. typing "s"
	// extends the query rather than cycling the sort).
	if s.filterFocused {
		return m.handleFilterKey(s, msg)
	}
	// When a reindex confirmation is armed, capture the next key here: `y`
	// (case-insensitive) executes; anything else cancels. Using y/n instead of
	// a second Enter avoids running REINDEX on an accidental double-tap.
	if s.pendingReindex != "" {
		if msg.String() == "y" || msg.String() == "Y" {
			idx := s.pendingReindex
			s.pendingReindex = ""
			s.reindexing = idx
			s.reindexErr = nil
			return m, m.reindexIndexCmd(s.table, idx)
		}
		s.pendingReindex = ""
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		// On the buffer-tables level the bars carry a lot of semantics that
		// aren't obvious — use ? to toggle a dedicated reference overlay
		// instead of expanding the key list. Other levels keep the standard
		// help-expansion behaviour.
		if s.level == levelBufferTables {
			m.showInfo = !m.showInfo
			break
		}
		m.help.ShowAll = !m.help.ShowAll
	case key.Matches(msg, m.keys.Filter):
		s.filterFocused = true
	case key.Matches(msg, m.keys.Down):
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	case key.Matches(msg, m.keys.Up):
		if s.cursor > 0 {
			s.cursor--
		}
	case key.Matches(msg, m.keys.PageDown):
		s.cursor = max(min(s.cursor+m.pageStep(), s.visibleLen()-1), 0)
	case key.Matches(msg, m.keys.PageUp):
		s.cursor = max(s.cursor-m.pageStep(), 0)
	case key.Matches(msg, m.keys.Top):
		s.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		s.cursor = max(s.visibleLen()-1, 0)
	case key.Matches(msg, m.keys.Sort):
		m.cycleSort(s)
	case key.Matches(msg, m.keys.ReverseSort):
		s.sortDesc = !s.sortDesc
		m.applySort(s)
	case key.Matches(msg, m.keys.Refresh):
		return m, m.loadCurrent()
	case key.Matches(msg, m.keys.ToggleBloat):
		m.fetchBloat = !m.fetchBloat
	case key.Matches(msg, m.keys.Install):
		return m, m.triggerInstall(s)
	case key.Matches(msg, m.keys.Back):
		// Esc is shared with Back; when an overlay/filter is up, Esc closes
		// that instead of unwinding the stack. Other Back keys (←/h/
		// backspace) always navigate back so muscle memory for "go up a
		// level" is preserved.
		if msg.Type == tea.KeyEsc && m.showInfo {
			m.showInfo = false
			break
		}
		if msg.Type == tea.KeyEsc && s.filter != "" {
			s.filter = ""
			s.cursor = 0
			s.offset = 0
			break
		}
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
	case key.Matches(msg, m.keys.Enter):
		if cmd := m.handleReindexEnter(s); cmd != nil {
			return m, cmd
		}
		if s.level == levelParts && reindexCandidate(s) != "" {
			// First ENTER on a bloated index row → request confirmation;
			// don't drill (index rows don't drill anyway).
			return m, nil
		}
		return m, m.drillIn()
	}
	return m, nil
}

// handleFilterKey is the input handler while s.filterFocused is true. Esc
// clears + blurs, Enter blurs (keeps the query), Backspace deletes the last
// rune (and blurs if it empties the query), Up/Down navigate the filtered
// list live, and any printable input is appended to the query. Editing the
// query resets cursor/offset so the user always lands on the first match.
func (m *Model) handleFilterKey(s *screen, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		s.filter = ""
		s.filterFocused = false
		s.cursor = 0
		s.offset = 0
	case tea.KeyEnter:
		s.filterFocused = false
		s.clampCursor()
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
			s.cursor = 0
			s.offset = 0
		} else {
			s.filterFocused = false
		}
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
		}
	case tea.KeyDown:
		if s.cursor < s.visibleLen()-1 {
			s.cursor++
		}
	case tea.KeyRunes, tea.KeySpace:
		if msg.Alt {
			return m, nil
		}
		s.filter += string(msg.Runes)
		s.cursor = 0
		s.offset = 0
	}
	return m, nil
}

// reindexCandidate returns the index name to reindex if the current row on a
// parts screen is an index part with bloat > reindexBloatThreshold. Returns
// "" when the row doesn't qualify (wrong level, wrong kind, bloat unknown or
// below threshold, or another reindex is already in flight on this screen).
func reindexCandidate(s *screen) string {
	if s.level != levelParts || s.reindexing != "" {
		return ""
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return ""
	}
	it := s.items[vis[s.cursor]]
	p, ok := it.data.(pg.Part)
	if !ok || p.Kind != pg.PartIndex {
		return ""
	}
	if !it.hasBloat || it.size <= 0 {
		return ""
	}
	if float64(it.bloat)/float64(it.size) <= reindexBloatThreshold {
		return ""
	}
	return p.Name
}

// handleReindexEnter arms the y/n reindex confirmation when Enter lands on a
// qualifying bloated index row. The execute path lives in handleKey, which
// intercepts the next keystroke. Returns nil when the press isn't part of the
// reindex flow, so the caller can fall through to the normal drill-in.
func (m *Model) handleReindexEnter(s *screen) tea.Cmd {
	if s.level != levelParts {
		return nil
	}
	cand := reindexCandidate(s)
	if cand == "" {
		return nil
	}
	s.pendingReindex = cand
	s.reindexErr = nil
	return nil
}

// triggerInstall is a no-op unless the current screen has an extPrompt
// that's still installable. Sets `installing` so the view can show a
// spinner, and dispatches the CREATE EXTENSION command.
func (m *Model) triggerInstall(s *screen) tea.Cmd {
	if s.extPrompt == nil || !s.extPrompt.installable || s.installing {
		return nil
	}
	s.installing = true
	s.extPrompt.err = nil
	return m.installExtensionCmd(s.extPrompt.db, s.extPrompt.name)
}
