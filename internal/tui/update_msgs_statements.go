package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

func (m *Model) onStatementsLoaded(msg statementsLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatements)
	if s == nil || s.db != msg.db {
		return nil
	}
	s.loading = false
	s.loaded = true
	if ext := asMissingExt(msg.err); ext != nil {
		s.diagCols = nil
		return setExtensionPrompt(s, ext, extPromptReasonStatStatements)
	}
	if ext := asOutdatedExt(msg.err); ext != nil {
		s.diagCols = nil
		return setUpgradePrompt(s, ext, extPromptReasonStatStatements)
	}
	s.err = msg.err
	if msg.err != nil {
		return nil
	}
	s.stat.sampledAt = time.Now()
	s.stat.trackPlanning = msg.trackPlanning
	// Best-effort "now" magnitude for the L browser's live anchor; updated every tick.
	s.stat.liveCount = len(msg.stats)

	// The very first live sample is the session anchor: the "session start" row
	// in L restores the window from when the tool opened. Captured once,
	// whatever base the entry picker chose — a disk snapshot or the cumulative
	// window installs its own baseline before this load, but the session still
	// started here — and left alone by later live re-bases (R).
	if s.stat.sessionBaseline == nil {
		s.stat.sessionBaseline = make(map[int64]pg.QueryStat, len(msg.stats))
		for _, q := range msg.stats {
			s.stat.sessionBaseline[q.QueryID] = q
		}
		s.stat.sessionStart = s.stat.sampledAt
	}

	// First snapshot becomes the baseline: the window opens here, so there are
	// no deltas to show yet — the table fills in as queries run. A disk baseline
	// (statBaseSnap) is installed before this load, so statBaseline is non-nil
	// and we skip straight to the diff path below.
	if s.stat.baseline == nil {
		if s.stat.sessionStart.Equal(s.stat.sampledAt) {
			// Session-start window: share the anchor's map rather than building
			// the same one twice.
			s.stat.baseline = s.stat.sessionBaseline
		} else {
			s.stat.baseline = make(map[int64]pg.QueryStat, len(msg.stats))
			for _, q := range msg.stats {
				s.stat.baseline[q.QueryID] = q
			}
		}
		s.stat.baselineAt = s.stat.sampledAt
		s.stat.rows = nil
		m.rebuildStatementItems(s)
		return nil
	}

	// A disk baseline can produce negative deltas if the counters were reset
	// between capture and now; clamp them. (Snapshots invalidated this way are
	// already filtered out of the L browser, so this is just defence in depth.)
	if s.stat.baseSnap != nil {
		s.stat.rows = pg.DiffStatementsClamped(s.stat.baseline, msg.stats)
	} else {
		s.stat.rows = pg.DiffStatements(s.stat.baseline, msg.stats)
	}
	// For a cumulative window (empty baseline) the baseline time is the server's
	// last stats reset, not when the tool opened. Update it on every tick so it
	// stays correct if a reset happens while the window is live.
	if s.stat.cumulative && !msg.statsReset.IsZero() {
		s.stat.baselineAt = msg.statsReset
	}
	// rebuildStatementItems preserves the user's chosen sort column (tracked by id)
	// and the current column visibility across refreshes.
	m.rebuildStatementItems(s)
	return nil
}

