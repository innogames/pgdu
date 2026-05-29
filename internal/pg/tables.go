package pg

import (
	"context"
	"fmt"
)

// ListTables returns every base table in db.schema with heap/index/toast/total
// size and the planner's row estimate. Result is unsorted; sort at the call
// site.
func (c *Client) ListTables(ctx context.Context, db, schema string) ([]Table, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlTables, schema)
	if err != nil {
		return nil, fmt.Errorf("list tables in %q.%q: %w", db, schema, err)
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		t := Table{DB: db, Schema: schema}
		if err := rows.Scan(&t.OID, &t.Name, &t.HeapBytes, &t.IndexesBytes, &t.ToastBytes, &t.TotalBytes, &t.EstRows, &t.ToastOID, &t.ToastName); err != nil {
			return nil, fmt.Errorf("list tables in %q.%q: %w", db, schema, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables in %q.%q: %w", db, schema, err)
	}
	return out, nil
}
