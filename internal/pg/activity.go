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

// toastKey keys the session TOAST-owner cache. TOAST OIDs are database-local, so
// the same pg_toast_<oid> relname can name different tables in different
// databases — the db must be part of the key.
func toastKey(db, relname string) string { return db + "\x00" + relname }

// ResolveToastOwners maps TOAST relnames (pg_toast_<oid>, without the pg_toast.
// schema prefix) to the table that owns them, so the Activity tool can label an
// autovacuum-of-TOAST row with the real table instead of the opaque OID. Results
// are cached for the session (see toastCache); only relnames not already cached
// trigger the catalog query. A relname that resolves to nothing (dropped table,
// unreadable catalog) is cached as "" so it isn't re-queried on every refresh.
// The returned map has one entry per requested relname ("" = unresolved).
func (c *Client) ResolveToastOwners(ctx context.Context, db string, relnames []string) (map[string]string, error) {
	out := make(map[string]string, len(relnames))
	var miss []string
	c.toastMu.Lock()
	for _, rn := range relnames {
		if owner, ok := c.toastCache[toastKey(db, rn)]; ok {
			out[rn] = owner
		} else {
			miss = append(miss, rn)
		}
	}
	c.toastMu.Unlock()
	if len(miss) == 0 {
		return out, nil
	}

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return out, fmt.Errorf("resolve toast owners in %q: %w", db, err)
	}
	rows, err := collect(ctx, pool, fmt.Sprintf("resolve toast owners in %q", db),
		sqlToastOwners, []any{miss},
		func(row pgx.CollectableRow) (toastOwner, error) {
			var o toastOwner
			err := row.Scan(&o.toast, &o.owner)
			return o, err
		})
	if err != nil {
		return out, err
	}

	found := make(map[string]string, len(rows))
	for _, o := range rows {
		found[o.toast] = o.owner
	}
	c.toastMu.Lock()
	for _, rn := range miss {
		owner := found[rn] // "" when the relation didn't resolve — negative-cached
		c.toastCache[toastKey(db, rn)] = owner
		out[rn] = owner
	}
	c.toastMu.Unlock()
	return out, nil
}

// toastOwner is one row of sqlToastOwners: a TOAST relname and its owning table.
type toastOwner struct {
	toast string
	owner string
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