// rebuildStatementItems regenerates the top-queries table from the already-fetched
// window deltas (s.stat.rows) for the current view (Tab), group narrowing
// (Enter/Esc), column-visibility set and track_planning state — no DB
// round-trip. Used by every load site, the view/narrowing keys and the C
// column-config toggles so the columns, cells, footer, bar and sort stay
// consistent. The time% denominator is always the whole window, whichever
// rows the view shows.
func (m *Model) rebuildStatementItems(s *screen) {
	// First population of this screen: a baseline installed by the entry picker
	// (cumulative, disk snapshot, frozen window) bypasses the live first-load
	// branch above, so the default sort has to be applied here as well —
	// otherwise the screen keeps its zero-value ascending direction.
	if s.stat.cols == nil && s.stat.groupCols == nil {
		m.defaultStatementSort(s)
	}
	windowMs := windowExecMs(s.stat.rows)
	var total []pg.DiagCell
	if v := s.stat.view; v.grouped() {
		var descs []stmtGroupColDesc
		s.items, descs, total = m.buildStatementGroupItems(v, s.stat.rows, windowMs, s.stat.trackPlanning)
		s.stat.groupCols, s.stat.cols = descs, nil
		s.diagCols = diagColumnsFrom(descs)
		stmtGroupSpec(v).syncSort(&m.stmtGroupTable, s, descs)
	} else {
		rows := s.stat.rows
		if s.stat.group != nil {
			rows = make([]pg.QueryStat, 0, len(s.stat.rows))
			for _, q := range s.stat.rows {
				if s.stat.group.matches(q) {
					rows = append(rows, q)
				}
			}
		}
		var descs []stmtColDesc
		s.items, descs, total = m.buildStatementItems(rows, windowMs, s.stat.trackPlanning)
		s.stat.cols, s.stat.groupCols = descs, nil
		s.diagCols = diagColumnsFrom(descs)
		stmtSpec.syncSort(&m.stmtTable, s, descs)
	}
	s.stat.windowExecMs = windowMs
	s.diagTotalRow = total
	s.diagMetricsDirty = true
	m.syncStmtBar(s)
	m.applySort(s)
}

// narrowedCount is the number of window rows the group narrowing keeps (the
// header's "N of M queries"); the whole window when nothing is narrowed.
func (s *screen) narrowedCount() int {
	if s.stat.group == nil {
		return len(s.stat.rows)
	}
	n := 0
	for _, q := range s.stat.rows {
		if s.stat.group.matches(q) {
			n++
		}
	}
	return n
}

// defaultStatementSort puts a freshly opened top-queries table in its default
// order: total_ms descending, whichever column an earlier visit left on the
// shared stmtTable.
func (m *Model) defaultStatementSort(s *screen) {
	m.stmtTable.sortColID = colTotalMs
	s.sortDesc = true
}

// onStatementsTick keeps the live window fresh. It re-samples only while the
// top-queries table is on top, but keeps rescheduling while the user is in its
// detail view too, so the window resumes updating when they return. When the
// user leaves the tool entirely the loop stops (statTicking flips false) until
// loadCurrent restarts it on re-entry.
func (m *Model) onStatementsTick() tea.Cmd {
	top := m.top()
	if top.level != levelStatements && top.level != levelStatementDetail {
		m.statTicking = false
		return nil
	}
	next := m.statementsTick()
	if next == nil {
		// Auto-refresh was disabled or cycled off while the tool was open; stop the
		// loop. Cycling refresh back on (t) or re-entry restarts it.
		m.statTicking = false
		return nil
	}
	if top.level == levelStatements {
		// A frozen A→B diff has no "now" to re-sample — keep the tick alive (so it
		// resumes if the user returns to a live window) but don't reload.
		if top.stat.endSnap != nil {
			return next
		}
		return tea.Batch(m.loadStatementsCmd(top.db), next)
	}
	return next
}

// onSnapshotSaved reports the dump's path (or error) in the transient notice.
func (m *Model) onSnapshotSaved(msg snapshotSavedMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "snapshot failed: " + msg.err.Error()
		return nil
	}
	m.notice = "snapshot saved → " + msg.path
	return nil
}

