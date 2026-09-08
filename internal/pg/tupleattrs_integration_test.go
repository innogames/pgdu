package pg

import (
	"context"
	"slices"
	"testing"
)

// Exercises ListTupleAttrs against a table covering the layout engine's edge
// cases: mixed alignments, a NULL, a dropped column, a column added after the
// row was written, and a forced out-of-line TOAST value. Skipped unless
// PGDU_TEST_DSN is set (needs pageinspect + superuser).
func TestIntegration_ListTupleAttrs(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_xray`)
	for _, sql := range []string{
		`DROP TYPE IF EXISTS pgdu_xray_mood`,
		`CREATE TYPE pgdu_xray_mood AS ENUM ('sad', 'ok', 'happy')`,
		`CREATE TABLE pgdu_xray (a int2, b int8, s text, big text, n text, mood pgdu_xray_mood)`,
		`ALTER TABLE pgdu_xray ALTER COLUMN big SET STORAGE EXTERNAL`,
		`INSERT INTO pgdu_xray VALUES (1, 2, 'abc', repeat('x', 100000), NULL, 'ok')`,
		`ALTER TABLE pgdu_xray DROP COLUMN s`,
		`ALTER TABLE pgdu_xray ADD COLUMN later int4`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_xray`)
		_, _ = pool.Exec(context.Background(), `DROP TYPE IF EXISTS pgdu_xray_mood`)
	})

	var oid uint32
	if err := pool.QueryRow(ctx, `SELECT 'pgdu_xray'::regclass::oid`).Scan(&oid); err != nil {
		t.Fatalf("oid: %v", err)
	}
	table := Table{DB: db, Schema: "public", Name: "pgdu_xray", OID: oid}

	page, err := c.ListHeapTuples(ctx, table, 0, nil)
	tuples := page.Tuples
	if err != nil {
		t.Fatalf("ListHeapTuples: %v", err)
	}
	if len(tuples) == 0 || tuples[0].LPFlags != LPNormal {
		t.Fatalf("expected a NORMAL tuple at lp 1, got %+v", tuples)
	}

	attrs, err := c.ListTupleAttrs(ctx, table, 0, tuples[0].LP)
	if err != nil {
		t.Fatalf("ListTupleAttrs: %v", err)
	}
	// a, b, s (dropped), big, n, mood, later — the dropped column keeps its slot.
	if len(attrs) != 7 {
		t.Fatalf("got %d attrs, want 7: %+v", len(attrs), attrs)
	}
	for i, a := range attrs {
		if a.Attnum != int32(i+1) {
			t.Errorf("attr %d has attnum %d, want %d", i, a.Attnum, i+1)
		}
	}

	if s := attrs[2]; !s.Dropped || !s.Stored || len(s.Value) == 0 {
		t.Errorf("dropped column should keep its stored bytes: %+v", s)
	}
	if big := attrs[3]; len(big.Value) != 18 || big.Value[0] != 0x01 {
		t.Errorf("external value should be an 18 B TOAST pointer starting 0x01: %d bytes % x…", len(big.Value), big.Value[:min(4, len(big.Value))])
	}
	if n := attrs[4]; !n.Stored || n.Value != nil {
		t.Errorf("NULL column should be stored with nil value: %+v", n)
	}
	if mood := attrs[5]; mood.TypCategory != "E" || len(mood.Value) != 4 ||
		mood.EnumLabel == nil || *mood.EnumLabel != "ok" {
		t.Errorf("enum column should carry its resolved label: %+v", mood)
	}
	if later := attrs[6]; later.Stored || later.Value != nil {
		t.Errorf("column added after insert should be not-stored: %+v", later)
	}
	if a := attrs[0]; a.Len != 2 || a.Align != "s" || a.TypName != "int2" || len(a.Value) != 2 {
		t.Errorf("int2 metadata mismatch: %+v", a)
	}
}

// Exercises the tuple list's row-content projection (sqlHeapTuplesCols): the
// default pick is the primary key, an explicit pick projects the visible row's
// text plus every NORMAL tuple's page bytes — including the dead version an
// UPDATE leaves behind, which only the bytes can still show. Skipped unless
// PGDU_TEST_DSN is set (needs pageinspect + superuser).
func TestIntegration_ListHeapTuplesColumns(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_cols`)
	for _, sql := range []string{
		`CREATE TABLE pgdu_cols (id int4 PRIMARY KEY, n int4, s text) WITH (autovacuum_enabled = off)`,
		`INSERT INTO pgdu_cols VALUES (1, 10, 'one'), (2, 20, NULL)`,
		`UPDATE pgdu_cols SET n = 11 WHERE id = 1`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_cols`) })

	var oid uint32
	if err := pool.QueryRow(ctx, `SELECT 'pgdu_cols'::regclass::oid`).Scan(&oid); err != nil {
		t.Fatalf("oid: %v", err)
	}
	table := Table{DB: db, Schema: "public", Name: "pgdu_cols", OID: oid}

	page, err := c.ListHeapTuples(ctx, table, 0, nil)
	if err != nil {
		t.Fatalf("ListHeapTuples(default): %v", err)
	}
	if len(page.Columns) != 3 || !page.Columns[0].PK || page.Columns[1].PK {
		t.Fatalf("columns = %+v, want id(pk), n, s", page.Columns)
	}
	if !slices.Equal(page.ShownNames(), []string{"id"}) || !slices.Equal(page.PKCols, []string{"id"}) {
		t.Errorf("default pick shows %v (pk %v), want [id]", page.ShownNames(), page.PKCols)
	}
	// lp 1 is the superseded version of id=1: no visible row, bytes still there.
	if len(page.Tuples) != 3 {
		t.Fatalf("got %d line pointers, want 3 (two rows + one dead version): %+v", len(page.Tuples), page.Tuples)
	}
	old := page.Tuples[0]
	if old.Vals != nil || old.PK != nil || len(old.Raws) != 1 || len(old.Raws[0]) != 4 {
		t.Errorf("dead version should have no visible text but its 4 B id bytes: vals=%v pk=%v raws=%v", old.Vals, old.PK, old.Raws)
	}

	page, err = c.ListHeapTuples(ctx, table, 0, []string{"s", "n", "nope"})
	if err != nil {
		t.Fatalf("ListHeapTuples(pick): %v", err)
	}
	if !slices.Equal(page.ShownNames(), []string{"n", "s"}) {
		t.Errorf("explicit pick shows %v, want [n s] (attnum order, unknown dropped)", page.ShownNames())
	}
	for _, tup := range page.Tuples {
		if tup.PK == nil {
			continue // the dead version
		}
		switch *tup.PK {
		case "1":
			if len(tup.Vals) != 2 || tup.Vals[0] == nil || *tup.Vals[0] != "11" || tup.Vals[1] == nil || *tup.Vals[1] != "one" {
				t.Errorf("row 1 vals = %v, want [11 one]", tup.Vals)
			}
		case "2":
			// A NULL in a visible row is a nil element, not a missing slice.
			if len(tup.Vals) != 2 || tup.Vals[1] != nil || len(tup.Raws) != 2 || tup.Raws[1] != nil {
				t.Errorf("row 2 should carry a NULL s in both provenances: vals=%v raws=%v", tup.Vals, tup.Raws)
			}
		}
	}
}
