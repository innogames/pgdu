package pg

import "strings"

// MainTable extracts the primary table a normalized statement reads from or
// writes to — the relation the query is "about", used to label the top-queries
// row and to drive its `d` (describe) and `u` (disk usage) actions. It is a
// deliberately shallow parse, not a full SQL grammar: it finds the keyword that
// introduces the first base relation (FROM / UPDATE / INTO / COPY / LOCK) and
// returns the following identifier.
//
// For a multi-table query (joins, subqueries) it returns the first FROM table,
// which is the driving relation in the common ORM-generated shape. A WITH query
// is resolved against the statement that follows its CTE definitions (so a
// data-modifying `WITH … UPDATE t …` points at t, not a CTE named in its FROM).
// When that statement's FROM names a CTE rather than a base relation
// (`WITH data AS (SELECT … FROM t …) SELECT * FROM data`), it is resolved through
// the CTE to the relation the CTE itself reads from (t) — the CTE name is kept
// only when its body has no useful table (WITH c AS (SELECT 1) …).
// It returns "" when there's nothing useful to point at: VALUES, a leading
// subquery (FROM (SELECT …)), or an unrecognised statement. The result keeps any
// schema qualification (public.t) so to_regclass can resolve it as-is.
func MainTable(query string) string {
	if v, ok := mainTableMemo.Load(query); ok {
		return v.(string)
	}
	r := parseMainTable(query)
	mainTableMemo.Store(query, r)
	return r
}

func parseMainTable(query string) string {
	toks := sqlWords(query)
	if len(toks) == 0 {
		return ""
	}
	// An autovacuum worker reports its work in pg_stat_activity as a status line,
	// not SQL: "autovacuum: VACUUM[ ANALYZE] schema.table[ (to prevent
	// wraparound)]" or "autovacuum: ANALYZE schema.table". Drop the prefix so the
	// VACUUM/ANALYZE keyword behind it drives the lookup like a manual command.
	if strings.EqualFold(toks[0], "autovacuum") {
		toks = toks[1:]
		if len(toks) == 0 {
			return ""
		}
	}
	kw := 0
	var ctes map[string]int
	// A WITH query's real subject is the statement that runs *after* its CTE
	// definitions (SELECT/UPDATE/DELETE/INSERT/MERGE), not the first FROM — that
	// FROM often names a CTE (e.g. UPDATE … FROM cte) rather than a base relation.
	// Skip past the (parenthesized) CTE bodies to that statement's keyword, and
	// record the CTE bodies so a `FROM <cte>` in that statement can be resolved
	// back to the relation the CTE reads from.
	if strings.EqualFold(toks[0], "with") {
		ctes = cteBodies(toks)
		kw = mainStmtAfterWith(toks)
		if kw < 0 {
			return ""
		}
	}
	name := tableForStmt(toks, kw)
	if len(ctes) > 0 {
		name = resolveThroughCTEs(toks, name, ctes)
	}
	return name
}

// resolveThroughCTEs follows a main-statement relation name that turns out to be
// a CTE defined in the same WITH clause down to the base relation that CTE reads
// from, chaining through nested CTE references (b → a → real_t). It stops and
// returns the current name when it isn't a CTE, when the CTE body yields no table
// (keep the CTE name as the label), or when a name repeats — the guard against a
// RECURSIVE CTE that references itself.
func resolveThroughCTEs(toks []string, name string, ctes map[string]int) string {
	seen := map[string]bool{}
	for {
		key := strings.ToLower(name)
		body, ok := ctes[key]
		if !ok || seen[key] {
			return name
		}
		seen[key] = true
		inner := tableForStmt(toks, body)
		if inner == "" {
			return name
		}
		name = inner
	}
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
		return tableAfter(toks, indexOfAfter(toks, "into", kw, len(toks)))
	case "select", "delete":
		// SELECT … FROM <table>, DELETE FROM <table>. indexOfFrom is depth-aware so
		// a `from` inside a SELECT-list expression (extract(epoch FROM ts)) or a
		// scalar subquery isn't mistaken for the statement's own FROM clause.
		return tableAfter(toks, indexOfFrom(toks, kw))
	case "table":
		// TABLE <table> has no FROM; its operand is the next word.
		return cleanTable(toks, kw+1)
	case "copy":
		// COPY <table> [(cols)] FROM/TO … — the relation is the next word. The
		// COPY (SELECT … FROM t …) TO … form has a "(" there instead, so tableAfter
		// descends into the subquery and uses its first FROM relation (t).
		return tableAfter(toks, kw)
	case "lock":
		// LOCK [TABLE] [ONLY] <table> [IN … MODE] — the TABLE noise word is
		// optional; cleanTable already skips ONLY. A multi-table LOCK labels
		// with its first relation, like a multi-table FROM.
		i := kw + 1
		if i < len(toks) && strings.EqualFold(toks[i], "table") {
			i++
		}
		return cleanTable(toks, i)
	case "vacuum", "analyze":
		// VACUUM/ANALYZE <table> — manual commands and the autovacuum worker
		// status line (after its prefix is stripped). Options can sit between the
		// keyword and the relation, so tableForVacuum skips them.
		return tableForVacuum(toks, kw)
	default:
		return ""
	}
}

// tableForVacuum resolves the target relation of a VACUUM or ANALYZE. The table
// follows the keyword, but options may intervene: a parenthesized list
// (VACUUM (VERBOSE, ANALYZE) t) or the legacy bare keywords (VACUUM FULL FREEZE
// VERBOSE ANALYZE t, VACUUM ANALYZE t) — skip both before reading the name. A
// bare VACUUM/ANALYZE with no relation (the whole-database form) yields "".
func tableForVacuum(toks []string, kw int) string {
	i := kw + 1
	if i < len(toks) && toks[i] == "(" {
		i = skipParens(toks, i)
	}
	for i < len(toks) && isVacuumOption(toks[i]) {
		i++
	}
	return cleanTable(toks, i)
}

