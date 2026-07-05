package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListActivity returns the current backends from pg_stat_activity, filtered
// according to mode. Rows are ordered by query_start ascending so the
// longest-running query appears first after the caller sorts by query_age_ms
// descending.
func (c *Client) ListActivity(ctx context.Context, db string, mode ActivityFilter) ([]ActivityRow, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list activity in %q: %w", db, err)
	}
	var sql string
	switch mode {
	case ActivityNonIdle:
		sql = sqlActivityNonIdle
	case ActivityAll:
		sql = sqlActivityAll
	default:
		sql = sqlActivityActiveWaiting
	}
	return collect(ctx, pool, fmt.Sprintf("list activity in %q", db), sql, nil,
		func(row pgx.CollectableRow) (ActivityRow, error) {
			var r ActivityRow
			err := row.Scan(
				&r.PID, &r.Database, &r.Username, &r.AppName, &r.ClientAddr,
				&r.BackendType, &r.State, &r.WaitEventType, &r.WaitEvent,
				&r.BackendXid, &r.BackendXmin,
				&r.QueryAgeMs, &r.XactAgeMs, &r.StateAgeMs,
				&r.QueryID, &r.BlockedBy, &r.Query,
			)
			return r, err
		})
}

// ActivitySummary returns server-wide backend counts and the connection limit
// for the header, independent of the current row filter.
func (c *Client) ActivitySummary(ctx context.Context, db string) (ActivitySummary, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return ActivitySummary{}, fmt.Errorf("activity summary in %q: %w", db, err)
	}
	var s ActivitySummary
	err = pool.QueryRow(ctx, sqlActivitySummary).Scan(
		&s.Conns, &s.Active, &s.Waiting, &s.IdleInXact, &s.Idle, &s.Other,
		&s.MaxConnections,
	)
	if err != nil {
		return ActivitySummary{}, fmt.Errorf("activity summary in %q: %w", db, err)
	}
	return s, nil
}

// CancelBackend sends pg_cancel_backend to the given PID. Returns true when the
// signal was delivered, false when the backend no longer exists or the caller
// lacks permission.
func (c *Client) CancelBackend(ctx context.Context, db string, pid int32) (bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, fmt.Errorf("cancel backend %d in %q: %w", pid, db, err)
	}
	var ok bool
	err = pool.QueryRow(ctx, "SELECT pg_cancel_backend($1)", pid).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("cancel backend %d in %q: %w", pid, db, err)
	}
	return ok, nil
}

// TerminateBackend sends pg_terminate_backend to the given PID. Returns true
// when the backend was terminated, false when it no longer exists or the caller
// lacks permission.
func (c *Client) TerminateBackend(ctx context.Context, db string, pid int32) (bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, fmt.Errorf("terminate backend %d in %q: %w", pid, db, err)
	}
	var ok bool
	err = pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("terminate backend %d in %q: %w", pid, db, err)
	}
	return ok, nil
}
