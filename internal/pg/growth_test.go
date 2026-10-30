package pg

import (
	"errors"
	"testing"
	"time"
)

var growthT0 = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// pair builds two samples a day apart sharing one identity, so a test only has
// to state the rows that differ.
func pair(baseRows, endRows []SizeRow) (*SizeSample, *SizeSample) {
	mk := func(at time.Time, rows []SizeRow) *SizeSample {
		s := &SizeSample{
			Kind: sizeKind, Version: sizeVersion,
			Target: "db:5432", Database: "app",
			DatabaseOID: 16384, SystemID: 42,
			CapturedAt: at, StatsReset: growthT0.Add(-100 * time.Hour),
			Rows: rows,
		}
		s.RowCount = len(rows)
		return s
	}
	return mk(growthT0, baseRows), mk(growthT0.Add(24*time.Hour), endRows)
}

func rowByName(t *testing.T, rows []GrowthRow, name string) GrowthRow {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row named %q in %d rows", name, len(rows))
	return GrowthRow{}
}

func TestDiffSizesBasic(t *testing.T) {
	base, end := pair(
		[]SizeRow{{OID: 1, Schema: "public", Name: "orders", RelKind: "r", RelFilenode: 1,
			HeapBytes: 800, IndexBytes: 150, ToastBytes: 50, TotalBytes: 1000, NLive: 100}},
		[]SizeRow{{OID: 1, Schema: "public", Name: "orders", RelKind: "r", RelFilenode: 1,
			HeapBytes: 1200, IndexBytes: 250, ToastBytes: 50, TotalBytes: 1500, NLive: 150}},
	)
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if res.Window.Duration != 24*time.Hour {
		t.Errorf("Duration = %v, want 24h", res.Window.Duration)
	}
	r := rowByName(t, res.Rows, "orders")
	if r.Tag != GrowthSteady {
		t.Errorf("Tag = %v, want steady", r.Tag)
	}
	if r.DeltaBytes != 500 {
		t.Errorf("DeltaBytes = %d, want 500", r.DeltaBytes)
	}
	if r.DeltaHeap != 400 || r.DeltaIdx != 100 || r.DeltaToast != 0 {
		t.Errorf("breakdown wrong: heap %d idx %d toast %d", r.DeltaHeap, r.DeltaIdx, r.DeltaToast)
	}
	if r.BytesPerDay != 500 {
		t.Errorf("BytesPerDay = %v, want 500 over a one-day window", r.BytesPerDay)
	}
	pct, ok := r.DeltaPct()
	if !ok || pct != 50 {
		t.Errorf("DeltaPct = %v/%v, want 50/true", pct, ok)
	}
	if r.DeltaRows != 50 || !r.RowsKnown {
		t.Errorf("rows: delta %d known %v, want 50/true", r.DeltaRows, r.RowsKnown)
	}
}

