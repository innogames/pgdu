package tui

import (
	"pgdu/internal/pg"
	"pgdu/internal/pglog"
)

// enterLabel names what Enter does on screen s for the footer's "↵ …" hint —
// the screen it opens ("parts", "query detail"), the overlay ("byte layout"),
// or the in-place action ("unfold", "reindex") — and reports whether Enter
// does anything at all. ok == false disables the binding, so leaf levels and
// inert rows advertise no dead key. The label follows the cursor on levels
// where only some rows drill (parts, tuples, WAL sections, log sections), and
// stays enabled wherever drillIn answers with a notice rather than silence
// (activity rows without a query_id, GiST/BRIN/GIN pages with nothing to
// itemize, a B-tree high key) so the notice still gets its chance to explain.
// Keep it in step with drillIn and the item builders' hasChildren flags.
func enterLabel(s *screen) (label string, ok bool) {
	cur, hasCur := s.currentItem()
	switch s.level {
	case levelTools:
		return "open tool", true
	case levelDatabases:
		switch {
		case s.diag != nil:
			return "run", true
		case s.tool == toolQueries:
			return "top queries", true
		}
		return "schemas", true
	case levelSchemas:
		switch s.tool {
		case toolBuffers:
			return "buffers", true
		case toolPageInspect:
			return "relations", true
		case toolTableStats:
			return "table overview", true
		}
		return "tables", true
	case levelTables:
		if s.tool == toolPageInspect {
			return "heap pages", true
		}
		return "parts", true
	case levelParts:
		if reindexCandidate(s) != "" {
			return "reindex", true
		}
		if !hasCur {
			return "columns", true
		}
		p, isPart := cur.data.(pg.Part)
		return "columns", isPart && p.Kind == pg.PartHeap
	case levelTableStats:
		return "disk parts", true
	case levelBufferTables:
		return "buffer detail", true
	case levelHeapPages:
		return "tuples", true
	case levelHeapTuples:
		if !hasCur {
			return "byte layout", true
		}
		t, isTuple := cur.data.(pg.HeapTuple)
		switch {
		case !isTuple:
			return "", false
		case t.LPFlags == pg.LPRedirect:
			return "follow hop", true
		case t.LPFlags != pg.LPNormal || t.Ctid == nil:
			return "", false
		case t.ChunkID != nil:
			return "toast value", true
		case s.table.Schema == "pg_toast":
			return "row", true
		}
		return "byte layout", true
	case levelRelations:
		if hasCur {
			if r, isRel := cur.data.(pg.Relation); isRel {
				switch r.Kind {
				case pg.RelTable, pg.RelToast:
					return "heap pages", true
				case pg.RelBTreeIndex, pg.RelGist, pg.RelBrin, pg.RelGin:
					return "index pages", true
				}
				return "", false
			}
		}
		return "pages", true
	case levelIndexPages:
		switch s.pages.index.AccessMethod {
		case "brin":
			return "brin ranges", true
		case "gin":
			return "posting lists", true
		}
		return "index tuples", true
	case levelIndexTuples:
		return indexTupleEnterLabel(s, cur, hasCur)
	case levelWAL:
		if !hasCur {
			return "records", true
		}
		switch cur.data.(type) {
		case pg.WALRelStat:
			return "block refs", true
		case pg.WALRmgrStat:
			return "records", true
		}
		return "", false // Σ / title / header lines
	case levelWALRecords:
		return "block refs", true
	case levelWALBlocks, levelWALRelBlocks:
		return "block payload", true
	case levelStatements:
		return "query detail", true
	case levelStatementDetail, levelStatementSamples:
		// ANALYZE executes the query, so the detail and captured-values views
		// offer it for read-only shapes only (handleStatementAnalyze's gate).
		return "EXPLAIN ANALYZE", s.stat.detail != nil && pg.ReadOnlyQuery(s.stat.detail.Query)
	case levelSnapshots:
		return "pick window", true
	case levelMaintenance:
		return "reset stats", true
	case levelActivity:
		return "query detail", true
	case levelTriage:
		if hasCur {
			switch r := cur.data.(type) {
			case triageOKSummary:
				if s.triage.showOK {
					return "collapse", true
				}
				return "expand", true
			case pg.TriageResult:
				return triageTargetLabel(r), true
			}
		}
		return "open target", true
	case levelDiagnostics:
		return "run", true
	case levelDiagnosticResult:
		// Enter opens the suggested-fix overlay where the diagnostic defines
		// one and is a dead key otherwise.
		return "fix", s.diag != nil && s.diag.Fix != nil
	case levelLogFiles:
		return "open log", true
	case levelLogs:
		if s.log.view.table() {
			return "entry", true
		}
		if hasCur {
			if _, isGroup := cur.data.(*pglog.Group); !isGroup {
				return "", false // section header rows are inert
			}
		}
		return "entries", true
	case levelLogGroup:
		if s.log.params != logParamsOff {
			return "entries", true
		}
		return "entry", true
	case levelPgBouncers:
		return "overview", true
	case levelPgBouncer:
		if hasCur {
			if _, isLog := cur.data.(pgbLogRow); isLog {
				return "log analyzer", true
			}
		}
		return "SHOW table", true
	}
	// Leaves: columns, buffer detail, shmem, tuple row, WAL block detail,
	// statement result, settings, lock tree, wait profile, progress, pgbouncer
	// SHOW, describe, log entry.
	return "", false
}

// indexTupleEnterLabel is enterLabel for levelIndexTuples, where the action
// depends on the access method, the page role and the row: descend a downlink,
// open the heap tuple, fold/unfold a posting list, or jump to a BRIN range.
func indexTupleEnterLabel(s *screen, cur item, hasCur bool) (string, bool) {
	switch s.pages.index.AccessMethod {
	case "gin":
		return "", false // posting-list segments are terminal
	case "brin":
		return "heap pages", true
	case "gist":
		if s.pages.indexPageType == "intr" {
			return "child page", true
		}
		return "heap tuple", true
	}
	if !hasCur {
		return "heap tuple", true
	}
	switch t := cur.data.(type) {
	case postingMember:
		return "heap tuple", true
	case pg.IndexTuple:
		if len(t.Posting) > 0 {
			if s.pages.postingOpen[t.ItemOffset] {
				return "fold", true
			}
			return "unfold", true
		}
		if s.pages.indexPageType == "i" {
			// The high key at offset 1 stays enabled: drillIn explains it.
			return "child page", true
		}
		return "heap tuple", true
	}
	return "", false
}
