package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

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
		meta, ok := metaByPath(s.stat.snapMetas, path)
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

	// Refuse a window with a counter reset inside it. The deltas would be
	// clamped fiction, and the reset is the one thing the browser knows about
	// that the diff itself cannot recover.
	if reset, straddled := m.snapStraddledReset(s, startPath, endPath); straddled {
		m.notice = "counters were reset " + reset.Local().Format("2006-01-02 15:04:05") +
			", inside this window — pick two snapshots from the same side of it"
		return nil
	}

	// Dispatch by end. snapNow → live window; snapshot → frozen diff.
	switch endPath {
	case snapNow:
		switch startPath {
		case snapReset:
			// Cumulative live: empty baseline, table grows with each refresh tick.
			st.stat.baseSnap = nil
			st.stat.endSnap = nil
			st.stat.cumulative = true
			st.stat.baseline = map[int64]pg.QueryStat{}
			st.stat.baselineAt = time.Time{} // will be updated by the first statementsLoadedMsg
			m.popToStatements()
			return m.loadCurrent()
		case snapSession:
			// Restore the original session window: re-install the preserved baseline
			// and re-sample live, so the table shows everything since the tool opened.
			st.stat.baseline = st.stat.sessionBaseline
			st.stat.baselineAt = st.stat.sessionStart
			st.stat.baseSnap = nil
			st.stat.endSnap = nil
			st.stat.cumulative = false
			m.popToStatements()
			return m.loadCurrent()
		case snapNow:
			// Both now → fresh live-from-now (equivalent to R).
			st.stat.baseline = nil
			st.stat.baseSnap = nil
			st.stat.endSnap = nil
			st.stat.cumulative = false
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
	if st.stat.endSnap != nil {
		if meta, ok := metaByCapturedAt(s.stat.snapMetas, st.stat.endSnap.CapturedAt); ok {
			endPath = meta.Path
		} else {
			endPath = ""
		}
	}
	switch {
	case st.stat.cumulative:
		startPath = snapReset
	case st.stat.baseSnap != nil:
		if meta, ok := metaByCapturedAt(s.stat.snapMetas, st.stat.baseSnap.CapturedAt); ok {
			startPath = meta.Path
		}
	case !st.stat.sessionStart.IsZero() && st.stat.baselineAt.Equal(st.stat.sessionStart):
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
			return st.stat.sessionStart
		}
		return time.Time{}
	default:
		meta, ok := metaByPath(s.stat.snapMetas, path)
		if !ok {
			return time.Time{}
		}
		return meta.CapturedAt
	}
}

// preResetSnaps collects the snapshots of (target, db) whose counters a
// pg_stat_statements reset has since zeroed, with their total on-disk size.
//
// A zero liveReset means the server never reported one (pre-PG14, or the
// best-effort read failed), which reads as "no reset known" and leaves every
// snapshot usable — the same way a zero StatsReset does in snapStraddledReset.
// Being permissive on an unknown is the only safe direction here: this set is
// what P deletes.
func preResetSnaps(metas []pg.SnapshotMeta, target, db string, liveReset time.Time) (map[string]bool, int64) {
	if liveReset.IsZero() {
		return nil, 0
	}
	var paths map[string]bool
	var bytes int64
	for _, meta := range metas {
		if meta.Target != target || meta.Database != db {
			continue // another server/db: its own reset is none of our business
		}
		if !meta.CapturedAt.Before(liveReset) {
			continue
		}
		if paths == nil {
			paths = make(map[string]bool)
		}
		paths[meta.Path] = true
		bytes += meta.FileSize
	}
	return paths, bytes
}

// snapStraddledReset reports the pg_stat_statements reset that falls inside the
// window startPath→endPath, if any: the counters went to zero in between, so
// every delta the window would show is fiction.
//
// It is the pairing rule that replaced the browser's old habit of hiding
// pre-reset snapshots outright. The filter guaranteed no window could straddle
// a reset by making the invalid endpoints unreachable; this earns the same
// guarantee per pair, which is strictly narrower — a pair that predates the
// reset is fine, and the filter refused it anyway.
//
// The reset to compare against comes from the END of the window: a live end
// (@now) uses the server's current stats_reset, a snapshot end the one it
// recorded at capture. That is what makes a chain of old resets tractable with
// only the last one in hand — each snapshot carries the reset that was current
// when it was taken. A zero on either side is "unknown", which stays permissive
// (DiffStatementsClamped floors the negative deltas a missed reset would
// produce, so being wrong here is a dubious window, not a broken one).
func (m *Model) snapStraddledReset(s *screen, startPath, endPath string) (time.Time, bool) {
	start, ok := metaByPath(s.stat.snapMetas, startPath)
	if !ok {
		return time.Time{}, false // @reset / @session carry their own rules
	}
	reset := s.stat.liveReset
	if end, ok := metaByPath(s.stat.snapMetas, endPath); ok {
		reset = end.StatsReset
	}
	if reset.IsZero() || !reset.After(start.CapturedAt) {
		return time.Time{}, false
	}
	return reset, true
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
