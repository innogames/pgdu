package pg

import (
	"errors"
	"fmt"
	"time"
)

// shortGrowthWindow is where a pair stops being long enough for its per-day
// rates to mean much: below a minute the signal is autovacuum jitter and a
// single checkpoint, and BytesPerDay multiplies whatever it sees by 1440.
//
// It flags the window rather than refusing it. The measured byte deltas are
// real at any spacing — "did this table just grow by 2 GB" is a fair question
// to ask of two captures a few seconds apart — and only the extrapolated rates
// are suspect, which is what the flag says.
const shortGrowthWindow = 60 * time.Second

var (
	// ErrSampleMismatch refuses a pair that does not describe the same
	// database on the same server. A recreated database or a restored cluster
	// is NOT this error — see GrowthIdentity.
	ErrSampleMismatch = errors.New("size samples describe different databases")
)

// GrowthTag is what happened to a relation across the window. It is always a
// label on a real delta, never a silent correction to one.
type GrowthTag uint8

const (
	GrowthSteady    GrowthTag = iota // present in both samples, same relfilenode
	GrowthNew                        // absent from the baseline
	GrowthGone                       // absent from the end sample
	GrowthRewritten                  // relfilenode changed: VACUUM FULL / CLUSTER / REINDEX / a rewriting ALTER
)

func (t GrowthTag) String() string {
	switch t {
	case GrowthNew:
		return "new"
	case GrowthGone:
		return "gone"
	case GrowthRewritten:
		return "rewritten"
	}
	return ""
}

// GrowthIdentity records that the two samples name the same database but may
// not describe the same data. Both values still produce rows: a restore is a
// real event the user needs explained, not hidden behind an error.
type GrowthIdentity uint8

const (
	IdentityOK             GrowthIdentity = iota
	IdentityDBRecreated                   // same name, different pg_database.oid
	IdentityClusterChanged                // both system identifiers known and different
)

func (i GrowthIdentity) String() string {
	switch i {
	case IdentityDBRecreated:
		return "db recreated"
	case IdentityClusterChanged:
		return "restored from another cluster"
	}
	return ""
}

// GrowthScope is the level a row set is folded to. The leaf scope is the
// relation rows as captured.
type GrowthScope uint8

const (
	GrowthByTable GrowthScope = iota
	GrowthBySchema
	GrowthByDatabase
	GrowthByPartition
)

// GrowthWindow is everything true about the pair a row set came from, as
// opposed to any one row in it. The TUI banners these.
type GrowthWindow struct {
	BaseAt, EndAt time.Time
	Duration      time.Duration // always >= 0; see ClockSkew
	BaseRows      int           // RowCount of each sample, for the header
	EndRows       int
	// ClockSkew means the arguments' capture order disagreed and DiffSizes
	// ordered them by time — a sample copied from a host with a skewed clock.
	ClockSkew bool
	// ShortWindow means the pair is under shortGrowthWindow apart, so the
	// per-day rates extrapolate from very little. The byte deltas themselves
	// are as measured as any other window's.
	ShortWindow bool
	// StatsReset means the two samples straddle a statistics reset, so the row
	// counters cannot be diffed. Byte deltas are unaffected: sizes are
	// measured, rows are counted.
	StatsReset bool
	// FloorChanged means the two samples used different size floors, so a
	// new/gone row may be an artefact of the floor moving.
	FloorChanged bool
	Identity     GrowthIdentity
}

// GrowthRow is one relation across the window. Every byte figure is real disk;
// nothing here is estimated.
type GrowthRow struct {
	Database string
	Schema   string
	Name     string

	OID       uint32 // the end sample's, or the baseline's when Gone
	RelKind   string
	ParentOID uint32
	PartBound string

	Tag GrowthTag
	// RootKey is the ScopeKey of the root of this row's partition/inheritance
	// tree, set by ResolvePartitionRoots over a whole row set. It is what lets
	// the partition scope fold and narrow by the same key; a row outside any
	// tree is its own root.
	RootKey string
	// FloorCapped marks a new/gone row whose absent side was captured with a
	// size floor: the relation may simply have been under it, so the delta is
	// an upper bound overstated by at most that floor.
	FloorCapped bool
	// Parts is how many relations a roll-up row folded in; 0 on a leaf row.
	Parts int

	BaseBytes, EndBytes, DeltaBytes int64
	BaseHeap, EndHeap, DeltaHeap    int64
	BaseIdx, EndIdx, DeltaIdx       int64
	BaseToast, EndToast, DeltaToast int64

	BaseRows, EndRows, DeltaRows int64
	// RowsKnown is false across a statistics reset, where the counters lie.
	RowsKnown bool

	BytesPerDay float64
	RowsPerDay  float64

	pctChange float64
	pctKnown  bool
}

