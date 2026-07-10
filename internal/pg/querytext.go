package pg

import (
	"regexp"
	"strconv"
	"strings"
)

// ExplainableQuery reports whether query can be passed to EXPLAIN.
// First keyword, lower-cased. ORM-generated statements often carry a leading
// /* … */ tag, so strip comments before looking at the keyword.
func ExplainableQuery(query string) bool {
	fields := strings.Fields(StripSQLComments(query))
	if len(fields) == 0 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "select", "insert", "update", "delete", "merge", "with", "values", "table":
		return true
	default:
		return false
	}
}

// QueryKind returns a short tag for the statement's command type — S (SELECT),
// SL (a locking SELECT … FOR UPDATE/SHARE/…), L (a SELECT that acquires an
// advisory lock), I (INSERT), U (UPDATE), D (DELETE), M (MERGE), T
// (BEGIN/COMMIT), P (PREPARE) — for the top-queries `T` column. SL and L flag SELECTs that
// take locks rather than just reading, which stand out in a hot-query list. The
// leading keyword is taken after stripping any ORM comment tag; anything
// unrecognized returns "?".
func QueryKind(query string) string {
	if v, ok := queryKindMemo.Load(query); ok {
		return v.(string)
	}
	r := parseQueryKind(query)
	queryKindMemo.Store(query, r)
	return r
}

func parseQueryKind(query string) string {
	lower := strings.ToLower(StripSQLComments(query))
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return "?"
	}
	switch fields[0] {
	case "select", "table", "values", "with", "copy":
		switch {
		case isAdvisoryLock(lower):
			return "L"
		case hasLockingClause(fields):
			return "SL"
		default:
			return "S"
		}
	case "insert":
		return "I"
	case "update":
		return "U"
	case "delete":
		return "D"
	case "merge":
		return "M"
	case "begin", "commit", "rollback":
		// transactions
		return "T"
	case "prepare":
		// PREPARE TRANSACTION 'gid' is 2PC transaction control, not a prepared
		// statement — keep it with the other transaction commands.
		if len(fields) > 1 && fields[1] == "transaction" {
			return "T"
		}
		return "P"
	default:
		return "?"
	}
}

// isAdvisoryLock reports whether a (lower-cased) statement calls a PostgreSQL
// advisory-lock acquisition function: pg_advisory_lock / pg_advisory_xact_lock
// and their pg_try_* variants (each substring also covers the _shared form).
// These are SELECTs whose purpose is to take a lock rather than read data, so
// QueryKind tags them "L". Advisory *unlock* functions are deliberately
// excluded — they release a lock, not acquire one.
func isAdvisoryLock(lower string) bool {
	return strings.Contains(lower, "pg_advisory_lock") ||
		strings.Contains(lower, "pg_advisory_xact_lock") ||
		strings.Contains(lower, "pg_try_advisory_lock") ||
		strings.Contains(lower, "pg_try_advisory_xact_lock")
}

// ReadOnlyQuery reports whether a normalized statement is safe to actually
// execute, which EXPLAIN ANALYZE does (it runs the query). Only read-only
// shapes qualify: a bare SELECT / TABLE / VALUES, or a WITH whose CTEs contain
// no data-modifying statement. Plain DML and utility statements are excluded —
// running them for real would write. This is stricter than ExplainableQuery,
// which also admits DML (EXPLAIN without ANALYZE never executes).
func ReadOnlyQuery(query string) bool {
	fields := strings.Fields(strings.ToLower(StripSQLComments(query)))
	if len(fields) == 0 {
		return false
	}
	// A row-locking clause (SELECT … FOR UPDATE/SHARE/…) takes real locks the
	// moment it executes — and EXPLAIN ANALYZE executes — so it isn't safe to run
	// even inside a rolled-back transaction (it would block concurrent writers
	// for the duration). Reject it regardless of the leading keyword; the plain
	// EXPLAIN (no ANALYZE) path still shows its plan.
	if hasLockingClause(fields) {
		return false
	}
	switch fields[0] {
	case "select", "table", "values":
		return true
	case "with":
		// A data-modifying CTE (WITH … INSERT/UPDATE/DELETE/MERGE) writes when
		// executed, so reject any WITH that names one.
		for _, f := range fields {
			switch strings.Trim(f, "(),") {
			case "insert", "update", "delete", "merge":
				return false
			}
		}
		return true
	default:
		return false
	}
}

