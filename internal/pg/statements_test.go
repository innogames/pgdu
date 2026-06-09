package pg

import (
	"strings"
	"testing"
)

func TestDiffStatements(t *testing.T) {
	baseline := map[int64]QueryStat{
		1: {QueryID: 1, Calls: 10, Rows: 100, TotalExecTime: 50, SharedBlksHit: 80, SharedBlksRead: 20, WALBytes: 1000},
		2: {QueryID: 2, Calls: 5, Rows: 5, TotalExecTime: 5},
	}
	current := []QueryStat{
		// queryid 1: had activity in the window.
		{QueryID: 1, Query: "select $1", Calls: 12, Rows: 130, TotalExecTime: 80, SharedBlksHit: 90, SharedBlksRead: 30, WALBytes: 1500},
		// queryid 2: unchanged since baseline → 0 calls in window → dropped.
		{QueryID: 2, Calls: 5, Rows: 5, TotalExecTime: 5},
		// queryid 3: brand new since baseline → full counters kept.
		{QueryID: 3, Query: "insert", Calls: 3, Rows: 3, TotalExecTime: 9},
	}

	got := DiffStatements(baseline, current)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows with window activity, got %d", len(got))
	}

	byID := map[int64]QueryStat{}
	for _, q := range got {
		byID[q.QueryID] = q
	}

	q1, ok := byID[1]
	if !ok {
		t.Fatal("queryid 1 missing from diff")
	}
	if q1.Calls != 2 || q1.Rows != 30 || q1.TotalExecTime != 30 {
		t.Errorf("q1 delta wrong: calls=%d rows=%d total=%v", q1.Calls, q1.Rows, q1.TotalExecTime)
	}
	if q1.WALBytes != 500 {
		t.Errorf("q1 wal delta = %d, want 500", q1.WALBytes)
	}
	// MeanExecTime is recomputed from the delta, not carried from the snapshot.
	if want := 30.0 / 2.0; q1.MeanExecTime != want {
		t.Errorf("q1 mean = %v, want %v", q1.MeanExecTime, want)
	}
	// Identity (query text) comes from the newer snapshot.
	if q1.Query != "select $1" {
		t.Errorf("q1 query = %q, want carried from current snapshot", q1.Query)
	}

	q3, ok := byID[3]
	if !ok {
		t.Fatal("new queryid 3 missing from diff")
	}
	if q3.Calls != 3 || q3.TotalExecTime != 9 {
		t.Errorf("q3 (new) should keep full counters, got calls=%d total=%v", q3.Calls, q3.TotalExecTime)
	}

	if _, dropped := byID[2]; dropped {
		t.Error("queryid 2 had no window activity and should have been dropped")
	}
}

func TestBuildStatements(t *testing.T) {
	// pg_stat_statements 1.11+ (PG17 default / PG18): renamed shared_blk_* and
	// the new local_blk_* columns are used verbatim.
	v111 := statementsQuery(1, 11)
	for _, want := range []string{
		"shared_blk_read_time, shared_blk_write_time",
		"local_blk_read_time, local_blk_write_time",
		"temp_blk_read_time, temp_blk_write_time",
	} {
		if !strings.Contains(v111, want) {
			t.Errorf("1.11 query missing %q:\n%s", want, v111)
		}
	}
	// 1.11 has the timing columns natively, so the version-shim aliases never
	// appear. (We check the specific shims rather than any " AS " because the
	// aggregate wrapper legitimately adds its own AS clauses.)
	for _, shim := range []string{
		"blk_read_time AS shared_blk_read_time",
		"0::float8 AS local_blk_read_time",
	} {
		if strings.Contains(v111, shim) {
			t.Errorf("1.11 query should not need shim %q:\n%s", shim, v111)
		}
	}
	// Rows are collapsed to one per queryid so the queryid-keyed window baseline
	// can't collide across the multiple roles that ran the same statement.
	if !strings.Contains(v111, "GROUP BY queryid") {
		t.Errorf("query should aggregate by queryid:\n%s", v111)
	}

	// pg_stat_statements 1.10 (e.g. a PG17 cluster pg_upgrade'd from PG15, never
	// updated): the old blk_*_time names alias into shared_blk_*; local_blk_*
	// don't exist yet and fall back to zero. This is the case that crashed.
	v110 := statementsQuery(1, 10)
	for _, want := range []string{
		"blk_read_time AS shared_blk_read_time",
		"blk_write_time AS shared_blk_write_time",
		"0::float8 AS local_blk_read_time",
		"0::float8 AS local_blk_write_time",
		"temp_blk_read_time, temp_blk_write_time",
	} {
		if !strings.Contains(v110, want) {
			t.Errorf("1.10 query missing %q:\n%s", want, v110)
		}
	}
}

