package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A real blocking chain must surface as two nodes: the blocker (root, no
// blockers) and the waiter (blocked, with the blocker's PID and the relation).
func TestIntegration_LockWaiters(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_locktest`)
	if _, err := pool.Exec(ctx, `CREATE TABLE pgdu_locktest (id int PRIMARY KEY, v int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pgdu_locktest VALUES (1, 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_locktest`) })

	dsn := os.Getenv("PGDU_TEST_DSN")
	blk, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("blocker connect: %v", err)
	}
	defer func() { _ = blk.Close(ctx) }()
	if _, err := blk.Exec(ctx, `BEGIN`); err != nil {
		t.Fatalf("begin: %v", err)
	}
	// A table-level ACCESS EXCLUSIVE lock so the waiter blocks on a *relation*
	// lock — that exercises the regclass name resolution in the query.
	if _, err := blk.Exec(ctx, `LOCK TABLE pgdu_locktest IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("blocker lock: %v", err)
	}

	wait, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("waiter connect: %v", err)
	}
	defer func() { _ = wait.Close(ctx) }()
	go func() { _, _ = wait.Query(context.Background(), `SELECT * FROM pgdu_locktest`) }()

	// Poll for the wait relationship to establish.
	var nodes []LockNode
	for range 40 {
		time.Sleep(100 * time.Millisecond)
		nodes, err = c.ListLockWaiters(ctx, db)
		if err != nil {
			t.Fatalf("ListLockWaiters: %v", err)
		}
		waiters := 0
		for _, n := range nodes {
			if n.Waiting() {
				waiters++
			}
		}
		if waiters >= 1 && len(nodes) >= 2 {
			break
		}
	}

	if len(nodes) < 2 {
		t.Fatalf("expected ≥2 nodes in the chain, got %d: %+v", len(nodes), nodes)
	}
	var haveRoot, haveWaiter bool
	for _, n := range nodes {
		if !n.Waiting() && n.PID != 0 {
			haveRoot = true
		}
		if n.Waiting() {
			haveWaiter = true
			if n.WaitRelation == "" {
				t.Errorf("waiter %d missing wait relation: %+v", n.PID, n)
			}
		}
	}
	if !haveRoot || !haveWaiter {
		t.Fatalf("expected a root and a waiter; haveRoot=%v haveWaiter=%v nodes=%+v", haveRoot, haveWaiter, nodes)
	}
	_, _ = blk.Exec(ctx, `ROLLBACK`)
}