// DeltaPct is the size change as a percentage of the baseline, and whether it
// is defined at all. It is undefined against a zero baseline — a brand new
// relation has grown by no meaningful ratio — so callers render it as "new"
// and leave the cell without a number, which sinks it under the generic
// table's unknown-sorts-last rule instead of a +Inf that would sort first.
func (r GrowthRow) DeltaPct() (float64, bool) { return r.pctChange, r.pctKnown }

// Qualified is the relation's display name: schema-qualified unless it is in
// public, matching how the disk browser names relations.
func (r GrowthRow) Qualified() string {
	if r.Schema == "" || r.Schema == "public" {
		return r.Name
	}
	return r.Schema + "." + r.Name
}

// GrowthResult is one diffed window: the facts about the pair, the per-relation
// rows, the per-database totals and the folded tail.
type GrowthResult struct {
	Window    GrowthWindow
	Rows      []GrowthRow // per-relation, as captured (no roll-up)
	Databases []GrowthRow // from the samples' Databases arrays; Schema/Name empty
	// Other is the relations both samples' size floors folded away, as one
	// synthetic row, so a total still reconciles. Zero when neither sample had
	// a floor.
	Other GrowthRow
}

// ScopeKey is the key RollUpGrowth folds on at the given scope. It is exported
// because the TUI needs the identical key to narrow a leaf list to one roll-up
// row; a second implementation there would drift on the first partition-tree
// edge case.
func ScopeKey(r GrowthRow, by GrowthScope) string {
	switch by {
	case GrowthBySchema:
		return r.Database + "." + r.Schema
	case GrowthByDatabase:
		return r.Database
	case GrowthByPartition:
		// Set by ResolvePartitionRoots, which alone sees the whole tree; a row
		// outside any tree is its own root.
		if r.RootKey != "" {
			return r.RootKey
		}
		return r.Database + "." + r.Schema + "." + r.Name
	}
	return r.Database + "." + r.Schema + "." + r.Name
}

