package pg

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnsureStatements makes sure pg_stat_statements is installed in db. Mirrors
// EnsureWALInspect: returns *MissingExtensionError when missing so the TUI can
// offer an interactive install. Note that even after CREATE EXTENSION the view
// only collects data when the library is in shared_preload_libraries (which
// needs a server restart); when it isn't, the snapshot query surfaces that as
// an ordinary error.
func (c *Client) EnsureStatements(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_stat_statements", c.statStatementsReady)
}

// EnsureQualstats makes sure pg_qualstats is installed in db. Like
// EnsureStatements it returns *MissingExtensionError when missing; callers in
// the Top-queries view treat that as "no real parameters available" and fall
// back to synthesized literals rather than surfacing it as a failure. Note
// pg_qualstats only records data when its library is in
// shared_preload_libraries (a server restart), which CREATE EXTENSION alone
// can't arrange — so pgdu detects and uses it but does not offer to install it.
func (c *Client) EnsureQualstats(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_qualstats", c.qualstatsReady)
}

// QualstatsPreloaded reports whether pg_qualstats is listed in
// shared_preload_libraries. That's the precondition for it to actually collect
// quals once it's CREATE EXTENSION'd: without the preload, creating the
// extension makes its views exist but they stay empty until a server restart
// loads the library. pgdu therefore only offers a one-key install when this is
// true (a plain CREATE EXTENSION is then enough); otherwise it stays in
// detect-only mode and falls back to synthesized literals.
func (c *Client) QualstatsPreloaded(ctx context.Context, db string) (bool, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return false, err
	}
	var spl string
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE(current_setting('shared_preload_libraries', true), '')",
	).Scan(&spl); err != nil {
		return false, fmt.Errorf("read shared_preload_libraries in %q: %w", db, err)
	}
	for lib := range strings.SplitSeq(spl, ",") {
		if strings.TrimSpace(lib) == "pg_qualstats" {
			return true, nil
		}
	}
	return false, nil
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
	major, minor, err := c.statementsVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	// A cluster pg_upgraded to PG17 can still carry a 1.6/1.7 extension whose
	// catalog lacks total_exec_time/plans/wal_*; running the query would fail
	// with an opaque "column does not exist". Detect it here and hand the TUI a
	// typed error carrying the upgrade path instead.
	if !statementsAtLeast(major, minor, statementsMinMajor, statementsMinMinor) {
		var def string
		_ = pool.QueryRow(ctx, sqlStatementsDefaultVersion).Scan(&def)
		dMaj, dMin := parseExtVersion(def)
		return nil, &OutdatedExtensionError{
			Extension: "pg_stat_statements",
			DB:        db,
			Installed: fmt.Sprintf("%d.%d", major, minor),
			Available: def,
			Required:  fmt.Sprintf("%d.%d", statementsMinMajor, statementsMinMinor),
			Updatable: def != "" && statementsAtLeast(dMaj, dMin, statementsMinMajor, statementsMinMinor),
		}
	}
	rows, err := pool.Query(ctx, statementsQuery(major, minor))
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

// StatementsInfo returns the last time pg_stat_statements counters were reset
// for db (pg_stat_statements_info, PG14+). Best-effort: a zero time with nil
// error means the view exists but has never recorded a reset, or the read was
// not permitted — callers use it only to warn about an invalidated baseline.
func (c *Client) StatementsInfo(ctx context.Context, db string) (time.Time, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return time.Time{}, err
	}
	var reset *time.Time
	if err := pool.QueryRow(ctx, sqlStatementsInfo).Scan(&reset); err != nil {
		return time.Time{}, fmt.Errorf("read pg_stat_statements_info in %q: %w", db, err)
	}
	if reset == nil {
		return time.Time{}, nil
	}
	return *reset, nil
}

// statementsVersion returns the installed pg_stat_statements extension version as
// (major, minor), cached per database (it only changes on ALTER EXTENSION, so one
// read per session is enough). The version drives which I/O-timing column names
// statementsQuery selects — on a pg_upgraded PG17 cluster the extension can still
// be 1.10, which lacks the 1.11 shared_blk_*_time / local_blk_*_time columns. An
// unparseable version is treated as the newest (current) layout.
func (c *Client) statementsVersion(ctx context.Context, db string) (int, int, error) {
	c.mu.Lock()
	if c.statStatementsVerKnown[db] {
		v := c.statStatementsVer[db]
		c.mu.Unlock()
		return v[0], v[1], nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return 0, 0, err
	}
	var ver string
	if err := pool.QueryRow(ctx, sqlStatementsVersion).Scan(&ver); err != nil {
		return 0, 0, fmt.Errorf("read pg_stat_statements version in %q: %w", db, err)
	}
	major, minor := parseExtVersion(ver)

	c.mu.Lock()
	c.statStatementsVer[db] = [2]int{major, minor}
	c.statStatementsVerKnown[db] = true
	c.mu.Unlock()
	return major, minor, nil
}