func TestDiffSizesTags(t *testing.T) {
	base, end := pair(
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "steady", RelKind: "r", RelFilenode: 10, TotalBytes: 100},
			{OID: 2, Schema: "public", Name: "dropped", RelKind: "r", RelFilenode: 20, TotalBytes: 400},
			{OID: 3, Schema: "public", Name: "vacuumed", RelKind: "r", RelFilenode: 30, TotalBytes: 900},
			{OID: 4, Schema: "public", Name: "parent", RelKind: "p", RelFilenode: 0, TotalBytes: 0},
		},
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "steady", RelKind: "r", RelFilenode: 10, TotalBytes: 120},
			{OID: 3, Schema: "public", Name: "vacuumed", RelKind: "r", RelFilenode: 31, TotalBytes: 300},
			{OID: 4, Schema: "public", Name: "parent", RelKind: "p", RelFilenode: 0, TotalBytes: 0},
			{OID: 5, Schema: "public", Name: "fresh", RelKind: "r", RelFilenode: 50, TotalBytes: 4000},
		},
	)
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if len(res.Rows) != 5 {
		t.Fatalf("want 5 rows (4 end + 1 gone), got %d", len(res.Rows))
	}

	if got := rowByName(t, res.Rows, "steady"); got.Tag != GrowthSteady {
		t.Errorf("steady: %v", got.Tag)
	}

	// A new relation's whole size is growth.
	fresh := rowByName(t, res.Rows, "fresh")
	if fresh.Tag != GrowthNew || fresh.DeltaBytes != 4000 {
		t.Errorf("fresh: tag %v delta %d, want new/4000", fresh.Tag, fresh.DeltaBytes)
	}
	if _, ok := fresh.DeltaPct(); ok {
		t.Error("fresh: DeltaPct must be undefined against a zero baseline")
	}

	gone := rowByName(t, res.Rows, "dropped")
	if gone.Tag != GrowthGone || gone.DeltaBytes != -400 || gone.EndBytes != 0 {
		t.Errorf("dropped: tag %v delta %d end %d", gone.Tag, gone.DeltaBytes, gone.EndBytes)
	}
	if gone.RelKind != "r" {
		t.Errorf("a gone row must still say what it was, got relkind %q", gone.RelKind)
	}

	// A rewrite's shrink is real disk, so the delta is reported unmodified.
	rw := rowByName(t, res.Rows, "vacuumed")
	if rw.Tag != GrowthRewritten {
		t.Errorf("vacuumed: tag %v, want rewritten", rw.Tag)
	}
	if rw.DeltaBytes != -600 {
		t.Errorf("vacuumed: delta %d, want -600 unmodified", rw.DeltaBytes)
	}

	// A partitioned parent has relfilenode 0 in both samples and must never
	// read as rewritten.
	if p := rowByName(t, res.Rows, "parent"); p.Tag != GrowthSteady {
		t.Errorf("partitioned parent tagged %v, want steady", p.Tag)
	}
}

func TestDiffSizesShortWindow(t *testing.T) {
	for _, d := range []time.Duration{0, 30 * time.Second, 59 * time.Second} {
		base, end := pair(nil, nil)
		end.CapturedAt = base.CapturedAt.Add(d)
		if _, err := DiffSizes(base, end); !errors.Is(err, ErrWindowTooShort) {
			t.Errorf("%v apart: err = %v, want ErrWindowTooShort", d, err)
		}
	}
	base, end := pair(nil, nil)
	end.CapturedAt = base.CapturedAt.Add(61 * time.Second)
	if _, err := DiffSizes(base, end); err != nil {
		t.Errorf("61s apart: %v, want accepted", err)
	}
}

func TestDiffSizesClockSkew(t *testing.T) {
	base, end := pair(
		[]SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 100}},
		[]SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 300}},
	)
	// Hand them over in the wrong order.
	res, err := DiffSizes(end, base)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if !res.Window.ClockSkew {
		t.Error("ClockSkew not flagged")
	}
	if res.Window.Duration <= 0 {
		t.Errorf("Duration = %v, want positive after ordering", res.Window.Duration)
	}
	// Ordered by time, so the growth still reads as growth.
	if r := rowByName(t, res.Rows, "t"); r.DeltaBytes != 200 {
		t.Errorf("DeltaBytes = %d, want +200 in time order", r.DeltaBytes)
	}
}

func TestDiffSizesStatsReset(t *testing.T) {
	rows := []SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 100, NLive: 10}}
	endRows := []SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 300, NLive: 2}}

	clean, err := DiffSizes(pair(rows, endRows))
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	base, end := pair(rows, endRows)
	end.StatsReset = base.StatsReset.Add(time.Hour) // a reset inside the window
	reset, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}

	if !reset.Window.StatsReset {
		t.Error("StatsReset not flagged")
	}
	if r := rowByName(t, reset.Rows, "t"); r.RowsKnown {
		t.Error("RowsKnown must be false across a reset — the counters lie")
	}
	// Sizes are measured, not counted, so the byte delta is unaffected.
	cb := rowByName(t, clean.Rows, "t")
	rb := rowByName(t, reset.Rows, "t")
	if cb.DeltaBytes != rb.DeltaBytes || rb.DeltaBytes != 200 {
		t.Errorf("byte delta changed across a reset: %d vs %d", cb.DeltaBytes, rb.DeltaBytes)
	}
}

func TestDiffSizesUnknownStatsResetIsNotTrusted(t *testing.T) {
	base, end := pair(nil, nil)
	base.StatsReset = time.Time{} // unreadable
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if !res.Window.StatsReset {
		t.Error("an unknown reset must be treated as a possible reset")
	}
}

