package pg

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ListColumns returns per-column space estimates for one table.
//
// For varlena columns that frequently get TOAST'd (jsonb, bytea, text, …),
// pg_stats.avg_width records only the in-heap width — typically the ~18-byte
// toast pointer — which dramatically undercounts on-disk usage. So we run a
// small TABLESAMPLE'd scan and use pg_column_size(), which includes compressed
// TOAST bytes, to overwrite the per-column averages.
//
// Falls back to the pure-pg_stats numbers when sampling can't run (empty
// table, no rows in the sample, query error / permissions).
func (c *Client) ListColumns(ctx context.Context, t Table) ([]Column, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	cols, err := listColumnsFromStats(ctx, pool, t.OID)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 || t.EstRows <= 0 || t.HeapBytes <= 0 {
		return cols, nil
	}
	// Best-effort sampled refinement. On any failure we keep the pg_stats
	// numbers we already have.
	if err := refineColumnsBySampling(ctx, pool, t, cols); err == nil {
		sort.SliceStable(cols, func(i, j int) bool {
			if cols[i].EstBytes != cols[j].EstBytes {
				return cols[i].EstBytes > cols[j].EstBytes
			}
			return cols[i].Name < cols[j].Name
		})
	}
	return cols, nil
}

func listColumnsFromStats(ctx context.Context, pool *pgxpool.Pool, oid uint32) ([]Column, error) {
	rows, err := pool.Query(ctx, sqlColumns, oid)
	if err != nil {
		return nil, fmt.Errorf("list columns for oid %d: %w", oid, err)
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		if err := rows.Scan(&col.Name, &col.Type, &col.AvgWidth, &col.NullFrac, &col.EstBytes, &col.Toastable); err != nil {
			return nil, fmt.Errorf("list columns for oid %d: %w", oid, err)
		}
		out = append(out, col)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list columns for oid %d: %w", oid, err)
	}
	return out, nil
}

// refineColumnsBySampling runs one TABLESAMPLE'd query that computes
// AVG(pg_column_size(col)) and the null fraction for every column, then
// overwrites the matching entries in cols.
func refineColumnsBySampling(ctx context.Context, pool *pgxpool.Pool, t Table, cols []Column) error {
	// Aim for ~50k rows in the sample. SYSTEM picks pages, so for very large
	// tables this still reads a bounded slice.
	const targetRows = 50000
	pct := 100.0 * float64(targetRows) / float64(t.EstRows)
	switch {
	case pct > 100:
		pct = 100
	case pct < 0.01:
		pct = 0.01
	}

	var sb strings.Builder
	sb.WriteString("WITH s AS (SELECT * FROM ")
	sb.WriteString(quoteIdent(t.Schema))
	sb.WriteByte('.')
	sb.WriteString(quoteIdent(t.Name))
	fmt.Fprintf(&sb, " TABLESAMPLE SYSTEM (%.4f) LIMIT %d) SELECT count(*)::bigint", pct, targetRows)
	for i, col := range cols {
		fmt.Fprintf(&sb, ", AVG(pg_column_size(%s))::float8 AS w%d", quoteIdent(col.Name), i)
		fmt.Fprintf(&sb, ", (count(*) FILTER (WHERE %s IS NULL))::bigint AS nu%d", quoteIdent(col.Name), i)
	}
	sb.WriteString(" FROM s")

	row := pool.QueryRow(ctx, sb.String())
	dest := make([]any, 1+2*len(cols))
	var n int64
	dest[0] = &n
	widths := make([]sql.NullFloat64, len(cols)) // NULL when column is 100% null in sample
	nulls := make([]int64, len(cols))
	for i := range cols {
		dest[1+2*i] = &widths[i]
		dest[2+2*i] = &nulls[i]
	}
	if err := row.Scan(dest...); err != nil {
		return fmt.Errorf("sample %q.%q: %w", t.Schema, t.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("sample %q.%q: empty result", t.Schema, t.Name)
	}
	for i := range cols {
		nullFrac := float64(nulls[i]) / float64(n)
		w := 0.0
		if widths[i].Valid {
			w = widths[i].Float64
		}
		cols[i].AvgWidth = int(w + 0.5)
		cols[i].NullFrac = nullFrac
		cols[i].EstBytes = int64(w * (1 - nullFrac) * float64(t.EstRows))
	}
	return nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
