package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListProgress returns every operation currently reporting into a
// pg_stat_progress_* view. The views are cluster-wide, so db only selects the
// pool to query through; an empty result means nothing is in flight.
func (c *Client) ListProgress(ctx context.Context, db string) ([]ProgressRow, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list progress in %q: %w", db, err)
	}
	return collect(ctx, pool, fmt.Sprintf("list progress in %q", db), sqlProgressOps, nil,
		func(row pgx.CollectableRow) (ProgressRow, error) {
			var r ProgressRow
			err := row.Scan(
				&r.PID, &r.Command, &r.Relation, &r.Phase, &r.Unit,
				&r.Done, &r.Total, &r.Approx, &r.RunningMs, &r.Username,
			)
			return r, err
		})
}
