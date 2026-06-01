package pg

import (
	"context"
	"fmt"
)

// DescribeTable fetches a psql-\d-style description of a table: its columns
// (with types, NOT NULL, and defaults), its indexes (full CREATE INDEX text),
// and its constraints (pg_get_constraintdef). Size and row-count come from
// the Table struct passed in — no extra round-trip for those.
func (c *Client) DescribeTable(ctx context.Context, t Table) (*Description, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}

	d := &Description{
		Kind:      DescribeTable,
		OID:       t.OID,
		Title:     t.Qualified(),
		SizeBytes: t.TotalBytes,
		EstRows:   t.EstRows,
	}

	// Columns
	rows, err := pool.Query(ctx, sqlDescribeColumns, t.OID)
	if err != nil {
		return nil, fmt.Errorf("describe columns for %q.%q: %w", t.Schema, t.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var col DescribeColumn
		if err := rows.Scan(&col.Name, &col.Type, &col.NotNull, &col.Default); err != nil {
			return nil, fmt.Errorf("describe columns for %q.%q: %w", t.Schema, t.Name, err)
		}
		d.Columns = append(d.Columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("describe columns for %q.%q: %w", t.Schema, t.Name, err)
	}
	rows.Close()

	// Indexes
	idxRows, err := pool.Query(ctx, sqlDescribeIndexes, t.OID)
	if err != nil {
		return nil, fmt.Errorf("describe indexes for %q.%q: %w", t.Schema, t.Name, err)
	}
	defer idxRows.Close()
	for idxRows.Next() {
		var idx DescribeIndexDef
		if err := idxRows.Scan(&idx.Name, &idx.Def, &idx.IsPrimary, &idx.IsUnique); err != nil {
			return nil, fmt.Errorf("describe indexes for %q.%q: %w", t.Schema, t.Name, err)
		}
		d.Indexes = append(d.Indexes, idx)
	}
	if err := idxRows.Err(); err != nil {
		return nil, fmt.Errorf("describe indexes for %q.%q: %w", t.Schema, t.Name, err)
	}
	idxRows.Close()

	// Constraints
	conRows, err := pool.Query(ctx, sqlDescribeConstraints, t.OID)
	if err != nil {
		return nil, fmt.Errorf("describe constraints for %q.%q: %w", t.Schema, t.Name, err)
	}
	defer conRows.Close()
	for conRows.Next() {
		var con DescribeConstraint
		if err := conRows.Scan(&con.Name, &con.Def); err != nil {
			return nil, fmt.Errorf("describe constraints for %q.%q: %w", t.Schema, t.Name, err)
		}
		d.Constraints = append(d.Constraints, con)
	}
	if err := conRows.Err(); err != nil {
		return nil, fmt.Errorf("describe constraints for %q.%q: %w", t.Schema, t.Name, err)
	}

	return d, nil
}

// DescribeIndex fetches a psql-\d-style description of one index: the full
// CREATE INDEX text from pg_get_indexdef, access method, unique/primary flags,
// the parent table name, and the partial-index predicate if any.
func (c *Client) DescribeIndex(ctx context.Context, db string, oid uint32, name string) (*Description, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}

	d := &Description{
		Kind:  DescribeIndex,
		OID:   oid,
		Title: name,
	}
	if err := pool.QueryRow(ctx, sqlDescribeIndex, oid).Scan(
		&d.IndexDef,
		&d.AccessMethod,
		&d.IdxUnique,
		&d.IdxPrimary,
		&d.Predicate,
		&d.ParentTable,
	); err != nil {
		return nil, fmt.Errorf("describe index %q: %w", name, err)
	}

	return d, nil
}