func TestDiffSizesIdentity(t *testing.T) {
	t.Run("other database refused", func(t *testing.T) {
		base, end := pair(nil, nil)
		end.Database = "billing"
		if _, err := DiffSizes(base, end); !errors.Is(err, ErrSampleMismatch) {
			t.Errorf("err = %v, want ErrSampleMismatch", err)
		}
	})
	t.Run("other server refused", func(t *testing.T) {
		base, end := pair(nil, nil)
		end.Target = "elsewhere:5432"
		if _, err := DiffSizes(base, end); !errors.Is(err, ErrSampleMismatch) {
			t.Errorf("err = %v, want ErrSampleMismatch", err)
		}
	})
	t.Run("restored cluster is reported, not refused", func(t *testing.T) {
		base, end := pair(
			[]SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 100}},
			[]SizeRow{{OID: 1, Schema: "public", Name: "t", TotalBytes: 200}})
		end.SystemID = 999
		res, err := DiffSizes(base, end)
		if err != nil {
			t.Fatalf("DiffSizes: %v", err)
		}
		if res.Window.Identity != IdentityClusterChanged {
			t.Errorf("Identity = %v, want IdentityClusterChanged", res.Window.Identity)
		}
		if len(res.Rows) != 1 {
			t.Error("rows must still be produced — the numbers are real")
		}
	})
	t.Run("recreated database is reported", func(t *testing.T) {
		base, end := pair(nil, nil)
		end.DatabaseOID = 99999
		res, err := DiffSizes(base, end)
		if err != nil {
			t.Fatalf("DiffSizes: %v", err)
		}
		if res.Window.Identity != IdentityDBRecreated {
			t.Errorf("Identity = %v, want IdentityDBRecreated", res.Window.Identity)
		}
	})
	t.Run("unknown ids are not a mismatch", func(t *testing.T) {
		base, end := pair(nil, nil)
		base.SystemID, base.DatabaseOID = 0, 0
		end.SystemID, end.DatabaseOID = 7, 7
		res, err := DiffSizes(base, end)
		if err != nil {
			t.Fatalf("DiffSizes: %v", err)
		}
		if res.Window.Identity != IdentityOK {
			t.Errorf("Identity = %v, want OK when one side is unknown", res.Window.Identity)
		}
	})
}

func TestDiffSizesFloorCapped(t *testing.T) {
	base, end := pair(
		[]SizeRow{{OID: 1, Schema: "public", Name: "big", TotalBytes: 10000}},
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "big", TotalBytes: 10000},
			{OID: 2, Schema: "public", Name: "maybe-new", TotalBytes: 500},
		},
	)
	base.MinBytes = 1000 // the baseline had a floor this row is under
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if !res.Window.FloorChanged {
		t.Error("FloorChanged not flagged for mismatched floors")
	}
	r := rowByName(t, res.Rows, "maybe-new")
	if r.Tag != GrowthNew || !r.FloorCapped {
		t.Errorf("tag %v floorCapped %v, want new/true", r.Tag, r.FloorCapped)
	}
	if got := rowByName(t, res.Rows, "big"); got.FloorCapped {
		t.Error("a row present in both samples is never floor-capped")
	}
}

func TestDiffSizesOtherRow(t *testing.T) {
	base, end := pair(nil, nil)
	base.OtherBytes, base.OtherCount = 1000, 5
	end.OtherBytes, end.OtherCount = 1600, 6
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if res.Other.DeltaBytes != 600 || res.Other.Parts != 6 {
		t.Errorf("Other = delta %d parts %d, want 600/6", res.Other.DeltaBytes, res.Other.Parts)
	}
	base2, end2 := pair(nil, nil)
	res2, err := DiffSizes(base2, end2)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if res2.Other.Parts != 0 || res2.Other.EndBytes != 0 {
		t.Error("Other must be zero when neither sample had a floor")
	}
}

