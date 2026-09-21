package pg

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
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
	return collect(ctx, pool, fmt.Sprintf("qualstats samples in %q", db), sqlQualstatsSamples, []any{queryID},
		func(row pgx.CollectableRow) (QualSample, error) {
			var s QualSample
			err := row.Scan(&s.Relation, &s.Column, &s.Operator, &s.ConstValue, &s.Position, &s.Occurrences)
			s.Truncated = qualConstTruncated(s.ConstValue)
			return s, err
		})
}

// QualstatsQualTracked reports how many quals pg_qualstats has tracked for
// queryID regardless of whether it captured a constant for any of them, so a
// caller with no samples can tell "pg_qualstats never saw this query" from
// "it saw it, but always with bound parameters" (see sqlQualstatsQualCount).
// Callers should EnsureQualstats first.
func (c *Client) QualstatsQualTracked(ctx context.Context, db string, queryID int64) (int, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return 0, err
	}
	var n int
	if err := pool.QueryRow(ctx, sqlQualstatsQualCount, queryID).Scan(&n); err != nil {
		return 0, fmt.Errorf("qualstats qual count in %q: %w", db, err)
	}
	return n, nil
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
	// against a leftover from a prior aborted call on the same pooled conn —
	// but only if one exists: an unconditional DEALLOCATE of a missing
	// statement is an ERROR that lands in the server log on every call.
	const name = "pgdu_infer_params"
	var stale bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_prepared_statements WHERE name = $1)", name,
	).Scan(&stale); err == nil && stale {
		_, _ = conn.Exec(ctx, "DEALLOCATE "+name)
	}
	// EXTRACT($n FROM …) and INTERVAL $n pseudo-parameters would make PREPARE fail
	// with a syntax error; rewrite them to bindable forms first (ordinals are
	// preserved, so the returned ParamType ordinals still match the original $n).
	if _, err := conn.Exec(ctx, "PREPARE "+name+" AS "+rewriteNormalizedParams(query)); err != nil {
		return nil, fmt.Errorf("infer parameters: %w", err)
	}
	// Deallocate even when ctx was cancelled mid-call, so the pooled conn is
	// handed back clean and the stale-statement path above stays rare.
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "DEALLOCATE "+name) }()

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

// BuildSampleCall substitutes each $n in a normalized query with its captured
// literal from real, highest ordinal first so "$1" doesn't clobber the prefix of
// "$10". It returns "" unless every parameter has a value: a sample call is only
// ever a complete, runnable statement — values are never guessed, so a partial
// fill is not shown at all. A query without parameters is its own sample call.
// Pure (no DB access).
func BuildSampleCall(query string, params []ParamType, real map[int]string) string {
	for _, p := range params {
		if real[p.Ordinal] == "" {
			return ""
		}
	}
	out := query
	for _, p := range slices.Backward(params) {
		out = strings.ReplaceAll(out, "$"+strconv.Itoa(p.Ordinal), real[p.Ordinal])
	}
	return out
}

// MapQualConstants picks, for each $n placeholder it can tie to a column, the
// pg_qualstats constant captured for that column — the most-frequent one, since
// samples arrive occurrences-DESC (sqlQualstatsSamples). Best-effort: ordinals
// with no resolvable column or no matching captured qual are simply absent.
//
// ConstValue is a cast-carrying literal (e.g. `'{…}'::text[]`, `true::boolean`)
// that splices straight into the sample call when its shape matches the slot:
// an array constant lands inside a `col = ANY($n)` form unchanged. The planner
// folds `col IN ($1,…,$n)` into a single `col = ANY('{…}')` qual, though, so
// pg_qualstats then holds one array for a run of scalar placeholders; its
// elements are dealt out across that column's ordinals in order, and any
// ordinal beyond the last element stays absent, leaving the call incomplete.
// Constants cut at PGQS_CONSTANT_SIZE (qualConstTruncated) are never spliced as
// they are: a truncated scalar is skipped, a truncated array contributes only
// its complete elements. Pure (no DB).
func MapQualConstants(query string, params []ParamType, samples []QualSample) map[int]string {
	cols := paramColumns(query)
	if len(cols) == 0 || len(samples) == 0 {
		return nil
	}
	// First usable value wins per column (samples are occurrences-DESC), matched
	// case-insensitively to the parsed column (catalog names fold unless quoted).
	// A truncated scalar is useless, so a rarer but complete value beats it; a
	// truncated array still has real elements and is kept when nothing better comes.
	byCol := make(map[string]string, len(samples))
	for _, s := range samples {
		if s.Column == "" || s.ConstValue == "" {
			continue
		}
		k := strings.ToLower(s.Column)
		prev, seen := byCol[k]
		switch {
		case !seen:
			if _, _, _, isArr := qualArrayConst(s.ConstValue); isArr || !s.Truncated {
				byCol[k] = s.ConstValue
			}
		case s.Truncated:
		case qualConstTruncated(prev):
			byCol[k] = s.ConstValue
		}
	}
	// Ordinals per column in $n order, so array elements are dealt out the way
	// the IN list spelled them.
	typ := make(map[int]string, len(params))
	byColOrds := map[string][]int{}
	for _, p := range params {
		if col, ok := cols[p.Ordinal]; ok {
			k := strings.ToLower(col)
			if _, ok := byCol[k]; ok {
				byColOrds[k] = append(byColOrds[k], p.Ordinal)
				typ[p.Ordinal] = p.Type
			}
		}
	}
	out := map[int]string{}
	for k, ords := range byColOrds {
		slices.Sort(ords)
		lit := byCol[k]
		elems, _, complete, isArr := qualArrayConst(lit)
		for i, ord := range ords {
			switch {
			case !isArr:
				out[ord] = lit
			case isArrayType(typ[ord]):
				// `col = ANY($n)`: the whole array is the value. Re-encode a truncated
				// one from its complete elements so the cast and closing quote are back.
				if complete {
					out[ord] = lit
				} else if len(elems) > 0 {
					out[ord] = arrayLiteral(elems, typ[ord])
				}
			case i < len(elems):
				out[ord] = elemLiteral(elems[i], typ[ord])
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isArrayType reports whether a regtype name from InferParams denotes an array.
func isArrayType(regtype string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(regtype)), "[]")
}

// ResolveSampleParams pairs each $n with the pg_qualstats constant mapped to it
// (qual, from MapQualConstants) or marks it ParamMissing with an empty Value. It
// returns the ordinal → literal map for BuildSampleCall (non-nil, possibly empty)
// and the full per-parameter breakdown for the verbose table, which is shown
// even when the call is incomplete so the user sees which $n are missing. Pure
// (no DB access).
func ResolveSampleParams(query string, params []ParamType, qual map[int]string) (map[int]string, []SampleParam) {
	cols := paramColumns(query)
	real := map[int]string{}
	breakdown := make([]SampleParam, 0, len(params))
	for _, p := range params {
		sp := SampleParam{Ordinal: p.Ordinal, Type: p.Type, Column: cols[p.Ordinal]}
		if v := qual[p.Ordinal]; v != "" {
			sp.Source, sp.Value = ParamQualstats, v
			real[p.Ordinal] = v
		}
		breakdown = append(breakdown, sp)
	}
	return real, breakdown
}
