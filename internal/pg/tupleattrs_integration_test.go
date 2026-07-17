package pg

import (
	"context"
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

	tuples, err := c.ListHeapTuples(ctx, table, 0)
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
