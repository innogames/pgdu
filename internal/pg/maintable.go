package pg

import "strings"

// MainTable extracts the primary table a normalized statement reads from or
// writes to — the relation the query is "about", used to label the top-queries
// row and to drive its `d` (describe) action. It is a deliberately shallow
// parse, not a full SQL grammar: it finds the keyword that introduces the first
// base relation (FROM / UPDATE / INTO) and returns the following identifier.
//
// For a multi-table query (joins, subqueries) it returns the first FROM table,
// which is the driving relation in the common ORM-generated shape. It returns ""
// when there's nothing useful to point at: VALUES, a leading subquery
// (FROM (SELECT …)), or an unrecognised statement. The result keeps any schema
// qualification (public.t) so to_regclass can resolve it as-is.
func MainTable(query string) string {
	toks := sqlWords(query)
	if len(toks) == 0 {
		return ""
	}
	switch strings.ToLower(toks[0]) {
	case "update":
		// UPDATE <table> SET … — the table is the immediate next word.
		return tableAfter(toks, 0)
	case "insert", "merge":
		// INSERT INTO <table> … / MERGE INTO <table> …
		return tableAfter(toks, indexOf(toks, "into"))
	case "select", "with", "delete", "table":
		// SELECT … FROM <table>, DELETE FROM <table>, WITH … FROM <table>.
		// TABLE <table> has no FROM; its operand is the next word.
		if strings.ToLower(toks[0]) == "table" {
			return cleanTable(toks, 1)
		}
		return tableAfter(toks, indexOf(toks, "from"))
	default:
		return ""
	}
}

// tableAfter returns the relation name following the keyword at index kw. A
// negative kw (keyword absent) or a missing/parenthesised operand yields "".
func tableAfter(toks []string, kw int) string {
	if kw < 0 {
		return ""
	}
	return cleanTable(toks, kw+1)
}

// cleanTable returns toks[i] as a relation name, or "" when it's absent or a
// subquery marker ("(") rather than an identifier.
func cleanTable(toks []string, i int) string {
	if i < 0 || i >= len(toks) || toks[i] == "(" {
		return ""
	}
	return toks[i]
}

// indexOf returns the position of the first token equal (case-insensitively) to
// word, or -1. The "(" marker never matches a keyword, so a subquery opening
// before the keyword can't be confused for it.
func indexOf(toks []string, word string) int {
	for i, t := range toks {
		if strings.EqualFold(t, word) {
			return i
		}
	}
	return -1
}

// sqlWords tokenizes a normalized statement into identifier words plus a bare
// "(" marker for each opening parenthesis. Identifier characters are letters,
// digits, underscore, dollar (placeholders), dot (schema qualification) and the
// double quote (quoted identifiers); every other byte is a separator. The "("
// marker lets callers tell `FROM (SELECT …)` (a subquery) apart from `FROM t`.
func sqlWords(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '$', c == '.', c == '"':
			cur.WriteByte(c)
		case c == '(':
			flush()
			out = append(out, "(")
		default:
			flush()
		}
	}
	flush()
	return out
}