// DiffSizes matches two samples on the logical identity (database, schema,
// relname) and returns the per-relation deltas plus the facts about the window.
//
// Logical identity beats OID, which survives pg_upgrade but not a dump/restore,
// and beats name-only, which could not notice a rewrite. The consequences
// surface as tags, never as silent corrections: a rename reads as one gone plus
// one new, which is documented rather than fixed.
//
// Samples naming different databases or servers are refused outright; a
// recreated database or a restored cluster is reported through Window.Identity
// instead, because the numbers are still real and the user needs to know why.
func DiffSizes(base, end *SizeSample) (*GrowthResult, error) {
	if base == nil || end == nil {
		return nil, errors.New("diff sizes: need two samples")
	}
	// Order by time first, so every calculation below sees a non-negative
	// window however the caller passed them.
	skew := false
	if end.CapturedAt.Before(base.CapturedAt) {
		base, end = end, base
		skew = true
	}

	if base.Target != end.Target || base.Database != end.Database {
		return nil, fmt.Errorf("%w: %s/%s and %s/%s",
			ErrSampleMismatch, base.Target, base.Database, end.Target, end.Database)
	}

	dur := end.CapturedAt.Sub(base.CapturedAt)

	w := GrowthWindow{
		BaseAt: base.CapturedAt, EndAt: end.CapturedAt, Duration: dur,
		BaseRows: base.RowCount, EndRows: end.RowCount,
		ClockSkew:    skew,
		ShortWindow:  dur < shortGrowthWindow,
		FloorChanged: base.MinBytes != end.MinBytes,
	}
	// pg_stat_database.stats_reset is NULL until a database's statistics are
	// reset for the first time, so two zeros are not two unknowns: they are
	// both samples reporting that no reset had ever happened by the time they
	// were taken, which settles that none happened between them. Reading that
	// as ignorance — as this once did — blanked the rows and rows/day columns
	// on every cluster whose statistics have simply never been reset, which is
	// most of them, and left no way to tell that from a real reset.
	//
	// One zero against one timestamp stays flagged, and is the case that
	// matters: a reset inside the window is exactly what turns the NULL side
	// into a time. A failed meta read also lands on zero and so reads as
	// never-reset, the same permissive way a zero DatabaseOID reads as
	// compatible — and it costs only counted rows, never the measured bytes.
	w.StatsReset = !base.StatsReset.Equal(end.StatsReset)
	// Checked in this order: the cluster changing is the stronger statement.
	switch {
	case base.SystemID != 0 && end.SystemID != 0 && base.SystemID != end.SystemID:
		w.Identity = IdentityClusterChanged
	case base.DatabaseOID != 0 && end.DatabaseOID != 0 && base.DatabaseOID != end.DatabaseOID:
		w.Identity = IdentityDBRecreated
	}

	res := &GrowthResult{Window: w}
	rowsKnown := !w.StatsReset

	baseByKey := make(map[string]SizeRow, len(base.Rows))
	for _, r := range base.Rows {
		baseByKey[r.Key()] = r
	}

	res.Rows = make([]GrowthRow, 0, len(end.Rows))
	for _, e := range end.Rows {
		k := e.Key()
		b, ok := baseByKey[k]
		if !ok {
			g := newGrowthRow(end.Database, e, SizeRow{}, GrowthNew, rowsKnown, dur)
			// The relation may simply have been under the baseline's floor.
			g.FloorCapped = base.MinBytes > 0
			res.Rows = append(res.Rows, g)
			continue
		}
		delete(baseByKey, k)
		tag := GrowthSteady
		// A partitioned parent has relfilenode 0, so the zero check is what
		// keeps it from reading as rewritten on every window.
		if b.RelFilenode != 0 && e.RelFilenode != 0 && b.RelFilenode != e.RelFilenode {
			tag = GrowthRewritten
		}
		res.Rows = append(res.Rows, newGrowthRow(end.Database, e, b, tag, rowsKnown, dur))
	}
	// Whatever is left in the baseline map is gone.
	for _, b := range baseByKey {
		g := newGrowthRow(end.Database, SizeRow{Schema: b.Schema, Name: b.Name, OID: b.OID,
			RelKind: b.RelKind, ParentOID: b.ParentOID, PartBound: b.PartBound}, b,
			GrowthGone, rowsKnown, dur)
		g.FloorCapped = end.MinBytes > 0
		res.Rows = append(res.Rows, g)
	}

	ResolvePartitionRoots(res.Rows)
	res.Databases = diffDatabases(base, end, rowsKnown, dur)
	res.Other = diffOther(base, end, dur)
	return res, nil
}

// newGrowthRow builds one row from its two sides. A Gone row passes a zeroed
// end side, a New row a zeroed base side; the arithmetic is the same either
// way, which is what makes a new relation's whole size count as growth and a
// dropped one's count as a loss.
func newGrowthRow(db string, e, b SizeRow, tag GrowthTag, rowsKnown bool, dur time.Duration) GrowthRow {
	var endSide SizeRow
	if tag != GrowthGone {
		endSide = e
	}
	g := GrowthRow{
		Database: db, Schema: e.Schema, Name: e.Name,
		OID: e.OID, RelKind: e.RelKind, ParentOID: e.ParentOID, PartBound: e.PartBound,
		Tag:       tag,
		RowsKnown: rowsKnown,

		BaseBytes: b.TotalBytes, EndBytes: endSide.TotalBytes,
		BaseHeap: b.HeapBytes, EndHeap: endSide.HeapBytes,
		BaseIdx: b.IndexBytes, EndIdx: endSide.IndexBytes,
		BaseToast: b.ToastBytes, EndToast: endSide.ToastBytes,
		BaseRows: b.NLive, EndRows: endSide.NLive,
	}
	if tag == GrowthGone {
		// Keep the shape it had, so a gone row still says what it was.
		g.OID, g.RelKind, g.ParentOID, g.PartBound = b.OID, b.RelKind, b.ParentOID, b.PartBound
	}
	g.finish(dur)
	return g
}