// onSnapshotsListed fills the snapshots browser with the directory listing.
// Snapshots from the current server/database whose counters have since been
// reset (CapturedAt predates the live stats_reset) are dropped silently — they
// can't serve as a baseline, so there's nothing to load. Snapshots from a
// different server/database are kept but flagged incompatible (dimmed, not
// loadable), since we can't judge their validity.
func (m *Model) onSnapshotsListed(msg snapshotsListedMsg) tea.Cmd {
	s := m.findLevel(levelSnapshots)
	if s == nil {
		return nil
	}
	firstLoad := !s.loaded
	s.loading = false
	s.loaded = true
	s.err = msg.err

	s.stat.liveReset = msg.liveReset

	st := m.findLevel(levelStatements)
	curDB := ""
	if st != nil {
		curDB = st.db
	}

	metas := make([]pg.SnapshotMeta, 0, len(msg.metas))
	for _, meta := range msg.metas {
		compatible := meta.Target == m.target && meta.Database == curDB
		if compatible && !msg.liveReset.IsZero() && meta.CapturedAt.Before(msg.liveReset) {
			continue // invalidated by a stats reset since capture
		}
		metas = append(metas, meta)
	}

	s.stat.snapMetas = metas
	// Synthetic timeline anchors bracket the real snapshots, newest→oldest: "now"
	// (live end) at the top, then "session start" (the in-memory baseline from when
	// the tool opened) when we have one, the saved snapshots, and "since last reset"
	// (cumulative origin) at the bottom. The anchors use sentinel paths (@now /
	// @session / @reset) that can't match real file paths, so metaByPath returns
	// false for them and D (delete) is a safe no-op.
	//
	// The entry picker (tool just opened, nothing sampled yet) is the same list
	// minus "now": the end is always live there, and "session start" *is* now —
	// it becomes the first baseline the moment the pick lands, so it is listed
	// unconditionally and barred with the live statement count.
	items := make([]item, 0, len(metas)+3)
	liveCount := int64(msg.liveCount)
	if liveCount == 0 && st != nil {
		liveCount = int64(st.stat.liveCount)
	}
	if !s.stat.entry {
		items = append(items, item{name: "now · live", snapPath: snapNow, size: liveCount})
	}
	if s.stat.entry {
		items = append(items, item{name: "session start", snapPath: snapSession, size: liveCount})
	} else if st != nil && !st.stat.sessionStart.IsZero() {
		items = append(items, item{
			name:     "session start",
			snapPath: snapSession,
			size:     int64(len(st.stat.sessionBaseline)),
		})
	}
	for _, meta := range metas {
		items = append(items, item{
			name:     snapshotLabel(meta),
			size:     int64(meta.QueryCount),
			snapPath: meta.Path,
		})
	}
	items = append(items, item{name: "since last reset · cumulative", snapPath: snapReset})
	s.items = items
	s.itemsRev++ // doesn't go through applySort; invalidate the filter cache
	// The entry picker opens on its default, the session-start window.
	if s.stat.entry && firstLoad {
		s.cursor = max(slices.IndexFunc(items, func(it item) bool { return it.snapPath == snapSession }), 0)
	}
	// Clamp the cursor: a delete (or filter) can shrink the list out from under it.
	if s.cursor >= len(s.items) {
		s.cursor = max(len(s.items)-1, 0)
	}
	return nil
}

// onSnapshotBaseLoaded installs a disk snapshot as the live window's baseline,
// then re-samples so the table shows everything since the snapshot till now.
func (m *Model) onSnapshotBaseLoaded(msg snapshotBaseLoadedMsg) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}
	if msg.err != nil || msg.snap == nil {
		m.notice = "load snapshot failed: " + errText(msg.err)
		m.abandonSnapshotPick()
		return nil
	}
	st.stat.baseSnap = msg.snap
	st.stat.cumulative = false
	st.stat.endSnap = nil
	st.stat.baseline = msg.snap.BaselineMap()
	st.stat.baselineAt = msg.snap.CapturedAt
	m.popToStatements()
	return m.loadCurrent()
}

