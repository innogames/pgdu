package tui

// Table keys under which each tool's column-visibility selection is persisted in
// the user prefs file. Adding a new persistable table = a new constant here plus
// one saveColPrefs call in that table's C-picker toggle handler.
const (
	colPrefsActivity   = "activity"
	colPrefsQueries    = "queries"
	colPrefsTableStats = "tablestats"
)

// colVisToStrings converts a typed column-visibility map (e.g. map[actColID]bool)
// to the map[string]bool the prefs layer stores. The id types are all ~string,
// so this is a straight key cast.
func colVisToStrings[K ~string](m map[K]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

// colVisFromStrings is the inverse: it rehydrates a typed visibility map from the
// persisted map[string]bool when seeding a tool's state at startup.
func colVisFromStrings[K ~string](m map[string]bool) map[K]bool {
	out := make(map[K]bool, len(m))
	for k, v := range m {
		out[K(k)] = v
	}
	return out
}

// saveColPrefs records a table's current column-visibility selection to the user
// prefs file. Best-effort: a write failure is surfaced in the status line but
// never blocks the toggle. No-op when prefs are unavailable.
func (m *Model) saveColPrefs(table string, vis map[string]bool) {
	if m.colPrefs == nil {
		return
	}
	m.colPrefs.SetColumns(table, vis)
	if err := m.colPrefs.Save(); err != nil {
		m.notice = "could not save column prefs: " + err.Error()
	}
}
