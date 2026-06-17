package tui

import (
	"testing"

	"pgdu/internal/pg"
)

func TestValidSorts(t *testing.T) {
	cases := []struct {
		level level
		want  []sortMode
	}{
		{levelTools, []sortMode{sortByName}},
		{levelTables, []sortMode{sortBySize, sortByHeap, sortByIndex, sortByRows, sortByName}},
		{levelParts, []sortMode{sortBySize, sortByBloat, sortByName}},
		{levelHeapPages, []sortMode{sortByBlkno, sortByDeadRatio, sortByFreeSpace}},
		{levelColumns, []sortMode{sortBySize, sortByName}}, // default fallback
	}
	for _, c := range cases {
		got := validSorts(c.level)
		if len(got) != len(c.want) {
			t.Errorf("validSorts(%v) = %v, want %v", c.level, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("validSorts(%v) = %v, want %v", c.level, got, c.want)
				break
			}
		}
	}
}

func TestSortModeDefaultDesc(t *testing.T) {
	if !sortBySize.defaultDesc() {
		t.Error("sortBySize should default descending")
	}
	if sortByName.defaultDesc() {
		t.Error("sortByName should default ascending")
	}
	if sortByHitRatio.defaultDesc() {
		t.Error("sortByHitRatio should default ascending (worst-cached first)")
	}
}

func TestSortModeLess(t *testing.T) {
	small := item{size: 10}
	big := item{size: 20}
	if !sortBySize.less(small, big) {
		t.Error("sortBySize: 10 should be < 20")
	}
	if sortBySize.less(big, small) {
		t.Error("sortBySize: 20 should not be < 10")
	}
	if sortByName.less(small, big) {
		t.Error("sortByName.less is always false (name tiebreak handled by applySort)")
	}

	// "unknown" rows (no row estimate) stay a distinct bucket below known rows.
	known := item{data: pg.Table{EstRows: 5}}
	unknown := item{data: nil}
	if sortByRows.less(known, unknown) {
		t.Error("known rows should not sort below unknown via less")
	}
	if !sortByRows.less(unknown, known) {
		t.Error("unknown rows should sort below known via less")
	}
}

func TestItemRows(t *testing.T) {
	cases := []struct {
		name   string
		it     item
		want   int64
		wantOK bool
	}{
		{"table", item{data: pg.Table{EstRows: 100}}, 100, true},
		{"relation", item{data: pg.Relation{EstRows: 7}}, 7, true},
		{"negative est (stats missing)", item{data: pg.Table{EstRows: -1}}, 0, false},
		{"no payload", item{data: nil}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := itemRows(c.it)
			if got != c.want || ok != c.wantOK {
				t.Errorf("itemRows = (%d, %v), want (%d, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestItemBufferExtractors(t *testing.T) {
	good := item{data: pg.TableBufferStat{BufferedBytes: 50, TotalBytes: 200, Hits: 3, Reads: 1}}
	noIO := item{data: pg.TableBufferStat{TotalBytes: 200}}
	other := item{data: pg.Table{}}

	if r, ok := itemHitRatio(good); !ok || r != 0.75 {
		t.Errorf("itemHitRatio = (%v, %v), want (0.75, true)", r, ok)
	}
	if _, ok := itemHitRatio(noIO); ok {
		t.Error("itemHitRatio should be false when no I/O recorded")
	}
	if _, ok := itemHitRatio(other); ok {
		t.Error("itemHitRatio should be false for non-buffer items")
	}

	if r, ok := itemCachedRatio(good); !ok || r != 0.25 {
		t.Errorf("itemCachedRatio = (%v, %v), want (0.25, true)", r, ok)
	}
	if b, ok := itemTotalBytes(good); !ok || b != 200 {
		t.Errorf("itemTotalBytes = (%v, %v), want (200, true)", b, ok)
	}
	if _, ok := itemTotalBytes(other); ok {
		t.Error("itemTotalBytes should be false for non-buffer items")
	}
}