// onSnapshotFrozenLoaded builds a frozen diff: either a real A→B snapshot diff or
// a cumulative "since last reset → snapshot" window (msg.cumulative == true, base nil).
func (m *Model) onSnapshotFrozenLoaded(msg snapshotFrozenLoadedMsg) tea.Cmd {
	st := m.findLevel(levelStatements)
	if st == nil {
		return nil
	}
	if msg.err != nil || msg.end == nil {
		m.notice = "load snapshots failed: " + errText(msg.err)
		m.abandonSnapshotPick()
		return nil
	}
	if msg.cumulative {
		// Empty baseline — the diff against nothing yields the raw cumulative counters
		// as they stood at the snapshot's capture time.
		st.stat.baseSnap = nil
		st.stat.cumulative = true
		st.stat.baseline = map[int64]pg.QueryStat{}
		st.stat.baselineAt = msg.end.StatsReset // zero when unknown
		st.stat.endSnap = msg.end
		st.stat.sampledAt = msg.end.CapturedAt
		st.stat.trackPlanning = msg.end.TrackPlanning
	} else {
		if msg.base == nil {
			m.notice = "load snapshots failed: base snapshot missing"
			m.abandonSnapshotPick()
			return nil
		}
		st.stat.baseSnap = msg.base
		st.stat.cumulative = false
		st.stat.endSnap = msg.end
		st.stat.baseline = msg.base.BaselineMap()
		st.stat.baselineAt = msg.base.CapturedAt
		st.stat.sampledAt = msg.end.CapturedAt
		st.stat.trackPlanning = msg.base.TrackPlanning && msg.end.TrackPlanning
	}
	// A reset between the two captures yields negative deltas; clamping floors them.
	m.populateFrozenWindow(st)
	// A pick that landed as the end keeps the browser open (its markers now show
	// the frozen range) so the user can immediately pick the matching start.
	if !msg.stay {
		m.popToStatements()
	}
	return nil
}

// populateFrozenWindow recomputes a frozen window's rows/items from statBaseline and
// statEndSnap. Using statBaseline directly (instead of re-deriving it from statBaseSnap)
// means the cumulative case (empty baseline, no base snapshot) also works here.
func (m *Model) populateFrozenWindow(st *screen) {
	st.stat.rows = pg.DiffStatementsClamped(st.stat.baseline, st.stat.endSnap.Stats)
	m.rebuildStatementItems(st)
	st.loading = false
	st.loaded = true
}

// popToStatements unwinds the screen stack back to the top-queries table,
// dropping any snapshots-browser screen pushed on top of it.
func (m *Model) popToStatements() {
	for len(m.stack) > 1 && m.top().level != levelStatements {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

// abandonSnapshotPick returns to the table after a snapshot pick failed to load.
// From the entry picker there is no table to return to — it has never loaded —
// so the browser stays up (the notice explains why) for another pick.
func (m *Model) abandonSnapshotPick() {
	if top := m.top(); top.level == levelSnapshots && top.stat.entry {
		return
	}
	m.popToStatements()
}

func (m *Model) onStatementSampleLoaded(msg statementSampleLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.stat.detail == nil || s.stat.detail.Query != msg.query {
		return nil
	}
	s.stat.sampleCall = msg.sample
	s.stat.sampleParams = msg.params
	s.stat.sampleReal = msg.real
	s.stat.sampleFromData = msg.fromData
	s.stat.sampleFromQual = msg.fromQual
	s.stat.qualstats = msg.qualstats
	s.stat.sampleErr = msg.err
	// Offer a one-key install when pg_qualstats is absent but already preloaded —
	// then CREATE EXTENSION alone unlocks real values. Otherwise drop any stale
	// qualstats prompt (e.g. after the user just installed it out of band).
	if !msg.qualstats && msg.installable {
		s.extPrompt = &extPrompt{
			name:        extQualstats,
			db:          s.db,
			installable: true,
			reason:      extPromptReasonQualstats,
			blocking:    false,
		}
	} else if s.extPrompt != nil && s.extPrompt.name == extQualstats {
		s.extPrompt = nil
	}
	// Auto-run the plan once the sample source is known (set up at drill-in,
	// where statExplaining was flipped on). A real sample → plain EXPLAIN on it;
	// otherwise the generic plan, which doesn't need the sample at all, so it
	// still runs when parameter inference failed.
	if s.stat.explaining {
		return m.statementPlanCmd(s)
	}
	return nil
}

// onStatementHotLoaded stores the main table's HOT-update counters for the
// detail view. The query-text guard rejects a result that arrives after the
// user has drilled into a different statement. A fetch error is kept quiet —
// the HOT row is simply omitted rather than cluttering a secondary metric.
func (m *Model) onStatementHotLoaded(msg statementHotLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.stat.detail == nil || s.stat.detail.Query != msg.query {
		return nil
	}
	s.stat.hotStats = msg.stats
	s.stat.hotErr = msg.err
	return nil
}

func (m *Model) onExportDone(msg exportDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "export failed: " + msg.err.Error()
		return nil
	}
	m.notice = fmt.Sprintf("exported %d rows → %s", msg.rows, msg.path)
	return nil
}

func (m *Model) onStatementSamplesLoaded(msg statementSamplesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementSamples)
	if s == nil || s.stat.detail == nil || s.stat.detail.QueryID != msg.queryID {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = sampleItems(msg.samples)
	s.itemsRev++ // doesn't go through applySort; invalidate the filter cache
	s.cursor, s.offset = 0, 0
	return nil
}

// onStatementResultLoaded fills the executed-query result table. It mirrors
// onDiagnosticLoaded's row→item projection so the shared renderDiagResult,
// generic sort and CSV export all work, but finds its target by the executed
// query text (the screen carries no Diagnostic).
func (m *Model) onStatementResultLoaded(msg statementResultLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementResult)
	if s == nil || s.stat.detail == nil || s.stat.detail.Query != msg.query {
		return nil
	}
	s.loading = false
	s.loaded = true
	s.err = msg.err
	s.items = s.items[:0]
	if msg.err != nil || msg.result == nil {
		return nil
	}
	s.diagCols = msg.result.Columns
	s.diagBarCol = -1
	s.diagSortCol = 0
	s.sortDesc = false
	// item.name is the space-joined cell display so the fuzzy filter can match
	// any column value; data carries the row for the renderer and CSV export.
	for _, row := range msg.result.Rows {
		parts := make([]string, len(row))
		for i, cell := range row {
			parts[i] = cell.Display
		}
		s.items = append(s.items, item{name: strings.Join(parts, " "), data: row})
	}
	s.diagMetricsDirty = true
	m.applySort(s)
	if msg.truncated {
		m.notice = fmt.Sprintf("showing first %d rows", statementResultMaxRows)
	}
	return nil
}

