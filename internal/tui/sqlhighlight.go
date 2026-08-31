package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// SQL presentation for the query-detail view: tokenize the statement, break a
// new line before each top-level clause, word-wrap at token boundaries, and
// style tokens only at emission. Wrapping must happen on plain text — widths
// would be wrong (and escape codes could be severed) if ANSI were inserted
// before the layout pass; this is the same ordering colorizeExplain uses.

type sqlTokenKind int

const (
	tokKeyword sqlTokenKind = iota
	tokIdent
	tokQuotedIdent // "name" — quotes dimmed, body default
	tokString      // '...' ('' escape) and $tag$...$tag$
	tokNumber
	tokParam   // $1, $2, …
	tokOp      // operators and punctuation
	tokComment // -- to end of line, /* ... */
)

type sqlToken struct {
	kind        sqlTokenKind
	text        string
	spaceBefore bool // whitespace separated it from the previous token
	depth       int  // paren depth at token start; clause breaks only at 0
}

var (
	// Bold on the default foreground: keywords should structure the text, not
	// compete with the accent colours the metrics above already use.
	styleSQLKeyword = lipgloss.NewStyle().Bold(true)
	// Literals and $n parameters share the sample-value accent (styleBarAlt),
	// so a sample call's substituted values pop the same way the old all-yellow
	// rendering did — but now only on the values themselves.
	styleSQLLiteral = styleBarAlt
)

// sqlKeywords is a presentation-only keyword set, curated for the DML shapes
// pg_stat_statements records. Deliberately not shared with internal/pg's
// query-kind checks: those answer "is it explainable/read-only", this answers
// "what should read bold".
var sqlKeywords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(
		"SELECT INSERT UPDATE DELETE MERGE FROM WHERE JOIN LEFT RIGHT INNER " +
			"OUTER CROSS FULL LATERAL ON AS AND OR NOT IN IS NULL TRUE FALSE " +
			"LIKE ILIKE SIMILAR BETWEEN EXISTS ANY SOME ALL CASE WHEN THEN " +
			"ELSE END GROUP BY ORDER HAVING LIMIT OFFSET UNION INTERSECT " +
			"EXCEPT DISTINCT VALUES SET INTO RETURNING WITH RECURSIVE " +
			"CONFLICT DO NOTHING ASC DESC USING NULLS FIRST LAST CAST " +
			"COALESCE NULLIF GREATEST LEAST INTERVAL EXTRACT FILTER OVER " +
			"PARTITION WINDOW FETCH NEXT ROWS ONLY FOR SHARE OF SKIP LOCKED " +
			"DEFAULT ARRAY COLLATE TO CURRENT_DATE CURRENT_TIME " +
			"CURRENT_TIMESTAMP LOCALTIMESTAMP") {
		sqlKeywords[w] = struct{}{}
	}
}

// sqlClauses are the keyword sequences that start a new display line when they
// appear at paren depth 0, longest match first so LEFT OUTER JOIN wins over
// its JOIN suffix. Bare ON is absent on purpose — join conditions stay inline;
// ON CONFLICT breaks.
var sqlClauses = [][]string{
	{"LEFT", "OUTER", "JOIN"}, {"RIGHT", "OUTER", "JOIN"}, {"FULL", "OUTER", "JOIN"},
	{"GROUP", "BY"}, {"ORDER", "BY"}, {"LEFT", "JOIN"}, {"RIGHT", "JOIN"},
	{"INNER", "JOIN"}, {"CROSS", "JOIN"}, {"FULL", "JOIN"}, {"ON", "CONFLICT"},
	{"FROM"}, {"WHERE"}, {"HAVING"}, {"LIMIT"}, {"OFFSET"}, {"RETURNING"},
	{"SET"}, {"VALUES"}, {"UNION"}, {"INTERSECT"}, {"EXCEPT"}, {"WINDOW"}, {"JOIN"},
}

