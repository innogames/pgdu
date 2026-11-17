package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
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
	for w := range strings.FieldsSeq(
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
// hard wrap). A line holds a newline only where a single token does (a
// multi-line comment or string literal); sqlLayout splits those.
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

// sqlLayout keeps one statement's highlightSQL lines (each behind indent) for
// as long as the text and the width stay what they were. The detail body is
// re-rendered for every frame — every ↓ of a scroll — and laying out a
// statement with thousands of array elements each time is what made scrolling
// it crawl. Keyed by value, so whatever replaces the text (a new sample call,
// a reload) invalidates it without anyone having to remember to.
type sqlLayout struct {
	src, indent string
	width       int
	lines       []string
	valid       bool
}

func (l *sqlLayout) get(src string, width int, indent string) []string {
	if !l.valid || l.width != width || l.indent != indent || l.src != src {
		laid := highlightSQL(src, width)
		lines := make([]string, 0, len(laid))
		for _, ln := range laid {
			// A block comment or string literal that spans lines is a single
			// token and brings its newlines along; the body is paged by
			// physical line, so those count.
			if strings.IndexByte(ln, '\n') < 0 {
				lines = append(lines, indent+ln)
			} else {
				lines = append(lines, strings.Split(indent+ln, "\n")...)
			}
		}
		*l = sqlLayout{src: src, indent: indent, width: width, lines: lines, valid: true}
	}
	return l.lines
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
	write := func(t sqlToken, text string, w int) {
		b.WriteString(styleSQLText(t.kind, text))
		lineW += w
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
			write(t, t.text, tw)
		case tw <= width-len(indent):
			newline()
			write(t, t.text, tw)
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
				write(t, chunk, displayWidth(chunk))
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

// sgrPair is a style reduced to the two escape sequences it wraps text in. A
// statement is styled token by token, and an `= any(ARRAY[…])` with thousands
// of elements means thousands of Style.Render calls per layout — each one
// re-deriving the same sequence and running the margin/border/alignment
// passes a bare foreground never needs. That was over half the layout cost.
type sgrPair struct {
	style       lipgloss.Style
	open, close string
}

// newSGRPair asks lipgloss once how it would wrap a cell, so the fast path
// cannot drift from what Render emits. Only for styles that do nothing but
// set attributes (no padding, width or border): for those Render is exactly
// open + text + close on single-line text.
func newSGRPair(st lipgloss.Style) sgrPair {
	const probe = "x" // not a byte an SGR sequence can contain
	open, closing, ok := strings.Cut(st.Render(probe), probe)
	if !ok {
		open, closing = "", ""
	}
	return sgrPair{style: st, open: open, close: closing}
}

// render is Style.Render for the text a token can be. Control bytes (a tab or
// newline inside a string literal or comment) and the empty string keep the
// full path, which expands tabs and styles each line on its own.
func (p sgrPair) render(text string) string {
	if text == "" || hasControlByte(text) {
		return p.style.Render(text)
	}
	return p.open + text + p.close
}

func hasControlByte(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

var (
	sgrSQLKeyword = newSGRPair(styleSQLKeyword)
	sgrSQLLiteral = newSGRPair(styleSQLLiteral)
	sgrSQLMuted   = newSGRPair(styleMuted)
)

// styleSQLText styles one token (or a hard-split chunk of one). For quoted
// identifiers only the quote characters at the chunk's edges dim, so the name
// itself keeps the default foreground.
func styleSQLText(kind sqlTokenKind, text string) string {
	switch kind {
	case tokKeyword:
		return sgrSQLKeyword.render(text)
	case tokString, tokNumber, tokParam:
		return sgrSQLLiteral.render(text)
	case tokComment:
		return sgrSQLMuted.render(text)
	case tokQuotedIdent:
		pre, post := "", ""
		if strings.HasPrefix(text, `"`) {
			pre = sgrSQLMuted.render(`"`)
			text = text[1:]
		}
		if strings.HasSuffix(text, `"`) {
			post = sgrSQLMuted.render(`"`)
			text = text[:len(text)-1]
		}
		return pre + text + post
	default:
		return text
	}
}

const sqlOpRunes = "+-*/<>=!~%^|:&#?@"

// sqlMaxKeywordLen bounds the keyword lookup's stack buffer (the longest
// entry is CURRENT_TIMESTAMP).
const sqlMaxKeywordLen = 24

// sqlRuneAt decodes the rune at byte offset i, or (-1, 0) past the end, which
// no character class below matches. ASCII — nearly all of any statement —
// skips the decoder.
func sqlRuneAt(q string, i int) (rune, int) {
	if i >= len(q) {
		return -1, 0
	}
	if c := q[i]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	return utf8.DecodeRuneInString(q[i:])
}

func sqlDigitAt(q string, i int) bool {
	c, _ := sqlRuneAt(q, i)
	return unicode.IsDigit(c)
}

func isSQLOpByte(c byte) bool {
	return strings.IndexByte(sqlOpRunes, c) >= 0
}

// isSQLKeyword is the case-insensitive set lookup. An ASCII word is folded
// into a stack buffer so the map probe allocates nothing — a statement with
// thousands of identifiers used to pay a ToUpper string for each.
func isSQLKeyword(word string) bool {
	if len(word) > sqlMaxKeywordLen {
		return false
	}
	var buf [sqlMaxKeywordLen]byte
	for i := range len(word) {
		c := word[i]
		if c >= utf8.RuneSelf {
			// Unicode case folding can land on ASCII (ſ → S); rare enough to
			// leave to the allocating path.
			_, ok := sqlKeywords[strings.ToUpper(word)]
			return ok
		}
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		buf[i] = c
	}
	_, ok := sqlKeywords[string(buf[:len(word)])]
	return ok
}

// tokenizeSQL scans the raw (unflattened) query so -- comments still end at
// their original newline instead of swallowing everything after them. Any
// malformed input (unterminated string/ident/comment/dollar-quote) consumes
// the rest as one token — text is never lost and the scan never panics.
//
// It walks bytes and hands out sub-slices of q: converting a several-hundred-KB
// statement to []rune and back, one string per token, was a fifth of the
// layout cost.
func tokenizeSQL(q string) []sqlToken {
	if !utf8.ValidString(q) {
		// One U+FFFD per stray byte (a log in a non-UTF-8 encoding), so the
		// offsets below always sit on rune boundaries.
		q = string([]rune(q))
	}
	// A rough token-per-bytes guess; append still grows it for dense text.
	toks := make([]sqlToken, 0, len(q)/4+1)
	depth := 0
	space := false
	i := 0
	emit := func(kind sqlTokenKind, start, end int) {
		toks = append(toks, sqlToken{kind: kind, text: q[start:end], spaceBefore: space, depth: depth})
		space = false
	}
	for i < len(q) {
		c, sz := sqlRuneAt(q, i)
		switch {
		case unicode.IsSpace(c):
			space = true
			i += sz
		case c == '\'':
			start := i
			i = scanQuoted(q, i, '\'')
			emit(tokString, start, i)
		case c == '"':
			start := i
			i = scanQuoted(q, i, '"')
			emit(tokQuotedIdent, start, i)
		case c == '$':
			start := i
			kind, end, ok := scanDollar(q, i)
			if !ok {
				kind, end = tokOp, i+1
			}
			i = end
			emit(kind, start, i)
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			start := i
			if nl := strings.IndexByte(q[i:], '\n'); nl >= 0 {
				i += nl
			} else {
				i = len(q)
			}
			emit(tokComment, start, i)
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			start := i
			if end := strings.Index(q[i+2:], "*/"); end >= 0 {
				i += 2 + end + 2
			} else {
				i = len(q)
			}
			emit(tokComment, start, i)
		case unicode.IsDigit(c) || (c == '.' && sqlDigitAt(q, i+1)):
			start := i
			i = scanNumber(q, i)
			emit(tokNumber, start, i)
		case unicode.IsLetter(c) || c == '_':
			start := i
			i = scanSQLWord(q, i)
			kind := tokIdent
			if isSQLKeyword(q[start:i]) {
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
		case c < utf8.RuneSelf && isSQLOpByte(byte(c)):
			start := i
			for i < len(q) && isSQLOpByte(q[i]) {
				// Stop a run before an embedded comment opener (a - -- x, a / /* x).
				if i > start && i+1 < len(q) &&
					((q[i] == '-' && q[i+1] == '-') || (q[i] == '/' && q[i+1] == '*')) {
					break
				}
				i++
			}
			emit(tokOp, start, i)
		default:
			emit(tokOp, i, i+sz)
			i += sz
		}
	}
	return toks
}

func isSQLWordCont(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_'
}

// scanSQLWord returns the end of the identifier/keyword run starting at i.
func scanSQLWord(q string, i int) int {
	for i < len(q) {
		c, sz := sqlRuneAt(q, i)
		if !isSQLWordCont(c) {
			break
		}
		i += sz
	}
	return i
}

// scanQuoted consumes a '…' or "…" token starting at the opening quote,
// treating a doubled quote as an escape. Unterminated → rest of input. The
// quote is ASCII, so a byte scan cannot land inside a multi-byte rune.
func scanQuoted(q string, i int, quote byte) int {
	i++
	for i < len(q) {
		if q[i] == quote {
			if i+1 < len(q) && q[i+1] == quote {
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
func scanDollar(q string, i int) (sqlTokenKind, int, bool) {
	j := i + 1
	if sqlDigitAt(q, j) {
		for j < len(q) {
			c, sz := sqlRuneAt(q, j)
			if !unicode.IsDigit(c) {
				break
			}
			j += sz
		}
		return tokParam, j, true
	}
	k := j
	if c, sz := sqlRuneAt(q, k); unicode.IsLetter(c) || c == '_' {
		k = scanSQLWord(q, k+sz)
	}
	if k >= len(q) || q[k] != '$' {
		return 0, 0, false
	}
	tag := q[i : k+1]
	if before := strings.Index(q[k+1:], tag); before >= 0 {
		return tokString, k + 1 + before + len(tag), true
	}
	return tokString, len(q), true
}

// scanNumber consumes digits with at most one '.' and an optional exponent.
func scanNumber(q string, i int) int {
	seenDot := false
	for i < len(q) {
		c, sz := sqlRuneAt(q, i)
		switch {
		case unicode.IsDigit(c):
			i += sz
		case c == '.' && !seenDot:
			seenDot = true
			i++
		case c == 'e' || c == 'E':
			// The exponent marker plus the digit or sign after it, when a
			// digit follows; otherwise the e belongs to the next token.
			next, nsz := sqlRuneAt(q, i+1)
			switch {
			case unicode.IsDigit(next):
			case (next == '+' || next == '-') && sqlDigitAt(q, i+2):
			default:
				return i
			}
			i += 1 + nsz
		default:
			return i
		}
	}
	return i
}
