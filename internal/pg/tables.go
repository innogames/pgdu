package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListTables returns every base table in db.schema with heap/index/toast/total
// size and the planner's row estimate. Result is unsorted; sort at the call
// site.
func (c *Client) ListTables(ctx context.Context, db, schema string) ([]Table, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("list tables in %q.%q", db, schema), sqlTables, []any{schema},
		func(row pgx.CollectableRow) (Table, error) {
			t := Table{DB: db, Schema: schema}
			err := row.Scan(&t.OID, &t.Name, &t.HeapBytes, &t.IndexesBytes, &t.ToastBytes, &t.TotalBytes, &t.EstRows, &t.ToastOID, &t.ToastName)
			return t, err
		})
}
