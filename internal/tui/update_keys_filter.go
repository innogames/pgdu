package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleFilterKey is the input handler while s.filterFocused is true. Esc
// clears + blurs, Enter blurs (keeps the query), Backspace deletes the last
// rune (and blurs if it empties the query), Up/Down navigate the filtered
// list live, and any printable input is appended to the query. Editing the
// query resets cursor/offset so the user always lands on the first match.
func (m *Model) handleFilterKey(s *screen, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEsc:
		s.filter = ""
		s.filterFocused = false
		s.resetCursor()
	case tea.KeyEnter:
		s.filterFocused = false
		s.clampCursor()
	case tea.KeyBackspace, tea.KeyDelete:
		if r := []rune(s.filter); len(r) > 0 {
			s.filter = string(r[:len(r)-1])
			s.resetCursor()
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
	default:
		if msg.Text == "" || msg.Mod.Contains(tea.ModAlt) {
			return m, nil
		}
		s.filter += msg.Text
		s.resetCursor()
	}
	return m, nil
}

// handlePaste feeds a bracketed paste into whichever input has focus, the way
// v1 delivered a paste as one rune keypress; outside an input it is dropped.
func (m *Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	s := m.top()
	in := tea.KeyPressMsg{Text: msg.Content}
	switch {
	case s.filterFocused:
		return m.handleFilterKey(s, in)
	case s.pages.seekFocused:
		return m.handleSeekKey(s, in)
	}
	return m, nil
}
