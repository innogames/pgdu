package pg

import (
	"math"
	"sort"
	"time"
)

// sizeUnknown marks a bucket whose value this build could not establish, as
// opposed to one it established to be zero. The distinction is the whole point
// of the type: a relation missing from a sample that had a size floor may
// simply have been under it, and plotting that as a drop to zero would invent
// a shrink that never happened.
const sizeUnknown = int64(-1)

// minSeriesBucket floors the bucket width. A history whose samples are seconds
// apart would otherwise produce sub-millisecond buckets, and the integer
// division that assigns a sample to one would land everything in bucket zero.
const minSeriesBucket = time.Second

// SizeSeries is one database's per-relation size history laid on a time axis
// of a fixed number of cells.
//
// The axis is time, not captures: one glyph per sample would draw an afternoon
// of manual captures as wide as a fortnight of daily ones. But samples accrue
// irregularly — the daily auto capture, plus whatever S and cron added, often
// in pairs minutes apart — and dividing the span into equal buckets then
// spends most of the axis on emptiness and folds the pairs away. Six captures
// over four days came out as three points on seventeen cells of gap.
//
// So the axis has two layouts, and Bucket says which:
//
//   - Proportional (Bucket == 0), when the samples fit: every sample gets its
//     own cell and the cells left over are shared among the gaps in
//     proportion to the time they span. Nothing is folded away, and the
//     spacing still reads as elapsed time.
//   - Uniform (Bucket > 0), when there are more samples than cells: cell i
//     covers [Start+i*Bucket, Start+(i+1)*Bucket) and keeps the newest sample
//     in it. Something must be dropped at that point, and the newest of a
//     bucket is the honest choice.
//
// Either way the last sample lands on the last cell, so a trace always ends
// at "now" on the right-hand edge.
//
// The layout is also what bounds the read: one file per occupied cell, so a
// burst of captures in one bucket costs one open, not five.
type SizeSeries struct {
	// Bucket is the time one cell spans under the uniform layout, and zero
	// under the proportional one, where cells do not span equal times.
	Bucket time.Duration
	Start  time.Time
	// At[i] is the capture time of the sample that filled cell i, zero when
	// none did. A zero entry is a gap in the history — pgdu was not run — and
	// must never read as "size zero".
	At []time.Time
	// Floored[i] records that cell i's sample was captured with a size floor,
	// so a relation absent from it is unknown rather than absent.
	Floored []bool
	// Samples is how many captures the axis spans and Plotted how many of
	// them actually got a cell. They differ only under the uniform layout,
	// where samples sharing a cell fold together; reporting both is what
	// keeps a header from promising six points over a trace showing three.
	// Skipped is how many were listed for this database but left out as
	// incompatible (a restored cluster or a recreated database).
	Samples int
	Plotted int
	Skipped int

	bytes map[string][]int64
}

// Cells is the width of the axis in buckets.
func (s *SizeSeries) Cells() int { return len(s.At) }

// Span is the first and last capture times on the axis. Both are zero when
// nothing landed on it.
func (s *SizeSeries) Span() (first, last time.Time) {
	for _, t := range s.At {
		if t.IsZero() {
			continue
		}
		if first.IsZero() {
			first = t
		}
		last = t
	}
	return first, last
}

// Series is one relation's trace, NaN where the value is unknown. The result
// is freshly allocated and safe for the caller to fold into.
func (s *SizeSeries) Series(key string) []float64 {
	raw, ok := s.bytes[key]
	if !ok {
		return nil
	}
	out := make([]float64, len(raw))
	for i, v := range raw {
		if v == sizeUnknown {
			out[i] = math.NaN()
			continue
		}
		out[i] = float64(v)
	}
	return out
}

// Keys is every relation the history knows about, in no particular order.
func (s *SizeSeries) Keys() []string {
	out := make([]string, 0, len(s.bytes))
	for k := range s.bytes {
		out = append(out, k)
	}
	return out
}

