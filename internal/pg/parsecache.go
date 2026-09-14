package pg

import "sync"

// MainTable, QueryKind and JoinedTables are pure functions of a statement's
// normalized text, and that text is stable for a given pg_stat_statements
// queryid. The top-queries table rebuilds on every refresh tick over every row
// (thousands of them), so without memoization the shallow SQL parse —
// sqlWords/StripSQLComments, the dominant cost in a profile of a busy server —
// reruns for each row on each tick. Cache the results keyed by query text; the
// working set is bounded by pg_stat_statements.max, so the maps stay small, and
// sync.Map keeps the lookup safe regardless of which goroutine builds the rows.
var (
	mainTableMemo sync.Map // query string → table string
	queryKindMemo sync.Map // query string → kind string
	// JoinedTables walks the whole token stream rather than stopping at the
	// first relation, so it is the most expensive of the three; the cached
	// slice is shared with every caller and must not be modified.
	joinedTablesMemo sync.Map // query string → []string
)
