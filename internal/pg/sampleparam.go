package pg

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// paramColumns maps each $n placeholder in a normalized statement to the bare
// column name it is directly compared against (col = $1, col IN ($1,…),
// col > $1, col = ANY($1), …). Best-effort and structural: it only recognises
// the "<column> <connector…> $n" shape, so placeholders used as function
// arguments or in projections are simply absent from the result. The column is
// returned as its last dotted, unquoted component so it can be matched against a
// catalog column list. Returns nil when nothing tied to a column.
func paramColumns(query string) map[int]string {
	toks := sqlWords(query)
	out := map[int]string{}
	for i, t := range toks {
		if !strings.HasPrefix(t, "$") {
			continue
		}
		ord, err := strconv.Atoi(t[1:])
		if err != nil {
			continue // "$" that isn't a $n placeholder (shouldn't occur in normalized text)
		}
		if col := columnBefore(toks, i); col != "" {
			out[ord] = col
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sampleConnector is the set of keywords that can legitimately sit between a
// column and the placeholder it is compared to (operators and parentheses are
// already dropped by sqlWords). columnBefore steps over these to find the column.
var sampleConnector = map[string]bool{
	"in": true, "any": true, "all": true, "not": true, "like": true,
	"ilike": true, "similar": true, "to": true, "between": true,
	"and": true, "symmetric": true, "escape": true,
}

// columnBefore walks backwards from the $n token at index i to the column
// reference it is compared against, stepping over the connector keywords,
// parentheses and earlier placeholders that can sit in between (col IN ($1,$2),
// col = ANY($1), col BETWEEN $1 AND $2). It returns the first plain identifier
// it reaches, or "" if there is none. Whatever it returns is later checked
// against the real catalog column list, so a wrong guess (e.g. landing on
// VALUES) harmlessly resolves to "no real value".
func columnBefore(toks []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		t := toks[j]
		if t == "(" || strings.HasPrefix(t, "$") || sampleConnector[strings.ToLower(t)] {
			continue
		}
		return bareColumn(t)
	}
	return ""
}

// bareColumn strips schema/table qualification and quoting from a column
// reference token: "t.country" → "country", `"User"."Id"` → "Id".
func bareColumn(tok string) string {
	if i := strings.LastIndexByte(tok, '.'); i >= 0 {
		tok = tok[i+1:]
	}
	return strings.Trim(tok, `"`)
}

// SampleParamValues fetches a real, non-NULL value from the live table for each
// $n placeholder whose column it can identify in the statement's main table,
// returning ordinal → ready-to-substitute SQL literal (e.g. 'germany'::text).
// This gives a synthesized sample call (and its EXPLAIN) values that actually
// exist in the data, so the plan is far more representative than a generic
// 'sample'/1 constant — without needing pg_qualstats installed.
//
// Strictly best-effort: every failure path (unparseable query, unresolvable
// table, missing column, permission error, empty/all-NULL column) just omits
// that ordinal so the caller falls back to a synthesized literal. It never
// returns an error and only reads one row per column via scalar subqueries.
func (c *Client) SampleParamValues(ctx context.Context, db, query string, params []ParamType) map[int]string {
	cols := paramColumns(query)
	table := MainTable(query)
	if len(cols) == 0 || table == "" {
		return nil
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil
	}

	// Resolve the relation and its real column names from the catalog so the
	// value-fetch query is built only from trusted identifiers — never from the
	// parsed statement text — and so we skip guessed columns that don't exist.
	var schema, name string
	var attnames []string
	if err := pool.QueryRow(ctx, sqlSampleTableColumns, table).Scan(&schema, &name, &attnames); err != nil {
		return nil
	}
	attreal := make(map[string]string, len(attnames)) // lower(attname) → real attname
	for _, a := range attnames {
		attreal[strings.ToLower(a)] = a
	}

	// Build one round-trip of independent scalar subqueries, each grabbing the
	// first non-NULL value of its column already wrapped by quote_literal so it
	// comes back as a paste-ready SQL literal.
	type slot struct {
		ord int
		typ string
	}
	var slots []slot
	from := qualifiedIdent(schema, name)
	var sb strings.Builder
	sb.WriteString("SELECT ")
	for _, p := range params {
		guess, ok := cols[p.Ordinal]
		if !ok {
			continue
		}
		real, ok := attreal[strings.ToLower(guess)]
		if !ok {
			continue
		}
		if len(slots) > 0 {
			sb.WriteString(", ")
		}
		qc := quoteIdent(real)
		fmt.Fprintf(&sb, "(SELECT quote_literal(%s::text) FROM %s WHERE %s IS NOT NULL LIMIT 1)", qc, from, qc)
		slots = append(slots, slot{ord: p.Ordinal, typ: p.Type})
	}
	if len(slots) == 0 {
		return nil
	}

	dest := make([]any, len(slots))
	vals := make([]sql.NullString, len(slots))
	for i := range vals {
		dest[i] = &vals[i]
	}
	if err := pool.QueryRow(ctx, sb.String()).Scan(dest...); err != nil {
		return nil
	}
	out := map[int]string{}
	for i, s := range slots {
		if vals[i].Valid {
			out[s.ord] = sampleLiteralFromValue(vals[i].String, s.typ)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sampleLiteralFromValue turns a quote_literal()'d value fetched from a table
// into a typed literal matching the placeholder's inferred type, so it drops
// into the sample call exactly where BuildSampleCall would place a synthesized
// one. Array placeholders (col = ANY($1)) wrap the scalar in a one-element array.
func sampleLiteralFromValue(quoted, regtype string) string {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(regtype)), "[]") {
		return "ARRAY[" + quoted + "]::" + regtype
	}
	return quoted + "::" + regtype
}
