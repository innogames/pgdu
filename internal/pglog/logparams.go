package pglog

import (
	"bytes"
	"sort"
	"strings"
	"time"
)

// paramsPrefix opens the DETAIL line PostgreSQL attaches to a logged
// statement when log_parameter_max_length (or …_on_error) is enabled.
var paramsPrefix = []byte("Parameters:")

// Params extracts an entry's parameter values: the bound values from its
// DETAIL line ("Parameters: $1 = '42', $2 = NULL") in $n order, or, when the
// statement was logged without one, the literals inlined in its SQL text (the
// values NormalizeSQL masks to build the group). ok is false when neither
// yields anything. Values keep their quotes so a NULL, an empty string and a
// numeric literal stay distinguishable; doubled quotes inside a value are
// left as logged since the text is only compared and displayed.
func Params(e *Entry) (vals []string, ok bool) {
	d := bytes.TrimSpace(e.Detail)
	if !bytes.HasPrefix(d, paramsPrefix) {
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
	d = d[len(paramsPrefix):]
	// Walk "$n = value" pairs. A value is either a quoted literal (with ''
	// escapes) or a bare token (NULL) running to the next ", $".
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
	return vals, true
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
// Entries with neither a Parameters DETAIL nor inlined literals share the
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
