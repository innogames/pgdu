package pg

import (
	"cmp"
	"strconv"
	"strings"
)

// paramSlot is what a $n placeholder is compared against, as far as the
// normalized text tells: the column, the table that column belongs to and the
// operator. Each field is "" when it can't be read off the text; matching then
// treats it as a wildcard. Relation and Op exist because the column alone is
// ambiguous: the same column name recurs across subqueries (`sub.attribute_id`
// against two different tables) and one column can carry several quals
// (`hostname ~ $2` and `hostname = $7`).
type paramSlot struct {
	Column   string // bare column name
	Relation string // bare relname the column resolves to
	Op       string // pg_operator.oprname the qual carries (IN → "=", LIKE → "~~")
}

// opUnmatchable marks a slot whose qual pg_qualstats records in a form that is
// not the placeholder's value (SIMILAR TO is stored as `~ similar_to_escape(…)`,
// so its constant is the translated regex). No sample operator equals it.
const opUnmatchable = "?"

// paramColumns maps each $n placeholder in a normalized statement to the bare
// column name it is directly compared against (col = $1, col IN ($1,…),
// col > $1, col = ANY($1), …). Best-effort and structural: it only recognises
// the "<column> <connector…> $n" shape, so placeholders used as function
// arguments or in projections are simply absent from the result. The column is
// returned as its last dotted, unquoted component so it can be matched against a
// catalog column list. Returns nil when nothing tied to a column.
func paramColumns(query string) map[int]string {
	slots := paramSlots(query)
	if len(slots) == 0 {
		return nil
	}
	out := make(map[int]string, len(slots))
	for ord, s := range slots {
		out[ord] = s.Column
	}
	return out
}