// clauseStartLen reports how many tokens of a clause opener start at toks[i],
// or 0 when no top-level clause starts there.
func clauseStartLen(toks []sqlToken, i int) int {
	if toks[i].kind != tokKeyword || toks[i].depth != 0 {
		return 0
	}
	for _, cl := range sqlClauses {
		if i+len(cl) > len(toks) {
			continue
		}
		ok := true
		for j, w := range cl {
			t := toks[i+j]
			if t.kind != tokKeyword || t.depth != 0 || !strings.EqualFold(t.text, w) {
				ok = false
				break
			}
		}
		if ok {
			return len(cl)
		}
	}
	return 0
}

// clauseMode selects extra line-splitting inside a clause body, so the parts a
// reader scans for (updated columns, predicate conditions) each get a line.
type clauseMode int

const (
	clausePlain clauseMode = iota
	clauseSet              // SET: break after each top-level comma (one assignment per line)
	clauseCond             // WHERE/HAVING: break before each top-level AND/OR
)

// highlightSQL renders a SQL statement as ready-to-print styled lines for the
// detail panel: width is the usable text width (clamped to 8, like the old
// hard wrap). Lines never contain newlines, so scrollWindow's split stays valid.
func highlightSQL(query string, width int) []string {
	width = max(width, 8)
	toks := tokenizeSQL(query)
	if len(toks) == 0 {
		return nil
	}
	const bodyLead = "  "
	var out []string
	var cur []sqlToken
	curLead := ""
	mode := clausePlain
	flush := func() {
		if len(cur) > 0 {
			out = append(out, wrapSQLTokens(cur, width, curLead)...)
			cur = nil
		}
	}
	leading := true
	for i := 0; i < len(toks); {
		// Leading comments (ORM query tags) get their own line so the
		// statement itself starts flush-left below them.
		if leading && toks[i].kind == tokComment {
			out = append(out, wrapSQLTokens(toks[i:i+1], width, "")...)
			i++
			continue
		}
		leading = false
		// Consume a matched clause opener as a unit so its trailing JOIN/BY
		// can't re-match as a fresh (shorter) clause on the next iteration.
		if n := clauseStartLen(toks, i); n > 0 {
			flush()
			curLead = ""
			switch {
			case n == 1 && strings.EqualFold(toks[i].text, "SET"):
				// SET stands alone; every assignment below it gets its own line.
				out = append(out, wrapSQLTokens(toks[i:i+1], width, "")...)
				mode = clauseSet
				curLead = bodyLead
			case n == 1 && (strings.EqualFold(toks[i].text, "WHERE") ||
				strings.EqualFold(toks[i].text, "HAVING")):
				mode = clauseCond
				cur = append(cur, toks[i])
			default:
				mode = clausePlain
				cur = append(cur, toks[i:i+n]...)
			}
			i += n
			continue
		}
		t := toks[i]
		switch {
		case mode == clauseSet && t.kind == tokOp && t.text == "," && t.depth == 0:
			cur = append(cur, t)
			flush()
			curLead = bodyLead
		case mode == clauseCond && t.kind == tokKeyword && t.depth == 0 &&
			(strings.EqualFold(t.text, "AND") || strings.EqualFold(t.text, "OR")):
			flush()
			curLead = bodyLead
			cur = append(cur, t)
		default:
			cur = append(cur, t)
		}
		i++
	}
	flush()
	return out
}

