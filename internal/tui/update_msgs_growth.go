package tui

import (
	"errors"
	"slices"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/humanize"
	"pgdu/internal/pg"
)

// onSizeSamplesListed fills the sample browser. The listing is already newest
// first; the live "now" anchor is prepended except in entry mode, where the
// pick is always the window's start and "now" is implied.
//
// Unlike the query-snapshot browser nothing is filtered out: sizes have no
// reset to go stale against, so even the oldest sample is listable — which is
// the point of a history tool. Incompatible samples are flagged by the
// renderer rather than hidden, so a restored cluster is explained.
func (m *Model) onSizeSamplesListed(msg sizeSamplesListedMsg) tea.Cmd {
	s := m.findLevel(levelGrowthWindow)
	if s == nil || s.db != msg.db {
		return nil
	}
	first := !s.loaded
	s.loading, s.loaded = false, true
	s.err = msg.err
	s.growth.metas = msg.metas
	s.growth.liveOID = msg.liveOID
	s.growth.liveSystemID = msg.liveSystemID
	s.growth.liveTotal = msg.liveTotal

	items := make([]item, 0, len(msg.metas)+1)
	if !s.growth.entry {
		items = append(items, item{
			name:     "now · live",
			size:     msg.liveTotal,
			snapPath: growthNow,
		})
	}
	for _, meta := range msg.metas {
		items = append(items, item{
			name:     sizeSampleLabel(meta),
			size:     meta.TotalBytes,
			snapPath: meta.Path,
		})
	}
	s.items = items
	// Bypass applySort: the browser is a timeline, so its order is the
	// listing's, not a sort mode's.
	s.itemsRev++
	s.visCacheOK = false

	if first && s.growth.entry {
		// Entry mode has no @session to default to, so start on the newest
		// sample — the shortest, most meaningful window.
		if i := slices.IndexFunc(items, func(it item) bool { return it.snapPath != growthNow }); i >= 0 {
			s.cursor = i
		}
	}
	if s.cursor >= len(items) {
		s.cursor = max(len(items)-1, 0)
	}
	// Opening the tool takes the day's sample if nothing has. This hangs off the
	// listing rather than loadCurrent because only here are both halves of "the
	// tool just opened for this db" visible: entry marks the browser the tool
	// pushes first, first marks its first load, and the listing that just landed
	// is the free answer to whether today already has one. first is what keeps
	// the re-list a capture triggers from firing a second one.
	if first && s.growth.entry && msg.err == nil {
		return m.autoCaptureSizesCmd(s.db, msg.metas)
	}
	return nil
}

// onSizeSampleCaptured handles an S or auto capture (written to disk, so the
// browser re-lists to show it) and the live end of an applied window.
func (m *Model) onSizeSampleCaptured(msg sizeSampleCapturedMsg) tea.Cmd {
	m.setCapturingSizes(false)
	if msg.err != nil {
		// A failed auto capture says nothing: the user never asked for it, and
		// a cluster this role cannot scan would otherwise greet them with an
		// error banner on every tool open. S still reports in full.
		if msg.mode != captureAuto {
			m.notice = "capture sizes: " + msg.err.Error()
		}
		// Unstick the table only if this capture was its load (the @now end of
		// a window). An auto capture fired from the browser must not mark the
		// still-unpicked table loaded.
		if s := m.findLevel(levelGrowth); s != nil && s.loading {
			s.loading = false
			s.loaded = true
		}
		return nil
	}

	if msg.mode != captureLive {
		if msg.mode == captureAuto {
			// No path: that is S's receipt for something the user asked for.
			m.notice = "auto-sample: today's size sample captured"
		} else {
			m.notice = "captured " + formatRows(int64(msg.sample.RowCount)) + " relations · " +
				humanize.Bytes(msg.sample.TotalBytes) + " · " + msg.path
		}
		// Re-list so the new sample joins the browser immediately.
		if s := m.findLevel(levelGrowthWindow); s != nil {
			return m.listSizeSamplesCmd(m.snapshotDir, s.db)
		}
		return nil
	}

	// The live end of an applied window: re-diff against the kept baseline.
	s := m.findLevel(levelGrowth)
	if s == nil || s.growth.base == nil {
		return nil
	}
	s.loading, s.loaded = false, true
	res, err := pg.DiffSizes(s.growth.base, msg.sample)
	if err != nil {
		s.err = err
		return nil
	}
	s.err = nil
	s.growth.end = msg.sample
	return m.applyGrowthResult(s, res)
}

