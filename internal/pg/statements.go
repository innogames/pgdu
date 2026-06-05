package pg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// EnsureStatements makes sure pg_stat_statements is installed in db. Mirrors
// EnsureWALInspect: returns *MissingExtensionError when missing so the TUI can
// offer an interactive install. Note that even after CREATE EXTENSION the view
// only collects data when the library is in shared_preload_libraries (which
// needs a server restart); when it isn't, the snapshot query surfaces that as
// an ordinary error.
func (c *Client) EnsureStatements(ctx context.Context, db string) error {
	c.mu.Lock()
	if c.statStatementsReady[db] {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	st, err := c.ProbeExtension(ctx, db, "pg_stat_statements")
	if err != nil {
		return err
	}
	if !st.Installed {
		return &MissingExtensionError{Extension: "pg_stat_statements", DB: db, Installable: st.Available}
	}
	c.mu.Lock()
	c.statStatementsReady[db] = true
	c.mu.Unlock()
	return nil
}

// StatementSnapshot reads the current (cumulative-since-reset) pg_stat_statements
// counters for db. Callers diff two snapshots to build a window (DiffStatements).
func (c *Client) StatementSnapshot(ctx context.Context, db string) ([]QueryStat, error) {
	if err := c.EnsureStatements(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlStatements)
	if err != nil {
		return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
	}
	defer rows.Close()
	var out []QueryStat
	for rows.Next() {
		var q QueryStat
		if err := rows.Scan(
			&q.QueryID, &q.UserID, &q.DBID, &q.Query,
			&q.Calls, &q.Rows,
			&q.TotalExecTime, &q.MinExecTime, &q.MaxExecTime, &q.MeanExecTime, &q.StddevExecTime,
			&q.Plans, &q.TotalPlanTime,
			&q.SharedBlksHit, &q.SharedBlksRead, &q.SharedBlksDirtied, &q.SharedBlksWritten,
			&q.LocalBlksHit, &q.LocalBlksRead, &q.LocalBlksDirtied, &q.LocalBlksWritten,
			&q.TempBlksRead, &q.TempBlksWritten,
			&q.SharedBlkReadTime, &q.SharedBlkWriteTime,
			&q.LocalBlkReadTime, &q.LocalBlkWriteTime,
			&q.TempBlkReadTime, &q.TempBlkWriteTime,
			&q.WALRecords, &q.WALFPI, &q.WALBytes,
		); err != nil {
			return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pg_stat_statements in %q: %w", db, err)
	}
	return out, nil
}

// TrackPlanning reports whether pg_stat_statements.track_planning is on. It is
// off by default, in which case total_plan_time is always 0 — the Top-queries
// view shows the plan_ms column as "not collected" rather than a misleading 0.
func (c *Client) TrackPlanning(ctx context.Context, db string) (bool, error) {
	c.mu.Lock()
	if c.trackPlanningKnown[db] {
		v := c.trackPlanning[db]
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, err
	}
	var v string
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(current_setting('pg_stat_statements.track_planning', true), 'off')",
	).Scan(&v); err != nil {
		return false, fmt.Errorf("read track_planning in %q: %w", db, err)
	}
	c.mu.Lock()
	c.trackPlanning[db] = v == "on"
	c.trackPlanningKnown[db] = true
	c.mu.Unlock()
	return v == "on", nil
}

// ExplainableQuery reports whether a normalized statement can be EXPLAINed and
// PREPAREd. Utility statements (EXPLAIN, SET, VACUUM, CREATE, …) cannot — they
// have no plan and PREPARE rejects them — so the detail view skips the EXPLAIN
// / sample-call machinery for them instead of surfacing a raw syntax error.
func ExplainableQuery(query string) bool {
	// First keyword, lower-cased. Leading SQL comments are uncommon in
	// normalized text, so a simple Fields split is enough.
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "select", "insert", "update", "delete", "merge", "with", "values", "table":
		return true
	default:
		return false
	}
}