// wrapSQLTokens greedily fills physical lines with one segment's tokens,
// measuring plain text and styling each token as it is written. The first line
// starts with lead (the segment's clause-body indent); continuation lines get
// two further spaces so wrapped bodies read as such.
func wrapSQLTokens(toks []sqlToken, width int, lead string) []string {
	indent := lead + "  "
	if width <= len(indent) {
		lead, indent = "", ""
	}
	var out []string
	var b strings.Builder
	b.WriteString(lead)
	lineW := len(lead)
	atStart := true

	newline := func() {
		out = append(out, b.String())
		b.Reset()
		b.WriteString(indent)
		lineW = len(indent)
		atStart = true
	}
	write := func(t sqlToken, text string) {
		b.WriteString(styleSQLText(t.kind, text))
		lineW += displayWidth(text)
		atStart = false
	}

	for _, t := range toks {
		tw := displayWidth(t.text)
		sep := 0
		if t.spaceBefore && !atStart {
			sep = 1
		}
		switch {
		case lineW+sep+tw <= width:
			if sep == 1 {
				b.WriteByte(' ')
				lineW++
			}
			write(t, t.text)
		case tw <= width-len(indent):
			newline()
			write(t, t.text)
		default:
			// Wider than any full line: hard-split into chunks, each styled on
			// its own (widths stay right because chunks are still plain text).
			// The separating space only fits if a chunk cell remains after it.
			if sep == 1 {
				if lineW+1 < width {
					b.WriteByte(' ')
					lineW++
				} else {
					newline()
				}
			}
			for r := []rune(t.text); len(r) > 0; {
				chunk, rest := takeRunes(r, width-lineW)
				if chunk == "" {
					if !atStart {
						newline()
						continue
					}
					// A single rune wider than the whole line: emit it anyway
					// so the loop always makes progress.
					chunk, rest = string(r[:1]), r[1:]
				}
				write(t, chunk)
				r = rest
				if len(r) > 0 {
					newline()
				}
			}
		}
	}
	if !atStart || len(out) == 0 {
		out = append(out, b.String())
	}
	return out
}

// takeRunes returns the longest prefix of r that fits in room display cells,
// possibly empty when even the first rune doesn't fit.
func takeRunes(r []rune, room int) (string, []rune) {
	i, w := 0, 0
	for i < len(r) {
		rw := displayWidth(string(r[i]))
		if w+rw > room {
			break
		}
		w += rw
		i++
	}
	return string(r[:i]), r[i:]
}

// styleSQLText styles one token (or a hard-split chunk of one). For quoted
// identifiers only the quote characters at the chunk's edges dim, so the name
// itself keeps the default foreground.
func styleSQLText(kind sqlTokenKind, text string) string {
	switch kind {
	case tokKeyword:
		return styleSQLKeyword.Render(text)
	case tokString, tokNumber, tokParam:
		return styleSQLLiteral.Render(text)
	case tokComment:
		return styleMuted.Render(text)
	case tokQuotedIdent:
		pre, post := "", ""
		if strings.HasPrefix(text, `"`) {
			pre = styleMuted.Render(`"`)
			text = text[1:]
		}
		if strings.HasSuffix(text, `"`) {
			post = styleMuted.Render(`"`)
			text = text[:len(text)-1]
		}
		return pre + text + post
	default:
		return text
	}
}

const sqlOpRunes = "+-*/<>=!~%^|:&#?@"

