package pglog

// BoundCall is one logged execution of a statement together with the bind
// values it logged. Values are as logged, quotes included; nil when the text
// already carries every value (a simple-protocol client that inlined them), in
// which case the SQL is the call as it ran.
type BoundCall struct {
	Entry  *Entry
	Values []string
}

// Call returns the statement as it ran: Entry.SQL with Values spliced into its
// $n placeholders (SubstituteParams).
func (c BoundCall) Call() string {
	sql := string(c.Entry.SQL)
	if len(c.Values) == 0 {
		return sql
	}
	out, _ := SubstituteParams(sql, c.Values)
	return out
}

// LatestBoundCall finds the newest logged execution of a pg_stat_statements
// query whose text plus logged bind values make a complete, untruncated call —
// something that can be EXPLAINed or run as it stands, with no value guessed.
// Candidates are the slow-query and log_statement entries carrying statement
// text; one from another database is skipped when the prefix names it (the log
// is cluster-wide, and the same text in another database is a different
// query). An entry matches by query id when both it and queryID are non-zero
// (%Q, csvlog/jsonlog on PG14+), else by call shape (NormalizeCall). An id match
// wins over any text match, however old; the text fallback still applies to
// entries whose id differs, since two pg_stat_statements rows can share one
// text. ok is false when nothing qualifies.
func (r *Report) LatestBoundCall(queryID int64, query, db string) (BoundCall, bool) {
	if r == nil || len(r.Entries) == 0 {
		return BoundCall{}, false
	}
	// Every member of a group shares the representative's shape, so the text key
	// is computed once per group and entries compare by Group index.
	byText := map[int32]bool{}
	if want := NormalizeCall(query); want != "" {
		for gi, g := range r.Groups {
			if !callCategory(g.Category) || len(g.Samples) == 0 {
				continue
			}
			rep := &r.Entries[g.Samples[0]]
			if len(rep.SQL) > 0 && NormalizeCall(string(rep.SQL)) == want {
				byText[int32(gi)] = true
			}
		}
	}
	var textHit *BoundCall
	for i := len(r.Entries) - 1; i >= 0; i-- {
		e := &r.Entries[i]
		if !callCategory(e.Category) || len(e.SQL) == 0 {
			continue
		}
		if db != "" && len(e.DB) > 0 && string(e.DB) != db {
			continue
		}
		idMatch := queryID != 0 && e.QueryID == queryID
		if !idMatch && (textHit != nil || !byText[e.Group]) {
			continue
		}
		vals, ok := completeValues(e)
		if !ok {
			continue
		}
		bc := BoundCall{Entry: e, Values: vals}
		if idMatch {
			return bc, true
		}
		textHit = &bc
	}
	if textHit != nil {
		return *textHit, true
	}
	return BoundCall{}, false
}

// callCategory reports whether entries of this category carry executed
// statement text in SQL.
func callCategory(c Category) bool { return c == CatSlowQuery || c == CatStatement }

// completeValues returns the bind values that make e a complete call: every $n
// in its text has a logged, untruncated value. An entry without a parameter
// list is complete only when its text has no placeholder at all — the client
// inlined every value — and then needs nothing spliced (nil, true).
func completeValues(e *Entry) ([]string, bool) {
	sql := string(e.SQL)
	vals, ok := BoundParams(e)
	if !ok {
		return nil, UnfilledParams(sql, nil) == nil
	}
	if len(vals) == 0 || ParamsTruncated(vals) || UnfilledParams(sql, vals) != nil {
		return nil, false
	}
	return vals, true
}
