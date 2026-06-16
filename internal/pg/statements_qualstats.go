package pg

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

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
	// EXTRACT($n FROM …) and INTERVAL $n pseudo-parameters would make PREPARE fail
	// with a syntax error; rewrite them to bindable forms first (ordinals are
	// preserved, so the returned ParamType ordinals still match the original $n).
	if _, err := conn.Exec(ctx, "PREPARE "+name+" AS "+rewriteNormalizedParams(query)); err != nil {
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

// MapQualConstants picks, for each $n placeholder it can tie to a column, the
// pg_qualstats constant captured for that column — the most-frequent one, since
// samples arrive occurrences-DESC (sqlQualstatsSamples). Best-effort: ordinals
// with no resolvable column or no matching captured qual are simply absent.
// ConstValue is already a cast-carrying literal (e.g. `'{…}'::text[]`,
// `true::boolean`), so the result splices straight into the sample call — an
// array constant lands inside a `col = ANY($n)` form unchanged. Pure (no DB).
func MapQualConstants(query string, params []ParamType, samples []QualSample) map[int]string {
	cols := paramColumns(query)
	if len(cols) == 0 || len(samples) == 0 {
		return nil
	}
	// First value wins per column (samples are occurrences-DESC), matched
	// case-insensitively to the parsed column the same way SampleParamValues does.
	byCol := make(map[string]string, len(samples))
	for _, s := range samples {
		if s.Column == "" || s.ConstValue == "" {
			continue
		}
		k := strings.ToLower(s.Column)
		if _, seen := byCol[k]; !seen {
			byCol[k] = s.ConstValue
		}
	}
	out := map[int]string{}
	for _, p := range params {
		if col, ok := cols[p.Ordinal]; ok {
			if lit, ok := byCol[strings.ToLower(col)]; ok {
				out[p.Ordinal] = lit
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResolveSampleParams decides the literal and source for each $n placeholder,
// applying precedence EXTRACT/INTERVAL slot > pg_qualstats > live-table > synthesized.
// It returns the per-ordinal literals for the non-synthesized slots (to hand to
// BuildSampleCall) and the full per-parameter breakdown for the verbose table.
// EXTRACT slots get 'epoch' (a bare field literal every temporal type accepts)
// and INTERVAL slots get a bare '1 day' string (so `INTERVAL $n` stays parseable
// rather than becoming `INTERVAL '…'::interval`); synthesized slots get
// sampleLiteral(type). Pure (no DB access).
func ResolveSampleParams(query string, params []ParamType, qual, live map[int]string, extractOrds, intervalOrds []int) (map[int]string, []SampleParam) {
	cols := paramColumns(query)
	extract := make(map[int]bool, len(extractOrds))
	for _, o := range extractOrds {
		extract[o] = true
	}
	interval := make(map[int]bool, len(intervalOrds))
	for _, o := range intervalOrds {
		interval[o] = true
	}
	real := map[int]string{}
	breakdown := make([]SampleParam, 0, len(params))
	for _, p := range params {
		sp := SampleParam{Ordinal: p.Ordinal, Type: p.Type, Column: cols[p.Ordinal]}
		switch {
		case extract[p.Ordinal]:
			sp.Source, sp.Value = ParamExtractField, "'epoch'"
		case interval[p.Ordinal]:
			// `INTERVAL $n` parses with "INTERVAL" as the preceding token, which
			// paramColumns picks up as a bogus predicate column — it's the typed-
			// literal keyword, not a column. Clear it.
			sp.Source, sp.Value, sp.Column = ParamIntervalLiteral, "'1 day'", ""
		case qual[p.Ordinal] != "":
			sp.Source, sp.Value = ParamQualstats, qual[p.Ordinal]
		case live[p.Ordinal] != "":
			sp.Source, sp.Value = ParamLiveData, live[p.Ordinal]
		default:
			sp.Source, sp.Value = ParamSynthesized, sampleLiteral(p.Type)
		}
		if sp.Source != ParamSynthesized {
			real[p.Ordinal] = sp.Value
		}
		breakdown = append(breakdown, sp)
	}
	return real, breakdown
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
