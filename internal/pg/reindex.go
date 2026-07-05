package pg

import (
	"context"
	"errors"
	"fmt"

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
// INDEX, keyed by the table's relid. The scan phase counts in blocks and the
// validation phase in tuples, so pick whichever counter has a live total —
// that keeps the bar meaningful across the phase change instead of snapping
// back to an empty blocks total mid-build.
const sqlReindexProgress = `
SELECT
    phase,
    (CASE WHEN blocks_total > 0 THEN blocks_done  ELSE tuples_done  END)::bigint AS done,
    (CASE WHEN blocks_total > 0 THEN blocks_total ELSE tuples_total END)::bigint AS total
FROM pg_stat_progress_create_index
WHERE relid = $1
ORDER BY pid
LIMIT 1
`

// ReindexProgress returns the live progress of a REINDEX/CREATE INDEX running
// on tableOID, or ok=false when no build is currently reporting (not started
// yet, between phases, or already finished). Best-effort: callers poll it and
// tolerate a missing row.
func (c *Client) ReindexProgress(ctx context.Context, db string, tableOID uint32) (ProgressRow, bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return ProgressRow{}, false, fmt.Errorf("reindex progress in %q: %w", db, err)
	}
	r := ProgressRow{Unit: "blocks"}
	err = pool.QueryRow(ctx, sqlReindexProgress, tableOID).Scan(&r.Phase, &r.Done, &r.Total)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgressRow{}, false, nil
	}
	if err != nil {
		return ProgressRow{}, false, fmt.Errorf("reindex progress in %q: %w", db, err)
	}
	return r, true, nil
}
