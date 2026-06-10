package tui

import (
	"fmt"
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
	s.statSampledAt = time.Now()
	s.statTrackPlanning = msg.trackPlanning
	// Best-effort "now" magnitude for the L browser's live anchor; updated every tick.
	s.statLiveCount = len(msg.stats)

	// First snapshot becomes the baseline: the window opens here, so there are
	// no deltas to show yet — the table fills in as queries run. A disk baseline
	// (statBaseSnap) is installed before this load, so statBaseline is non-nil
	// and we skip straight to the diff path below.
	if s.statBaseline == nil {
		s.statBaseline = make(map[int64]pg.QueryStat, len(msg.stats))
		for _, q := range msg.stats {
			s.statBaseline[q.QueryID] = q
		}
		s.statBaselineAt = s.statSampledAt
		// Preserve this very first baseline as the session anchor: the "session
		// start" row in L restores it even after a disk baseline replaces
		// statBaseline. Captured once — later live re-bases (R) keep the original.
		if s.statSessionBaseline == nil {
			s.statSessionBaseline = s.statBaseline
			s.statSessionStart = s.statBaselineAt
		}
		s.statRows = nil
		s.items = s.items[:0]
		s.statWindowExecMs = 0
		descs := m.visibleStmtCols(stmtCtx{trackPlanning: s.statTrackPlanning})
		s.stmtCols = descs
		s.diagCols = diagColumnsFrom(descs)
		s.diagBarCol = -1
		m.stmtSortColID = colTotalMs
		s.sortDesc = true
		m.syncStmtSort(s, descs)
		return nil
	}

	// A disk baseline can produce negative deltas if the counters were reset
	// between capture and now; clamp them. (Snapshots invalidated this way are
	// already filtered out of the L browser, so this is just defence in depth.)
	if s.statBaseSnap != nil {
		s.statRows = pg.DiffStatementsClamped(s.statBaseline, msg.stats)
	} else {
		s.statRows = pg.DiffStatements(s.statBaseline, msg.stats)
	}
	// For a cumulative window (empty baseline) the baseline time is the server's
	// last stats reset, not when the tool opened. Update it on every tick so it
	// stays correct if a reset happens while the window is live.
	if s.statCumulative && !msg.statsReset.IsZero() {
		s.statBaselineAt = msg.statsReset
	}
	// rebuildStatementItems preserves the user's chosen sort column (tracked by id)
	// and the current column visibility across refreshes.
	m.rebuildStatementItems(s)
	return nil
}

// rebuildStatementItems regenerates the top-queries table from the already-fetched
// window deltas (s.statRows) for the current column-visibility set and
// track_planning state — no DB round-trip. Used by every load site and by the C
// column-config toggles so the columns, cells, footer and sort stay consistent.
func (m *Model) rebuildStatementItems(s *screen) {
	items, descs, windowMs, total := m.buildStatementItems(s.statRows, s.statTrackPlanning)
	s.items = items
	s.stmtCols = descs
	s.diagCols = diagColumnsFrom(descs)
	s.statWindowExecMs = windowMs
	s.diagTotalRow = total
	s.diagBarCol = -1
	s.diagMetricsDirty = true
	m.syncStmtSort(s, descs)
	m.applySort(s)
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
		if top.statEndSnap != nil {
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
	s.loading = false
	s.loaded = true
	s.err = msg.err

	s.statLiveReset = msg.liveReset

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

	s.statSnapMetas = metas
	// Synthetic timeline anchors bracket the real snapshots, newest→oldest: "now"
	// (live end) at the top, then "session start" (the in-memory baseline from when
	// the tool opened) when we have one, the saved snapshots, and "since last reset"
	// (cumulative origin) at the bottom. The anchors use sentinel paths (@now /
	// @session / @reset) that can't match real file paths, so metaByPath returns
	// false for them and D (delete) is a safe no-op.
	items := make([]item, 0, len(metas)+3)
	now := item{name: "now · live", snapPath: snapNow}
	if st != nil {
		now.size = int64(st.statLiveCount)
	}
	items = append(items, now)
	if st != nil && !st.statSessionStart.IsZero() {
		items = append(items, item{
			name:     "session start",
			snapPath: snapSession,
			size:     int64(len(st.statSessionBaseline)),
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
		m.popToStatements()
		return nil
	}
	st.statBaseSnap = msg.snap
	st.statCumulative = false
	st.statEndSnap = nil
	st.statBaseline = msg.snap.BaselineMap()
	st.statBaselineAt = msg.snap.CapturedAt
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
		m.popToStatements()
		return nil
	}
	if msg.cumulative {
		// Empty baseline — the diff against nothing yields the raw cumulative counters
		// as they stood at the snapshot's capture time.
		st.statBaseSnap = nil
		st.statCumulative = true
		st.statBaseline = map[int64]pg.QueryStat{}
		st.statBaselineAt = msg.end.StatsReset // zero when unknown
		st.statEndSnap = msg.end
		st.statSampledAt = msg.end.CapturedAt
		st.statTrackPlanning = msg.end.TrackPlanning
	} else {
		if msg.base == nil {
			m.notice = "load snapshots failed: base snapshot missing"
			m.popToStatements()
			return nil
		}
		st.statBaseSnap = msg.base
		st.statCumulative = false
		st.statEndSnap = msg.end
		st.statBaseline = msg.base.BaselineMap()
		st.statBaselineAt = msg.base.CapturedAt
		st.statSampledAt = msg.end.CapturedAt
		st.statTrackPlanning = msg.base.TrackPlanning && msg.end.TrackPlanning
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
	st.statRows = pg.DiffStatementsClamped(st.statBaseline, st.statEndSnap.Stats)
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

func (m *Model) onStatementSampleLoaded(msg statementSampleLoadedMsg) tea.Cmd {
	s := m.findLevel(levelStatementDetail)
	if s == nil || s.statDetail == nil || s.statDetail.Query != msg.query {
		return nil
	}
	s.statSampleCall = msg.sample
	s.statSampleReal = msg.real
	s.statSampleFromData = msg.fromData
	s.statQualstats = msg.qualstats
	s.statSampleErr = msg.err
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
	if s.statExplaining {
		return m.statementPlanCmd(s)
	}
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
	if s == nil || s.statDetail == nil || s.statDetail.QueryID != msg.queryID {
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
	if s == nil || s.statDetail == nil || s.statDetail.Query != msg.query {
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
// to bare value (then "=") when pg_qualstats couldn't resolve the left side.
func sampleLabel(sm pg.QualSample) string {
	if sm.Column == "" {
		return sm.ConstValue
	}
	col := sm.Column
	if sm.Relation != "" {
		col = sm.Relation + "." + sm.Column
	}
	op := sm.Operator
	if op == "" {
		op = "="
	}
	return col + " " + op + " " + sm.ConstValue
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
	s.statExplaining = false
	s.statExplain = msg.plan
	s.statExplainErr = msg.err
	s.statExplainAnalyze = msg.analyze
	return nil
}

// findExplainTarget returns the topmost statement screen whose EXPLAIN is in
// flight for the given normalized query — the one that issued the request.
func (m *Model) findExplainTarget(query string) *screen {
	for i := len(m.stack) - 1; i >= 0; i-- {
		s := m.stack[i]
		if s.level != levelStatementDetail && s.level != levelStatementSamples {
			continue
		}
		if s.statExplaining && s.statDetail != nil && s.statDetail.Query == query {
			return s
		}
	}
	return nil
}
