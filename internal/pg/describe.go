package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// ResolveTableAnyDB is ResolveTable for a name whose database is unknown — a
// log line whose prefix carries no %d says which user ran the statement but
// not where. The preferred database is tried first, then every other
// connectable database in ListDatabases order (largest first), and the first
// hit wins: a table that exists in several databases resolves to the largest,
// the likeliest home of a logged statement. A database that fails to connect
// is skipped, so the outcome is a hit or a not-found, never a connect error
// for some unrelated database.
func (c *Client) ResolveTableAnyDB(ctx context.Context, preferred, name string) (Table, error) {
	var t Table
	found, err := c.findInAnyDB(ctx, preferred, func(db string) error {
		var rerr error
		t, rerr = c.ResolveTable(ctx, db, name)
		return rerr
	})
	if err != nil {
		return Table{}, err
	}
	if !found {
		return Table{}, &MissingRelationError{Name: name, AnyDB: true}
	}
	return t, nil
}

// ResolveIndexAnyDB is ResolveIndex across databases, the index analogue of
// ResolveTableAnyDB; db names the database the index was found in.
func (c *Client) ResolveIndexAnyDB(ctx context.Context, preferred, name string) (db string, oid uint32, qualified string, err error) {
	found, err := c.findInAnyDB(ctx, preferred, func(cand string) error {
		var rerr error
		oid, qualified, rerr = c.ResolveIndex(ctx, cand, name)
		if rerr == nil {
			db = cand
		}
		return rerr
	})
	if err != nil {
		return "", 0, "", err
	}
	if !found {
		return "", 0, "", &MissingRelationError{Name: name, AnyDB: true}
	}
	return db, oid, qualified, nil
}

// FilenodeRel is what a WAL block reference's relfilenode resolves to: the
// owning table (TOAST hops to its parent) or an index by OID and display name.
type FilenodeRel struct {
	IsIndex   bool
	Table     Table  // when !IsIndex
	IndexOID  uint32 // when IsIndex
	IndexName string // when IsIndex — schema.name, as DescribeIndex titles it
}

// ResolveFilenode maps a WAL block reference's (tablespace, relfilenode) pair to
// the relation `d` should describe, looked up in db because pg_filenode_relation
// only knows the connected database's files. A relation that is no longer in the
// catalog (dropped, or rewritten by VACUUM FULL / REINDEX so the filenode moved
// on) is a plain error the describe panel shows as is; a relkind the panel
// cannot describe (sequence, view, composite type) likewise.
func (c *Client) ResolveFilenode(ctx context.Context, db string, tablespace, filenode uint32) (FilenodeRel, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return FilenodeRel{}, err
	}
	var (
		oid          uint32
		kind, schema string
		name         string
		size, rows   int64
	)
	err = pool.QueryRow(ctx, sqlResolveFilenode, tablespace, filenode).Scan(&oid, &kind, &schema, &name, &size, &rows)
	if errors.Is(err, pgx.ErrNoRows) {
		return FilenodeRel{}, fmt.Errorf("relfilenode %d is not in the catalog of %q (dropped or rewritten since)", filenode, db)
	}
	if err != nil {
		return FilenodeRel{}, fmt.Errorf("resolve relfilenode %d in %q: %w", filenode, db, err)
	}
	switch kind {
	case "r", "p", "m", "f":
		return FilenodeRel{Table: Table{DB: db, Schema: schema, Name: name, OID: oid, TotalBytes: size, EstRows: rows}}, nil
	case "i", "I":
		return FilenodeRel{IsIndex: true, IndexOID: oid, IndexName: schema + "." + name}, nil
	}
	return FilenodeRel{}, fmt.Errorf("relfilenode %d in %q is %s.%s (relkind %s), which has no describe panel", filenode, db, schema, name, kind)
}

// findInAnyDB runs try against the preferred database and then every other
// connectable one until it succeeds. A *MissingRelationError or a connect
// failure moves on to the next database; any other error (a broken catalog
// query, a cancelled context) is returned as is, since retrying it elsewhere
// would only repeat it. The preferred database is the one exception: its
// connect failure is reported, because the caller could not have described
// anything there either and hiding it would misreport the cause.
func (c *Client) findInAnyDB(ctx context.Context, preferred string, try func(db string) error) (found bool, err error) {
	if preferred == "" {
		preferred = c.DefaultDB()
	}
	err = try(preferred)
	if err == nil {
		return true, nil
	}
	var missing *MissingRelationError
	if !errors.As(err, &missing) {
		return false, err
	}
	dbs, err := c.ListDatabases(ctx)
	if err != nil {
		return false, err
	}
	for _, d := range dbs {
		if d.Name == preferred {
			continue
		}
		if _, perr := c.PoolFor(ctx, d.Name); perr != nil {
			continue
		}
		switch err = try(d.Name); {
		case err == nil:
			return true, nil
		case errors.As(err, &missing):
			continue
		default:
			return false, err
		}
	}
	return false, nil
}

// MissingRelationError reports that a name couldn't be resolved to a describable
// relation (e.g. it's a CTE alias, a view, or simply doesn't exist). AnyDB
// marks a miss after a sweep over every database, so the message says how far
// the search went.
type MissingRelationError struct {
	Name  string
	AnyDB bool
}

func (e *MissingRelationError) Error() string {
	// Names from diagnostic rows arrive pre-quoted for to_regclass; don't
	// wrap those in a second layer of quotes.
	msg := fmt.Sprintf("no table named %q", e.Name)
	if strings.Contains(e.Name, `"`) {
		msg = "no table named " + e.Name
	}
	if e.AnyDB {
		msg += " in any database"
	}
	return msg
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
				&idx.SizeBytes, &idx.Predicate, &idx.EstEntries,
				&idx.Scans, &idx.LastScan, &idx.TupRead, &idx.TupFetch,
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
		&d.IdxEstEntries,
		&d.IdxParentRows,
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
