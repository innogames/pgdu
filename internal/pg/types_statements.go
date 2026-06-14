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

// sub returns the window delta of q relative to a baseline snapshot b. Counter
// fields are subtracted; identity (QueryID/Query/ids) comes from q (the newer
// snapshot, in case the query text was re-normalised). MeanExecTime is
// recomputed from the delta; the extrema are not subtractable so they're zero.
func (q QueryStat) sub(b QueryStat) QueryStat {
	d := q
	d.Calls = q.Calls - b.Calls
	d.Rows = q.Rows - b.Rows
	d.TotalExecTime = q.TotalExecTime - b.TotalExecTime
	d.Plans = q.Plans - b.Plans
	d.TotalPlanTime = q.TotalPlanTime - b.TotalPlanTime
	d.SharedBlksHit = q.SharedBlksHit - b.SharedBlksHit
	d.SharedBlksRead = q.SharedBlksRead - b.SharedBlksRead
	d.SharedBlksDirtied = q.SharedBlksDirtied - b.SharedBlksDirtied
	d.SharedBlksWritten = q.SharedBlksWritten - b.SharedBlksWritten
	d.LocalBlksHit = q.LocalBlksHit - b.LocalBlksHit
	d.LocalBlksRead = q.LocalBlksRead - b.LocalBlksRead
	d.LocalBlksDirtied = q.LocalBlksDirtied - b.LocalBlksDirtied
	d.LocalBlksWritten = q.LocalBlksWritten - b.LocalBlksWritten
	d.TempBlksRead = q.TempBlksRead - b.TempBlksRead
	d.TempBlksWritten = q.TempBlksWritten - b.TempBlksWritten
	d.SharedBlkReadTime = q.SharedBlkReadTime - b.SharedBlkReadTime
	d.SharedBlkWriteTime = q.SharedBlkWriteTime - b.SharedBlkWriteTime
	d.LocalBlkReadTime = q.LocalBlkReadTime - b.LocalBlkReadTime
	d.LocalBlkWriteTime = q.LocalBlkWriteTime - b.LocalBlkWriteTime
	d.TempBlkReadTime = q.TempBlkReadTime - b.TempBlkReadTime
	d.TempBlkWriteTime = q.TempBlkWriteTime - b.TempBlkWriteTime
	d.WALRecords = q.WALRecords - b.WALRecords
	d.WALFPI = q.WALFPI - b.WALFPI
	d.WALBytes = q.WALBytes - b.WALBytes
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

// DiffStatements computes the window deltas of a fresh snapshot against a
// baseline keyed by queryid. Queries with no activity in the window (≤0 calls)
// are dropped; queries new since the baseline keep their full counters.
func DiffStatements(baseline map[int64]QueryStat, current []QueryStat) []QueryStat {
	out := make([]QueryStat, 0, len(current))
	for _, c := range current {
		d := c
		if b, ok := baseline[c.QueryID]; ok {
			d = c.sub(b)
		}
		if d.Calls <= 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// clampNonNeg zeroes any negative counter field. A delta against a *disk*
// baseline can go negative when pg_stat_statements was reset, or the query was
// evicted and re-added with smaller counters, between the snapshot and now —
// in which case the difference is meaningless. We clamp so the table shows 0
// rather than nonsense, and the caller surfaces a warning separately.
func (q QueryStat) clampNonNeg() QueryStat {
	nz := func(v *int64) {
		if *v < 0 {
			*v = 0
		}
	}
	nf := func(v *float64) {
		if *v < 0 {
			*v = 0
		}
	}
	nz(&q.Calls)
	nz(&q.Rows)
	nf(&q.TotalExecTime)
	nz(&q.Plans)
	nf(&q.TotalPlanTime)
	nz(&q.SharedBlksHit)
	nz(&q.SharedBlksRead)
	nz(&q.SharedBlksDirtied)
	nz(&q.SharedBlksWritten)
	nz(&q.LocalBlksHit)
	nz(&q.LocalBlksRead)
	nz(&q.LocalBlksDirtied)
	nz(&q.LocalBlksWritten)
	nz(&q.TempBlksRead)
	nz(&q.TempBlksWritten)
	nf(&q.SharedBlkReadTime)
	nf(&q.SharedBlkWriteTime)
	nf(&q.LocalBlkReadTime)
	nf(&q.LocalBlkWriteTime)
	nf(&q.TempBlkReadTime)
	nf(&q.TempBlkWriteTime)
	nz(&q.WALRecords)
	nz(&q.WALFPI)
	nz(&q.WALBytes)
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
	out := make([]QueryStat, 0, len(current))
	for _, c := range current {
		d := c
		if b, ok := baseline[c.QueryID]; ok {
			d = c.sub(b).clampNonNeg()
		}
		if d.Calls <= 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ParamType describes one positional parameter ($1, $2, …) of a normalized
// query, as inferred by PREPARE. Type is the regtype name, e.g. "integer".
type ParamType struct {
	Ordinal int
	Type    string
}

// ParamSource records where the literal substituted for a $n placeholder in a
// sample call came from — drives the verbose parameter table's source column.
type ParamSource int

const (
	ParamSynthesized     ParamSource = iota // generic typed literal (sampleLiteral)
	ParamLiveData                           // real value sampled from the live table
	ParamQualstats                          // real constant captured per-predicate by pg_qualstats
	ParamExtractField                       // EXTRACT($n FROM …) field slot ('epoch')
	ParamIntervalLiteral                    // INTERVAL $n value slot ('1 day')
)

// SampleParam describes how one $n placeholder was filled when building the
// sample call: its inferred type, the predicate column it compares against (if
// any), the literal substituted, and where that literal came from.
type SampleParam struct {
	Ordinal int
	Type    string // inferred regtype, e.g. "integer", "event_ids", "integer[]"
	Column  string // predicate column it compares against ("" if not column-tied)
	Value   string // the literal substituted into the sample call
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
}
