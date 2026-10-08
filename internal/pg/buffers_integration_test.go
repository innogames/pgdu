package pg

import (
	"context"
	"testing"
)

// The buffers list's per-relation rows: one heap, one toast (its index folded
// in) and one per index, whose sizes sum to pg_total_relation_size, and a
// roll-up that agrees with the describe panel's single-table figure. Skipped
// unless PGDU_TEST_DSN is set; needs pg_buffercache (installed if missing).
func TestIntegration_RelationBufferStats(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := c.CreateExtension(ctx, db, "pg_buffercache"); err != nil {
		t.Skipf("pg_buffercache: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_bufsplit`)
	// EXTERNAL storage pushes every doc into TOAST uncompressed, so the toast
	// pair has pages to buffer; the hash index is an AM pageinspect can't read.
	for _, sql := range []string{
		`CREATE TABLE pgdu_bufsplit (id int PRIMARY KEY, doc text)`,
		`ALTER TABLE pgdu_bufsplit ALTER COLUMN doc SET STORAGE EXTERNAL`,
		`CREATE INDEX pgdu_bufsplit_doc ON pgdu_bufsplit USING hash (doc)`,
		`INSERT INTO pgdu_bufsplit SELECT g, repeat('x', 4000) FROM generate_series(1, 50) g`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_bufsplit`) })

	var (
		oid, toastOID uint32
		schema        string
		total         int64
	)
	err = pool.QueryRow(ctx, `SELECT c.oid, c.reltoastrelid, n.nspname, pg_total_relation_size(c.oid)
	                          FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	                          WHERE c.oid = 'pgdu_bufsplit'::regclass`).Scan(&oid, &toastOID, &schema, &total)
	if err != nil {
		t.Fatalf("table: %v", err)
	}

	all, err := c.RelationBufferStats(ctx, db, schema)
	if err != nil {
		t.Fatalf("RelationBufferStats: %v", err)
	}
	var rels []TableBufferStat
	kinds := map[PartKind]int{}
	var sum, buffered int64
	for _, r := range all {
		if r.OID != oid {
			continue
		}
		if r.Rel == nil {
			t.Fatalf("per-relation row without Rel: %+v", r)
		}
		rels = append(rels, r)
		kinds[r.Rel.Kind]++
		sum += r.TotalBytes
		buffered += r.BufferedBytes
		if r.Rel.Kind == PartToast && r.Rel.OID != toastOID {
			t.Errorf("toast row oid %d, want %d", r.Rel.OID, toastOID)
		}
	}
	if kinds[PartHeap] != 1 || kinds[PartToast] != 1 || kinds[PartIndex] != 2 {
		t.Fatalf("relation kinds = %v, want 1 heap, 1 toast, 2 indexes: %+v", kinds, rels)
	}
	if sum != total {
		t.Errorf("relation sizes sum to %d, pg_total_relation_size is %d", sum, total)
	}
	if buffered <= 0 {
		t.Errorf("freshly written table has nothing in shared_buffers: %+v", rels)
	}

	rolled := RollUpBufferStats(rels)
	if len(rolled) != 1 || rolled[0].Rel != nil || rolled[0].TotalBytes != total || rolled[0].BufferedBytes != buffered {
		t.Errorf("roll-up = %+v, want one table row of %d total / %d buffered", rolled, total, buffered)
	}
	one, err := c.TableBufferStatByOID(ctx, db, oid)
	if err != nil {
		t.Fatalf("TableBufferStatByOID: %v", err)
	}
	if one.OID != oid || one.TotalBytes != total || one.Rel != nil {
		t.Errorf("TableBufferStatByOID = %+v, want the rolled-up table (total %d)", one, total)
	}

	// The toast pair alone buffers no more than the whole table does.
	countBufs := func(oid uint32, whole bool) int64 {
		counts, _, err := c.TableBufferUsageCounts(ctx, db, oid, whole)
		if err != nil || len(counts) != 6 {
			t.Fatalf("TableBufferUsageCounts(%d, %v): %d rows, err %v", oid, whole, len(counts), err)
		}
		var n int64
		for _, u := range counts {
			n += u.Buffers
		}
		return n
	}
	if whole, toast := countBufs(oid, true), countBufs(toastOID, false); toast <= 0 || toast > whole {
		t.Errorf("histogram buffers: toast pair %d, whole table %d; want 0 < toast ≤ whole", toast, whole)
	}
}
