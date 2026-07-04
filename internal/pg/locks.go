package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListLockWaiters returns every backend involved in a lock-wait relationship —
// blocked, blocking, or both — with its blockers and the lock it is waiting on.
// The caller assembles the blocking forest from the pid→blockers edges. An
// empty result means no contention. pg_blocking_pids and pg_locks are readable
// by any role, so this needs no special privilege (relation names it can't
// resolve simply come back empty).
func (c *Client) ListLockWaiters(ctx context.Context, db string) ([]LockNode, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list lock waiters in %q: %w", db, err)
	}
	return collect(ctx, pool, fmt.Sprintf("list lock waiters in %q", db), sqlLockWaiters, nil,
		func(row pgx.CollectableRow) (LockNode, error) {
			var n LockNode
			err := row.Scan(
				&n.PID, &n.Blockers,
				&n.Database, &n.Username, &n.AppName, &n.State,
				&n.WaitEventType, &n.WaitEvent,
				&n.XactAgeMs,
				&n.WaitLockType, &n.WaitMode, &n.WaitRelation,
				&n.Query,
			)
			return n, err
		})
}
