package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"pgdu/internal/pg"
)

// handleStatementAnalyze runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) for the
// detail view's query. ANALYZE executes the query for real, so it's gated to
// read-only SELECT shapes (ReadOnlyQuery) and needs the sample call — the
// query with synthesized literals filling its $n — to be ready. Returns nil
// (a no-op) when any of those don't hold.
func (m *Model) handleStatementAnalyze(s *screen) tea.Cmd {
	if s.stat.detail == nil || s.stat.explaining {
		return nil
	}
	if !pg.ReadOnlyQuery(s.stat.detail.Query) || s.stat.sampleCall == "" {
		return nil
	}
	s.stat.explaining = true
	s.stat.explain = ""
	s.stat.explainErr = nil
	s.stat.explainAnalyze = true
	return m.loadStatementExplainAnalyzeCmd(s.db, s.stat.detail.Query, s.stat.sampleCall)
}

// statementPlanCmd issues the right (non-ANALYZE) EXPLAIN for the detail view:
// a plain EXPLAIN on the real example call when one is available (real captured
// values from pg_qualstats), otherwise the generic plan on the normalized query.
// The caller is responsible for setting statExplaining / clearing prior output.
func (m *Model) statementPlanCmd(s *screen) tea.Cmd {
	if s.stat.sampleReal && s.stat.sampleCall != "" {
		return m.loadStatementExplainLiteralCmd(s.db, s.stat.detail.Query, s.stat.sampleCall)
	}
	return m.loadStatementExplainCmd(s.db, s.stat.detail.Query)
}

// handleSampleAnalyze runs EXPLAIN (ANALYZE, …) for the highlighted captured
// value on the samples level. Reconstruction is only reliable when the
// normalized query has exactly one placeholder — then the captured constant is
// unambiguously that $1, and we substitute it for a true per-value plan. For
// multi-parameter queries we can't map one captured constant to one of several
// placeholders, so we fall back to the representative real example query
// (statSampleCall). Gated to read-only shapes since ANALYZE executes.
func (m *Model) handleSampleAnalyze(s *screen) tea.Cmd {
	if s.stat.detail == nil || s.stat.explaining || !pg.ReadOnlyQuery(s.stat.detail.Query) {
		return nil
	}
	sm, ok := s.selectedSample()
	if !ok {
		return nil
	}
	q := sampleAnalyzeQuery(s.stat.detail.Query, s.stat.sampleCall, sm)
	if q == "" {
		return nil
	}
	s.stat.explaining = true
	s.stat.explain = ""
	s.stat.explainErr = nil
	s.stat.explainAnalyze = true
	return m.loadStatementExplainAnalyzeCmd(s.db, s.stat.detail.Query, q)
}

// selectedSnapshot resolves the snapshot meta under the cursor on the snapshots
// level, honouring the active filter via visibleIndexes.
func (s *screen) selectedSnapshot() (pg.SnapshotMeta, bool) {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return pg.SnapshotMeta{}, false
	}
	path := s.items[vis[s.cursor]].snapPath
	return metaByPath(s.stat.snapMetas, path)
}

// selectedSample resolves the captured value under the cursor on the samples
// level, honouring the active filter via visibleIndexes.
func (s *screen) selectedSample() (pg.QualSample, bool) {
	vis := s.visibleIndexes()
	if s.cursor < 0 || s.cursor >= len(vis) {
		return pg.QualSample{}, false
	}
	sm, ok := s.items[vis[s.cursor]].data.(pg.QualSample)
	return sm, ok
}

// sampleAnalyzeQuery builds the literal query to EXPLAIN ANALYZE for a captured
// value: a clean $1 substitution for single-parameter queries, else the real
// example query. Returns "" when neither is usable.
func sampleAnalyzeQuery(normalized, example string, sm pg.QualSample) string {
	if uniqueParams(normalized) == 1 && sm.ConstValue != "" {
		return strings.ReplaceAll(normalized, "$1", sm.ConstValue)
	}
	return example
}

// uniqueParams counts the distinct $n placeholders in a normalized query.
func uniqueParams(query string) int {
	seen := map[string]struct{}{}
	for _, p := range reParam.FindAllString(query, -1) {
		seen[p] = struct{}{}
	}
	return len(seen)
}

// stmtDescribeTarget names the main table of the highlighted (or detailed) query for `d`.
func stmtDescribeTarget(s *screen) (descTarget, bool) {
	curItem := s.currentItem
	switch s.level {
	case levelStatements:
		// item.name is the flattened statement text; parse out its main table and
		// describe it by name (resolved server-side, since we have no OID here).
		it, ok := curItem()
		if !ok {
			return descTarget{}, false
		}
		name := pg.MainTable(it.name)
		if name == "" {
			return descTarget{}, false
		}
		return descTarget{byName: true, db: s.db, tableName: name}, true
	case levelStatementDetail, levelStatementSamples:
		if s.stat.detail == nil {
			return descTarget{}, false
		}
		name := pg.MainTable(s.stat.detail.Query)
		if name == "" {
			return descTarget{}, false
		}
		return descTarget{byName: true, db: s.db, tableName: name}, true
	}
	return descTarget{}, false
}

// handleStatementEnter handles Enter on the statement detail and captured-values
// screens, which don't drill: Enter confirms an EXPLAIN ANALYZE run instead.
// ok is false on every other level.
func (m *Model) handleStatementEnter(s *screen, msg tea.KeyMsg) (tea.Cmd, bool) {
	if s.level == levelStatementDetail {
		// The detail view doesn't drill further; Enter (not l/→) confirms an
		// EXPLAIN ANALYZE run on read-only queries.
		if msg.Type == tea.KeyEnter {
			return m.handleStatementAnalyze(s), true
		}
		return nil, true
	}
	if s.level == levelStatementSamples {
		// The captured-values list doesn't drill further; Enter runs EXPLAIN
		// ANALYZE for the highlighted real value (read-only queries only).
		if msg.Type == tea.KeyEnter {
			return m.handleSampleAnalyze(s), true
		}
		return nil, true
	}
	return nil, false
}
