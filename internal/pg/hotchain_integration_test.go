package pg

import (
	"context"
	"testing"
)

// A HOT update leaves the index entry pointing at the chain root, which
// pruning turns into an LP_REDIRECT; only fillHotChains can resolve that hop.
// The query behind it once failed to prepare (parameter type deduced twice)
// and, being best-effort, failed silently — so this asserts the hop lands.
// Skipped unless PGDU_TEST_DSN is set (needs pageinspect + superuser).
func TestIntegration_IndexTuplesFollowHotRedirect(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_hot`)
	for _, sql := range []string{
		// fillfactor leaves room so the HOT update stays on the page.
		`CREATE TABLE pgdu_hot (k text PRIMARY KEY, v int) WITH (fillfactor = 50, autovacuum_enabled = off)`,
		`INSERT INTO pgdu_hot SELECT md5(i::text), i FROM generate_series(1, 20) i`,
		// v is not indexed, so this is a HOT update: the pkey entry keeps
		// pointing at the old line pointer.
		`UPDATE pgdu_hot SET v = v + 1`,
		// A page read after the update's xid is past the horizon prunes the
		// chain, rewriting the old line pointer into an LP_REDIRECT.
		`SELECT count(*) FROM pgdu_hot`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_hot`)
	})

	var redirects int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM heap_page_items(get_raw_page('pgdu_hot', 'main', 0)) WHERE lp_flags = 2`).Scan(&redirects); err != nil {
		t.Skipf("heap_page_items: %v", err)
	}
	if redirects == 0 {
		t.Skip("page was not pruned; no redirect line pointers to follow")
	}

	var idxOID, tblOID uint32
	if err := pool.QueryRow(ctx, `SELECT 'pgdu_hot_pkey'::regclass::oid, 'pgdu_hot'::regclass::oid`).Scan(&idxOID, &tblOID); err != nil {
		t.Fatalf("oids: %v", err)
	}
	idx := Relation{DB: db, Schema: "public", Name: "pgdu_hot_pkey", OID: idxOID, ParentOID: tblOID, ParentName: "pgdu_hot"}

	// Single-page index: block 1 is the root and the only leaf.
	tuples, err := c.ListIndexTuples(ctx, idx, 1, "r")
	if err != nil {
		t.Fatalf("ListIndexTuples: %v", err)
	}
	var hops, resolved int
	for _, tup := range tuples {
		if tup.HotCtid == nil {
			continue
		}
		hops++
		if tup.HotDecoded != nil {
			resolved++
		}
	}
	if hops == 0 {
		t.Fatalf("no index entry followed a HOT redirect although the heap page has %d; tuples: %+v", redirects, tuples)
	}
	if resolved != hops {
		t.Errorf("%d of %d HOT hops decoded a key; want all", resolved, hops)
	}
}