func TestExplainableQuery(t *testing.T) {
	yes := []string{
		"select 1", "SELECT * FROM t WHERE id = $1", "  with x as (select 1) select * from x",
		"INSERT INTO t VALUES ($1)", "update t set x = $1", "DELETE FROM t WHERE id = $1",
		"values ($1)", "TABLE t", "merge into t ...",
		// ORM-tagged statements: the leading comment must not hide the keyword.
		"/* EquipmentRepository.findByPlayerId */ select e.id from equipment e where e.player_id = $1",
		"-- name: GetUser\nSELECT * FROM users WHERE id = $1",
	}
	for _, q := range yes {
		if !ExplainableQuery(q) {
			t.Errorf("ExplainableQuery(%q) = false, want true", q)
		}
	}
	no := []string{
		"", "   ",
		"EXPLAIN (GENERIC_PLAN, FORMAT TEXT) SELECT 1",
		"SET pg_stat_statements.track = 'none'",
		"PREPARE pgdu_infer_params AS SELECT 1",
		"VACUUM ANALYZE t", "CREATE INDEX ON t (x)", "BEGIN", "COMMIT", "SHOW all",
	}
	for _, q := range no {
		if ExplainableQuery(q) {
			t.Errorf("ExplainableQuery(%q) = true, want false", q)
		}
	}
}

func TestReadOnlyQuery(t *testing.T) {
	yes := []string{
		"select 1", "SELECT * FROM t WHERE id = $1", "TABLE t", "values ($1)",
		"  with x as (select 1) select * from x",
		"/* EquipmentRepository.findByPlayerId */ select e.id from equipment e where e.player_id = $1",
	}
	for _, q := range yes {
		if !ReadOnlyQuery(q) {
			t.Errorf("ReadOnlyQuery(%q) = false, want true", q)
		}
	}
	no := []string{
		"", "   ",
		"INSERT INTO t VALUES ($1)", "update t set x = $1", "DELETE FROM t WHERE id = $1",
		"merge into t ...",
		// Data-modifying CTEs execute writes, so they must be rejected.
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d",
		"with ins as (insert into t values (1) returning id) select * from ins",
		"VACUUM t", "SET x = 1",
		// Row-locking clauses take real locks when ANALYZE executes them.
		"SELECT * FROM t WHERE id = $1 FOR UPDATE",
		"select resource from game_bag_resource where bag_id = $1 for update",
		"SELECT 1 FROM t FOR SHARE",
		"SELECT 1 FROM t FOR NO KEY UPDATE",
		"SELECT 1 FROM t FOR KEY SHARE",
		"SELECT * FROM t FOR UPDATE OF t",
	}
	for _, q := range no {
		if ReadOnlyQuery(q) {
			t.Errorf("ReadOnlyQuery(%q) = true, want false", q)
		}
	}

	// FOR as a function argument keyword (substring) is not a locking clause.
	if !ReadOnlyQuery("SELECT substring(name FROM 1 FOR 3) FROM t WHERE id = $1") {
		t.Error("substring(... FOR n) must not be mistaken for a locking clause")
	}
}