// parseExtVersion parses an extension version like "1.11" into (1, 11). A version
// it can't parse (empty / unexpected shape) defaults to a high number so callers
// assume the newest column layout rather than the legacy one.
func parseExtVersion(v string) (int, int) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	major, err1 := strconv.Atoi(parts[0])
	if err1 != nil {
		return 999, 0
	}
	minor := 0
	if len(parts) > 1 {
		if m, err2 := strconv.Atoi(parts[1]); err2 == nil {
			minor = m
		}
	}
	return major, minor
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
	// First keyword, lower-cased. ORM-generated statements often carry a leading
	// /* … */ tag, so strip comments before looking at the keyword.
	fields := strings.Fields(StripSQLComments(query))
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

// QueryKind returns a short tag for the statement's command type — S (SELECT),
// SL (a locking SELECT … FOR UPDATE/SHARE/…), L (a SELECT that acquires an
// advisory lock), I (INSERT), U (UPDATE), D (DELETE), M (MERGE), T
// (BEGIN/COMMIT) — for the top-queries `T` column. SL and L flag SELECTs that
// take locks rather than just reading, which stand out in a hot-query list. The
// leading keyword is taken after stripping any ORM comment tag; anything
// unrecognized returns "?".
func QueryKind(query string) string {
	if v, ok := queryKindMemo.Load(query); ok {
		return v.(string)
	}
	r := parseQueryKind(query)
	queryKindMemo.Store(query, r)
	return r
}

func parseQueryKind(query string) string {
	lower := strings.ToLower(StripSQLComments(query))
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return "?"
	}
	switch fields[0] {
	case "select", "table", "values", "with":
		switch {
		case isAdvisoryLock(lower):
			return "L"
		case hasLockingClause(fields):
			return "SL"
		default:
			return "S"
		}
	case "insert":
		return "I"
	case "update":
		return "U"
	case "delete":
		return "D"
	case "merge":
		return "M"
	case "begin", "commit":
		return "T"
	default:
		return "?"
	}
}

// isAdvisoryLock reports whether a (lower-cased) statement calls a PostgreSQL
// advisory-lock acquisition function: pg_advisory_lock / pg_advisory_xact_lock
// and their pg_try_* variants (each substring also covers the _shared form).
// These are SELECTs whose purpose is to take a lock rather than read data, so
// QueryKind tags them "L". Advisory *unlock* functions are deliberately
// excluded — they release a lock, not acquire one.
func isAdvisoryLock(lower string) bool {
	return strings.Contains(lower, "pg_advisory_lock") ||
		strings.Contains(lower, "pg_advisory_xact_lock") ||
		strings.Contains(lower, "pg_try_advisory_lock") ||
		strings.Contains(lower, "pg_try_advisory_xact_lock")
}

