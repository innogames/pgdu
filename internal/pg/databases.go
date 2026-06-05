package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ListDatabases returns every database visible to the connecting role, with
// its on-disk size. Run against the initial database since pg_database is a
// shared catalog.
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	pool, err := c.PoolFor(ctx, c.cfg.Database)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, "list databases", sqlDatabases, nil,
		func(row pgx.CollectableRow) (Database, error) {
			var d Database
			err := row.Scan(&d.Name, &d.SizeBytes)
			return d, err
		})
}
