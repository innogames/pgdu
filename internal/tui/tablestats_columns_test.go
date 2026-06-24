package tui

import (
	"testing"
	"time"

	"pgdu/internal/cli"
	"pgdu/internal/pg"
	"pgdu/internal/prefs"
)

// TestNewModelSeedsTableStatsColumns verifies persisted Table-overview column
// selections are loaded into the in-memory visibility map at construction.
func TestNewModelSeedsTableStatsColumns(t *testing.T) {
	t.Setenv("PGDU_CONFIG_DIR", t.TempDir())
	p := prefs.Load()
	p.SetColumns(colPrefsTableStats, map[string]bool{
		string(tblColSeqScan): false, // hide a default-on column
		string(tblColHeap):    true,  // show a default-off column
	})

	m := NewModel(pg.New(cli.Config{}), 2*time.Second, "", p, "")

	if m.tblColEnabled(tblColSeqScan, true) {
		t.Errorf("tblColSeqScan should be hidden per persisted prefs")
	}
	if !m.tblColEnabled(tblColHeap, false) {
		t.Errorf("tblColHeap should be shown per persisted prefs (overriding default off)")
	}
	// A column the user never touched keeps its registry default.
	if !m.tblColEnabled(tblColSize, true) {
		t.Errorf("tblColSize should fall back to its default-on")
	}
}

// TestVisibleTblColsDefaults checks the projection keeps the mandatory table-name
// column and the default-on set, and drops default-off columns.
func TestVisibleTblColsDefaults(t *testing.T) {
	m := &Model{}
	descs := m.visibleTblCols()
	has := func(id tblColID) bool { return indexOfTblCol(descs, id) >= 0 }
	if !has(tblColTable) {
		t.Errorf("mandatory table column must always be present")
	}
	if !has(tblColSize) || !has(tblColDeadPct) || !has(tblColCache) {
		t.Errorf("default-on columns missing from projection")
	}
	if has(tblColHeap) || has(tblColFill) || has(tblColPersist) {
		t.Errorf("default-off columns should not appear by default")
	}
}

// TestBuildTableStatItems verifies each row's cells stay parallel to the
// projected columns and carry the relation OID for drill-in / describe lookup.
func TestBuildTableStatItems(t *testing.T) {
	m := &Model{}
	rows := []pg.TableStat{
		{OID: 42, Name: "orders", TotalBytes: 2048, NLive: 100},
		{OID: 7, Name: "users", TotalBytes: 1024, NLive: 5},
	}
	items, descs := m.buildTableStatItems(rows)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	for i, it := range items {
		cells, ok := it.data.([]pg.DiagCell)
		if !ok {
			t.Fatalf("item %d data is not []pg.DiagCell", i)
		}
		if len(cells) != len(descs) {
			t.Errorf("item %d: %d cells != %d columns", i, len(cells), len(descs))
		}
	}
	if items[0].statQueryID != 42 {
		t.Errorf("first item should carry OID 42, got %d", items[0].statQueryID)
	}
}

// TestSyncTblSortDefault checks the default sort (no prior selection) lands on
// the size column, descending.
func TestSyncTblSortDefault(t *testing.T) {
	m := &Model{}
	s := &screen{level: levelTableStats}
	descs := m.visibleTblCols()
	m.syncTblSort(s, descs)
	if got := descs[s.diagSortCol].id; got != tblColSize {
		t.Errorf("default sort column = %q, want %q", got, tblColSize)
	}
	if !s.sortDesc {
		t.Errorf("default sort should be descending")
	}
}

// TestTableStatRatios sanity-checks the derived ratio helpers used by the cells.
func TestTableStatRatios(t *testing.T) {
	ts := pg.TableStat{
		NLive: 80, NDead: 20, // 20% dead
		NUpdate: 100, NHotUpdate: 40, // 40% HOT
		SeqScan: 3, IdxScan: 1, // 75% sequential
		HeapBlksRead: 1, HeapBlksHit: 9, // 90% cache hit
	}
	if got := ts.DeadPct(); got != 20 {
		t.Errorf("DeadPct = %v, want 20", got)
	}
	if got, ok := ts.HotPct(); !ok || got != 40 {
		t.Errorf("HotPct = %v ok=%v, want 40 true", got, ok)
	}
	if got, ok := ts.SeqPct(); !ok || got != 75 {
		t.Errorf("SeqPct = %v ok=%v, want 75 true", got, ok)
	}
	if got, ok := ts.HeapHitPct(); !ok || got != 90 {
		t.Errorf("HeapHitPct = %v ok=%v, want 90 true", got, ok)
	}
	// Undefined ratios report ok=false rather than a misleading 0.
	if _, ok := (pg.TableStat{}).HotPct(); ok {
		t.Errorf("HotPct on zero updates should be ok=false")
	}
}