// paramSlots is paramColumns plus the table (resolved through the FROM-clause
// aliases visible from the placeholder's subquery) and the operator of each
// column-tied $n. Returns nil when nothing tied to a column.
func paramSlots(query string) map[int]paramSlot {
	toks := sqlTokens(query)
	var sc *sqlScopes
	out := map[int]paramSlot{}
	for i, t := range toks {
		ord, ok := placeholderOrdinal(t)
		if !ok {
			continue
		}
		col, qual, op := slotBefore(toks, i)
		if col == "" {
			continue
		}
		if sc == nil {
			sc = newSQLScopes(toks)
		}
		out[ord] = paramSlot{Column: col, Relation: sc.resolve(i, qual), Op: op}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// inertParams reports the placeholders whose value cannot change what the
// statement returns or how it is planned: the lone select-list item of an
// EXISTS subquery (`EXISTS (SELECT $3 FROM …)`, pg_stat_statements' rendering
// of the conventional `SELECT 1`). EXISTS only asks whether a row comes back,
// so any constant stands in for the original without being a guess at data.
func inertParams(query string) map[int]bool {
	toks := sqlTokens(query)
	var out map[int]bool
	for i, t := range toks {
		ord, ok := placeholderOrdinal(t)
		if !ok || i < 3 || i+1 >= len(toks) {
			continue
		}
		if !strings.EqualFold(toks[i-1], "select") || toks[i-2] != "(" || !strings.EqualFold(toks[i-3], "exists") {
			continue
		}
		next := strings.ToLower(toks[i+1])
		if next != "from" && (next != "as" || i+3 >= len(toks) || !strings.EqualFold(toks[i+3], "from")) {
			continue
		}
		if out == nil {
			out = map[int]bool{}
		}
		out[ord] = true
	}
	return out
}

func placeholderOrdinal(tok string) (int, bool) {
	if !strings.HasPrefix(tok, "$") {
		return 0, false
	}
	ord, err := strconv.Atoi(tok[1:])
	return ord, err == nil
}

// sqlTokens is sqlWords that keeps operators: each run of operator characters
// is its own token (so `::` casts and the comparison next to a placeholder stay
// visible). Quotes and brackets still only separate.
func sqlTokens(s string) []string {
	s = StripSQLComments(s)
	var out []string
	start, inOp := -1, false
	flush := func(i int) {
		if start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '$', c == '.', c == '"':
			if inOp {
				flush(i)
			}
			if start < 0 {
				start, inOp = i, false
			}
		case strings.IndexByte("+-*/<>=~!@#%^&|`?:", c) >= 0:
			if !inOp {
				flush(i)
			}
			if start < 0 {
				start, inOp = i, true
			}
		case c == '(' || c == ')' || c == ',':
			flush(i)
			out = append(out, string(c))
		default:
			flush(i)
		}
	}
	flush(len(s))
	return out
}

func isOpToken(t string) bool {
	return t != "" && strings.IndexByte("+-*/<>=~!@#%^&|`?:", t[0]) >= 0
}

// slotBefore walks backwards from the $n token at index i to the column
// reference it is compared against, stepping over the connector keywords,
// parentheses, commas, casts and earlier placeholders that can sit in between
// (col IN ($1,$2), col = ANY($1), col = ANY(CAST($1 AS text[])), col BETWEEN
// $1 AND $2, col::text ~ $1). It returns the bare column, its qualifier (the
// table or alias before the last dot) and the operator nearest the placeholder,
// or col "" if no identifier is reached. Whatever it returns is later checked
// against captured predicates, so a wrong reading (e.g. landing on VALUES)
// harmlessly resolves to "no captured value". The INTERVAL and EXTRACT keywords
// precede a $n in `INTERVAL $n` / `EXTRACT($n FROM …)` — typed-literal slots,
// not predicates — and are not reported as columns.
func slotBefore(toks []string, i int) (col, qual, op string) {
	sawAnd := false
	for j := i - 1; j >= 0; j-- {
		t := toks[j]
		lt := strings.ToLower(t)
		switch {
		case t == "(" || t == ")" || t == "," || strings.HasPrefix(t, "$"):
		case isOpToken(t):
			if op == "" && t != "::" {
				op = t
				if op == "!=" {
					op = "<>"
				}
			}
		case j >= 1 && toks[j-1] == "::":
			j-- // `col::text`: the type name, then the cast itself
		case (lt == "varying" || lt == "precision") && j >= 2 && toks[j-2] == "::":
			j -= 2 // `::character varying`, `::double precision`
		case lt == "any" || lt == "all" || lt == "cast" || lt == "symmetric":
		case lt == "and":
			sawAnd = true
		case lt == "in":
			op = cmp.Or(op, "=")
		case lt == "like":
			op = cmp.Or(op, "~~")
		case lt == "ilike":
			op = cmp.Or(op, "~~*")
		case lt == "between":
			if op == "" {
				op = ">="
				if sawAnd {
					op = "<="
				}
			}
		case lt == "not":
			op = negatedOp(op)
		case lt == "similar" || lt == "to" || lt == "escape":
			op = opUnmatchable
		case lt == "interval" || lt == "extract" || slotStop[lt]:
			return "", "", ""
		default:
			q, c := splitQualified(t)
			return c, q, op
		}
	}
	return "", "", ""
}

// slotStop are keywords a placeholder can follow directly without being
// compared to anything (`SELECT $1`, `LIMIT $2`, `THEN $3`): no column there.
var slotStop = map[string]bool{
	"select": true, "where": true, "or": true, "on": true, "when": true, "then": true,
	"else": true, "case": true, "values": true, "limit": true, "offset": true, "by": true,
	"returning": true, "set": true,
}

// negatedOp is the operator a NOT in front of IN/LIKE/BETWEEN turns op into.
func negatedOp(op string) string {
	switch op {
	case "=":
		return "<>"
	case "~~", "~~*":
		return "!" + op
	case ">=":
		return "<"
	case "<=":
		return ">"
	}
	return op
}

// splitQualified splits a column reference token into its qualifier (the
// table or alias component, "" if unqualified) and the bare column, both
// unquoted: "t.country" → ("t", "country"), `s."User"."Id"` → ("User", "Id").
func splitQualified(tok string) (qual, col string) {
	parts := strings.Split(tok, ".")
	col = strings.Trim(parts[len(parts)-1], `"`)
	if len(parts) > 1 {
		qual = strings.Trim(parts[len(parts)-2], `"`)
	}
	return qual, col
}

// bareColumn strips schema/table qualification and quoting from a column
// reference token: "t.country" → "country", `"User"."Id"` → "Id".
func bareColumn(tok string) string {
	_, col := splitQualified(tok)
	return col
}

// sqlScopes resolves column qualifiers to tables the way SQL scoping does:
// each parenthesised group is a scope, and a qualifier means the nearest
// enclosing scope whose FROM list names it (as a table or an alias). That is
// what tells `sub.attribute_id` in one EXISTS from `sub.attribute_id` in the
// next when both alias a different table as "sub".
type sqlScopes struct {
	toks  []string
	group []int // index of the "(" enclosing each token, -1 at top level
	rels  map[int]*scopeRels
}

type scopeRels struct {
	names map[string]string // lower(alias or table) → bare relname ("" for a derived table)
	items []string          // FROM items in order (relname, "" for a derived table)
}

func newSQLScopes(toks []string) *sqlScopes {
	group := make([]int, len(toks))
	var stack []int
	for i, t := range toks {
		cur := -1
		if len(stack) > 0 {
			cur = stack[len(stack)-1]
		}
		switch t {
		case "(":
			group[i] = cur
			stack = append(stack, i)
		case ")":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			group[i] = -1
			if len(stack) > 0 {
				group[i] = stack[len(stack)-1]
			}
		default:
			group[i] = cur
		}
	}
	return &sqlScopes{toks: toks, group: group, rels: map[int]*scopeRels{}}
}

// resolve names the table a column at token i belongs to. A qualifier is looked
// up scope by scope outward; an unqualified column belongs to the innermost
// scope with a FROM list, and only when that list has exactly one table.
func (sc *sqlScopes) resolve(i int, qual string) string {
	lq := strings.ToLower(qual)
	for g := sc.group[i]; ; g = sc.group[g] {
		r := sc.scope(g)
		if qual != "" {
			if rel, ok := r.names[lq]; ok {
				return rel
			}
		} else if len(r.items) > 0 {
			if len(r.items) == 1 {
				return r.items[0]
			}
			return ""
		}
		if g < 0 {
			return ""
		}
	}
}

// fromStop ends a FROM item: a keyword in the alias position is not an alias.
var fromStop = map[string]bool{
	"where": true, "join": true, "inner": true, "left": true, "right": true, "full": true,
	"outer": true, "cross": true, "natural": true, "on": true, "using": true, "group": true,
	"order": true, "limit": true, "offset": true, "having": true, "window": true, "union": true,
	"intersect": true, "except": true, "for": true, "tablesample": true, "returning": true,
	"set": true, "values": true, "fetch": true, "select": true, "default": true,
}

// scope parses (once) the FROM/JOIN/UPDATE/INTO items written directly in
// group g — not inside a nested parenthesis, whose subquery is its own scope.
func (sc *sqlScopes) scope(g int) *scopeRels {
	if r, ok := sc.rels[g]; ok {
		return r
	}
	// The group's own tokens, a nested group collapsed to one "()" token.
	var d []string
	for i, t := range sc.toks {
		if sc.group[i] != g {
			continue
		}
		switch t {
		case "(":
			d = append(d, "()")
		case ")":
		default:
			d = append(d, t)
		}
	}
	r := &scopeRels{names: map[string]string{}}
	isName := func(t string) bool {
		return t != "" && t != "()" && t != "," && !isOpToken(t) && !strings.HasPrefix(t, "$") && !fromStop[strings.ToLower(t)]
	}
	for k, t := range d {
		switch strings.ToLower(t) {
		case "from", "join", "into":
		case "update":
			// FOR [NO KEY] UPDATE [SKIP LOCKED] and DO UPDATE SET name no table.
			if k > 0 {
				if prev := strings.ToLower(d[k-1]); prev == "for" || prev == "key" || prev == "do" {
					continue
				}
			}
		default:
			continue
		}
		for p := k + 1; p < len(d); {
			if lt := strings.ToLower(d[p]); lt == "lateral" || lt == "only" {
				p++
				continue
			}
			var rel string
			switch {
			case d[p] == "()":
			case isName(d[p]):
				rel = bareColumn(d[p])
				r.names[strings.ToLower(rel)] = rel
			default:
				p = len(d)
				continue
			}
			p++
			if p < len(d) && strings.EqualFold(d[p], "as") {
				p++
			}
			if p < len(d) && isName(d[p]) {
				r.names[strings.ToLower(strings.Trim(d[p], `"`))] = rel
				p++
			}
			r.items = append(r.items, rel)
			if p < len(d) && d[p] == "()" { // column alias list of a derived table
				p++
			}
			if p >= len(d) || d[p] != "," {
				break
			}
			p++
		}
	}
	sc.rels[g] = r
	return r
}
