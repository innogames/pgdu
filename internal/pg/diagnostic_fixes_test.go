package pg

import (
	"strings"
	"testing"
)

func fixGetter(vals map[string]string) func(string) (string, bool) {
	cols := make([]DiagColumn, 0, len(vals))
	row := make([]DiagCell, 0, len(vals))
	for k, v := range vals {
		cols = append(cols, DiagColumn{Name: k})
		row = append(row, DiagCell{Display: v})
	}
	return DiagRowGetter(cols, row)
}

func TestDiagRowGetter(t *testing.T) {
	get := fixGetter(map[string]string{"Schema": "public", "relname": "t", "empty": ""})
	if v, ok := get("schema"); !ok || v != "public" {
		t.Errorf("case-insensitive lookup: got %q, %v", v, ok)
	}
	if _, ok := get("empty"); ok {
		t.Error("blank cell must report ok=false")
	}
	if _, ok := get("missing"); ok {
		t.Error("missing column must report ok=false")
	}
}

func TestFixQualify(t *testing.T) {
	get := fixGetter(map[string]string{"schema": "my schema", "table_name": "t", "reg": "public.foo"})
	// only the identifier that needs quoting gets it
	if q, _ := fixQualify(get, "schema", "table_name"); q != `"my schema".t` {
		t.Errorf("qualified: got %s", q)
	}
	// regclass output is already qualified and passes through verbatim
	if q, _ := fixQualify(get, "schema", "reg"); q != "public.foo" {
		t.Errorf("regclass pass-through: got %s", q)
	}
	// no schema column → bare name (search_path resolution)
	if q, _ := fixQualify(get, "nope", "table_name"); q != "t" {
		t.Errorf("bare: got %s", q)
	}
	if _, ok := fixQualify(get, "schema", "nope"); ok {
		t.Error("missing name column must fail")
	}
}

func TestFixIdent(t *testing.T) {
	cases := map[string]string{
		"orders":       "orders",
		"_t1":          "_t1",
		"public":       "public",
		"Orders":       `"Orders"`,   // upper case folds — must quote
		"my table":     `"my table"`, // space
		"1st":          `"1st"`,      // leading digit
		"a$b":          `"a$b"`,      // quote_ident quotes $ too
		"user":         `"user"`,     // reserved
		"time":         `"time"`,     // col_name keyword, quoted like quote_ident
		"concurrently": `"concurrently"`,
		"name":         "name", // unreserved keyword stays bare
		"data":         "data",
		`we"ird`:       `"we""ird"`,
	}
	for in, want := range cases {
		if got := fixIdent(in); got != want {
			t.Errorf("fixIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestFixTableFillfactor(t *testing.T) {
	sql, ok := fixTableFillfactor(fixGetter(map[string]string{
		"schema": "public", "relname": "hot_tbl", "current_fill": "100", "suggested_fill": "72",
	}))
	if !ok {
		t.Fatal("expected a fix")
	}
	for _, want := range []string{
		"SET lock_timeout = '3s';",
		"ALTER TABLE public.hot_tbl SET (fillfactor = 72);",
		"current 100 → 72",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("fix missing %q:\n%s", want, sql)
		}
	}
	// suggestion == current → nothing to change
	if _, ok := fixTableFillfactor(fixGetter(map[string]string{
		"schema": "public", "relname": "t", "current_fill": "90", "suggested_fill": "90",
	})); ok {
		t.Error("equal current/suggested must produce no fix")
	}
}

func TestFixBuilders(t *testing.T) {
	reindex := fixReindex("schema_name", "index_name", "-- extra")
	sql, ok := reindex(fixGetter(map[string]string{"schema_name": "s", "index_name": "i"}))
	if !ok || !strings.HasPrefix(sql, "REINDEX INDEX CONCURRENTLY s.i;") || !strings.Contains(sql, "-- extra") {
		t.Errorf("reindex: got %q, %v", sql, ok)
	}

	drop := fixDropIndex("schema", "index_name")
	if sql, ok := drop(fixGetter(map[string]string{"schema": "s", "index_name": "i"})); !ok ||
		sql != "DROP INDEX CONCURRENTLY s.i;" {
		t.Errorf("drop: got %q, %v", sql, ok)
	}

	analyze := fixTableStmt("ANALYZE", "schema", "table_name")
	if sql, ok := analyze(fixGetter(map[string]string{"schema": "s", "table_name": "t"})); !ok ||
		sql != "ANALYZE s.t;" {
		t.Errorf("analyze: got %q, %v", sql, ok)
	}
	if _, ok := analyze(fixGetter(map[string]string{"schema": "s"})); ok {
		t.Error("missing table column must produce no fix")
	}

	sql, ok = fixDropDuplicateIndex(fixGetter(map[string]string{"idx1": "public.a", "idx2": "public.b"}))
	if !ok || !strings.Contains(sql, "DROP INDEX CONCURRENTLY public.b;") || !strings.Contains(sql, "public.a") {
		t.Errorf("duplicate: got %q, %v", sql, ok)
	}

	sql, ok = fixTableBloat(fixGetter(map[string]string{
		"databasename": "game", "schemaname": "public", "tablename": "Events",
	}))
	if !ok || !strings.HasPrefix(sql, `VACUUM (VERBOSE) public."Events";`) ||
		!strings.Contains(sql, `pg_repack -d game -t public."Events"`) ||
		!strings.Contains(sql, "TRUNCATE") {
		t.Errorf("bloat: got %q, %v", sql, ok)
	}

	sql, ok = fixClusterOn(fixGetter(map[string]string{
		"schema": "s", "table_name": "t", "index_name": "i", "clustered": "f",
	}))
	if !ok || !strings.Contains(sql, "ALTER TABLE s.t CLUSTER ON i;") || !strings.Contains(sql, "pg_repack") {
		t.Errorf("cluster on: got %q, %v", sql, ok)
	}
	if _, ok := fixClusterOn(fixGetter(map[string]string{
		"schema": "s", "table_name": "t", "index_name": "i", "clustered": "t",
	})); ok {
		t.Error("already-clustered row must produce no fix")
	}

	sql, ok = fixDropRedundantIndex(fixGetter(map[string]string{
		"schema": "s", "redundant_index": "r", "covered_by": "wide_idx",
	}))
	if !ok || !strings.Contains(sql, "DROP INDEX CONCURRENTLY s.r;") || !strings.Contains(sql, "wide_idx") {
		t.Errorf("redundant: got %q, %v", sql, ok)
	}
}
