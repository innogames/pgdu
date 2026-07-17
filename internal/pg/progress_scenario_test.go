package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A real CREATE INDEX must surface in ListProgress with a phase-appropriate
// counter (blocks while scanning, tuples while sorting/loading, lockers while
// waiting). The build can outrun the poll loop on fast machines, so a run
// that never catches it skips rather than fails.
func TestIntegration_Progress(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	// Idle smoke check first: the query itself must always succeed.
	if _, err := c.ListProgress(ctx, db); err != nil {
		t.Fatalf("ListProgress: %v", err)
	}

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS pgdu_progtest`)
	if _, err := pool.Exec(ctx,
		`CREATE TABLE pgdu_progtest AS SELECT g AS id, md5(g::text) AS v FROM generate_series(1, 2000000) g`); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS pgdu_progtest`) })

	builder, err := pgx.Connect(ctx, os.Getenv("PGDU_TEST_DSN"))
	if err != nil {
		t.Fatalf("builder connect: %v", err)
	}
	defer func() { _ = builder.Close(context.Background()) }()
	done := make(chan error, 1)
	go func() {
		_, err := builder.Exec(context.Background(), `CREATE INDEX pgdu_progtest_idx ON pgdu_progtest (v)`)
		done <- err
	}()

	// Poll for the build to show up in pg_stat_progress_create_index.
	var got ProgressRow
	found := false
poll:
	for range 40 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("create index: %v", err)
			}
			break poll
		default:
		}
		rows, err := c.ListProgress(ctx, db)
		if err != nil {
			t.Fatalf("ListProgress: %v", err)
		}
		for _, r := range rows {
			if r.Command == "CREATE INDEX" {
				got, found = r, true
				break poll
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !found {
		t.Skip("index build finished before a progress sample landed")
	}
	switch got.Unit {
	case "blocks", "tuples", "lockers":
	default:
		t.Errorf("unit = %q, want blocks/tuples/lockers", got.Unit)
	}
	if got.PID == 0 {
		t.Errorf("missing pid: %+v", got)
	}
	if got.Phase == "" {
		t.Errorf("missing phase: %+v", got)
	}
}