// tokenizeSQL scans the raw (unflattened) query so -- comments still end at
// their original newline instead of swallowing everything after them. Any
// malformed input (unterminated string/ident/comment/dollar-quote) consumes
// the rest as one token — text is never lost and the scan never panics.
func tokenizeSQL(q string) []sqlToken {
	r := []rune(q)
	var toks []sqlToken
	depth := 0
	space := false
	i := 0
	emit := func(kind sqlTokenKind, start, end int) {
		toks = append(toks, sqlToken{kind: kind, text: string(r[start:end]), spaceBefore: space, depth: depth})
		space = false
	}
	for i < len(r) {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			space = true
			i++
		case c == '\'':
			start := i
			i = scanQuoted(r, i, '\'')
			emit(tokString, start, i)
		case c == '"':
			start := i
			i = scanQuoted(r, i, '"')
			emit(tokQuotedIdent, start, i)
		case c == '$':
			start := i
			kind, end, ok := scanDollar(r, i)
			if !ok {
				kind, end = tokOp, i+1
			}
			i = end
			emit(kind, start, i)
		case c == '-' && i+1 < len(r) && r[i+1] == '-':
			start := i
			for i < len(r) && r[i] != '\n' {
				i++
			}
			emit(tokComment, start, i)
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			start := i
			if end := strings.Index(string(r[i+2:]), "*/"); end >= 0 {
				i += 2 + len([]rune(string(r[i+2:])[:end])) + 2
			} else {
				i = len(r)
			}
			emit(tokComment, start, i)
		case unicode.IsDigit(c) || (c == '.' && i+1 < len(r) && unicode.IsDigit(r[i+1])):
			start := i
			i = scanNumber(r, i)
			emit(tokNumber, start, i)
		case unicode.IsLetter(c) || c == '_':
			start := i
			for i < len(r) && isSQLWordCont(r[i]) {
				i++
			}
			kind := tokIdent
			if _, ok := sqlKeywords[strings.ToUpper(string(r[start:i]))]; ok {
				kind = tokKeyword
			}
			emit(kind, start, i)
		case c == '(':
			emit(tokOp, i, i+1)
			depth++
			i++
		case c == ')':
			// Floor at 0 so an unbalanced ')' can't push later clause keywords
			// to a negative depth that would never match the break check.
			if depth > 0 {
				depth--
			}
			emit(tokOp, i, i+1)
			i++
		case c == '[' || c == ']' || c == ',' || c == ';' || c == '.':
			emit(tokOp, i, i+1)
			i++
		case strings.ContainsRune(sqlOpRunes, c):
			start := i
			for i < len(r) && strings.ContainsRune(sqlOpRunes, r[i]) {
				// Stop a run before an embedded comment opener (a - -- x, a / /* x).
				if i > start && i+1 < len(r) &&
					((r[i] == '-' && r[i+1] == '-') || (r[i] == '/' && r[i+1] == '*')) {
					break
				}
				i++
			}
			emit(tokOp, start, i)
		default:
			emit(tokOp, i, i+1)
			i++
		}
	}
	return toks
}

func isSQLWordCont(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_'
}

// scanQuoted consumes a '…' or "…" token starting at the opening quote,
// treating a doubled quote as an escape. Unterminated → rest of input.
func scanQuoted(r []rune, i int, q rune) int {
	i++
	for i < len(r) {
		if r[i] == q {
			if i+1 < len(r) && r[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// scanDollar disambiguates the three '$' forms: $1 (parameter), $$…$$ /
// $tag$…$tag$ (dollar-quoted string, scanned to its matching close tag), and a
// lone '$' (not ours — ok=false).
func scanDollar(r []rune, i int) (sqlTokenKind, int, bool) {
	j := i + 1
	if j < len(r) && unicode.IsDigit(r[j]) {
		for j < len(r) && unicode.IsDigit(r[j]) {
			j++
		}
		return tokParam, j, true
	}
	k := j
	if k < len(r) && (unicode.IsLetter(r[k]) || r[k] == '_') {
		k++
		for k < len(r) && isSQLWordCont(r[k]) {
			k++
		}
	}
	if k >= len(r) || r[k] != '$' {
		return 0, 0, false
	}
	tag := string(r[i : k+1])
	rest := string(r[k+1:])
	if idx := strings.Index(rest, tag); idx >= 0 {
		return tokString, k + 1 + len([]rune(rest[:idx])) + len([]rune(tag)), true
	}
	return tokString, len(r), true
}

// scanNumber consumes digits with at most one '.' and an optional exponent.
func scanNumber(r []rune, i int) int {
	seenDot := false
	for i < len(r) {
		c := r[i]
		switch {
		case unicode.IsDigit(c):
			i++
		case c == '.' && !seenDot:
			seenDot = true
			i++
		case (c == 'e' || c == 'E') && i+1 < len(r) &&
			(unicode.IsDigit(r[i+1]) ||
				((r[i+1] == '+' || r[i+1] == '-') && i+2 < len(r) && unicode.IsDigit(r[i+2]))):
			i += 2
		default:
			return i
		}
	}
	return i
}
