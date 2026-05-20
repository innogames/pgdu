package pg

import "context"

func (c *Client) ListTables(ctx context.Context, db, schema string) ([]Table, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlTables, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		t := Table{DB: db, Schema: schema}
		if err := rows.Scan(&t.OID, &t.Name, &t.HeapBytes, &t.IndexesBytes, &t.ToastBytes, &t.TotalBytes, &t.EstRows); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
