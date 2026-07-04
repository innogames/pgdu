package pg

import (
	"context"
	"testing"
)

// ReindexProgress must run cleanly and report "nothing in flight" for a table
// with no build in progress — the idle path the poller hits between and after
// rebuilds. The live-during-REINDEX path is exercised by hand (view_overlays
// banner); here we pin the query parses and the no-rows contract.
func TestIntegration_ReindexProgressIdle(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_reindex_idle`)
	if _, err := pool.Exec(ctx, `CREATE TABLE pgdu_reindex_idle (id int PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_reindex_idle`) })

	var oid uint32
	if err := pool.QueryRow(ctx, `SELECT 'pgdu_reindex_idle'::regclass::oid`).Scan(&oid); err != nil {
		t.Fatalf("oid: %v", err)
	}

	row, ok, err := c.ReindexProgress(ctx, db, oid)
	if err != nil {
		t.Fatalf("ReindexProgress: %v", err)
	}
	if ok {
		t.Errorf("expected no build in flight, got %+v", row)
	}
}
