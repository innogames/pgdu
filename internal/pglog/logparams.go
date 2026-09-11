package pglog

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"time"
)

// paramsPrefix opens the DETAIL line PostgreSQL attaches to a logged statement
// when log_parameter_max_length is enabled.
var paramsPrefix = []byte("Parameters:")

// ctxParamsMark is how the same list reaches the log when the statement
// failed: log_parameter_max_length_on_error routes it through an error context
// callback, so it arrives on the CONTEXT line as
// `unnamed portal with parameters: $1 = …` (or `portal "p1" with parameters:`).
var ctxParamsMark = []byte("parameters: ")

// Params extracts an entry's parameter values: the bound values logged for it
// ("Parameters: $1 = '42', $2 = NULL") in $n order, or, when the
// statement was logged without one, the literals inlined in its SQL text (the
// values NormalizeSQL masks to build the group). ok is false when neither
// yields anything. Values keep their quotes so a NULL, an empty string and a
// numeric literal stay distinguishable; doubled quotes inside a value are
// left as logged since the text is only compared and displayed.
func Params(e *Entry) (vals []string, ok bool) {
	if vals, ok := BoundParams(e); ok {
		return vals, true
	}
	sql := e.SQL
	if len(sql) == 0 {
		sql = e.Statement
	}
	if len(sql) == 0 {
		return nil, false
	}
	lits := SQLLiterals(string(sql))
	return lits, len(lits) > 0
}

// BoundParams extracts only the values bound over the extended protocol, in
// ordinal order: the entry's "Parameters:" DETAIL line, or the list an error
// carries on its CONTEXT line instead. Unlike Params it does not fall back to
// literals inlined in the SQL, so a caller that substitutes into $n
// placeholders (SubstituteParams) cannot splice a statement's own constants
// back into it.
func BoundParams(e *Entry) (vals []string, ok bool) {
	if d := bytes.TrimSpace(e.Detail); bytes.HasPrefix(d, paramsPrefix) {
		return parseParamPairs(d[len(paramsPrefix):]), true
	}
	if list, ok := contextParams(e.Context); ok {
		return parseParamPairs(list), true
	}
	return nil, false
}

// contextParams finds the parameter list inside an error's CONTEXT line: the
// text after "parameters: ", required to start at a $n so an unrelated mention
// of the word cannot be mistaken for one.
func contextParams(ctx []byte) ([]byte, bool) {
	_, after, ok := bytes.Cut(ctx, ctxParamsMark)
	if !ok {
		return nil, false
	}
	rest := bytes.TrimLeft(after, " ")
	if len(rest) == 0 || rest[0] != '$' {
		return nil, false
	}
	return rest, true
}

// parseParamPairs walks the "$n = value" pairs of a logged parameter list,
// returning the values in the order they appear. A value is either a quoted
// literal (doubled quotes escaping) or a bare token (NULL) running to the next ", $".
func parseParamPairs(d []byte) (vals []string) {
	for {
		d = bytes.TrimLeft(d, " ,\t\r\n")
		if len(d) == 0 || d[0] != '$' {
			break
		}
		eq := bytes.IndexByte(d, '=')
		if eq < 0 {
			break
		}
		d = bytes.TrimLeft(d[eq+1:], " ")
		if len(d) == 0 {
			break
		}
		var val []byte
		if d[0] == '\'' {
			end := closingQuote(d)
			if end < 0 {
				vals = append(vals, string(d))
				break
			}
			val, d = d[:end+1], d[end+1:]
		} else {
			end := bytes.Index(d, []byte(", $"))
			if end < 0 {
				end = len(d)
			}
			val, d = bytes.TrimRight(d[:end], " \r\n"), d[end:]
		}
		vals = append(vals, string(val))
	}
	return vals
}

// closingQuote returns the index of the quote ending the literal that opens
// at b[0], skipping doubled quotes, or -1 when unterminated.
func closingQuote(b []byte) int {
	for i := 1; i < len(b); i++ {
		if b[i] != '\'' {
			continue
		}
		if i+1 < len(b) && b[i+1] == '\'' {
			i++
			continue
		}
		return i
	}
	return -1
}