// finish derives every computed field from the four base/end pairs. Roll-ups
// call it again after summing, so a folded row's rates and ratio are pooled
// from the totals rather than averaged from its members.
func (r *GrowthRow) finish(dur time.Duration) {
	r.DeltaBytes = r.EndBytes - r.BaseBytes
	r.DeltaHeap = r.EndHeap - r.BaseHeap
	r.DeltaIdx = r.EndIdx - r.BaseIdx
	r.DeltaToast = r.EndToast - r.BaseToast
	r.DeltaRows = r.EndRows - r.BaseRows

	if secs := dur.Seconds(); secs > 0 {
		r.BytesPerDay = float64(r.DeltaBytes) * 86400 / secs
		r.RowsPerDay = float64(r.DeltaRows) * 86400 / secs
	}
	if r.BaseBytes > 0 {
		r.pctChange = float64(r.DeltaBytes) / float64(r.BaseBytes) * 100
		r.pctKnown = true
	} else {
		r.pctChange, r.pctKnown = 0, false
	}
}

// diffDatabases matches the per-database totals the samples carry, so the
// database scope works from a single database's sample.
func diffDatabases(base, end *SizeSample, rowsKnown bool, dur time.Duration) []GrowthRow {
	byName := make(map[string]int64, len(base.Databases))
	for _, d := range base.Databases {
		byName[d.Name] = d.SizeBytes
	}
	out := make([]GrowthRow, 0, len(end.Databases))
	for _, d := range end.Databases {
		b, ok := byName[d.Name]
		tag := GrowthSteady
		if !ok {
			tag = GrowthNew
		}
		delete(byName, d.Name)
		g := GrowthRow{Database: d.Name, Tag: tag, RowsKnown: rowsKnown,
			BaseBytes: b, EndBytes: d.SizeBytes}
		g.finish(dur)
		out = append(out, g)
	}
	for name, b := range byName {
		g := GrowthRow{Database: name, Tag: GrowthGone, RowsKnown: rowsKnown, BaseBytes: b}
		g.finish(dur)
		out = append(out, g)
	}
	return out
}

// diffOther turns the two samples' folded tails into one row, so a total still
// reconciles when either sample was taken with a size floor.
func diffOther(base, end *SizeSample, dur time.Duration) GrowthRow {
	if base.OtherCount == 0 && end.OtherCount == 0 {
		return GrowthRow{}
	}
	g := GrowthRow{
		Database:  end.Database,
		Name:      fmt.Sprintf("(%d relations under the size floor)", max(base.OtherCount, end.OtherCount)),
		Parts:     max(base.OtherCount, end.OtherCount),
		BaseBytes: base.OtherBytes, EndBytes: end.OtherBytes,
	}
	g.finish(dur)
	return g
}

// RollUpGrowth folds rows to the given scope, returning the same row type so
// every scope shares one column registry and one picker. The leaf scope is
// returned unchanged.
//
// The partition fold walks ParentOID to the root client-side rather than using
// a server-side recursive CTE: the tree differs between the two samples, since
// partitions get attached, detached and dropped, so the fold is a property of
// the matched pair and only the client holds both files.
func RollUpGrowth(rows []GrowthRow, by GrowthScope) []GrowthRow {
	if by == GrowthByTable {
		return rows
	}
	if by == GrowthByPartition {
		return rollUpPartitions(rows)
	}
	return foldBy(rows, by, func(r GrowthRow) GrowthRow {
		out := GrowthRow{Database: r.Database}
		if by == GrowthBySchema {
			out.Schema, out.Name = r.Schema, r.Schema
		} else {
			out.Name = r.Database
		}
		return out
	})
}

