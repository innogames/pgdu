package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

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
// (with types, NOT NULL, and defaults), its indexes (full CREATE INDEX text
// plus size/usage counters), and the detail-mode stat counters (Stats). Size
// and row-count come from the Table struct passed in — no extra round-trip
// for those.
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
		LoadedAt:  time.Now(),
	}

	d.Columns, err = collect(ctx, pool, fmt.Sprintf("describe columns for %q.%q", t.Schema, t.Name), sqlDescribeColumns, []any{t.OID},
		func(row pgx.CollectableRow) (DescribeColumn, error) {
			var col DescribeColumn
			err := row.Scan(&col.Name, &col.Type, &col.NotNull, &col.Default, &col.Indexed)
			return col, err
		})
	if err != nil {
		return nil, err
	}

	d.Indexes, err = collect(ctx, pool, fmt.Sprintf("describe indexes for %q.%q", t.Schema, t.Name), sqlDescribeIndexes, []any{t.OID},
		func(row pgx.CollectableRow) (DescribeIndexDef, error) {
			var idx DescribeIndexDef
			err := row.Scan(&idx.Name, &idx.Def, &idx.IsPrimary, &idx.IsUnique, &idx.Clustered,
				&idx.SizeBytes, &idx.Scans, &idx.LastScan, &idx.TupRead, &idx.TupFetch,
				&idx.BlksHit, &idx.BlksRead)
			return idx, err
		})
	if err != nil {
		return nil, err
	}

	// Foreign keys, both directions. Both are cheap pg_constraint scans, so
	// they ride this same describe round-trip rather than a separate Cmd.
	fkOp := fmt.Sprintf("describe foreign keys for %q.%q", t.Schema, t.Name)
	if d.FKOutgoing, err = queryFKs(ctx, pool, fkOp, sqlDescribeFKOutgoing, t.OID); err != nil {
		return nil, err
	}
	if d.FKIncoming, err = queryFKs(ctx, pool, fkOp, sqlDescribeFKIncoming, t.OID); err != nil {
		return nil, err
	}

	// Options
	if err := pool.QueryRow(ctx, sqlDescribeOptions, t.OID).Scan(&d.Options); err != nil {
		return nil, fmt.Errorf("describe options for %q.%q: %w", t.Schema, t.Name, err)
	}

	// Detail-mode counters (size split, tuple churn, scans, maintenance).
	st := &DescribeStats{}
	if err := pool.QueryRow(ctx, sqlDescribeStats, t.OID).Scan(
		&st.HeapBytes, &st.IndexBytes, &st.ToastBytes,
		&st.LiveTup, &st.DeadTup,
		&st.Inserts, &st.Updates, &st.Deletes, &st.HotUpdates,
		&st.ModSinceAnalyze, &st.InsSinceVacuum,
		&st.SeqScans, &st.IdxScans, &st.LastSeqScan, &st.LastIdxScan,
		&st.LastVacuum, &st.LastAnalyze, &st.VacuumCount, &st.AnalyzeCount,
		&st.FrozenXIDAge,
	); err != nil {
		return nil, fmt.Errorf("describe stats for %q.%q: %w", t.Schema, t.Name, err)
	}
	d.Stats = st

	return d, nil
}

// queryFKs runs one of the describe FK queries (outgoing/incoming share a column
// shape) against a table oid and maps the action codes to labels.
func queryFKs(ctx context.Context, pool *pgxpool.Pool, op, sql string, oid uint32) ([]DescribeFK, error) {
	return collect(ctx, pool, op, sql, []any{oid},
		func(row pgx.CollectableRow) (DescribeFK, error) {
			var fk DescribeFK
			var delCode, updCode string
			if err := row.Scan(&fk.Name, &fk.LocalCols, &fk.OtherTable, &fk.OtherCols, &delCode, &updCode); err != nil {
				return fk, err
			}
			fk.OnDelete = fkAction(delCode)
			fk.OnUpdate = fkAction(updCode)
			return fk, nil
		})
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
		Kind:     DescribeIndex,
		OID:      oid,
		Title:    name,
		LoadedAt: time.Now(),
	}
	if err := pool.QueryRow(ctx, sqlDescribeIndex, oid).Scan(
		&d.IndexDef,
		&d.AccessMethod,
		&d.IdxUnique,
		&d.IdxPrimary,
		&d.Predicate,
		&d.ParentTable,
		&d.IdxSizeBytes,
		&d.IdxScans,
		&d.IdxLastScan,
		&d.IdxTupRead,
		&d.IdxTupFetch,
		&d.IdxBlksHit,
		&d.IdxBlksRead,
	); err != nil {
		return nil, fmt.Errorf("describe index %q: %w", name, err)
	}

	return d, nil
}