// ParamKey is the grouping key of an entry's parameters: the whole tuple, or
// only $1 when firstOnly is set (a coarser view when later parameters vary per
// call — timestamps, offsets — and would otherwise make every row unique).
// Entries with neither a logged parameter list nor inlined literals share the
// NoParams key.
func ParamKey(e *Entry, firstOnly bool) string {
	vals, ok := Params(e)
	if !ok {
		return NoParams
	}
	if len(vals) == 0 {
		return "(empty)"
	}
	if firstOnly {
		return vals[0]
	}
	return strings.Join(vals, ", ")
}

// NoParams is the ParamKey of entries whose DETAIL carries no parameters.
const NoParams = "(no parameters)"

// ParamGroup aggregates one group's entries that share a parameter key.
type ParamGroup struct {
	Key   string
	Count int
	First time.Time
	Last  time.Time
	// Sample indexes the newest member in Report.Entries.
	Sample int
	// Duration figures (ms) over the members: statement duration for slow
	// queries, lock wait for lock lines. HasDur is false for categories with
	// neither, so the caller can drop those columns.
	HasDur bool
	SumMs  float64
	MaxMs  float64
	P95Ms  float64
	res    []float64
}

// AvgMs is the mean duration over the members.
func (p *ParamGroup) AvgMs() float64 {
	if p.Count == 0 {
		return 0
	}
	return p.SumMs / float64(p.Count)
}

// entryDuration is the figure ParamGroup aggregates for an entry.
func entryDuration(e *Entry) (float64, bool) {
	switch {
	case e.Category == CatSlowQuery:
		return e.DurationMs, true
	case e.LockWaitMs > 0:
		return e.LockWaitMs, true
	}
	return 0, false
}

// GroupParams splits group gi of r by parameter key over every member entry
// (Entry.Group, not the capped Samples). Rows come back count-descending, then
// newest first; the count of rows is bounded by the distinct keys, which for a
// timestamp-bearing parameter list can be every entry — callers offer the
// firstOnly variant for that case.
func GroupParams(r *Report, gi int, firstOnly bool) []ParamGroup {
	if r == nil || gi < 0 || gi >= len(r.Groups) {
		return nil
	}
	byKey := make(map[string]*ParamGroup)
	var order []*ParamGroup
	for i := range r.Entries {
		e := &r.Entries[i]
		if int(e.Group) != gi {
			continue
		}
		key := ParamKey(e, firstOnly)
		p := byKey[key]
		if p == nil {
			p = &ParamGroup{Key: key, First: e.Time, Last: e.Time, Sample: i}
			byKey[key] = p
			order = append(order, p)
		}
		p.Count++
		if !e.Time.IsZero() {
			if p.First.IsZero() || e.Time.Before(p.First) {
				p.First = e.Time
			}
			if !e.Time.Before(p.Last) {
				p.Last = e.Time
				p.Sample = i
			}
		}
		if ms, ok := entryDuration(e); ok {
			p.HasDur = true
			p.SumMs += ms
			if ms > p.MaxMs {
				p.MaxMs = ms
			}
			if len(p.res) < slowReservoirCap {
				p.res = append(p.res, ms)
			}
		}
	}
	out := make([]ParamGroup, len(order))
	for i, p := range order {
		p.P95Ms = percentile(p.res, 0.95)
		p.res = nil
		out[i] = *p
	}
	sortParamGroups(out)
	return out
}

func sortParamGroups(ps []ParamGroup) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Count != ps[j].Count {
			return ps[i].Count > ps[j].Count
		}
		return ps[i].Last.After(ps[j].Last)
	})
}

// FormatParamKey renders a key for a table cell: the tuple as logged, or
// "$1 = …" in first-only mode so the column reads unambiguously.
func FormatParamKey(key string, firstOnly bool) string {
	if key == NoParams || !firstOnly {
		return key
	}
	return "$1 = " + key
}

