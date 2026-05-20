package pg

import "context"

func (c *Client) ListSchemas(ctx context.Context, db string) ([]Schema, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlSchemas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schema
	for rows.Next() {
		s := Schema{DB: db}
		if err := rows.Scan(&s.Name, &s.SizeBytes, &s.TableCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
