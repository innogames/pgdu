package pg

import (
	"context"
	"fmt"
)

// ListRelations returns the page-inspector tool's mixed list of heap tables
// and B-tree indexes for one schema. Non-btree indexes are dropped at the
// SQL layer — they're not drillable through bt_page_stats / bt_page_items.
// Result is ordered by pg_relation_size (DESC); callers may resort.
func (c *Client) ListRelations(ctx context.Context, db, schema string) ([]Relation, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlRelations, schema)
	if err != nil {
		return nil, fmt.Errorf("list relations in %q.%q: %w", db, schema, err)
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		r := Relation{DB: db}
		var kind string
		if err := rows.Scan(
			&r.OID, &r.Name, &kind, &r.AccessMethod,
			&r.SizeBytes, &r.EstRows, &r.Pages,
			&r.ParentOID, &r.ParentName, &r.Schema,
		); err != nil {
			return nil, fmt.Errorf("list relations in %q.%q: %w", db, schema, err)
		}
		switch kind {
		case "i":
			r.Kind = RelBTreeIndex
		case "t":
			r.Kind = RelToast
		default:
			r.Kind = RelTable
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list relations in %q.%q: %w", db, schema, err)
	}
	return out, nil
}
