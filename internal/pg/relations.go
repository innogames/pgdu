package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListRelations returns the page-inspector tool's mixed list of heap tables,
// drillable indexes (btree/gist/brin/gin), and TOAST heaps for one schema.
// Hash indexes are dropped at the SQL layer — pgdu has no hash drill path.
// Result is ordered by pg_relation_size (DESC); callers may resort.
func (c *Client) ListRelations(ctx context.Context, db, schema string) ([]Relation, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("list relations in %q.%q", db, schema), sqlRelations, []any{schema},
		func(row pgx.CollectableRow) (Relation, error) {
			r := Relation{DB: db}
			var kind string
			if err := row.Scan(
				&r.OID, &r.Name, &kind, &r.AccessMethod,
				&r.SizeBytes, &r.EstRows, &r.Pages,
				&r.ParentOID, &r.ParentName, &r.Schema,
			); err != nil {
				return r, err
			}
			switch kind {
			case "i":
				switch r.AccessMethod {
				case "gist":
					r.Kind = RelGist
				case "brin":
					r.Kind = RelBrin
				case "gin":
					r.Kind = RelGin
				default:
					r.Kind = RelBTreeIndex
				}
			case "t":
				r.Kind = RelToast
			default:
				r.Kind = RelTable
			}
			return r, nil
		})
}
