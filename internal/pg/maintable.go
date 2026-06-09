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
// negative kw (keyword absent) or a missing operand yields "".
//
// When the operand is a subquery (FROM (SELECT … FROM t …)) it descends one
// level and uses the subquery's own first FROM relation. This catches the
// common count/paginate wrapper shape `SELECT … FROM (SELECT … FROM t …) AS x`,
// where the relation the query is "about" is t, not the anonymous subquery.
func tableAfter(toks []string, kw int) string {
	if kw < 0 {
		return ""
	}
	if kw+1 < len(toks) && toks[kw+1] == "(" {
		return tableAfter(toks, indexOfFrom(toks, kw+2))
	}
	// A set-returning function in FROM (unnest(…), generate_series(…),
	// jsonb_to_recordset(…)) is an identifier immediately followed by "(", not a
	// base relation. Skip it and use the next FROM relation — the table the query
	// is really about, commonly inside an EXISTS or JOIN subquery. Guarded to FROM
	// so an INSERT INTO t (col, …) column list isn't mistaken for a function call.
	if strings.EqualFold(toks[kw], "from") && kw+2 < len(toks) && toks[kw+2] == "(" {
		return tableAfter(toks, indexOfFrom(toks, kw+2))
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

// indexOfFrom returns the position of the first "from" token at or after start,
// or -1. Used to descend into a subquery operand (FROM (SELECT … FROM t …)).
func indexOfFrom(toks []string, start int) int {
	for i := start; i < len(toks); i++ {
		if strings.EqualFold(toks[i], "from") {
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
//
// Comments are stripped first so an ORM tag like `/* update for … */` can't be
// mistaken for the statement keyword or its table.
func sqlWords(s string) []string {
	s = StripSQLComments(s)
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

// StripSQLComments replaces every SQL comment with a single space, so the
// keyword/field parsers don't trip over an ORM tag like `/* … */` prepended to
// the statement (Hibernate's use_sql_comments) or a trailing `-- note`. Block
// comments may span lines and nest, matching Postgres; an unterminated comment
// consumes the rest of the string. Comments are replaced rather than deleted so
// `a/* */b` stays two tokens.
func StripSQLComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			i = skipBlockComment(s, i) // lands on the closing '/' (or end)
			b.WriteByte(' ')
		case s[i] == '-' && i+1 < len(s) && s[i+1] == '-':
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// skipBlockComment returns the index of the last byte of the /* … */ comment
// that opens at start, so the caller's loop advances past it. Block comments
// nest in Postgres, so an inner /* … */ must close before the outer one does.
// An unterminated comment consumes the rest of the string.
func skipBlockComment(s string, start int) int {
	depth := 0
	for i := start; i < len(s); i++ {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			depth++
			i++
		case s[i] == '*' && i+1 < len(s) && s[i+1] == '/':
			depth--
			i++
			if depth == 0 {
				return i
			}
		}
	}
	return len(s)
}
