package pg

import "context"

// ListColumns returns per-column space estimates for one table. Sourced from
// pg_stats and pg_attribute — no table scan, so it's safe to run against
// large tables. Results are only as fresh as the last ANALYZE.
func (c *Client) ListColumns(ctx context.Context, db string, tableOID uint32) ([]Column, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlColumns, tableOID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Name, &col.Type, &col.AvgWidth, &col.NullFrac, &col.EstBytes); err != nil {
			return nil, err
		}
		out = append(out, col)
	}
	return out, rows.Err()
}
