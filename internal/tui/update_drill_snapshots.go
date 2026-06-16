package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// open so the matching start can be picked next.
//
// The synthetic sentinel anchors (@now / @session / @reset) are valid endpoints
// and are always compatible (they don't carry a server/db identity). Real
// snapshot rows are checked against the current target/db.
func (m *Model) loadSelectedSnapshot(s *screen, cur item) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}

	// The anchor is the applied window's start; "" (no explicit start) and a
	// re-picked anchor both mean the pick spans pick → now.
	anchor, _ := m.appliedWindowPaths(st, s)
	startPath, endPath := cur.snapPath, snapNow
	if anchor != "" && anchor != snapSession && anchor != cur.snapPath {
		startPath, endPath = anchor, cur.snapPath
	}

	// Compatibility check: real snapshots must match the current server/db. The
	// synthetic anchors (now / session / reset) carry no server-db identity and are
	// always usable for the current database.
	for _, path := range []string{startPath, endPath} {
		if path == snapNow || path == snapReset || path == snapSession {
			continue
		}
		meta, ok := metaByPath(s.statSnapMetas, path)
		if !ok {
			return nil
		}
		if meta.Target != m.target || meta.Database != st.db {
			m.notice = "snapshot is from a different server/database — can't diff this window"
			return nil
		}
	}

	// Order oldest→newest so the delta (end − start) is non-negative. The pick
	// "lands" as whichever side it ends up on; an end pick keeps the browser open.
	stay := false
	if m.snapTime(s, endPath).Before(m.snapTime(s, startPath)) {
		startPath, endPath = endPath, startPath
	}
	if endPath == cur.snapPath && endPath != snapNow {
		stay = true
	}

	// The session anchor is an in-memory baseline with no Stats slice, so it can
	// only restore the live "since session start → now" window — never a frozen
	// endpoint. Reject any pairing that would put it on either side of a diff.
	if (startPath == snapSession || endPath == snapSession) && (startPath != snapSession || endPath != snapNow) {
		m.notice = "session start can only be diffed against now"
		return nil
	}

	// Dispatch by end. snapNow → live window; snapshot → frozen diff.
	switch endPath {
	case snapNow:
		switch startPath {
		case snapReset:
			// Cumulative live: empty baseline, table grows with each refresh tick.
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = true
			st.statBaseline = map[int64]pg.QueryStat{}
			st.statBaselineAt = time.Time{} // will be updated by the first statementsLoadedMsg
			m.popToStatements()
			return m.loadCurrent()
		case snapSession:
			// Restore the original session window: re-install the preserved baseline
			// and re-sample live, so the table shows everything since the tool opened.
			st.statBaseline = st.statSessionBaseline
			st.statBaselineAt = st.statSessionStart
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = false
			m.popToStatements()
			return m.loadCurrent()
		case snapNow:
			// Both now → fresh live-from-now (equivalent to R).
			st.statBaseline = nil
			st.statBaseSnap = nil
			st.statEndSnap = nil
			st.statCumulative = false
			m.popToStatements()
			return m.loadCurrent()
		default:
			// Snapshot → now: existing base→live flow.
			return m.loadSnapshotBaseCmd(startPath)
		}
	default:
		// Frozen window: load snapshots for the diff (snapReset base handled inside).
		return m.loadSnapshotFrozenCmd(startPath, endPath, stay)
	}
}

// appliedWindowPaths maps the statements screen's window state onto snapshot
// browser row paths, so the key handler (the pairing anchor) and the view (the
// start/end markers) agree on which rows the applied window touches. The start
// is "" for a window with no representable row — a fresh R re-base, whose
// baseline is neither the session anchor nor any snapshot.
func (m *Model) appliedWindowPaths(st, s *screen) (startPath, endPath string) {
	endPath = snapNow
	if st.statEndSnap != nil {
		if meta, ok := metaByCapturedAt(s.statSnapMetas, st.statEndSnap.CapturedAt); ok {
			endPath = meta.Path
		} else {
			endPath = ""
		}
	}
	switch {
	case st.statCumulative:
		startPath = snapReset
	case st.statBaseSnap != nil:
		if meta, ok := metaByCapturedAt(s.statSnapMetas, st.statBaseSnap.CapturedAt); ok {
			startPath = meta.Path
		}
	case !st.statSessionStart.IsZero() && st.statBaselineAt.Equal(st.statSessionStart):
		startPath = snapSession
	}
	return startPath, endPath
}

// metaByCapturedAt finds the snapshot meta with the given capture time — how a
// loaded *pg.Snapshot (which doesn't carry its file path) is matched back to
// its browser row.
func metaByCapturedAt(metas []pg.SnapshotMeta, at time.Time) (pg.SnapshotMeta, bool) {
	for _, meta := range metas {
		if meta.CapturedAt.Equal(at) {
			return meta, true
		}
	}
	return pg.SnapshotMeta{}, false
}

// snapTime returns the time associated with a snapshot browser path for the
// purpose of ordering a start/end pair. snapReset maps to the zero time (earliest);
// snapNow maps to a far-future time (latest); snapSession to the recorded session
// start; real snapshot paths use CapturedAt.
func (m *Model) snapTime(s *screen, path string) time.Time {
	switch path {
	case snapReset:
		return time.Time{} // zero = year 1 = earliest
	case snapNow:
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	case snapSession:
		if st := m.findLevel(levelStatements); st != nil {
			return st.statSessionStart
		}
		return time.Time{}
	default:
		meta, ok := metaByPath(s.statSnapMetas, path)
		if !ok {
			return time.Time{}
		}
		return meta.CapturedAt
	}
}

// metaByPath finds the snapshot meta with the given file path.
func metaByPath(metas []pg.SnapshotMeta, path string) (pg.SnapshotMeta, bool) {
	for _, meta := range metas {
		if meta.Path == path {
			return meta, true
		}
	}
	return pg.SnapshotMeta{}, false
}