// ReadOnlyQuery reports whether a normalized statement is safe to actually
// execute, which EXPLAIN ANALYZE does (it runs the query). Only read-only
// shapes qualify: a bare SELECT / TABLE / VALUES, or a WITH whose CTEs contain
// no data-modifying statement. Plain DML and utility statements are excluded —
// running them for real would write. This is stricter than ExplainableQuery,
// which also admits DML (EXPLAIN without ANALYZE never executes).
func ReadOnlyQuery(query string) bool {
	fields := strings.Fields(strings.ToLower(StripSQLComments(query)))
	if len(fields) == 0 {
		return false
	}
	// A row-locking clause (SELECT … FOR UPDATE/SHARE/…) takes real locks the
	// moment it executes — and EXPLAIN ANALYZE executes — so it isn't safe to run
	// even inside a rolled-back transaction (it would block concurrent writers
	// for the duration). Reject it regardless of the leading keyword; the plain
	// EXPLAIN (no ANALYZE) path still shows its plan.
	if hasLockingClause(fields) {
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

// hasLockingClause reports whether the (lower-cased, whitespace-split) tokens
// contain a row-level locking clause: FOR UPDATE, FOR SHARE, FOR NO KEY UPDATE,
// or FOR KEY SHARE. It keys off "for" immediately followed by one of those
// lock-strength keywords, which distinguishes it from the other SQL uses of FOR
// (e.g. substring(x FROM a FOR b)), whose next token is an expression.
func hasLockingClause(fields []string) bool {
	for i, f := range fields {
		if strings.Trim(f, "(),") != "for" || i+1 >= len(fields) {
			continue
		}
		switch strings.Trim(fields[i+1], "(),") {
		case "update", "share", "no", "key":
			return true
		}
	}
	return false
}

// QualstatsExampleUsable reports whether a pg_qualstats example query is a
// complete denormalization of the normalized statement rather than a truncated
// fragment. pg_qualstats stores example queries capped at track_activity_query_size
// (1 KB by default), so a long statement comes back cut off mid-token — wrapping
// such a fragment in EXPLAIN yields a syntax error. We detect this structurally:
// the text following the last $n placeholder in the normalized query carries no
// constants, so a complete example must end with that same suffix; a truncated
// one won't. When the query has no trailing constant-free text to anchor on (it
// ends at/with its last parameter), we can't tell and optimistically accept it —
// such queries are short and rarely truncated.
func QualstatsExampleUsable(normalized, example string) bool {
	// A complete denormalization has every constant spliced in. When pg_qualstats
	// can't reconstruct a qual (e.g. WHERE alias.id=$1) it leaves the $n in place;
	// a plain EXPLAIN on that fails with "there is no parameter $n" since, unlike
	// GENERIC_PLAN, it supplies no value. Reject so the caller falls back to the
	// synthesized sample call (which EXPLAINs via GENERIC_PLAN).
	if hasParamPlaceholder(example) {
		return false
	}
	tail := strings.TrimSpace(normalizedTailAfterLastParam(normalized))
	if tail == "" {
		return true
	}
	return strings.HasSuffix(collapseSpaces(example), collapseSpaces(tail))
}

// hasParamPlaceholder reports whether s contains a $n placeholder (a '$'
// immediately followed by one or more digits).
func hasParamPlaceholder(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

// normalizedTailAfterLastParam returns the substring following the highest-
// numbered $n placeholder occurrence in a normalized query (the constant-free
// suffix). Returns "" when the query has no placeholders.
func normalizedTailAfterLastParam(query string) string {
	last := -1
	for i := 0; i < len(query); i++ {
		if query[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			j++
		}
		if j > i+1 { // at least one digit → a real placeholder
			last = j
		}
	}
	if last < 0 {
		return ""
	}
	return query[last:]
}

// collapseSpaces normalizes all whitespace runs to single spaces (and trims),
// so a suffix comparison ignores the indentation/newlines that differ between
// pg_stat_statements text and a reconstructed example.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
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

	// Rewrite EXTRACT($n FROM …) pseudo-parameters to the bindable function-call
	// form; otherwise GENERIC_PLAN fails to parse the verbatim normalized text
	// (see rewriteExtractFieldParams). The remaining $n stay placeholders for
	// GENERIC_PLAN to plan without values.
	sql := "BEGIN READ ONLY;\nEXPLAIN (GENERIC_PLAN, FORMAT TEXT) " + rewriteExtractFieldParams(query) + ";\nROLLBACK;"
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

// ExplainLiteral runs a plain EXPLAIN (VERBOSE, FORMAT TEXT) on a fully-literal
// query — e.g. a real example query from pg_qualstats, whose constants are real
// captured values, not the $n placeholders pg_stat_statements stores. Unlike
// ExplainGeneric it does NOT pass GENERIC_PLAN, so the planner sees the real
// values and picks the plan a real call would get; unlike ExplainAnalyze it does
// NOT execute (no ANALYZE), so it's safe even for DML example queries. Sent via
// the raw pgconn simple-query path inside a READ ONLY transaction, same as
// ExplainGeneric (see that method for why pool.Query can't be used here).
func (c *Client) ExplainLiteral(ctx context.Context, db, query string) (string, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", fmt.Errorf("explain in %q: %w", db, err)
	}
	defer conn.Release()

	sql := "BEGIN READ ONLY;\nEXPLAIN (VERBOSE, FORMAT TEXT) " + query + ";\nROLLBACK;"
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

// QualstatsExampleQuery returns one real example query for queryID — the
// normalized statement with real constants spliced back in, as reconstructed by
// pg_qualstats from the values it sampled. Returns "" (not an error) when
// pg_qualstats has captured nothing for that queryid yet. Requires pg_qualstats;
// callers should EnsureQualstats first and treat its absence as "no real sample".
func (c *Client) QualstatsExampleQuery(ctx context.Context, db string, queryID int64) (string, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", err
	}
	var example *string
	if err := pool.QueryRow(ctx, sqlQualstatsExample, queryID).Scan(&example); err != nil {
		return "", fmt.Errorf("qualstats example in %q: %w", db, err)
	}
	if example == nil {
		return "", nil
	}
	return *example, nil
}

// QualstatsSamples lists the real predicate constants pg_qualstats captured for
// queryID, most-frequent first (see sqlQualstatsSamples). Empty when nothing has
// been sampled. Callers should EnsureQualstats first.
func (c *Client) QualstatsSamples(ctx context.Context, db string, queryID int64) ([]QualSample, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlQualstatsSamples, queryID)
	if err != nil {
		return nil, fmt.Errorf("qualstats samples in %q: %w", db, err)
	}
	defer rows.Close()
	var out []QualSample
	for rows.Next() {
		var s QualSample
		if err := rows.Scan(&s.Relation, &s.Column, &s.Operator, &s.ConstValue, &s.Position, &s.Occurrences); err != nil {
			return nil, fmt.Errorf("qualstats samples in %q: %w", db, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("qualstats samples in %q: %w", db, err)
	}
	return out, nil
}

// extractFieldRe matches an EXTRACT(field FROM source) expression whose field
// slot is a $n placeholder. pg_stat_statements normalizes *every* constant to a
// $n, including the EXTRACT field keyword ("epoch", "year", …) — but in the
// SQL grammar that slot is a field identifier / string literal, not a bind
// parameter, so the verbatim normalized text ("extract($2 FROM log_date)") is
// neither PREPARE-able nor EXPLAIN-able (both fail with "syntax error at or near
// $n"). The pattern is case-insensitive and tolerates whitespace; it captures the
// ordinal so callers can fill that placeholder with a real field literal.
var extractFieldRe = regexp.MustCompile(`(?i)\bextract\s*\(\s*\$(\d+)\s+from\b`)

// rewriteExtractFieldParams turns each EXTRACT($n FROM source) back into a form
// PostgreSQL will parse with the $n still a bindable parameter, by switching the
// SQL-keyword syntax to the equivalent function-call form extract($n, source)
// (the SQL-standard EXTRACT maps to pg_catalog.extract(text, …) since PG14). This
// keeps the placeholder numbering gap-free — $n stays $n — so inferred parameter
// types and sampled values still map back to the original query by ordinal.
func rewriteExtractFieldParams(query string) string {
	return extractFieldRe.ReplaceAllString(query, "extract($$${1},")
}

// ExtractFieldOrdinals returns the $n ordinals that sit in the field slot of an
// EXTRACT(field FROM source) expression in a normalized query. Those placeholders
// are not real bind parameters (see extractFieldRe), so BuildSampleCall must fill
// them with a valid bare field literal rather than a synthesized typed value.
func ExtractFieldOrdinals(query string) []int {
	matches := extractFieldRe.FindAllStringSubmatch(query, -1)
	var out []int
	for _, m := range matches {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
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
	// EXTRACT($n FROM …) pseudo-parameters would make PREPARE fail with a syntax
	// error; rewrite them to the bindable function-call form first (ordinals are
	// preserved, so the returned ParamType ordinals still match the original $n).
	if _, err := conn.Exec(ctx, "PREPARE "+name+" AS "+rewriteExtractFieldParams(query)); err != nil {
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

// BuildSampleCall substitutes each $n in a normalized query with a literal,
// producing a copy-pasteable example. real holds per-ordinal literals fetched
// from the live table (see SampleParamValues); ordinals missing from it fall
// back to a synthesized literal of the inferred type. Pure (no DB access) so it
// is unit-testable. Replacement runs from the highest ordinal down so "$1"
// doesn't clobber the prefix of "$10".
func BuildSampleCall(query string, params []ParamType, real map[int]string) string {
	out := query
	for i := len(params) - 1; i >= 0; i-- {
		p := params[i]
		lit, ok := real[p.Ordinal]
		if !ok {
			lit = sampleLiteral(p.Type)
		}
		placeholder := "$" + strconv.Itoa(p.Ordinal)
		out = strings.ReplaceAll(out, placeholder, lit)
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
