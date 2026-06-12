package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ResolveTable looks up a relation by (optionally schema-qualified) name and
// returns the Table metadata DescribeTable needs — OID, schema, size and row
// estimate. It exists so callers that only know a name (the top-queries view,
// which parses it out of the statement text) can still describe the relation.
// Returns *MissingRelationError when the name doesn't resolve to an ordinary
// table/partitioned/materialized/foreign relation, so the caller can show a
// friendly "no such table" rather than a blank panel.
func (c *Client) ResolveTable(ctx context.Context, db, name string) (Table, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return Table{}, err
	}
	t := Table{DB: db}
	err = pool.QueryRow(ctx, sqlResolveTable, name).
		Scan(&t.OID, &t.Schema, &t.Name, &t.TotalBytes, &t.EstRows)
	if errors.Is(err, pgx.ErrNoRows) {
		return Table{}, &MissingRelationError{Name: name}
	}
	if err != nil {
		return Table{}, fmt.Errorf("resolve table %q in %q: %w", name, db, err)
	}
	return t, nil
}

// MissingRelationError reports that a name couldn't be resolved to a describable
// relation (e.g. it's a CTE alias, a view, or simply doesn't exist).
type MissingRelationError struct{ Name string }

func (e *MissingRelationError) Error() string {
	return fmt.Sprintf("no table named %q", e.Name)
}

// DescribeTable fetches a psql-\d-style description of a table: its columns
// (with types, NOT NULL, and defaults) and its indexes (full CREATE INDEX
// text). Size and row-count come from the Table struct passed in — no extra
// round-trip for those.
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
		if err := idxRows.Scan(&idx.Name, &idx.Def, &idx.IsPrimary, &idx.IsUnique, &idx.Clustered); err != nil {
			return nil, fmt.Errorf("describe indexes for %q.%q: %w", t.Schema, t.Name, err)
		}
		d.Indexes = append(d.Indexes, idx)
	}
	if err := idxRows.Err(); err != nil {
		return nil, fmt.Errorf("describe indexes for %q.%q: %w", t.Schema, t.Name, err)
	}
	idxRows.Close()

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
