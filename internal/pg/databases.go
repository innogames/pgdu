package pg

import (
	"context"
	"fmt"
)

// ListDatabases returns every database visible to the connecting role, with
// its on-disk size. Run against the initial database since pg_database is a
// shared catalog.
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	pool, err := c.PoolFor(ctx, c.cfg.Database)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlDatabases)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.Name, &d.SizeBytes); err != nil {
			return nil, fmt.Errorf("list databases: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	return out, nil
}
