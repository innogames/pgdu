package pg

import (
	"context"
	"testing"
)

// OverallPct composes the per-phase counters into one 0–100 estimate via
// reindexPhaseSpan. The exact weights are free to change; what must hold is
// the shape: phases in execution order map to non-overlapping, increasing
// slices, waiting phases progress by lockers, and unmappable phases return -1
// so the caller's high-water clamp holds the bar instead of jumping.
func TestReindexProgressOverallPct(t *testing.T) {
	// The canonical REINDEX CONCURRENTLY phase sequence for a btree index, in
	// execution order. Each entry mid-phase must land inside its span and past
	// the previous phase's end.
	order := []string{
		"initializing",
		"waiting for writers before build",
		"building index: initializing",
		"building index: scanning table",
		"building index: sorting live tuples",
		"building index: loading tuples in tree",
		"waiting for writers before validation",
		"index validation: scanning index",
		"index validation: sorting tuples",
		"index validation: scanning table",
		"waiting for old snapshots",
		"waiting for readers before marking dead",
		"waiting for readers before dropping",
	}
	prev := -1.0
	for _, phase := range order {
		span, ok := reindexPhaseSpan[phase]
		if !ok {
			t.Fatalf("phase %q missing from reindexPhaseSpan", phase)
		}
		p := ReindexProgress{Phase: phase, Done: 1, Total: 2, LockersDone: 1, LockersTotal: 2}
		pct := p.OverallPct()
		if pct < span[0] || pct > span[1] {
			t.Errorf("%q: pct %.1f outside span [%.0f, %.0f]", phase, pct, span[0], span[1])
		}
		if pct <= prev {
			t.Errorf("%q: pct %.1f not past previous phase's %.1f", phase, pct, prev)
		}
		// A finished phase must not reach past where the next one starts.
		prev = span[1]
	}

	for _, tc := range []struct {
		name string
		p    ReindexProgress
		want float64
	}{
		{"phase start when total unknown",
			ReindexProgress{Phase: "building index: scanning table", Done: 10}, 3},
		{"full phase clamps at span end",
			ReindexProgress{Phase: "index validation: scanning table", Done: 300, Total: 200}, 95},
		{"waiting phase ignores blocks, uses lockers",
			ReindexProgress{Phase: "waiting for writers before build", Done: 99, Total: 100, LockersDone: 0, LockersTotal: 4}, 1},
		{"unknown phase", ReindexProgress{Phase: "doing something new"}, -1},
	} {
		if got := tc.p.OverallPct(); got != tc.want {
			t.Errorf("%s: got %.2f, want %.2f", tc.name, got, tc.want)
		}
	}

	// An AM subphase we don't know still stays inside the build slice.
	p := ReindexProgress{Phase: "building index: exotic am step", Done: 1, Total: 2}
	span := reindexPhaseSpan["building index"]
	if pct := p.OverallPct(); pct < span[0] || pct > span[1] {
		t.Errorf("unknown build subphase: pct %.1f outside build span [%.0f, %.0f]", pct, span[0], span[1])
	}
}

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