// hasLockingClause reports whether the (lower-cased, whitespace-split) tokens
// contain a row-level locking clause: FOR UPDATE, FOR SHARE, FOR NO KEY UPDATE,
// or FOR KEY SHARE. It keys off "for" immediately followed by one of those
// lock-strength keywords, which distinguishes it from the other SQL uses of FOR
// (e.g. substring(x FROM a FOR b)), whose next token is an expression.
func hasLockingClause(fields []string) bool {
	for i, f := range fields {
		if strings.Trim(f, "(),") != "for" || i+1 >= len(fields) {
			continue
		}
		switch strings.Trim(fields[i+1], "(),") {
		case "update", "share", "no", "key":
			return true
		}
	}
	return false
}

// QualstatsExampleUsable reports whether a pg_qualstats example query is a
// complete denormalization of the normalized statement rather than a truncated
// fragment. pg_qualstats stores example queries capped at track_activity_query_size
// (1 KB by default), so a long statement comes back cut off mid-token — wrapping
// such a fragment in EXPLAIN yields a syntax error. We detect this structurally:
// the text following the last $n placeholder in the normalized query carries no
// constants, so a complete example must end with that same suffix; a truncated
// one won't. When the query has no trailing constant-free text to anchor on (it
// ends at/with its last parameter), we can't tell and optimistically accept it —
// such queries are short and rarely truncated.
func QualstatsExampleUsable(normalized, example string) bool {
	// A complete denormalization has every constant spliced in. When pg_qualstats
	// can't reconstruct a qual (e.g. WHERE alias.id=$1) it leaves the $n in place;
	// a plain EXPLAIN on that fails with "there is no parameter $n" since, unlike
	// GENERIC_PLAN, it supplies no value. Reject so the caller falls back to the
	// synthesized sample call (which EXPLAINs via GENERIC_PLAN).
	if hasParamPlaceholder(example) {
		return false
	}
	// A truncated example is cut mid-statement, leaving unbalanced parentheses or
	// an unterminated string literal; EXPLAINing it then fails with a syntax
	// error (often "at or near \";\""). The suffix anchor below can't catch this
	// when the normalized query ends at its last $n (empty tail) — common for the
	// `… LIMIT $n OFFSET $n` shape, which is long yet anchorless — so reject any
	// structurally-unbalanced example up front regardless of the anchor.
	if !balancedDelimiters(example) {
		return false
	}
	tail := strings.TrimSpace(normalizedTailAfterLastParam(normalized))
	if tail == "" {
		return true
	}
	return strings.HasSuffix(collapseSpaces(example), collapseSpaces(tail))
}

// balancedDelimiters reports whether s has balanced parentheses and terminated
// single-quoted string literals — a necessary condition for a complete SQL
// statement, used to spot a pg_qualstats example truncated at
// track_activity_query_size before it reaches EXPLAIN. Handles the SQL `"`
// in-string quote escape; dollar-quoting and E'…\" backslash escapes are rare
// in normalized statements and not modelled (worst case a usable example is
// rejected and the caller falls back to the synthesized sample).
func balancedDelimiters(s string) bool {
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		if inStr {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // doubled '' is an escaped quote — stay in the string
					continue
				}
				inStr = false
			}
			continue
		}
		switch s[i] {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !inStr
}

// hasParamPlaceholder reports whether s contains a $n placeholder (a '$'
// immediately followed by one or more digits).
func hasParamPlaceholder(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			return true
		}
	}
	return false
}

