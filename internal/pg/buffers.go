package pg

import (
	"context"
	"fmt"
)

// EnsureBufferCache makes sure pg_buffercache is installed in db. Result is
// cached on the Client so repeated entries into the view don't re-probe.
//
// pg_buffercache is a built-in contrib extension but it's not enabled by
// default; CREATE EXTENSION requires database-owner or superuser, and
// querying its view requires SELECT (granted to pg_monitor by default in
// modern Postgres). We surface those failures verbatim so the user can see
// what privilege is missing.
func (c *Client) EnsureBufferCache(ctx context.Context, db string) error {
	c.mu.Lock()
	if c.bufCacheReady[db] {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return err
	}
	var installed bool
	if err := pool.QueryRow(ctx, sqlBufferCacheProbe).Scan(&installed); err != nil {
		return fmt.Errorf("probe pg_buffercache: %w", err)
	}
	if !installed {
		if _, err := pool.Exec(ctx, sqlBufferCacheCreate); err != nil {
			return fmt.Errorf("pg_buffercache not installed and CREATE EXTENSION failed (needs superuser or db owner): %w", err)
		}
	}
	c.mu.Lock()
	c.bufCacheReady[db] = true
	c.mu.Unlock()
	return nil
}

// TableBufferStats lists per-table shared-buffer footprint and cumulative
// hit/read counters for one schema. Buffer footprint sums heap + toast +
// indexes — the natural answer to "which table is using the cache".
func (c *Client) TableBufferStats(ctx context.Context, db, schema string) ([]TableBufferStat, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlBufferStats, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TableBufferStat
	for rows.Next() {
		s := TableBufferStat{DB: db, Schema: schema}
		if err := rows.Scan(&s.OID, &s.Schema, &s.Name, &s.BufferedBytes, &s.TotalBytes, &s.Hits, &s.Reads); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
