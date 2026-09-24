package pg

import (
	"cmp"
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
			err := row.Scan(&s.Relation, &s.Column, &s.Operator, &s.ConstValue, &s.Position, &s.Occurrences, &s.QualID, &s.UniqueQualID)
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

// MapQualConstants picks, for each $n placeholder it can tie to a column, a
// pg_qualstats constant captured for that column — the most-frequent one, since
// samples arrive occurrences-DESC (sqlQualstatsSamples). A sample must also
// agree with the slot's table and operator when the text reveals them
// (paramSlots), so `sub.attribute_id` in two subqueries over different tables,
// or `hostname ~ $2` next to `hostname = $7`, each get their own value. Within
// one AND expression the values come from a single captured combination (the
// most frequent uniquequalid), so the call reuses constants that really ran
// together; a slot that combination doesn't cover falls back to any capture.
// Best-effort: ordinals with no resolvable column or no matching captured qual
// are simply absent.
//
// ConstValue is a cast-carrying literal (e.g. `'{…}'::text[]`, `true::boolean`)
// that splices straight into the sample call when its shape matches the slot:
// an array constant lands inside a `col = ANY($n)` form unchanged. The planner
// folds `col IN ($1,…,$n)` into a single `col = ANY('{…}')` qual, though, so
// pg_qualstats then holds one array for a run of scalar placeholders; its
// elements are dealt out across that slot's ordinals in order, and any
// ordinal beyond the last element stays absent, leaving the call incomplete.
// Constants cut at PGQS_CONSTANT_SIZE (qualConstTruncated) are never spliced as
// they are: a truncated scalar is skipped, a truncated array contributes only
// its complete elements. Pure (no DB).
func MapQualConstants(query string, params []ParamType, samples []QualSample) map[int]string {
	slots := paramSlots(query)
	if len(slots) == 0 || len(samples) == 0 {
		return nil
	}
	combo := map[int64]int64{} // qualid → its most frequent uniquequalid
	for _, s := range samples {
		if _, seen := combo[s.QualID]; !seen && s.QualID != 0 && s.ConstValue != "" {
			combo[s.QualID] = s.UniqueQualID
		}
	}
	// First usable value wins (samples are occurrences-DESC). A truncated scalar
	// is useless, so a rarer but complete value beats it; a truncated array still
	// has real elements and is kept when nothing better comes.
	pick := func(sl paramSlot, coherent bool) string {
		var fallback string
		for _, s := range samples {
			if !sl.accepts(s) || coherent && s.QualID != 0 && s.UniqueQualID != combo[s.QualID] {
				continue
			}
			if !s.Truncated {
				return s.ConstValue
			}
			if _, _, _, isArr := qualArrayConst(s.ConstValue); isArr && fallback == "" {
				fallback = s.ConstValue
			}
		}
		return fallback
	}
	// Ordinals per slot in $n order, so array elements are dealt out the way
	// the IN list spelled them.
	typ := make(map[int]string, len(params))
	lits := map[paramSlot]string{}
	ordsBySlot := map[paramSlot][]int{}
	for _, p := range params {
		sl, ok := slots[p.Ordinal]
		if !ok {
			continue
		}
		sl = sl.key()
		lit, seen := lits[sl]
		if !seen {
			lit = cmp.Or(pick(sl, true), pick(sl, false))
			lits[sl] = lit
		}
		if lit != "" {
			ordsBySlot[sl] = append(ordsBySlot[sl], p.Ordinal)
			typ[p.Ordinal] = p.Type
		}
	}
	out := map[int]string{}
	for sl, ords := range ordsBySlot {
		slices.Sort(ords)
		lit := lits[sl]
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

// key folds the slot's names so slots that differ only in case share a value.
func (sl paramSlot) key() paramSlot {
	return paramSlot{Column: strings.ToLower(sl.Column), Relation: strings.ToLower(sl.Relation), Op: sl.Op}
}

// accepts reports whether a captured qual can be this slot's value: same
// column, and same table and operator wherever both sides know them.
func (sl paramSlot) accepts(s QualSample) bool {
	if s.Column == "" || s.ConstValue == "" || !strings.EqualFold(s.Column, sl.Column) {
		return false
	}
	if sl.Relation != "" && s.Relation != "" && !strings.EqualFold(s.Relation, sl.Relation) {
		return false
	}
	return sl.Op == "" || s.Operator == "" || s.Operator == sl.Op
}

// isArrayType reports whether a regtype name from InferParams denotes an array.
func isArrayType(regtype string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(regtype)), "[]")
}

// ResolveSampleParams pairs each $n with the pg_qualstats constant mapped to it
// (qual, from MapQualConstants), fills an EXISTS select-list placeholder with
// the inert 1 (inertParams), or marks it ParamMissing with an empty Value. It
// returns the ordinal → literal map for BuildSampleCall (non-nil, possibly empty)
// and the full per-parameter breakdown for the verbose table, which is shown
// even when the call is incomplete so the user sees which $n are missing. Pure
// (no DB access).
func ResolveSampleParams(query string, params []ParamType, qual map[int]string) (map[int]string, []SampleParam) {
	cols := paramColumns(query)
	inert := inertParams(query)
	real := map[int]string{}
	breakdown := make([]SampleParam, 0, len(params))
	for _, p := range params {
		sp := SampleParam{Ordinal: p.Ordinal, Type: p.Type, Column: cols[p.Ordinal]}
		switch v := qual[p.Ordinal]; {
		case v != "":
			sp.Source, sp.Value = ParamQualstats, v
		case inert[p.Ordinal]:
			sp.Source, sp.Value = ParamInert, "1"
		}
		if sp.Value != "" {
			real[p.Ordinal] = sp.Value
		}
		breakdown = append(breakdown, sp)
	}
	return real, breakdown
}
