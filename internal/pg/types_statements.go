package pg

// QueryStat is one row of pg_stat_statements. We read the 1.11 column set,
// which exists on PostgreSQL 17 (1.11) and is a subset of 18 (1.12), so the
// same query works on both. Counter fields (Calls, Rows, *Blks*, WAL*, the
// total/exec times) are cumulative since the last stats reset — the TUI takes
// a baseline snapshot on entry and shows the delta against it, which is how a
// time window is fabricated without storing history (see DiffStatements).
//
// Min/Max/Stddev exec time are kept for completeness but are NOT meaningful on
// a delta (you can't subtract two extrema), so the diff zeroes them and the
// detail view shows only window-decomposable metrics.
type QueryStat struct {
	QueryID int64
	UserID  uint32
	DBID    uint32
	Query   string

	Calls int64
	Rows  int64

	TotalExecTime  float64 // milliseconds
	MinExecTime    float64
	MaxExecTime    float64
	MeanExecTime   float64
	StddevExecTime float64

	Plans         int64
	TotalPlanTime float64 // milliseconds; 0 when track_planning is off

	SharedBlksHit     int64
	SharedBlksRead    int64
	SharedBlksDirtied int64
	SharedBlksWritten int64
	LocalBlksHit      int64
	LocalBlksRead     int64
	LocalBlksDirtied  int64
	LocalBlksWritten  int64
	TempBlksRead      int64
	TempBlksWritten   int64

	SharedBlkReadTime  float64 // milliseconds
	SharedBlkWriteTime float64
	LocalBlkReadTime   float64
	LocalBlkWriteTime  float64
	TempBlkReadTime    float64
	TempBlkWriteTime   float64

	WALRecords int64
	WALFPI     int64
	WALBytes   int64
}

// intCounters returns the addresses of every window-decomposable integer
// counter, in one canonical order shared by sub and clampNonNeg — a new counter
// field only needs to be added here to participate in both.
func (q *QueryStat) intCounters() []*int64 {
	return []*int64{
		&q.Calls, &q.Rows, &q.Plans,
		&q.SharedBlksHit, &q.SharedBlksRead, &q.SharedBlksDirtied, &q.SharedBlksWritten,
		&q.LocalBlksHit, &q.LocalBlksRead, &q.LocalBlksDirtied, &q.LocalBlksWritten,
		&q.TempBlksRead, &q.TempBlksWritten,
		&q.WALRecords, &q.WALFPI, &q.WALBytes,
	}
}

// floatCounters is intCounters' float sibling.
func (q *QueryStat) floatCounters() []*float64 {
	return []*float64{
		&q.TotalExecTime, &q.TotalPlanTime,
		&q.SharedBlkReadTime, &q.SharedBlkWriteTime,
		&q.LocalBlkReadTime, &q.LocalBlkWriteTime,
		&q.TempBlkReadTime, &q.TempBlkWriteTime,
	}
}

// sub returns the window delta of q relative to a baseline snapshot b. Counter
// fields are subtracted; identity (QueryID/Query/ids) comes from q (the newer
// snapshot, in case the query text was re-normalised). MeanExecTime is
// recomputed from the delta; the extrema are not subtractable so they're zero.
func (q QueryStat) sub(b QueryStat) QueryStat {
	d := q
	bi, bf := b.intCounters(), b.floatCounters()
	for i, p := range d.intCounters() {
		*p -= *bi[i]
	}
	for i, p := range d.floatCounters() {
		*p -= *bf[i]
	}
	d.MinExecTime, d.MaxExecTime, d.StddevExecTime = 0, 0, 0
	if d.Calls > 0 {
		d.MeanExecTime = d.TotalExecTime / float64(d.Calls)
	} else {
		d.MeanExecTime = 0
	}
	return d
}

// MeanTime is the average execution time per call in milliseconds.
func (q QueryStat) MeanTime() float64 {
	if q.Calls <= 0 {
		return 0
	}
	return q.TotalExecTime / float64(q.Calls)
}

// HitRatio is the shared-buffer cache hit ratio as a percentage. The bool is
// false when there was no block access at all (ratio undefined → render "—").
func (q QueryStat) HitRatio() (float64, bool) {
	total := q.SharedBlksHit + q.SharedBlksRead
	if total <= 0 {
		return 0, false
	}
	return float64(q.SharedBlksHit) / float64(total) * 100, true
}

// IOTime is the total block read+write time (shared+local+temp) in milliseconds.
func (q QueryStat) IOTime() float64 {
	return q.SharedBlkReadTime + q.SharedBlkWriteTime +
		q.LocalBlkReadTime + q.LocalBlkWriteTime +
		q.TempBlkReadTime + q.TempBlkWriteTime
}

