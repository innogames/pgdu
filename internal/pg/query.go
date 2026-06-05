package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// collect runs sql against pool and maps each row through scan, wrapping any
// failure with op for context (e.g. "list tables in \"db\".\"schema\""). It
// centralises the Query → rows.Next → Scan → append → rows.Err boilerplate that
// every straightforward list query repeats. Functions with per-row side effects
// or window bookkeeping — the page inspector, FillBloat, RunDiagnostic — keep
// their explicit loops since they don't fit this shape.
func collect[T any](ctx context.Context, pool *pgxpool.Pool, op, sql string, args []any, scan func(pgx.CollectableRow) (T, error)) ([]T, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return out, nil
}