func TestDiffDatabases(t *testing.T) {
	base, end := pair(nil, nil)
	base.Databases = []Database{{Name: "app", SizeBytes: 1000}, {Name: "old", SizeBytes: 50}}
	end.Databases = []Database{{Name: "app", SizeBytes: 1500}, {Name: "new", SizeBytes: 70}}
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if len(res.Databases) != 3 {
		t.Fatalf("want 3 database rows, got %d", len(res.Databases))
	}
	byDB := map[string]GrowthRow{}
	for _, d := range res.Databases {
		byDB[d.Database] = d
	}
	if byDB["app"].DeltaBytes != 500 {
		t.Errorf("app delta = %d, want 500", byDB["app"].DeltaBytes)
	}
	if byDB["new"].Tag != GrowthNew || byDB["old"].Tag != GrowthGone {
		t.Errorf("tags: new=%v old=%v", byDB["new"].Tag, byDB["old"].Tag)
	}
}

func TestRollUpSchemas(t *testing.T) {
	base, end := pair(
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "a", TotalBytes: 100, HeapBytes: 100, NLive: 1},
			{OID: 2, Schema: "public", Name: "b", TotalBytes: 200, HeapBytes: 200, NLive: 2},
			{OID: 3, Schema: "archive", Name: "c", TotalBytes: 300, HeapBytes: 300, NLive: 3},
		},
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "a", TotalBytes: 150, HeapBytes: 150, NLive: 1},
			{OID: 2, Schema: "public", Name: "b", TotalBytes: 450, HeapBytes: 450, NLive: 5},
			{OID: 3, Schema: "archive", Name: "c", TotalBytes: 300, HeapBytes: 300, NLive: 3},
		},
	)
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	folded := FinishRollUp(RollUpGrowth(res.Rows, GrowthBySchema), res.Window.Duration)
	if len(folded) != 2 {
		t.Fatalf("want 2 schema rows, got %d", len(folded))
	}
	pub := rowByName(t, folded, "public")
	if pub.Parts != 2 {
		t.Errorf("public Parts = %d, want 2", pub.Parts)
	}
	if pub.BaseBytes != 300 || pub.EndBytes != 600 || pub.DeltaBytes != 300 {
		t.Errorf("public totals: base %d end %d delta %d", pub.BaseBytes, pub.EndBytes, pub.DeltaBytes)
	}
	// The ratio must pool from the totals, not average the members' ratios
	// (50% and 125% would average to 87.5%).
	pct, ok := pub.DeltaPct()
	if !ok || pct != 100 {
		t.Errorf("public DeltaPct = %v/%v, want 100/true pooled from totals", pct, ok)
	}
	if pub.BytesPerDay != 300 {
		t.Errorf("public BytesPerDay = %v, want 300", pub.BytesPerDay)
	}
}

func TestRollUpDatabaseScope(t *testing.T) {
	base, end := pair(
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "a", TotalBytes: 100},
			{OID: 2, Schema: "archive", Name: "c", TotalBytes: 300},
		},
		[]SizeRow{
			{OID: 1, Schema: "public", Name: "a", TotalBytes: 150},
			{OID: 2, Schema: "archive", Name: "c", TotalBytes: 350},
		},
	)
	res, _ := DiffSizes(base, end)
	folded := FinishRollUp(RollUpGrowth(res.Rows, GrowthByDatabase), res.Window.Duration)
	if len(folded) != 1 {
		t.Fatalf("want 1 database row, got %d", len(folded))
	}
	if folded[0].DeltaBytes != 100 || folded[0].Parts != 2 {
		t.Errorf("delta %d parts %d, want 100/2", folded[0].DeltaBytes, folded[0].Parts)
	}
}