// LoadSizeSeries builds the size history of one database over cells buckets.
//
// end is the window's end sample, already in memory; endPath is where it came
// from, or "" when it is a live capture that was never written. Passing it in
// means the trace's right-hand end is the same figure the size column shows,
// and costs no read.
//
// It returns (nil, nil) when there is not enough history to plot — fewer than
// two distinct captures. That is the normal state on a cluster pgdu has only
// just been pointed at, not an error.
func LoadSizeSeries(dir, target, database string, cells int, end *SizeSample, endPath string) (*SizeSeries, error) {
	if cells < 2 {
		return nil, nil
	}
	metas, err := ListSizeSamples(dir)
	if err != nil {
		return nil, err
	}

	// Identity to judge the rest against: the window's end when there is one,
	// otherwise the newest listed sample. A sample from a restored cluster
	// describes different data under the same name, so it is skipped rather
	// than spliced into the trace — the same judgement the window picker makes
	// visible as a label.
	var ref SizeSampleMeta
	if end != nil {
		ref = end.meta(endPath, 0)
	}

	kept := make([]SizeSampleMeta, 0, len(metas))
	skipped := 0
	for _, m := range metas {
		if m.Target != target || m.Database != database {
			continue
		}
		if endPath != "" && m.Path == endPath {
			continue // already in hand as end
		}
		if ref.CapturedAt.IsZero() {
			ref = m
		}
		if ok, _ := ref.CompatibleWith(m); !ok {
			skipped++
			continue
		}
		kept = append(kept, m)
	}
	if end != nil {
		kept = append(kept, end.meta(endPath, 0))
	}
	if len(kept) < 2 {
		return nil, nil
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].CapturedAt.Before(kept[j].CapturedAt) })

	start := kept[0].CapturedAt
	s := &SizeSeries{
		Start:   start,
		At:      make([]time.Time, cells),
		Floored: make([]bool, cells),
		Samples: len(kept),
		Skipped: skipped,
		bytes:   make(map[string][]int64),
	}
	pick := make([]SizeSampleMeta, cells)
	place := func(cell int, m SizeSampleMeta) {
		s.At[cell] = m.CapturedAt
		s.Floored[cell] = m.MinBytes > 0
		pick[cell] = m
	}

	if len(kept) <= cells {
		// Proportional: every sample keeps its own cell, so nothing is folded
		// away, and the gaps between them carry the elapsed time.
		for i, cell := range proportionalSlots(kept, cells) {
			place(cell, kept[i])
		}
	} else {
		// Uniform: more samples than cells, so some must share one. Each cell
		// shows the newest sample in it, and every other file in that cell
		// goes unread.
		span := kept[len(kept)-1].CapturedAt.Sub(start)
		bucket := max(time.Duration(int64(span)/int64(cells)), minSeriesBucket)
		s.Bucket = bucket
		for _, m := range kept {
			b := max(int(m.CapturedAt.Sub(start)/bucket), 0)
			if b >= cells {
				b = cells - 1
			}
			if s.At[b].IsZero() || m.CapturedAt.After(s.At[b]) {
				place(b, m)
			}
		}
	}

	set := func(b int, key string, v int64) {
		row, ok := s.bytes[key]
		if !ok {
			row = make([]int64, cells)
			for i := range row {
				row[i] = sizeUnknown
			}
			s.bytes[key] = row
		}
		row[b] = v
	}

	for b, m := range pick {
		if s.At[b].IsZero() {
			continue // no sample landed in this bucket
		}
		// The end sample is already in memory, so the bucket it falls in is
		// filled from it rather than read back off disk — which is also what
		// makes the trace's right-hand end agree with the size column.
		if end != nil && m.Path == endPath && m.CapturedAt.Equal(end.CapturedAt) {
			for _, r := range end.Rows {
				set(b, r.Key(), r.TotalBytes)
			}
			continue
		}
		if _, err := readSizeRowsProjected(m.Path, func(r sizeRowBrief) {
			set(b, r.Key(), r.Bytes)
		}); err != nil {
			// One unreadable sample is a gap in the history, not a failed
			// history: blank the bucket and keep the rest of the axis.
			s.At[b] = time.Time{}
			s.Floored[b] = false
			for _, row := range s.bytes {
				row[b] = sizeUnknown
			}
		}
	}

	for _, a := range s.At {
		if !a.IsZero() {
			s.Plotted++
		}
	}

	// A relation absent from a sample is zero only when that sample itemised
	// everything. Under a size floor it is unknown, because the floor is
	// exactly the thing that would have hidden it.
	for _, row := range s.bytes {
		for b := range row {
			if row[b] != sizeUnknown || s.At[b].IsZero() || s.Floored[b] {
				continue
			}
			row[b] = 0
		}
	}
	return s, nil
}

