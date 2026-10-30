package tui

import (
	tea "charm.land/bubbletea/v2"

	"pgdu/internal/pg"
)

// reindexCandidate returns the index name to reindex if the current row on a
// parts screen is an index part whose *measured* bloat is above
// reindexBloatThreshold. Returns "" when the row doesn't qualify (wrong level,
// wrong kind, bloat unknown or merely estimated or below threshold, or another
// reindex is already in flight on this screen).
func reindexCandidate(s *screen) string {
	if s.level != levelParts || s.reindex.running != "" {
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
	if !it.hasBloat || it.bloatEstimated || it.size <= 0 {
		// An estimate is never grounds for a REINDEX: before the per-access-
		// method split every non-btree index carried a flat 10 % guess, which
		// cleared the threshold and armed the key on indexes nobody had
		// measured.
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
	s.reindex.pending = cand
	s.reindex.whole = false
	s.reindex.err = nil
	return nil
}

// armReindexTable arms REINDEX TABLE CONCURRENTLY on the open table. Unlike
// the single-index form it is not gated on a measured bloat figure: the point
// of rebuilding every index at once is usually that you don't want to judge
// them one by one, and the non-btree ones may have no figure to judge.
func armReindexTable(s *screen) {
	if s.level != levelParts || s.table.OID == 0 || s.reindex.running != "" {
		return
	}
	s.reindex.pending = s.table.Qualified()
	s.reindex.whole = true
	s.reindex.err = nil
}
