package pg

import "strings"

// pgqsConstantSize is pg_qualstats' PGQS_CONSTANT_SIZE: the extension copies each
// captured constant's deparsed text into an 80-byte buffer, so anything longer
// — a long string, and above all the array literal the planner folds an
// `IN ($1,…,$n)` list into — comes back cut off after 79 characters, usually
// mid-element and without its closing quote and `::type` cast.
const pgqsConstantSize = 80

// qualConstTruncated reports whether a pg_qualstats constvalue was cut at
// PGQS_CONSTANT_SIZE. The cut leaves an unterminated string literal (or an
// unbalanced parenthesis), which balancedDelimiters spots; a value that happens
// to end exactly on its closing quote but lost its cast is only caught by the
// length, hence the second test.
func qualConstTruncated(v string) bool {
	if !balancedDelimiters(v) {
		return true
	}
	return len(v) >= pgqsConstantSize-1 && !strings.Contains(v, "::") && strings.HasPrefix(v, "'")
}

// qualArrayConst decodes an array constant as pg_qualstats deparses it —
// `'{a,b,"c d"}'::type[]` — into its elements and element type. A truncated
// literal (see qualConstTruncated) yields only the elements that were complete
// before the cut, with complete=false and elemType "" (the cast is gone with the
// tail). ok is false for anything that is not an array literal.
func qualArrayConst(v string) (elems []string, elemType string, complete, ok bool) {
	if !strings.HasPrefix(v, "'{") {
		return nil, "", false, false
	}
	body := v[2:]
	if before, after, ok := strings.Cut(body, "}'"); ok {
		// Whole literal present: `…}'::text[]` → elements + the element type.
		cast := strings.TrimPrefix(after, "::")
		if !strings.HasSuffix(cast, "[]") {
			return nil, "", false, false
		}
		return splitArrayElems(before, true), strings.TrimSuffix(cast, "[]"), true, true
	}
	return splitArrayElems(body, false), "", false, true
}

// splitArrayElems splits the inside of a Postgres array literal (`{…}` without
// the braces) into unescaped element texts. NULL elements come back as "NULL".
// When complete is false the input was cut at an arbitrary byte, so the last
// element — possibly only partly present — is dropped.
func splitArrayElems(body string, complete bool) []string {
	var (
		out   []string
		cur   strings.Builder
		inQ   bool
		quote bool // current element was quoted (so "NULL" is a string, not NULL)
	)
	flush := func() {
		s := cur.String()
		if quote && s == "NULL" {
			s = `"NULL"` // a quoted "NULL" is the string, not the null element
		}
		out = append(out, s)
		cur.Reset()
		quote = false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case inQ && c == '\\' && i+1 < len(body):
			i++
			cur.WriteByte(body[i])
		case inQ && c == '"':
			inQ = false
		case inQ:
			cur.WriteByte(c)
		case c == '"':
			inQ, quote = true, true
		case c == ',':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	// A truncated body was cut at an arbitrary byte, so whatever sits after the
	// last comma may be a partial element and is dropped.
	if complete && (cur.Len() > 0 || quote) {
		flush()
	}
	return out
}

// elemLiteral renders one array element as a standalone typed literal for a
// scalar placeholder. Elements arrive unescaped from splitArrayElems, so quotes
// are doubled here; a NULL element stays NULL.
func elemLiteral(elem, typ string) string {
	if elem == "NULL" {
		return "NULL::" + typ
	}
	return "'" + strings.ReplaceAll(strings.Trim(elem, `"`), "'", "''") + "'::" + typ
}

// arrayLiteral re-encodes elements as an array literal of typ (an array type
// such as `bigint[]`), quoting every element so commas, braces and quotes in
// the data survive the round trip.
func arrayLiteral(elems []string, typ string) string {
	var b strings.Builder
	b.WriteString("'{")
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(',')
		}
		if e == "NULL" {
			b.WriteString("NULL")
			continue
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`, `'`, `''`).Replace(strings.Trim(e, `"`)))
		b.WriteByte('"')
	}
	b.WriteString("}'::" + typ)
	return b.String()
}