// SubstituteParams splices logged bind values into a statement's text: each $n
// placeholder becomes vals[n-1] exactly as the DETAIL line logged it (quotes
// included), so the result is the call as it actually ran. It returns the text
// and how many placeholders it filled — 0 means there was nothing to
// substitute and the caller should show nothing. The text is preserved
// byte-for-byte apart from the placeholders: a $n inside a string literal, a
// quoted identifier, a comment or a dollar-quoted body is left alone, as is an
// ordinal with no logged value (a statement logged with a shorter DETAIL than
// it has placeholders, or a truncated parameter list).
func SubstituteParams(sql string, vals []string) (string, int) {
	if len(vals) == 0 {
		return sql, 0
	}
	var b strings.Builder
	b.Grow(len(sql) + 8*len(vals))
	filled := 0
	for i := 0; i < len(sql); {
		switch ch := sql[i]; {
		case ch == '\'' || ch == '"':
			j := skipQuoted(sql, i)
			b.WriteString(sql[i:j])
			i = j
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = len(sql)
			} else {
				j += i
			}
			b.WriteString(sql[i:j])
			i = j
		case ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				j = len(sql)
			} else {
				j += i + 2 + 2
			}
			b.WriteString(sql[i:j])
			i = j
		case ch == '$' && i+1 < len(sql) && isDigit(sql[i+1]):
			j := i + 1
			for j < len(sql) && isDigit(sql[j]) {
				j++
			}
			ord, err := strconv.Atoi(sql[i+1 : j])
			if err != nil || ord < 1 || ord > len(vals) {
				b.WriteString(sql[i:j])
			} else {
				b.WriteString(vals[ord-1])
				filled++
			}
			i = j
		case ch == '$':
			j := dollarQuoteEnd(sql, i)
			if j < 0 {
				b.WriteByte(ch)
				i++
				continue
			}
			b.WriteString(sql[i:j])
			i = j
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String(), filled
}

// skipQuoted returns the index just past the string literal or quoted
// identifier opening at sql[i], treating a doubled quote as an escape and
// honouring the backslash escapes of an E-prefixed string. An unterminated literal
// runs to the end of the text.
func skipQuoted(sql string, i int) int {
	q := sql[i]
	esc := q == '\'' && i > 0 && (sql[i-1] == 'E' || sql[i-1] == 'e') && (i == 1 || !isIdentByte(sql[i-2]))
	for j := i + 1; j < len(sql); j++ {
		if esc && sql[j] == '\\' {
			j++
			continue
		}
		if sql[j] != q {
			continue
		}
		if j+1 < len(sql) && sql[j+1] == q {
			j++
			continue
		}
		return j + 1
	}
	return len(sql)
}

// dollarQuoteEnd returns the index just past the dollar-quoted string opening
// at sql[i] ($$…$$, $tag$…$tag$), or -1 when sql[i] does not open one. A tag
// cannot start with a digit, which is what keeps $1 a placeholder. An
// unterminated body runs to the end of the text.
func dollarQuoteEnd(sql string, i int) int {
	j := i + 1
	for j < len(sql) && isIdentByte(sql[j]) {
		if j == i+1 && isDigit(sql[j]) {
			return -1
		}
		j++
	}
	if j >= len(sql) || sql[j] != '$' {
		return -1
	}
	tag := sql[i : j+1]
	end := strings.Index(sql[j+1:], tag)
	if end < 0 {
		return len(sql)
	}
	return j + 1 + end + len(tag)
}

// ParamsTruncated reports whether any logged value was cut short by
// log_parameter_max_length: PostgreSQL then emits the opening quote, as much of
// the value as fits and a trailing "...", leaving the literal unterminated. A
// sample call built from such a list is not runnable as printed.
func ParamsTruncated(vals []string) bool {
	for _, v := range vals {
		if strings.HasPrefix(v, "'") && !strings.HasSuffix(v, "'") {
			return true
		}
	}
	return false
}
