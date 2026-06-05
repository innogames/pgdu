package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListSchemas returns user-visible schemas in db, with total size and the
// number of tables in each (rounded down to leaf relations).
func (c *Client) ListSchemas(ctx context.Context, db string) ([]Schema, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("list schemas in %q", db), sqlSchemas, nil,
		func(row pgx.CollectableRow) (Schema, error) {
			s := Schema{DB: db}
			err := row.Scan(&s.Name, &s.SizeBytes, &s.TableCount)
			return s, err
		})
}
