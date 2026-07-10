package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ReindexIndex runs REINDEX INDEX CONCURRENTLY on the named index. The index
// is resolved in the schema of the parent table — index relnames returned by
// sqlIndexes are bare, without schema.
func (c *Client) ReindexIndex(ctx context.Context, t Table, indexName string) error {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return err
	}
	stmt := "REINDEX INDEX CONCURRENTLY " + qualifiedIdent(t.Schema, indexName)
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("reindex %q.%q: %w", t.Schema, indexName, err)
	}
	return nil
}

// sqlReindexProgress reads pg_stat_progress_create_index for the (single)
// build in flight on tableOID. REINDEX reports into the same view as CREATE
// INDEX, keyed by the table's relid. Scan phases count in blocks and the
// sort/load phases in tuples, so pick whichever counter has a live total;
// the lockers columns carry progress through the "waiting for …" phases,
// where blocks/tuples don't move at all.
const sqlReindexProgress = `
SELECT
    phase,
    (CASE WHEN blocks_total > 0 THEN blocks_done  ELSE tuples_done  END)::bigint AS done,
    (CASE WHEN blocks_total > 0 THEN blocks_total ELSE tuples_total END)::bigint AS total,
    lockers_done,
    lockers_total
FROM pg_stat_progress_create_index
WHERE relid = $1
ORDER BY pid
LIMIT 1
`

// ReindexProgress is one poll of pg_stat_progress_create_index: the current
// phase, its own done/total counters (blocks or tuples, whichever the phase
// reports), and the lockers counters that stand in for progress during the
// "waiting for …" phases.
type ReindexProgress struct {
	Phase        string
	Done         int64
	Total        int64
	LockersDone  int64
	LockersTotal int64
}

// Waiting reports whether the build is in one of the lock-wait phases, where
// the meaningful counters are lockers, not blocks/tuples.
func (p ReindexProgress) Waiting() bool { return strings.HasPrefix(p.Phase, "waiting") }

// reindexPhaseSpan maps each REINDEX CONCURRENTLY phase (as spelled by the
// pg_stat_progress_create_index view, including the btree build subphases) to
// its slice of an overall 0–100% bar. The per-phase counters reset at every
// phase change, so a naive done/total bar snaps back to zero mid-rebuild;
// pinning each phase to a fixed span turns that into one left-to-right pass.
// The weights are guesses, not measurements — they only need to be monotonic
// and roughly plausible, so the two block-proportional table scans get the
// big slices and the sort/wait phases get slivers.
var reindexPhaseSpan = map[string][2]float64{
	"initializing":                            {0, 1},
	"waiting for writers before build":        {1, 2},
	"building index":                          {2, 65}, // AMs without subphase reporting
	"building index: initializing":            {2, 3},
	"building index: scanning table":          {3, 35},
	"building index: sorting live tuples":     {35, 40},
	"building index: loading tuples in tree":  {40, 65},
	"waiting for writers before validation":   {65, 66},
	"index validation: scanning index":        {66, 74},
	"index validation: sorting tuples":        {74, 77},
	"index validation: scanning table":        {77, 95},
	"waiting for old snapshots":               {95, 97},
	"waiting for readers before marking dead": {97, 98},
	"waiting for readers before dropping":     {98, 100},
}

// OverallPct places the current phase's own progress inside its
// reindexPhaseSpan slice and returns the composite 0..100 estimate, or -1 for
// a phase we can't map (new PG version, unknown AM) — callers hold the bar
// where it was rather than jumping. Totals are estimates (reltuples) and
// briefly read 0 on phase transitions, so callers must also clamp the result
// monotonic across polls.
func (p ReindexProgress) OverallPct() float64 {
	span, ok := reindexPhaseSpan[p.Phase]
	if !ok {
		// An unmapped AM-specific subphase still bounds us to the build slice.
		if !strings.HasPrefix(p.Phase, "building index:") {
			return -1
		}
		span = reindexPhaseSpan["building index"]
	}
	done, total := p.Done, p.Total
	if p.Waiting() {
		done, total = p.LockersDone, p.LockersTotal
	}
	frac := 0.0
	if total > 0 {
		frac = min(float64(done)/float64(total), 1)
	}
	return span[0] + frac*(span[1]-span[0])
}

// ReindexProgress returns the live progress of a REINDEX/CREATE INDEX running
// on tableOID, or ok=false when no build is currently reporting (not started
// yet, between phases, or already finished). Best-effort: callers poll it and
// tolerate a missing row.
func (c *Client) ReindexProgress(ctx context.Context, db string, tableOID uint32) (ReindexProgress, bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return ReindexProgress{}, false, fmt.Errorf("reindex progress in %q: %w", db, err)
	}
	var r ReindexProgress
	err = pool.QueryRow(ctx, sqlReindexProgress, tableOID).
		Scan(&r.Phase, &r.Done, &r.Total, &r.LockersDone, &r.LockersTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReindexProgress{}, false, nil
	}
	if err != nil {
		return ReindexProgress{}, false, fmt.Errorf("reindex progress in %q: %w", db, err)
	}
	return r, true, nil
}