// sampleItems maps captured pg_qualstats constants onto list items. The bar
// magnitude (item.size) is the occurrence count, so the frequency pattern is
// visible at a glance; data carries the QualSample for the Enter action. name
// is the readable predicate, which also drives the fuzzy filter.
func sampleItems(samples []pg.QualSample) []item {
	out := make([]item, len(samples))
	for i, sm := range samples {
		out[i] = item{name: sampleLabel(sm), size: sm.Occurrences, data: sm}
	}
	return out
}

// sampleLabel renders a captured qual as "table.column op value", falling back
// to bare value (then "=") when pg_qualstats couldn't resolve the left side. A
// value cut at the extension's 80-byte constant buffer says so, since what is
// shown is only the head of the real constant.
func sampleLabel(sm pg.QualSample) string {
	val := sm.ConstValue
	if sm.Truncated {
		val += "… (cut at 80 chars by pg_qualstats)"
	}
	if sm.Column == "" {
		return val
	}
	col := sm.Column
	if sm.Relation != "" {
		col = sm.Relation + "." + sm.Column
	}
	op := sm.Operator
	if op == "" {
		op = "="
	}
	return col + " " + op + " " + val
}

func (m *Model) onStatementExplainLoaded(msg statementExplainLoadedMsg) tea.Cmd {
	// The EXPLAIN can be launched from either the detail view or the captured-
	// values (samples) view — each carries its own statExplaining/statExplain.
	// Route the result to the screen that actually started it, otherwise the
	// samples view stays stuck on "running EXPLAIN ANALYZE…" while the plan lands
	// on the hidden detail screen below it.
	s := m.findExplainTarget(msg.query)
	if s == nil {
		return nil
	}
	s.stat.explaining = false
	s.stat.explain = msg.plan
	s.stat.explainErr = msg.err
	s.stat.explainAnalyze = msg.analyze
	return nil
}

// findExplainTarget returns the topmost statement screen whose EXPLAIN is in
// flight for the given normalized query — the one that issued the request.
func (m *Model) findExplainTarget(query string) *screen {
	for _, s := range slices.Backward(m.stack) {

		if s.level != levelStatementDetail && s.level != levelStatementSamples {
			continue
		}
		if s.stat.explaining && s.stat.detail != nil && s.stat.detail.Query == query {
			return s
		}
	}
	return nil
}
