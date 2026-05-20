package pg

import "context"

func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	pool, err := c.PoolFor(ctx, c.cfg.Database)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlDatabases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		var d Database
		if err := rows.Scan(&d.Name, &d.SizeBytes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
