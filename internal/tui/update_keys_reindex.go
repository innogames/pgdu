package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"pgdu/internal/humanize"
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
	if why := m.reindexRefusal(reindexNeedBytes(s, false)); why != "" {
		s.reindex.blocked = why
		// The press is still absorbed: handleKey's own reindexCandidate guard
		// stops the fall-through to a drill-in, exactly as it does for the
		// arm-the-confirm path.
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
func (m *Model) armReindexTable(s *screen) {
	if s.level != levelParts || s.table.OID == 0 || s.reindex.running != "" {
		return
	}
	if why := m.reindexRefusal(reindexNeedBytes(s, true)); why != "" {
		s.reindex.blocked = why
		return
	}
	s.reindex.pending = s.table.Qualified()
	s.reindex.whole = true
	s.reindex.err = nil
}

// reindexNeedBytes is the extra disk a CONCURRENTLY rebuild occupies at its
// peak: a second copy of every index it touches, on top of the originals,
// until the swap at the end. For the whole-table form that is the sum of the
// table's indexes rather than the largest of them — REINDEX TABLE
// CONCURRENTLY creates the new index relations for the whole table up front
// and drops the old ones only once all of them have been swapped in.
//
// It is a floor, not a budget: the sort that builds each index spills to the
// same filesystem, and the WAL the rebuild writes has to be retained until a
// checkpoint. That is why the gate refuses at "needs as much as is free"
// rather than at some fraction of it.
func reindexNeedBytes(s *screen, whole bool) int64 {
	if whole {
		var sum int64
		for _, it := range s.items {
			if p, ok := it.data.(pg.Part); ok && p.Kind == pg.PartIndex {
				sum += p.SizeBytes
			}
		}
		return sum
	}
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return 0
	}
	return s.items[vis[s.cursor]].size
}

// reindexRefusal returns the reason a rebuild needing need bytes must not be
// armed, or "" to let it through. A rebuild that runs the filesystem out of
// space does not just fail: every other session writing to that disk fails
// with it, and the cluster can stop accepting writes altogether — which is a
// far worse outcome than a bloated index left standing for another day.
//
// Best effort in both directions. With no reading of the server's disk (a
// remote host, an unprivileged connection) nothing is refused, because
// refusing on an unknown would make the key dead wherever pgdu cannot see the
// filesystem. And the figure it does have covers the data directory only: an
// index in another tablespace is sized against the wrong disk, which is why
// the refusal names the path it measured.
func (m *Model) reindexRefusal(need int64) string {
	d := m.disk
	if d == nil || need <= 0 || d.Avail <= 0 {
		return ""
	}
	if need < d.Avail {
		return ""
	}
	return fmt.Sprintf("needs ~%s of free space (a second copy of the indexes); %s has %s left",
		humanize.Bytes(need), d.Path, humanize.Bytes(d.Avail))
}
