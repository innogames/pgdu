package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExplainableQuery reports whether a normalized statement can be EXPLAINed and
// PREPAREd. Utility statements (EXPLAIN, SET, VACUUM, CREATE, …) cannot — they
// have no plan and PREPARE rejects them — so the detail view skips the EXPLAIN
// / sample-call machinery for them instead of surfacing a raw syntax error.
// ExplainGeneric returns the EXPLAIN (GENERIC_PLAN) of a normalized query —
// one that still carries its $1, $2 … placeholders, as stored by
// pg_stat_statements. GENERIC_PLAN (PostgreSQL 16+) plans the statement
// without supplying parameter values and without executing it.
//
// We send it through the raw pgconn simple-query path rather than pool.Query:
// pgx's normal extended protocol treats the $n as parameters to bind
// ("expected N arguments, got 0"), and even its simple-protocol mode does
// client-side $n interpolation ("insufficient arguments"). pgconn.Exec sends
// the text verbatim, leaving the placeholders for GENERIC_PLAN. The statement
// is wrapped in a READ ONLY transaction as defence in depth; EXPLAIN without
// ANALYZE never executes the query anyway, so even DML is safe.
// explainRun acquires a connection, sends sql via the raw pgconn simple-query
// path (required because EXPLAIN output arrives as text protocol rows, not the
// extended protocol the pgx pool uses), flattens the results into one line per
// plan row, and returns the joined plan text. op is used in error messages.
// All three Explain* methods share this body — they differ only in the SQL they
// compose before calling it.
func (c *Client) explainRun(ctx context.Context, db, op, sql string) (string, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("%s in %q: %w", op, db, err)
	}
	defer conn.Release()

	results, err := conn.Conn().PgConn().Exec(ctx, sql).ReadAll()
	if err != nil {
		return "", fmt.Errorf("%s in %q: %w", op, db, err)
	}
	return flattenExplainRows(results), nil
}

// flattenExplainRows collects the first column of every row from every result
// set returned by the EXPLAIN simple-query batch (BEGIN / EXPLAIN / ROLLBACK)
// into a single newline-joined plan text. Used by all three Explain* methods.
func flattenExplainRows(results []*pgconn.Result) string {
	var lines []string
	for _, r := range results {
		for _, row := range r.Rows {
			if len(row) > 0 {
				lines = append(lines, string(row[0]))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// ExplainGeneric runs EXPLAIN (GENERIC_PLAN, FORMAT TEXT) on a normalized
// pg_stat_statements query text. It sends the query through
// rewriteNormalizedParams first so that EXTRACT($n FROM …) and INTERVAL $n
// pseudo-parameters are rewritten to bindable forms — otherwise GENERIC_PLAN
// fails to parse the verbatim normalized text (see rewriteNormalizedParams).
// The remaining $n stay as placeholders for GENERIC_PLAN to plan without
// real values. Sent via the raw pgconn simple-query path inside a READ ONLY
// transaction (see ExplainLiteral for why pool.Query can't be used here).
func (c *Client) ExplainGeneric(ctx context.Context, db, query string) (string, error) {
	// Rewrite EXTRACT($n FROM …) and INTERVAL $n pseudo-parameters to bindable
	// forms; otherwise GENERIC_PLAN fails to parse the verbatim normalized text
	// (see rewriteNormalizedParams). The remaining $n stay placeholders for
	// GENERIC_PLAN to plan without values.
	sql := "BEGIN READ ONLY;\nEXPLAIN (GENERIC_PLAN, FORMAT TEXT) " + rewriteNormalizedParams(query) + ";\nROLLBACK;"
	return c.explainRun(ctx, db, "explain", sql)
}

// ExplainAnalyze runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) on a fully-literal
// query — i.e. a sample call where the $n placeholders have already been
// substituted (see BuildSampleCall) — and returns the plan text with real
// timing and buffer counters. Unlike ExplainGeneric this *executes* the query,
// so callers must restrict it to read-only statements (ReadOnlyQuery). It runs
// inside a transaction that always rolls back, so even a query with read-side
// side effects leaves nothing behind.
func (c *Client) ExplainAnalyze(ctx context.Context, db, query string) (string, error) {
	sql := "BEGIN;\nEXPLAIN (ANALYZE, VERBOSE, BUFFERS, FORMAT TEXT) " + query + ";\nROLLBACK;"
	return c.explainRun(ctx, db, "explain analyze", sql)
}

// ExplainLiteral runs a plain EXPLAIN (VERBOSE, FORMAT TEXT) on a fully-literal
// query — e.g. a real example query from pg_qualstats, whose constants are real
// captured values, not the $n placeholders pg_stat_statements stores. Unlike
// ExplainGeneric it does NOT pass GENERIC_PLAN, so the planner sees the real
// values and picks the plan a real call would get; unlike ExplainAnalyze it does
// NOT execute (no ANALYZE), so it's safe even for DML example queries. Sent via
// the raw pgconn simple-query path inside a READ ONLY transaction, same as
// ExplainGeneric (see that method for why pool.Query can't be used here).
func (c *Client) ExplainLiteral(ctx context.Context, db, query string) (string, error) {
	sql := "BEGIN READ ONLY;\nEXPLAIN (VERBOSE, FORMAT TEXT) " + query + ";\nROLLBACK;"
	return c.explainRun(ctx, db, "explain", sql)
}

// RunReadOnlyQuery executes a fully-literal query (a sample call where the $n
// placeholders have already been substituted) and returns its rows in the
// generic column/row form the TUI renderer uses. Like ExplainAnalyze it
// *executes* the query, so callers must restrict it to read-only statements
// (ReadOnlyQuery). It runs inside a READ ONLY transaction that always rolls
// back — defence-in-depth on top of that gate. At most maxRows rows are
// returned; the bool reports whether more were waiting (the result was
// truncated).
func (c *Client) RunReadOnlyQuery(ctx context.Context, db, query string, maxRows int) (*DiagResult, bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, false, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, false, fmt.Errorf("execute query in %q: %w", db, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, false, fmt.Errorf("execute query in %q: %w", db, err)
	}
	defer rows.Close()

	cols, resultRows, truncated, err := scanDiagRows(rows, maxRows)
	if err != nil {
		return nil, false, fmt.Errorf("execute query in %q: %w", db, err)
	}
	return &DiagResult{Columns: cols, Rows: resultRows, BarCol: -1, SortCol: -1}, truncated, nil
}
