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

// OverallPct places the current phase's own progress inside its
// reindexPhaseSpan slice (see progress_pct.go) and returns the composite
// 0..100 estimate, or -1 for a phase we can't map (new PG version, unknown
// AM) — callers hold the bar where it was rather than jumping. Totals are
// estimates (reltuples) and briefly read 0 on phase transitions, so callers
// must also clamp the result monotonic across polls.
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
	return spanPoint(span, done, total)
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