// isVacuumOption reports whether tok is a legacy unparenthesized VACUUM/ANALYZE
// option keyword that precedes the relation rather than naming it.
func isVacuumOption(tok string) bool {
	switch strings.ToLower(tok) {
	case "full", "freeze", "verbose", "analyze", "skip_locked":
		return true
	}
	return false
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
		// Descend into the subquery operand and use its own first FROM relation;
		// indexOfFrom starts just inside the paren and is bounded to this subquery
		// (it stops at the matching closing paren).
		if t := tableAfter(toks, indexOfFrom(toks, kw+2)); t != "" {
			return t
		}
		// The subquery has no FROM clause of its own — its real relation is buried
		// in a function argument or scalar subquery, e.g. FROM (SELECT
		// generate_series((SELECT max(id) FROM t …)) AS x). Fall back to the first
		// FROM relation anywhere inside this subquery. Bounded to the subquery (via
		// skipParens) so an unrelated table in the outer WHERE — a NOT IN / EXISTS
		// probe — isn't mistaken for the subject.
		return tableAfter(toks, indexOfAfter(toks, "from", kw+2, skipParens(toks, kw+1)))
	}
	// A set-returning function in FROM (unnest(…), generate_series(…),
	// jsonb_to_recordset(…)) is an identifier immediately followed by "(", not a
	// base relation. Skip it and use the next FROM relation — the table the query
	// is really about, commonly inside an EXISTS or JOIN subquery. The relation can
	// live at any paren depth (inside WHERE EXISTS (…)), so this is a flat scan for
	// the next "from", not the depth-aware one. Guarded to FROM so an
	// INSERT INTO t (col, …) column list isn't mistaken for a function call.
	if strings.EqualFold(toks[kw], "from") && kw+2 < len(toks) && toks[kw+2] == "(" {
		return tableAfter(toks, indexOfAfter(toks, "from", kw+2, len(toks)))
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
	// Drop the double quotes Postgres uses for case/keyword-sensitive identifiers
	// ("server", "schema"."table") — the label and to_regclass want the bare name,
	// not the quoted literal. The quote chars survive sqlWords as part of the token.
	return strings.ReplaceAll(toks[i], `"`, "")
}

// indexOfAfter returns the position of the first token equal (case-insensitively)
// to word in [start, end), or -1. Pass len(toks) for end to scan to the end;
// a smaller end bounds the search to a subquery. The "(" / ")" markers never
// match a keyword, so a paren near the keyword can't be confused for it.
func indexOfAfter(toks []string, word string, start, end int) int {
	if end > len(toks) {
		end = len(toks)
	}
	for i := start; i < end; i++ {
		if strings.EqualFold(toks[i], word) {
			return i
		}
	}
	return -1
}

// indexOfFrom returns the position of a statement's own FROM clause — the first
// "from" token at or after start that sits at the paren depth start begins at
// (treated as depth 0). A "from" nested in a SELECT-list expression
// (extract(epoch FROM ts)) or a scalar subquery is at a deeper depth and skipped,
// so the result is the FROM that introduces the statement's relation. A closing
// paren seen at depth 0 means the surrounding (sub)query ended first: returns -1.
// Used both for a top-level statement (start at its keyword) and to descend into
// a subquery operand, FROM (SELECT … FROM t …) (start just inside the paren).
func indexOfFrom(toks []string, start int) int {
	depth := 0
	for i := start; i < len(toks); i++ {
		switch {
		case toks[i] == "(":
			depth++
		case toks[i] == ")":
			if depth == 0 {
				return -1
			}
			depth--
		case depth == 0 && strings.EqualFold(toks[i], "from"):
			return i
		}
	}
	return -1
}

// cteBodies maps each CTE name defined in a WITH clause (lowercased, unquoted) to
// the token index of the keyword that opens its body — the token right after the
// "(" following AS. It lets a main statement whose FROM names a CTE
// (SELECT … FROM data) be resolved back to the relation the CTE reads from.
// CTE definitions live at paren depth 0 between WITH and the main statement;
// sqlWords drops the commas between them, so each definition runs until the next
// depth-0 token that is a main statement keyword (the WITH's subject) — anything
// else there starts the next CTE.
func cteBodies(toks []string) map[string]int {
	bodies := map[string]int{}
	i := 1 // past "with"
	if i < len(toks) && strings.EqualFold(toks[i], "recursive") {
		i++
	}
	for i < len(toks) && !isMainKeyword(toks[i]) {
		name := strings.ToLower(strings.ReplaceAll(toks[i], `"`, ""))
		i++
		// Optional column-list: name (col, …) AS — skip the parenthesized group.
		if i < len(toks) && toks[i] == "(" {
			i = skipParens(toks, i)
		}
		if i >= len(toks) || !strings.EqualFold(toks[i], "as") {
			break // malformed; give up rather than mis-parse the rest
		}
		i++
		// Optional NOT MATERIALIZED / MATERIALIZED between AS and the body.
		for i < len(toks) && (strings.EqualFold(toks[i], "not") || strings.EqualFold(toks[i], "materialized")) {
			i++
		}
		if i >= len(toks) || toks[i] != "(" {
			break
		}
		if i+1 < len(toks) {
			bodies[name] = i + 1
		}
		i = skipParens(toks, i)
	}
	return bodies
}

// skipParens returns the index just past the ")" that matches the "(" at open.
// An unbalanced run consumes the rest of the tokens.
func skipParens(toks []string, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i] {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(toks)
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
