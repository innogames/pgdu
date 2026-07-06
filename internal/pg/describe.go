package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// ResolveIndex resolves an index name (optionally schema-qualified) to its OID
// and qualified display name, so `d` on rows that only carry an index name
// (diagnostic results) can reach DescribeIndex.
func (c *Client) ResolveIndex(ctx context.Context, db, name string) (oid uint32, qualified string, err error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return 0, "", err
	}
	var schema, rel string
	err = pool.QueryRow(ctx, sqlResolveIndex, name).Scan(&oid, &schema, &rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", &MissingRelationError{Name: name}
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve index %q in %q: %w", name, db, err)
	}
	return oid, schema + "." + rel, nil
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

	// Foreign keys, both directions. Both are cheap pg_constraint scans, so
	// they ride this same describe round-trip rather than a separate Cmd.
	if d.FKOutgoing, err = queryFKs(ctx, pool, sqlDescribeFKOutgoing, t.OID); err != nil {
		return nil, fmt.Errorf("describe foreign keys for %q.%q: %w", t.Schema, t.Name, err)
	}
	if d.FKIncoming, err = queryFKs(ctx, pool, sqlDescribeFKIncoming, t.OID); err != nil {
		return nil, fmt.Errorf("describe foreign keys for %q.%q: %w", t.Schema, t.Name, err)
	}

	return d, nil
}

// queryFKs runs one of the describe FK queries (outgoing/incoming share a column
// shape) against a table oid and maps the action codes to labels.
func queryFKs(ctx context.Context, pool *pgxpool.Pool, sql string, oid uint32) ([]DescribeFK, error) {
	rows, err := pool.Query(ctx, sql, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fks []DescribeFK
	for rows.Next() {
		var fk DescribeFK
		var delCode, updCode string
		if err := rows.Scan(&fk.Name, &fk.LocalCols, &fk.OtherTable, &fk.OtherCols, &delCode, &updCode); err != nil {
			return nil, err
		}
		fk.OnDelete = fkAction(delCode)
		fk.OnUpdate = fkAction(updCode)
		fks = append(fks, fk)
	}
	return fks, rows.Err()
}

// fkAction maps a pg_constraint confdeltype/confupdtype code to a label,
// returning "" for 'a' (NO ACTION, the default) so callers can skip it.
func fkAction(code string) string {
	switch code {
	case "c":
		return "cascade"
	case "n":
		return "set null"
	case "d":
		return "set default"
	case "r":
		return "restrict"
	default: // 'a' NO ACTION
		return ""
	}
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
