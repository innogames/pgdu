package pg

import (
	"context"
	"fmt"
)

// ListSchemas returns user-visible schemas in db, with total size and the
// number of tables in each (rounded down to leaf relations).
func (c *Client) ListSchemas(ctx context.Context, db string) ([]Schema, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlSchemas)
	if err != nil {
		return nil, fmt.Errorf("list schemas in %q: %w", db, err)
	}
	defer rows.Close()
	var out []Schema
	for rows.Next() {
		s := Schema{DB: db}
		if err := rows.Scan(&s.Name, &s.SizeBytes, &s.TableCount); err != nil {
			return nil, fmt.Errorf("list schemas in %q: %w", db, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schemas in %q: %w", db, err)
	}
	return out, nil
}