// RowsPerCall is the average rows returned/affected per call.
func (q QueryStat) RowsPerCall() float64 {
	if q.Calls <= 0 {
		return 0
	}
	return float64(q.Rows) / float64(q.Calls)
}

// BlocksPerRow is the average shared blocks (cache hits + disk reads) touched
// per row returned/affected — a work-per-result-row signal where lower is
// better (a scan reading many pages to yield few rows scores high). The bool is
// false when the query returned no rows (ratio undefined → render "—").
func (q QueryStat) BlocksPerRow() (float64, bool) {
	if q.Rows <= 0 {
		return 0, false
	}
	return float64(q.SharedBlksHit+q.SharedBlksRead) / float64(q.Rows), true
}

// diffStatements computes the window deltas of a fresh snapshot against a
// baseline keyed by queryid. Queries with no activity in the window (≤0 calls)
// are dropped; queries new since the baseline keep their full counters. When
// clamp is set, negative deltas are zeroed (see clampNonNeg).
func diffStatements(baseline map[int64]QueryStat, current []QueryStat, clamp bool) []QueryStat {
	out := make([]QueryStat, 0, len(current))
	for _, c := range current {
		d := c
		if b, ok := baseline[c.QueryID]; ok {
			d = c.sub(b)
			if clamp {
				d = d.clampNonNeg()
			}
		}
		if d.Calls <= 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// DiffStatements computes the window deltas of a fresh snapshot against a
// baseline keyed by queryid. Queries with no activity in the window (≤0 calls)
// are dropped; queries new since the baseline keep their full counters.
func DiffStatements(baseline map[int64]QueryStat, current []QueryStat) []QueryStat {
	return diffStatements(baseline, current, false)
}

// clampNonNeg zeroes any negative counter field. A delta against a *disk*
// baseline can go negative when pg_stat_statements was reset, or the query was
// evicted and re-added with smaller counters, between the snapshot and now —
// in which case the difference is meaningless. We clamp so the table shows 0
// rather than nonsense, and the caller surfaces a warning separately.
func (q QueryStat) clampNonNeg() QueryStat {
	for _, p := range q.intCounters() {
		if *p < 0 {
			*p = 0
		}
	}
	for _, p := range q.floatCounters() {
		if *p < 0 {
			*p = 0
		}
	}
	if q.Calls > 0 {
		q.MeanExecTime = q.TotalExecTime / float64(q.Calls)
	} else {
		q.MeanExecTime = 0
	}
	return q
}

// DiffStatementsClamped is DiffStatements with every negative counter clamped to
// zero. Used when the baseline came from a disk snapshot, where a stats reset or
// eviction between capture and now can otherwise yield negative deltas. The
// in-memory live baseline can't go backwards, so it keeps the plain DiffStatements.
func DiffStatementsClamped(baseline map[int64]QueryStat, current []QueryStat) []QueryStat {
	return diffStatements(baseline, current, true)
}

// ParamType describes one positional parameter ($1, $2, …) of a normalized
// query, as inferred by PREPARE. Type is the regtype name, e.g. "integer".
type ParamType struct {
	Ordinal int
	Type    string
}

// ParamSource records where the value of a $n placeholder came from. Values are
// never guessed: a placeholder either has a captured value or none, and a
// sample call is only shown when every placeholder has one.
type ParamSource int

const (
	ParamMissing   ParamSource = iota // no captured value for this $n
	ParamQualstats                    // constant captured per-predicate by pg_qualstats
	ParamLog                          // bind value of a call the server logged (slow query log)
)

// SampleParam describes one $n placeholder of a sample call: its inferred type,
// the predicate column it compares against (if any), the captured literal and
// where it came from. Value is "" with Source ParamMissing when nothing captured
// it — the verbose table still lists the row so the gap is visible.
type SampleParam struct {
	Ordinal int
	Type    string // inferred regtype, e.g. "integer", "event_ids", "integer[]"
	Column  string // predicate column it compares against ("" if not column-tied)
	Value   string // the captured literal; "" when Source == ParamMissing
	Source  ParamSource
}

// QualSample is one real predicate constant captured by pg_qualstats for a
// given queryid (with pg_qualstats.track_constants on, each distinct value is
// a separate row). ConstValue is a ready-to-use, cast-carrying literal as
// stored by the extension (e.g. `'line 1'::text`), so it can be spliced into a
// query at Position. Relation/Column/Operator are resolved for display; they
// may be empty when the qual's left side isn't a plain column reference.
type QualSample struct {
	Relation    string
	Column      string
	Operator    string
	ConstValue  string
	Position    int   // constant_position: char offset in the original query text
	Occurrences int64 // occurences: how often this predicate fired
	// Truncated marks a ConstValue cut at pg_qualstats' 80-byte constant buffer:
	// it is a prefix of the real value, not a usable literal.
	Truncated bool
}