// foldBy sums rows sharing a key into one row seeded by seed, preserving
// first-seen order (applySort orders the items afterwards anyway).
//
// The seed only has to carry the group's identity: foldBy itself establishes
// the identity element of every field accumulate narrows. RowsKnown is the one
// that matters, because it folds by AND and accumulate can only ever clear it
// — a seed that forgot to set it true (as the schema and database seeds did)
// produced roll-ups whose row counts read "unknown" however trustworthy every
// member was, which is why the rule lives here and not in each caller.
func foldBy(rows []GrowthRow, by GrowthScope, seed func(GrowthRow) GrowthRow) []GrowthRow {
	idx := make(map[string]int, len(rows))
	out := make([]GrowthRow, 0, len(rows))
	for _, r := range rows {
		k := ScopeKey(r, by)
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			s := seed(r)
			s.RowsKnown = true
			out = append(out, s)
		}
		accumulate(&out[i], r)
	}
	return out
}

// accumulate adds one member's totals into a roll-up row.
func accumulate(dst *GrowthRow, src GrowthRow) {
	dst.BaseBytes += src.BaseBytes
	dst.EndBytes += src.EndBytes
	dst.BaseHeap += src.BaseHeap
	dst.EndHeap += src.EndHeap
	dst.BaseIdx += src.BaseIdx
	dst.EndIdx += src.EndIdx
	dst.BaseToast += src.BaseToast
	dst.EndToast += src.EndToast
	dst.BaseRows += src.BaseRows
	dst.EndRows += src.EndRows
	dst.Parts++
	// A roll-up is only as trustworthy as its least trustworthy member.
	if !src.RowsKnown {
		dst.RowsKnown = false
	}
}

// partitionRoots returns, for every row, the index of the root of its
// partition/inheritance tree.
//
// A row whose parent is not in the set is its own root — honest rather than
// invented, since the parent may have been under the size floor or in an
// excluded schema. The visited set bounds the walk: a cycle in pg_inherits
// would otherwise hang the UI.
func partitionRoots(rows []GrowthRow) []int {
	byOID := make(map[uint32]int, len(rows))
	for i, r := range rows {
		if r.OID != 0 {
			byOID[r.OID] = i
		}
	}
	roots := make([]int, len(rows))
	for i := range rows {
		j := i
		seen := map[uint32]bool{}
		for {
			parent := rows[j].ParentOID
			if parent == 0 || seen[parent] {
				break
			}
			seen[parent] = true
			p, ok := byOID[parent]
			if !ok {
				break
			}
			j = p
		}
		roots[i] = j
	}
	return roots
}

// ResolvePartitionRoots stamps every row with the ScopeKey of its tree root, in
// place. DiffSizes calls it, so a row set always arrives able to fold and to
// narrow by the same key.
func ResolvePartitionRoots(rows []GrowthRow) {
	roots := partitionRoots(rows)
	for i := range rows {
		r := rows[roots[i]]
		rows[i].RootKey = r.Database + "." + r.Schema + "." + r.Name
	}
}

// rollUpPartitions folds every partition into the root of its tree, seeding
// each group from the root row so the fold keeps the root's identity and tag.
// A tree containing a new or gone child keeps the root's own tag and reports
// the member count; a composite tag would claim more than it knows.
func rollUpPartitions(rows []GrowthRow) []GrowthRow {
	roots := partitionRoots(rows)
	idx := make(map[int]int, len(rows))
	out := make([]GrowthRow, 0, len(rows))
	for i, r := range rows {
		root := roots[i]
		j, ok := idx[root]
		if !ok {
			j = len(out)
			idx[root] = j
			seed := rows[root]
			seed.BaseBytes, seed.EndBytes = 0, 0
			seed.BaseHeap, seed.EndHeap = 0, 0
			seed.BaseIdx, seed.EndIdx = 0, 0
			seed.BaseToast, seed.EndToast = 0, 0
			seed.BaseRows, seed.EndRows = 0, 0
			seed.Parts = 0
			out = append(out, seed)
		}
		accumulate(&out[j], r)
	}
	return out
}

// FinishRollUp re-derives the computed fields of folded rows over the window,
// so a roll-up's rate and ratio are pooled from its totals rather than averaged
// from its members. Callers fold, then call this once.
func FinishRollUp(rows []GrowthRow, dur time.Duration) []GrowthRow {
	for i := range rows {
		rows[i].finish(dur)
	}
	return rows
}