// onGrowthWindowLoaded installs a picked window, or reports why it could not
// be. A refused window never becomes the applied one, so no error state has to
// live on growthState.
func (m *Model) onGrowthWindowLoaded(msg growthWindowLoadedMsg) tea.Cmd {
	s := m.findLevel(levelGrowth)
	if s == nil || s.db != msg.db {
		return nil
	}
	if msg.err != nil {
		// A too-short or mismatched window is a notice, not a screen of error:
		// the user is mid-pick and the previous window is still valid.
		m.notice = growthWindowError(msg.err)
		m.abandonGrowthPick()
		return nil
	}

	s.loading, s.loaded = false, true
	s.err = nil
	s.growth.base, s.growth.end = msg.base, msg.end
	s.growth.basePath, s.growth.endPath = msg.basePath, msg.endPath
	// A fresh window starts at the leaf scope with no narrowing: the previous
	// window's groups may not exist in this one.
	s.growth.group = nil
	cmd := m.applyGrowthResult(s, msg.result)

	if !msg.stay {
		m.popToGrowth()
	}
	return cmd
}

// applyGrowthResult installs a diff and rebuilds the table, and returns the
// command that fills in the trend column behind it — nil when the history it
// already holds is still current.
func (m *Model) applyGrowthResult(s *screen, res *pg.GrowthResult) tea.Cmd {
	s.growth.window = res.Window
	s.growth.rows = res.Rows
	s.growth.other = res.Other
	cmd := m.growthSeriesCmd(s)
	m.rebuildGrowthItems(s)
	s.resetCursor()
	return cmd
}

// growthSeriesCmd asks for the trend column's history when what is on hand no
// longer matches what is on disk.
//
// The guard is the point. levelGrowth re-captures on every refresh tick when
// the window ends @now, and re-reading a bucket's worth of sample files each
// time would turn a cheap refresh into a disk sweep. The sample set only
// changes when a capture writes a file, so comparing it is what lets the
// history be loaded once and kept.
func (m *Model) growthSeriesCmd(s *screen) tea.Cmd {
	if m.snapshotDir == "" {
		s.growth.trend.state = trendNone
		s.growth.trend.note = "no snapshot directory"
		return nil
	}
	paths := growthSeriesPaths(m.snapshotDir, s.db, m.target)
	if s.growth.trend.state != trendIdle && samePaths(s.growth.trend.paths, paths) {
		return nil
	}
	s.growth.trend.state = trendLoading
	return m.loadGrowthSeriesCmd(m.snapshotDir, s.db, m.target, s.growth.end, s.growth.endPath)
}

// samePaths compares two sample listings. ListSizeSamples orders by capture
// time, so equal length and equal order means the same set.
func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// onGrowthSeriesLoaded installs the trend column's history.
//
// A failure is not fatal and never reaches s.err: the trend is an enrichment
// over a window that is already drawn, so the column withdraws and the header
// says why rather than replacing the table with an error.
func (m *Model) onGrowthSeriesLoaded(msg growthSeriesLoadedMsg) tea.Cmd {
	s := m.findLevel(levelGrowth)
	if s == nil || s.db != msg.db {
		return nil
	}
	t := &s.growth.trend
	t.paths = msg.paths
	switch {
	case msg.err != nil:
		t.state, t.hist, t.note = trendNone, nil, "history unreadable"
	case msg.series == nil:
		t.state, t.hist = trendNone, nil
		t.note = "one sample so far — capture another to see a trend"
	default:
		t.state, t.hist, t.note = trendReady, msg.series, ""
	}
	// Re-project the whole table rather than patching the trend cells: the
	// rows are re-read from s.growth.rows, so there is no row to match by
	// name and no way for the cells to drift out of step with diagCols.
	m.rebuildGrowthItems(s)
	return nil
}

// growthWindowError phrases a refused window for the status line. The
// refusals are deliberate and each has a concrete remedy, so neither should
// surface as a raw error string.
func growthWindowError(err error) string {
	switch {
	case errors.Is(err, pg.ErrSampleMismatch):
		return "those samples describe different databases — can't diff this window"
	case errors.Is(err, pg.ErrSampleVersion):
		return "that sample was written by a newer pgdu"
	}
	return err.Error()
}

// popToGrowth returns to the growth table after a pick.
func (m *Model) popToGrowth() {
	for len(m.stack) > 1 && m.top().level != levelGrowth {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

// abandonGrowthPick returns to the table after a failed pick — unless the
// picker is the tool's entry screen, where there is no loaded table to return
// to and leaving would drop the user out of the tool.
func (m *Model) abandonGrowthPick() {
	if top := m.top(); top.level == levelGrowthWindow && top.growth.entry {
		return
	}
	m.popToGrowth()
}