func TestQueryKind(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		// Plain reads.
		{"select 1", "S"},
		{"SELECT * FROM t WHERE id = $1", "S"},
		{"  with x as (select 1) select * from x", "S"},
		{"TABLE t", "S"},
		{"values ($1)", "S"},
		// ORM comment tag must not hide the keyword.
		{"/* UserRepo.find */ select * from users where id = $1", "S"},

		// Locking SELECTs → SL.
		{"SELECT * FROM t WHERE id = $1 FOR UPDATE", "SL"},
		{"select resource from game_bag_resource where bag_id = $1 for update", "SL"},
		{"SELECT 1 FROM t FOR SHARE", "SL"},
		{"SELECT 1 FROM t FOR NO KEY UPDATE", "SL"},
		{"SELECT 1 FROM t FOR KEY SHARE", "SL"},
		{"SELECT * FROM t FOR UPDATE OF t", "SL"},
		{"with x as (select 1) select * from t for update", "SL"},

		// Advisory-lock acquisition → L (takes precedence over SL/S).
		{"SELECT pg_advisory_lock($1)", "L"},
		{"select pg_advisory_lock(123)", "L"},
		{"SELECT pg_advisory_xact_lock($1, $2)", "L"},
		{"SELECT pg_try_advisory_lock($1)", "L"},
		{"SELECT pg_advisory_lock_shared($1)", "L"},
		{"SELECT pg_try_advisory_xact_lock($1)", "L"},
		// Advisory unlock is not an acquisition — stays a plain SELECT.
		{"SELECT pg_advisory_unlock($1)", "S"},
		{"SELECT pg_advisory_unlock_all()", "S"},

		// substring(... FOR n) is not a locking clause.
		{"SELECT substring(name FROM 1 FOR 3) FROM t", "S"},

		// DML and transaction control.
		{"INSERT INTO t VALUES ($1)", "I"},
		{"update t set x = $1", "U"},
		{"DELETE FROM t WHERE id = $1", "D"},
		{"merge into t ...", "M"},
		{"BEGIN", "T"},
		{"commit", "T"},

		// Unknown / empty.
		{"VACUUM t", "?"},
		{"", "?"},
		{"   ", "?"},
	}
	for _, c := range cases {
		if got := QueryKind(c.query); got != c.want {
			t.Errorf("QueryKind(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

func TestQualstatsExampleUsable(t *testing.T) {
	normalized := "SELECT a, b FROM t WHERE x = $1 AND y <= $2 FOR UPDATE OF t"
	// A complete denormalization ends with the same constant-free suffix.
	full := "SELECT a, b FROM t WHERE x = '104188' AND y <= '1779' FOR UPDATE OF t"
	if !QualstatsExampleUsable(normalized, full) {
		t.Errorf("complete example wrongly rejected:\n%s", full)
	}
	// Truncated mid-token (what pg_qualstats returns past track_activity_query_size).
	trunc := "SELECT a, b FROM t WHERE x = '104188' AND y <= '17"
	if QualstatsExampleUsable(normalized, trunc) {
		t.Errorf("truncated example wrongly accepted:\n%s", trunc)
	}
	// Whitespace/indentation differences between catalog text and example must
	// not matter (the real example is multi-line, the suffix is normalized).
	multiline := "SELECT a, b\n  FROM t\n  WHERE x = '1'\n    AND y <= '2'\n  FOR UPDATE OF t"
	if !QualstatsExampleUsable(normalized, multiline) {
		t.Errorf("multi-line complete example wrongly rejected:\n%s", multiline)
	}
	// No placeholders → nothing to anchor on → accept.
	if !QualstatsExampleUsable("SELECT 1", "SELECT 1") {
		t.Error("placeholder-free query should be accepted")
	}
	// pg_qualstats failed to reconstruct the qual and left $1 in place. The query
	// ends at $1 so there's no tail to anchor on, but a leftover placeholder still
	// makes a plain EXPLAIN fail — reject it.
	leftover := "select s.data from t s where s.id=$1"
	if QualstatsExampleUsable(leftover, leftover) {
		t.Errorf("example with a leftover $n placeholder wrongly accepted:\n%s", leftover)
	}
}

func TestQueryStatHitRatioAndDerived(t *testing.T) {
	q := QueryStat{Calls: 4, Rows: 40, TotalExecTime: 20, SharedBlksHit: 75, SharedBlksRead: 25}
	if got := q.MeanTime(); got != 5 {
		t.Errorf("MeanTime = %v, want 5", got)
	}
	if got := q.RowsPerCall(); got != 10 {
		t.Errorf("RowsPerCall = %v, want 10", got)
	}
	if hr, ok := q.HitRatio(); !ok || hr != 75 {
		t.Errorf("HitRatio = %v ok=%v, want 75 true", hr, ok)
	}

	// No block access → ratio undefined.
	none := QueryStat{Calls: 1}
	if _, ok := none.HitRatio(); ok {
		t.Error("HitRatio should be undefined with no block access")
	}
	if got := none.MeanTime(); got != 0 {
		t.Errorf("MeanTime with calls but no time should be 0, got %v", got)
	}

	// Zero calls must not divide by zero.
	zero := QueryStat{}
	if got := zero.MeanTime(); got != 0 {
		t.Errorf("MeanTime zero-calls = %v, want 0", got)
	}
	if got := zero.RowsPerCall(); got != 0 {
		t.Errorf("RowsPerCall zero-calls = %v, want 0", got)
	}
}

func TestBuildSampleCall(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		params []ParamType
		real   map[int]string
		want   string
	}{
		{
			name:   "no params unchanged",
			query:  "SELECT now()",
			params: nil,
			want:   "SELECT now()",
		},
		{
			name:  "int and text",
			query: "SELECT * FROM t WHERE id = $1 AND name = $2",
			params: []ParamType{
				{Ordinal: 1, Type: "integer"},
				{Ordinal: 2, Type: "text"},
			},
			want: "SELECT * FROM t WHERE id = 1::integer AND name = 'sample'::text",
		},
		{
			name:  "real values override synthesized, missing ones fall back",
			query: "SELECT * FROM t WHERE id = $1 AND name = $2",
			params: []ParamType{
				{Ordinal: 1, Type: "integer"},
				{Ordinal: 2, Type: "text"},
			},
			real: map[int]string{2: "'germany'::text"},
			want: "SELECT * FROM t WHERE id = 1::integer AND name = 'germany'::text",
		},
		{
			// $10 must not be mangled by the $1 substitution.
			name:  "double-digit ordinals",
			query: "VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
			params: []ParamType{
				{Ordinal: 1, Type: "integer"}, {Ordinal: 2, Type: "integer"},
				{Ordinal: 3, Type: "integer"}, {Ordinal: 4, Type: "integer"},
				{Ordinal: 5, Type: "integer"}, {Ordinal: 6, Type: "integer"},
				{Ordinal: 7, Type: "integer"}, {Ordinal: 8, Type: "integer"},
				{Ordinal: 9, Type: "integer"}, {Ordinal: 10, Type: "boolean"},
			},
			want: "VALUES (1::integer,1::integer,1::integer,1::integer,1::integer,1::integer,1::integer,1::integer,1::integer,true)",
		},
		{
			name:   "unknown type falls back to typed null",
			query:  "SELECT $1",
			params: []ParamType{{Ordinal: 1, Type: "some_custom_type"}},
			want:   "SELECT NULL::some_custom_type",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildSampleCall(c.query, c.params, c.real); got != c.want {
				t.Errorf("BuildSampleCall()\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestParamColumns(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  map[int]string
	}{
		{
			name:  "equality and qualified column",
			query: "SELECT * FROM t WHERE id = $1 AND t.name = $2",
			want:  map[int]string{1: "id", 2: "name"},
		},
		{
			name:  "in list shares the column",
			query: "SELECT * FROM t WHERE country IN ($1, $2, $3)",
			want:  map[int]string{1: "country", 2: "country", 3: "country"},
		},
		{
			name:  "any array and comparison",
			query: "SELECT * FROM t WHERE id = ANY($1) AND created > $2",
			want:  map[int]string{1: "id", 2: "created"},
		},
		{
			name:  "between",
			query: "SELECT * FROM t WHERE n BETWEEN $1 AND $2",
			want:  map[int]string{1: "n", 2: "n"},
		},
		{
			name:  "no placeholders",
			query: "SELECT now()",
			want:  nil,
		},
		{
			name:  "quoted column",
			query: `SELECT * FROM t WHERE "UserId" = $1`,
			want:  map[int]string{1: "UserId"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := paramColumns(c.query)
			if len(got) != len(c.want) {
				t.Fatalf("paramColumns()\n got: %v\nwant: %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("paramColumns()[%d] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseExtVersion(t *testing.T) {
	cases := []struct {
		in               string
		wantMaj, wantMin int
	}{
		{"1.11", 1, 11},
		{"1.10", 1, 10},
		{"1.9", 1, 9},
		{" 1.12 ", 1, 12},
		{"2", 2, 0},
		{"1.11.0", 1, 11},
		{"", 999, 0},
		{"garbage", 999, 0},
	}
	for _, c := range cases {
		if maj, min := parseExtVersion(c.in); maj != c.wantMaj || min != c.wantMin {
			t.Errorf("parseExtVersion(%q) = (%d,%d), want (%d,%d)", c.in, maj, min, c.wantMaj, c.wantMin)
		}
	}
}

func TestStatementsQueryVersionColumns(t *testing.T) {
	// 1.11+: native shared_/local_ timing columns, no aliasing or 0-fills.
	modern := statementsQuery(1, 11)
	for _, want := range []string{"shared_blk_read_time", "local_blk_read_time", "temp_blk_read_time"} {
		if !strings.Contains(modern, want) {
			t.Errorf("1.11 query missing %q", want)
		}
	}
	if strings.Contains(modern, "blk_read_time AS shared_blk_read_time") {
		t.Error("1.11 query should not alias legacy blk_read_time")
	}

	// 1.10: legacy blk_*_time aliased to shared_*, local timing 0-filled, temp native.
	v110 := statementsQuery(1, 10)
	for _, want := range []string{
		"blk_read_time AS shared_blk_read_time",
		"0::float8 AS local_blk_read_time",
		"temp_blk_read_time, temp_blk_write_time",
	} {
		if !strings.Contains(v110, want) {
			t.Errorf("1.10 query missing %q", want)
		}
	}

	// 1.9: temp timing also unavailable → 0-filled.
	v19 := statementsQuery(1, 9)
	if !strings.Contains(v19, "0::float8 AS temp_blk_read_time") {
		t.Error("1.9 query should 0-fill temp_blk_read_time")
	}

	// The LIKE filters with literal % must survive the builder intact.
	if !strings.Contains(modern, "query NOT LIKE '%pg_stat_statements%'") {
		t.Error("builder mangled the LIKE filter literal %")
	}
}