func TestRollUpPartitionsNestedTree(t *testing.T) {
	// root ← mid ← {leaf1, leaf2}, plus root ← leaf3, plus a standalone table.
	rows := []SizeRow{
		{OID: 1, Schema: "public", Name: "root", RelKind: "p"},
		{OID: 2, Schema: "public", Name: "mid", RelKind: "p", ParentOID: 1},
		{OID: 3, Schema: "public", Name: "leaf1", RelKind: "r", ParentOID: 2, TotalBytes: 10},
		{OID: 4, Schema: "public", Name: "leaf2", RelKind: "r", ParentOID: 2, TotalBytes: 20},
		{OID: 5, Schema: "public", Name: "leaf3", RelKind: "r", ParentOID: 1, TotalBytes: 30},
		{OID: 9, Schema: "public", Name: "solo", RelKind: "r", TotalBytes: 7},
	}
	endRows := make([]SizeRow, len(rows))
	copy(endRows, rows)
	for i := range endRows {
		endRows[i].TotalBytes *= 2
	}
	base, end := pair(rows, endRows)
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}

	folded := FinishRollUp(RollUpGrowth(res.Rows, GrowthByPartition), res.Window.Duration)
	if len(folded) != 2 {
		t.Fatalf("want 2 roots (root tree + solo), got %d: %+v", len(folded), folded)
	}
	root := rowByName(t, folded, "root")
	if root.Parts != 5 {
		t.Errorf("root Parts = %d, want 5 (itself, mid and three leaves)", root.Parts)
	}
	if root.BaseBytes != 60 || root.DeltaBytes != 60 {
		t.Errorf("root base %d delta %d, want 60/60", root.BaseBytes, root.DeltaBytes)
	}
	if root.RelKind != "p" {
		t.Errorf("the fold must keep the root's identity, got relkind %q", root.RelKind)
	}
	if solo := rowByName(t, folded, "solo"); solo.Parts != 1 {
		t.Errorf("a standalone table is its own root, Parts = %d", solo.Parts)
	}
}

func TestRollUpPartitionsOrphanAndCycle(t *testing.T) {
	// "orphan" names a parent that is not in the set (under the floor, or in an
	// excluded schema): it must stand as its own root rather than vanish.
	// "loop_a"/"loop_b" point at each other, which must terminate.
	rows := []SizeRow{
		{OID: 3, Schema: "public", Name: "orphan", RelKind: "r", ParentOID: 404, TotalBytes: 10},
		{OID: 10, Schema: "public", Name: "loop_a", RelKind: "r", ParentOID: 11, TotalBytes: 1},
		{OID: 11, Schema: "public", Name: "loop_b", RelKind: "r", ParentOID: 10, TotalBytes: 2},
	}
	base, end := pair(rows, rows)
	res, err := DiffSizes(base, end)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}

	done := make(chan []GrowthRow, 1)
	go func() { done <- RollUpGrowth(res.Rows, GrowthByPartition) }()
	select {
	case folded := <-done:
		names := map[string]bool{}
		for _, r := range folded {
			names[r.Name] = true
		}
		if !names["orphan"] {
			t.Error("the orphan row was dropped instead of standing as its own root")
		}
		var total int64
		for _, r := range folded {
			total += r.BaseBytes
		}
		if total != 13 {
			t.Errorf("folded bytes = %d, want all 13 preserved", total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RollUpGrowth did not terminate on a pg_inherits cycle")
	}
}

func TestScopeKeyMatchesFold(t *testing.T) {
	rows := []SizeRow{
		{OID: 1, Schema: "public", Name: "root", RelKind: "p"},
		{OID: 2, Schema: "public", Name: "part", RelKind: "r", ParentOID: 1, TotalBytes: 5},
		{OID: 3, Schema: "archive", Name: "other", RelKind: "r", TotalBytes: 9},
	}
	base, end := pair(rows, rows)
	res, _ := DiffSizes(base, end)

	// Every scope's key must group leaf rows exactly the way RollUpGrowth
	// folds them, since the UI narrows with one and folds with the other.
	for _, sc := range []GrowthScope{GrowthBySchema, GrowthByDatabase, GrowthByPartition} {
		folded := RollUpGrowth(res.Rows, sc)
		groups := map[string]int{}
		for _, r := range res.Rows {
			groups[ScopeKey(r, sc)]++
		}
		if len(groups) != len(folded) {
			t.Errorf("scope %d: ScopeKey yields %d groups but the fold yields %d", sc, len(groups), len(folded))
		}
		for _, f := range folded {
			if n := groups[ScopeKey(f, sc)]; n != f.Parts {
				t.Errorf("scope %d: group %q has %d members but the folded row says %d",
					sc, ScopeKey(f, sc), n, f.Parts)
			}
		}
	}
}

func TestDiffSizesNilSamples(t *testing.T) {
	if _, err := DiffSizes(nil, nil); err == nil {
		t.Error("want an error for nil samples")
	}
}