// ReadOnlyQuery reports whether a normalized statement is safe to actually
// execute, which EXPLAIN ANALYZE does (it runs the query). Only read-only
// shapes qualify: a bare SELECT / TABLE / VALUES, or a WITH whose CTEs contain
// no data-modifying statement. Plain DML and utility statements are excluded —
// running them for real would write. This is stricter than ExplainableQuery,
// which also admits DML (EXPLAIN without ANALYZE never executes).
func ReadOnlyQuery(query string) bool {
	fields := strings.Fields(strings.ToLower(query))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "select", "table", "values":
		return true
	case "with":
		// A data-modifying CTE (WITH … INSERT/UPDATE/DELETE/MERGE) writes when
		// executed, so reject any WITH that names one.
		for _, f := range fields {
			switch strings.Trim(f, "(),") {
			case "insert", "update", "delete", "merge":
				return false
			}
		}
		return true
	default:
		return false
	}
}

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
func (c *Client) ExplainGeneric(ctx context.Context, db, query string) (string, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("explain in %q: %w", db, err)
	}
	defer conn.Release()

	sql := "BEGIN READ ONLY;\nEXPLAIN (GENERIC_PLAN, FORMAT TEXT) " + query + ";\nROLLBACK;"
	results, err := conn.Conn().PgConn().Exec(ctx, sql).ReadAll()
	if err != nil {
		return "", fmt.Errorf("explain in %q: %w", db, err)
	}
	var lines []string
	for _, r := range results {
		for _, row := range r.Rows {
			if len(row) > 0 {
				lines = append(lines, string(row[0]))
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// ExplainAnalyze runs EXPLAIN (ANALYZE, VERBOSE, BUFFERS) on a fully-literal
// query — i.e. a sample call where the $n placeholders have already been
// substituted (see BuildSampleCall) — and returns the plan text with real
// timing and buffer counters. Unlike ExplainGeneric this *executes* the query,
// so callers must restrict it to read-only statements (ReadOnlyQuery). It runs
// inside a transaction that always rolls back, so even a query with read-side
// side effects leaves nothing behind.
func (c *Client) ExplainAnalyze(ctx context.Context, db, query string) (string, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("explain analyze in %q: %w", db, err)
	}
	defer conn.Release()

	sql := "BEGIN;\nEXPLAIN (ANALYZE, VERBOSE, BUFFERS, FORMAT TEXT) " + query + ";\nROLLBACK;"
	results, err := conn.Conn().PgConn().Exec(ctx, sql).ReadAll()
	if err != nil {
		return "", fmt.Errorf("explain analyze in %q: %w", db, err)
	}
	var lines []string
	for _, r := range results {
		for _, row := range r.Rows {
			if len(row) > 0 {
				lines = append(lines, string(row[0]))
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// InferParams discovers the types of a normalized query's $n placeholders by
// PREPAREing it and reading pg_prepared_statements.parameter_types. Best-effort:
// utility statements and queries whose text was truncated by
// track_activity_query_size will fail to PREPARE and return an error the caller
// renders as a hint.
func (c *Client) InferParams(ctx context.Context, db, query string) ([]ParamType, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// A fixed name is fine: one connection, deallocated before release. Guard
	// against a leftover from a prior aborted call on the same pooled conn.
	const name = "pgdu_infer_params"
	_, _ = conn.Exec(ctx, "DEALLOCATE "+name)
	if _, err := conn.Exec(ctx, "PREPARE "+name+" AS "+query); err != nil {
		return nil, fmt.Errorf("infer parameters: %w", err)
	}
	defer func() { _, _ = conn.Exec(ctx, "DEALLOCATE "+name) }()

	var typeNames []string
	if err := conn.QueryRow(ctx,
		"SELECT parameter_types::text[] FROM pg_prepared_statements WHERE name = $1", name,
	).Scan(&typeNames); err != nil {
		return nil, fmt.Errorf("infer parameters: %w", err)
	}
	out := make([]ParamType, len(typeNames))
	for i, t := range typeNames {
		out[i] = ParamType{Ordinal: i + 1, Type: t}
	}
	return out, nil
}

// BuildSampleCall substitutes each $n in a normalized query with a synthesized
// literal of the inferred type, producing a copy-pasteable example. Pure (no
// DB access) so it is unit-testable. Replacement runs from the highest ordinal
// down so "$1" doesn't clobber the prefix of "$10".
func BuildSampleCall(query string, params []ParamType) string {
	out := query
	for i := len(params) - 1; i >= 0; i-- {
		p := params[i]
		placeholder := "$" + strconv.Itoa(p.Ordinal)
		out = strings.ReplaceAll(out, placeholder, sampleLiteral(p.Type))
	}
	return out
}

// sampleLiteral returns a plausible, type-cast literal for a regtype name. The
// cast (value::type) keeps the filled-in query type-correct so it can be
// EXPLAINed or run as-is. Unknown types fall back to a typed NULL.
func sampleLiteral(regtype string) string {
	t := strings.ToLower(strings.TrimSpace(regtype))
	// Array types: an empty array literal of the element type reads cleanly.
	if strings.HasSuffix(t, "[]") {
		return "'{}'::" + regtype
	}
	switch t {
	case "smallint", "int2", "integer", "int", "int4", "bigint", "int8", "oid":
		return "1::" + regtype
	case "numeric", "decimal", "real", "float4", "double precision", "float8", "money":
		return "1.0::" + regtype
	case "boolean", "bool":
		return "true"
	case "text", "character varying", "varchar", "character", "char", "bpchar", "name", "citext":
		return "'sample'::" + regtype
	case "uuid":
		return "'00000000-0000-0000-0000-000000000000'::uuid"
	case "date":
		return "CURRENT_DATE"
	case "timestamp", "timestamp without time zone":
		return "CURRENT_TIMESTAMP::timestamp"
	case "timestamptz", "timestamp with time zone":
		return "CURRENT_TIMESTAMP"
	case "time", "time without time zone", "timetz", "time with time zone":
		return "CURRENT_TIME"
	case "interval":
		return "'1 day'::interval"
	case "json", "jsonb":
		return "'{}'::" + regtype
	case "inet", "cidr":
		return "'127.0.0.1'::" + regtype
	case "bytea":
		return "'\\x00'::bytea"
	default:
		return "NULL::" + regtype
	}
}