// normalizedTailAfterLastParam returns the substring following the highest-
// numbered $n placeholder occurrence in a normalized query (the constant-free
// suffix). Returns "" when the query has no placeholders.
func normalizedTailAfterLastParam(query string) string {
	last := -1
	for i := 0; i < len(query); i++ {
		if query[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(query) && query[j] >= '0' && query[j] <= '9' {
			j++
		}
		if j > i+1 { // at least one digit → a real placeholder
			last = j
		}
	}
	if last < 0 {
		return ""
	}
	return query[last:]
}

// collapseSpaces normalizes all whitespace runs to single spaces (and trims),
// so a suffix comparison ignores the indentation/newlines that differ between
// pg_stat_statements text and a reconstructed example.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// extractFieldRe matches an EXTRACT(field FROM source) expression whose field
// slot is a $n placeholder. pg_stat_statements normalizes *every* constant to a
// $n, including the EXTRACT field keyword ("epoch", "year", …) — but in the
// SQL grammar that slot is a field identifier / string literal, not a bind
// parameter, so the verbatim normalized text ("extract($2 FROM log_date)") is
// neither PREPARE-able nor EXPLAIN-able (both fail with "syntax error at or near
// $n"). The pattern is case-insensitive and tolerates whitespace; it captures the
// ordinal so callers can fill that placeholder with a real field literal.
var extractFieldRe = regexp.MustCompile(`(?i)\bextract\s*\(\s*\$(\d+)\s+from\b`)

// rewriteExtractFieldParams turns each EXTRACT($n FROM source) back into a form
// PostgreSQL will parse with the $n still a bindable parameter, by switching the
// SQL-keyword syntax to the equivalent function-call form extract($n, source)
// (the SQL-standard EXTRACT maps to pg_catalog.extract(text, …) since PG14). This
// keeps the placeholder numbering gap-free — $n stays $n — so inferred parameter
// types and sampled values still map back to the original query by ordinal.
func rewriteExtractFieldParams(query string) string {
	return extractFieldRe.ReplaceAllString(query, "extract($$${1},")
}

// ExtractFieldOrdinals returns the $n ordinals that sit in the field slot of an
// EXTRACT(field FROM source) expression in a normalized query. Those placeholders
// are not real bind parameters (see extractFieldRe), so BuildSampleCall must fill
// them with a valid bare field literal rather than a synthesized typed value.
func ExtractFieldOrdinals(query string) []int {
	matches := extractFieldRe.FindAllStringSubmatch(query, -1)
	var out []int
	for _, m := range matches {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// intervalParamRe matches an INTERVAL $n typed-literal whose value slot is a $n
// placeholder. pg_stat_statements normalizes the string in `INTERVAL '1 day'` to
// a $n, yielding `INTERVAL $1` — but the SQL grammar's INTERVAL-literal form
// requires a string constant in that slot, not a bind parameter, so the verbatim
// normalized text is neither PREPARE-able nor EXPLAIN-able (both fail with
// "syntax error at or near $n"). The pattern is case-insensitive and tolerates
// whitespace; it captures the ordinal so callers can map it back to the original.
var intervalParamRe = regexp.MustCompile(`(?i)\binterval\s+\$(\d+)`)

// dtArithParamRe matches a niladic datetime expression (now(), current_timestamp,
// …) immediately followed by + or - and a bare $n placeholder, capturing the
// expression, the operator and the ordinal. An ORM that binds the interval as a
// parameter — `NOW() - ?` — normalizes to `NOW() - $1` with no INTERVAL keyword
// for intervalParamRe to anchor on. PREPARE/GENERIC_PLAN then can't resolve the
// operator: with an untyped $n the planner picks the `timestamptz - timestamptz →
// interval` candidate (inferring $n as timestamptz), so the surrounding
// `col < NOW() - $1` fails with "operator does not exist: timestamp with time zone
// < interval". Casting $n to interval forces the intended `timestamptz - interval
// → timestamptz` operator. Case-insensitive; tolerates whitespace and an optional
// precision on the keyword forms (current_timestamp(3)). The function-call forms
// require their () so a column literally named e.g. "now" isn't mistaken for now().
var dtArithParamRe = regexp.MustCompile(`(?i)\b((?:now|clock_timestamp|statement_timestamp|transaction_timestamp)\s*\(\s*\)|(?:current_timestamp|current_date|current_time|localtimestamp|localtime)(?:\s*\(\s*\d+\s*\))?)\s*([-+])\s*\$(\d+)`)

// rewriteNormalizedParams makes a pg_stat_statements-normalized query parseable
// with its $n placeholders intact, fixing the spots where normalization drops a
// $n where the grammar forbids one (EXTRACT(field FROM …), INTERVAL value) or
// leaves one whose type the planner can't infer (datetime ± $n). INTERVAL $n and
// the bare datetime-arithmetic $n both become the cast $n::interval — a bindable
// expression of the correct type. Ordinals are preserved throughout, so inferred
// parameter types and sampled values still map back to the original query by
// number.
func rewriteNormalizedParams(query string) string {
	q := rewriteExtractFieldParams(query)
	// Before intervalParamRe: this only matches a bare $n (no INTERVAL keyword), so
	// it never touches `NOW() - INTERVAL $1` — but running it after the interval
	// rewrite would re-match the `NOW() - $1` it produces and double-cast it.
	q = dtArithParamRe.ReplaceAllString(q, "${1} ${2} $$${3}::interval")
	return intervalParamRe.ReplaceAllString(q, "$$${1}::interval")
}

// IntervalParamOrdinals returns the $n ordinals that sit in the value slot of an
// INTERVAL $n typed-literal in a normalized query. Substituting a synthesized
// typed literal there would produce `INTERVAL '1 day'::interval` — still a syntax
// error — so BuildSampleCall must fill these with a bare interval string instead.
func IntervalParamOrdinals(query string) []int {
	matches := intervalParamRe.FindAllStringSubmatch(query, -1)
	var out []int
	for _, m := range matches {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, n)
		}
	}
	return out
}
