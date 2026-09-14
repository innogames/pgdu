package pg

import (
	"context"
	"testing"
)

// Exercises the TOAST side of the byte-layout overlay end to end: the attr
// split of a chunk row (chunk_id, chunk_seq, chunk_data) and the reassembly of
// a value that spans several chunks, with the owner's type riding along as the
// decoding hint. Skipped unless PGDU_TEST_DSN is set (needs pageinspect +
// superuser).
func TestIntegration_ReadToastValue(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_toastval`)
	// EXTERNAL storage keeps the value uncompressed, so the chunk bytes are the
	// payload itself and the round trip is byte-exact whatever the server's
	// default_toast_compression.
	for _, sql := range []string{
		`CREATE TABLE pgdu_toastval (id int, doc text)`,
		`ALTER TABLE pgdu_toastval ALTER COLUMN doc SET STORAGE EXTERNAL`,
		`INSERT INTO pgdu_toastval VALUES (1, repeat('toast me ', 1000))`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_toastval`) })

	var toast Table
	err = pool.QueryRow(ctx, `SELECT c.reltoastrelid, t.relname
	                          FROM pg_class c JOIN pg_class t ON t.oid = c.reltoastrelid
	                          WHERE c.oid = 'pgdu_toastval'::regclass`).Scan(&toast.OID, &toast.Name)
	if err != nil {
		t.Fatalf("toast relation: %v", err)
	}
	toast.DB, toast.Schema = db, "pg_toast"

	page, err := c.ListHeapTuples(ctx, toast, 0, nil)
	if err != nil {
		t.Fatalf("ListHeapTuples: %v", err)
	}
	if len(page.Tuples) == 0 || page.Tuples[0].ChunkID == nil {
		t.Fatalf("expected chunk rows on toast block 0, got %+v", page.Tuples)
	}
	first := page.Tuples[0]

	attrs, err := c.ListTupleAttrs(ctx, toast, 0, first.LP)
	if err != nil {
		t.Fatalf("ListTupleAttrs on the toast relation: %v", err)
	}
	if len(attrs) != 3 || attrs[0].Name != "chunk_id" || attrs[1].Name != "chunk_seq" || attrs[2].Name != "chunk_data" {
		t.Fatalf("toast attrs = %+v, want chunk_id, chunk_seq, chunk_data", attrs)
	}
	if attrs[2].Len != -1 || len(attrs[2].Value) < 5 {
		t.Errorf("chunk_data should be a stored varlena: %+v", attrs[2])
	}

	v, err := c.ReadToastValue(ctx, toast, *first.ChunkID)
	if err != nil {
		t.Fatalf("ReadToastValue: %v", err)
	}
	want := 9 * 1000
	if v.Chunks < 2 || v.StoredBytes != int64(want) || v.Truncated || len(v.Data) != want {
		t.Fatalf("chunks %d stored %d truncated %v data %d; want ≥2 chunks of %d B whole", v.Chunks, v.StoredBytes, v.Truncated, len(v.Data), want)
	}
	if string(v.Data[:9]) != "toast me " || string(v.Data[want-9:]) != "toast me " {
		t.Errorf("chunks not reassembled in order: %q … %q", v.Data[:9], v.Data[want-9:])
	}
	if len(v.OwnerTypes) != 1 || v.OwnerTypes[0].TypName != "text" || v.OwnerTypes[0].TypCategory != "S" {
		t.Errorf("owner types = %+v, want the one text column", v.OwnerTypes)
	}
}
