package pg

import "strings"

// MainTable extracts the primary table a normalized statement reads from or
// writes to — the relation the query is "about", used to label the top-queries
// row and to drive its `d` (describe) and `u` (disk usage) actions. It is a
// deliberately shallow parse, not a full SQL grammar: it finds the keyword that
// introduces the first base relation (FROM / UPDATE / INTO) and returns the
// following identifier.
//
// For a multi-table query (joins, subqueries) it returns the first FROM table,
// which is the driving relation in the common ORM-generated shape. A WITH query
// is resolved against the statement that follows its CTE definitions (so a
// data-modifying `WITH … UPDATE t …` points at t, not a CTE named in its FROM).
// It returns "" when there's nothing useful to point at: VALUES, a leading
// subquery (FROM (SELECT …)), or an unrecognised statement. The result keeps any
// schema qualification (public.t) so to_regclass can resolve it as-is.
func MainTable(query string) string {
	toks := sqlWords(query)
	if len(toks) == 0 {
		return ""
	}
	kw := 0
	// A WITH query's real subject is the statement that runs *after* its CTE
	// definitions (SELECT/UPDATE/DELETE/INSERT/MERGE), not the first FROM — that
	// FROM often names a CTE (e.g. UPDATE … FROM cte) rather than a base relation.
	// Skip past the (parenthesized) CTE bodies to that statement's keyword.
	if strings.EqualFold(toks[0], "with") {
		kw = mainStmtAfterWith(toks)
		if kw < 0 {
			return ""
		}
	}
	return tableForStmt(toks, kw)
}

// tableForStmt resolves the main relation of the statement whose leading keyword
// is at index kw — 0 for a plain statement, or the post-CTE keyword index for a
// WITH query. Index-relative searches (into/from after kw) keep it correct when
// kw is past a WITH clause whose CTE bodies contain their own from/into.
func tableForStmt(toks []string, kw int) string {
	switch strings.ToLower(toks[kw]) {
	case "update":
		// UPDATE <table> SET … — the table is the immediate next word.
		return tableAfter(toks, kw)
	case "insert", "merge":
		// INSERT INTO <table> … / MERGE INTO <table> …
		return tableAfter(toks, indexOfAfter(toks, "into", kw))
	case "select", "delete":
		// SELECT … FROM <table>, DELETE FROM <table>.
		return tableAfter(toks, indexOfAfter(toks, "from", kw))
	case "table":
		// TABLE <table> has no FROM; its operand is the next word.
		return cleanTable(toks, kw+1)
	default:
		return ""
	}
}

// mainStmtAfterWith returns the index of the primary statement keyword following
// a WITH query's CTE definitions — the SELECT/INSERT/UPDATE/DELETE/MERGE/TABLE/
// VALUES that actually executes — or -1 if none is found. CTE bodies are
// parenthesized, so the keyword we want is the first one at paren depth 0; this
// skips keywords *inside* a data-modifying CTE such as `WITH x AS (DELETE FROM
// t …) …`. Relies on sqlWords emitting "(" and ")" markers.
func mainStmtAfterWith(toks []string) int {
	depth := 0
	for i := 1; i < len(toks); i++ {
		switch {
		case toks[i] == "(":
			depth++
		case toks[i] == ")":
			if depth > 0 {
				depth--
			}
		case depth == 0 && isMainKeyword(toks[i]):
			return i
		}
	}
	return -1
}

// isMainKeyword reports whether tok introduces a top-level statement that can
// follow a WITH clause.
func isMainKeyword(tok string) bool {
	switch strings.ToLower(tok) {
	case "select", "insert", "update", "delete", "merge", "table", "values":
		return true
	}
	return false
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
	// ONLY is a no-inherit modifier, not a relation: FROM ONLY t, UPDATE ONLY t,
	// DELETE FROM ONLY t (PK/FK enforcement queries use it). Skip to the table.
	if strings.EqualFold(toks[i], "only") {
		return cleanTable(toks, i+1)
	}
	return toks[i]
}

// indexOfAfter returns the position of the first token equal (case-insensitively)
// to word at or after start, or -1. The "(" / ")" markers never match a keyword,
// so a paren near the keyword can't be confused for it.
func indexOfAfter(toks []string, word string, start int) int {
	for i := start; i < len(toks); i++ {
		if strings.EqualFold(toks[i], word) {
			return i
		}
	}
	return -1
}

// indexOfFrom returns the position of the first "from" token at or after start,
// or -1. Used to descend into a subquery operand (FROM (SELECT … FROM t …)).
func indexOfFrom(toks []string, start int) int {
	return indexOfAfter(toks, "from", start)
}

// sqlWords tokenizes a normalized statement into identifier words plus bare "("
// and ")" markers for each parenthesis. Identifier characters are letters,
// digits, underscore, dollar (placeholders), dot (schema qualification) and the
// double quote (quoted identifiers); every other byte is a separator. The "("
// marker lets callers tell `FROM (SELECT …)` (a subquery) apart from `FROM t`;
// the matching ")" lets them track paren depth (e.g. to skip WITH CTE bodies).
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
		case c == ')':
			flush()
			out = append(out, ")")
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
