package pg

import (
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
// parentheses, commas and earlier placeholders that can sit in between
// (col IN ($1,$2), col = ANY($1), col BETWEEN $1 AND $2). It returns the first plain identifier
// it reaches, or "" if there is none. Whatever it returns is later checked
// against captured predicate columns, so a wrong guess (e.g. landing on
// VALUES) harmlessly resolves to "no captured value". The INTERVAL and EXTRACT
// keywords precede a $n in `INTERVAL $n` / `EXTRACT($n FROM …)` — typed-literal
// slots, not predicates — and are not reported as columns.
func columnBefore(toks []string, i int) string {
	for j := i - 1; j >= 0; j-- {
		t := toks[j]
		if t == "(" || t == ")" || t == "," || strings.HasPrefix(t, "$") || sampleConnector[strings.ToLower(t)] {
			continue
		}
		if lt := strings.ToLower(t); lt == "interval" || lt == "extract" {
			return ""
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