// proportionalSlots places n samples on cells cells, one each, with the spare
// cells shared among the gaps in proportion to the time they span. It returns
// the cell index of each sample, in order.
//
// The first sample always lands on cell 0 and the last on cells-1, because the
// allocation hands out exactly the spare cells and no more: a trace therefore
// always starts at the left edge and ends at "now" on the right, whatever the
// spacing in between.
//
// Largest-remainder rather than plain truncation, so the cells lost to
// rounding go to the gaps with the strongest claim on them instead of
// accumulating at the end of the axis.
func proportionalSlots(kept []SizeSampleMeta, cells int) []int {
	n := len(kept)
	slots := make([]int, n)
	if n == 0 {
		return slots
	}
	spare := cells - n
	gaps := make([]float64, n-1)
	var total float64
	for i := range gaps {
		gaps[i] = max(kept[i+1].CapturedAt.Sub(kept[i].CapturedAt).Seconds(), 0)
		total += gaps[i]
	}
	// Samples all at one instant carry no spacing to honour, so the spare
	// cells are shared equally rather than piling up in one gap.
	if total <= 0 {
		for i := range gaps {
			gaps[i] = 1
		}
		total = float64(len(gaps))
	}

	extra := make([]int, len(gaps))
	type remainder struct {
		gap  int
		frac float64
	}
	rems := make([]remainder, 0, len(gaps))
	used := 0
	for i, g := range gaps {
		exact := float64(spare) * g / total
		extra[i] = int(exact)
		used += extra[i]
		rems = append(rems, remainder{i, exact - float64(extra[i])})
	}
	sort.Slice(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for i := 0; used < spare && len(rems) > 0; i++ {
		extra[rems[i%len(rems)].gap]++
		used++
	}

	for i := 1; i < n; i++ {
		slots[i] = slots[i-1] + 1 + extra[i-1]
	}
	return slots
}

// FoldSizeSeries projects the history onto the keys a row set is folded by, so
// a trace can sit beside a rolled-up row. It keys on ScopeKey, the same
// function RollUpGrowth folds with, so a trace can never belong to a different
// row than the numbers next to it.
//
// At leaf scope a member's unknown bucket stays unknown. At every folded scope
// an absent member contributes zero instead, which is the treatment
// sumGrowthRows and the Other row already give the relations a floor hid: the
// group's total is understated by at most the floor, and saying so for the
// whole group would blank a column that is mostly knowable. A bucket with no
// sample at all stays unknown at every scope — that is a fact about the
// history, not about any row in it.
func FoldSizeSeries(s *SizeSeries, rows []GrowthRow, by GrowthScope) map[string][]float64 {
	if s == nil {
		return nil
	}
	out := make(map[string][]float64, len(rows))
	for _, r := range rows {
		key := ScopeKey(r, by)
		vals := s.Series(r.Schema + "." + r.Name)
		if by == GrowthByTable {
			if vals != nil {
				out[key] = vals
			}
			continue
		}
		acc, ok := out[key]
		if !ok {
			acc = make([]float64, s.Cells())
			for i := range acc {
				if s.At[i].IsZero() {
					acc[i] = math.NaN()
				}
			}
			out[key] = acc
		}
		for i, v := range vals {
			if math.IsNaN(acc[i]) || math.IsNaN(v) {
				continue
			}
			acc[i] += v
		}
	}
	return out
}

// TrendPerDay is the long-run rate a trace implies, in units per day, and
// whether it is defined at all.
//
// It is the endpoint difference over the span between the two known ends —
// the same two-point arithmetic GrowthRow.finish applies to a window, just
// over a longer one. Deliberately not a fitted slope: everything else in the
// tool extrapolates from two measurements and says so, and a line fitted
// through a VACUUM FULL step would be harder to defend than a number the user
// can recompute from the first and last glyph.
func TrendPerDay(vals []float64, at []time.Time) (float64, bool) {
	first, last := -1, -1
	for i, v := range vals {
		if i >= len(at) || at[i].IsZero() || math.IsNaN(v) {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 || last <= first {
		return 0, false
	}
	secs := at[last].Sub(at[first]).Seconds()
	if secs <= 0 {
		return 0, false
	}
	return (vals[last] - vals[first]) * 86400 / secs, true
}
