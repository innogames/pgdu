package pg

import (
	"strings"
	"testing"
)

// The two statements built from bloatIndexEstimateHead/Rest share every CTE up
// to index_aligned_est and differ only in what follows it: the diagnostic
// continues the WITH chain, the per-index estimate ends it. A stray comma
// either way is a syntax error the compiler cannot see, and neither statement
// runs without a server, so the join is asserted here.
func TestBloatIndexEstimateComposes(t *testing.T) {
	for _, tc := range []struct {
		name, sql, pred, after string
	}{
		{"diagnostic", sqlDiagBloatIndex, "nspname NOT IN", "    FROM index_item_sizes\n),\nraw_bloat AS ("},
		{"per index", sqlBloatIndexEstimate, "indexclass.oid = $1::regclass", "    FROM index_item_sizes\n)\nSELECT "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.sql, tc.pred) {
				t.Errorf("predicate %q not spliced into btree_index_atts", tc.pred)
			}
			if !strings.Contains(tc.sql, tc.after) {
				t.Errorf("index_aligned_est is not followed by %q", tc.after)
			}
			// The predicate belongs inside btree_index_atts, where it keeps the
			// pg_stats join from running for every index in the database.
			if strings.Index(tc.sql, tc.pred) > strings.Index(tc.sql, "index_stat_targets") {
				t.Error("predicate lands after btree_index_atts")
			}
		})
	}
	if strings.Contains(sqlBloatIndexEstimate, "pg_stat_user_indexes") {
		t.Error("per-index estimate pulled in the diagnostic's reporting tail")
	}
}

// The budget in sqlBloatHeapApprox only protects anything from where it is: a
// WHERE is evaluated before the target list, so the pgstattuple_approx call
// never runs for a relation that fails it. Moved into the projection — or into
// a filter above the function scan — the gate would still be read as a gate
// while the sample it was meant to refuse ran anyway.
func TestBloatHeapApproxGatesBeforeSampling(t *testing.T) {
	where := strings.LastIndex(sqlBloatHeapApprox, "\nWHERE  t.dirty_bytes <= $2")
	if where < 0 {
		t.Fatal("the dirty-bytes budget is no longer a WHERE on the outer query")
	}
	if call := strings.Index(sqlBloatHeapApprox, "pgstattuple_approx("); call > where {
		t.Error("pgstattuple_approx is called after the budget instead of under it")
	}
	if !strings.Contains(sqlBloatHeapApprox, "relallvisible") {
		t.Error("budget no longer counts only the pages the sample must read")
	}
}
