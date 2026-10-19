package tui

import "pgdu/internal/pg"

// maintAction is one cursor-addressable row of the system overview: a
// recommendation that opens the screen explaining it, or a stats reset in the
// statistics block at the foot of the page. Everything else is read-only text.
type maintAction struct {
	reset  string     // one of maintResetRows; "" on recommendation rows
	advice *pg.Advice // nil on reset rows
	// missing marks an extension reset row whose extension is not installed
	// in the overview's database: the row keeps its cursor slot (so the
	// positions stay fixed) but Enter does nothing, since the reset function
	// would not exist.
	missing bool
}

// maintResetRows are the statistics rows in render order; the names are the
// pendingReset keys the y-confirm dispatches on.
var maintResetRows = []string{"statements", "qualstats", "tablestats", "tablestats-all"}

// key is the row's identity across reloads: the reset name or the advice key.
func (a maintAction) key() string {
	if a.advice != nil {
		return a.advice.Key
	}
	return a.reset
}

// enterLabel names what Enter does on the row for the footer, false when the
// recommendation has nowhere to go.
func (a maintAction) enterLabel() (string, bool) {
	if a.advice == nil {
		return "reset stats", !a.missing
	}
	return adviceTargetLabel(*a.advice)
}

// adviceTargetLabel names the screen Enter opens for a recommendation.
func adviceTargetLabel(a pg.Advice) (string, bool) {
	switch a.Target {
	case pg.AdviceTargetLockTree:
		return "lock tree", true
	case pg.AdviceTargetActivity:
		return "activity", true
	case pg.AdviceTargetSettings:
		return "settings", true
	case pg.AdviceTargetDiagnostic:
		if d, ok := pg.DiagnosticByKey(a.DiagKey); ok {
			return d.Title, true
		}
		return "diagnostic", true
	}
	return "", false
}

// actionRows lists the overview's action rows in render order: every
// recommendation the panel shows (advice.Actionable()), then the reset rows
// of the statistics block that closes the page.
func (st *maintState) actionRows() []maintAction {
	act := st.advice.Actionable()
	rows := make([]maintAction, 0, len(maintResetRows)+len(act))
	for i := range act {
		rows = append(rows, maintAction{advice: &act[i]})
	}
	for _, r := range maintResetRows {
		rows = append(rows, maintAction{reset: r, missing: !st.resetAvailable(r)})
	}
	return rows
}

// resetAvailable reports whether the reset row named r has something to
// reset: the extension rows need their extension installed (which only a
// loaded snapshot can tell); the table-stats rows reset built-in counters.
func (st *maintState) resetAvailable(r string) bool {
	switch r {
	case "statements":
		return st.info != nil && st.info.Statements.Installed
	case "qualstats":
		return st.info != nil && st.info.Qualstats.Installed
	}
	return true
}

// setCursor moves the cursor to row i of rows, remembers the row's key and
// asks the next render to bring it into view.
func (st *maintState) setCursor(i int, rows []maintAction) {
	st.cursor = max(min(i, len(rows)-1), 0)
	st.cursorKey = rows[st.cursor].key()
	st.follow = true
}

// refreshAdvice re-derives the recommendations after a load and puts the
// cursor back on the row it was on, by key; a recommendation that vanished
// leaves it at the same position, clamped to the new list.
func (st *maintState) refreshAdvice() {
	st.advice = pg.MaintAdvice(st.info, st.schema)
	rows := st.actionRows()
	if st.cursorKey != "" {
		for i, r := range rows {
			if r.key() == st.cursorKey {
				st.cursor = i
				return
			}
		}
	}
	st.cursor = max(min(st.cursor, len(rows)-1), 0)
	st.cursorKey = rows[st.cursor].key()
}

// maintEnterLabel is enterLabel for the overview: what Enter does on the
// cursor's action row.
func maintEnterLabel(s *screen) (string, bool) {
	rows := s.maintenance.actionRows()
	if s.maintenance.cursor < 0 || s.maintenance.cursor >= len(rows) {
		return "reset stats", true
	}
	return rows[s.maintenance.cursor].enterLabel()
}
