package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// A hash page type hash_page_stats refuses carries no counts at all, which must
// not sort or render as a clean (0% dead) bucket page.
func TestItemDeadRatioHashPage(t *testing.T) {
	cases := []struct {
		name    string
		p       pg.HashPageStat
		wantOK  bool
		wantVal float64
	}{
		{"metapage has no counts", pg.HashPageStat{PageType: "metapage"}, false, 0},
		{"empty bucket is unrankable", pg.HashPageStat{PageType: "bucket", LiveItems: new(int32(0)), DeadItems: new(int32(0))}, false, 0},
		{"clean bucket", pg.HashPageStat{PageType: "bucket", LiveItems: new(int32(10)), DeadItems: new(int32(0))}, true, 0},
		{"half dead", pg.HashPageStat{PageType: "overflow", LiveItems: new(int32(5)), DeadItems: new(int32(5))}, true, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := itemDeadRatio(item{data: c.p})
			if ok != c.wantOK || got != c.wantVal {
				t.Errorf("itemDeadRatio = (%v,%v), want (%v,%v)", got, ok, c.wantVal, c.wantOK)
			}
		})
	}
}

func TestHashPageToItem(t *testing.T) {
	cases := []struct {
		pageType  string
		wantDrill bool
	}{
		{"bucket", true},
		{"overflow", true},
		{"metapage", false},
		{"bitmap", false},
		{"unused", false},
	}
	for _, c := range cases {
		t.Run(c.pageType, func(t *testing.T) {
			it := hashPageToItem(pg.HashPageStat{Blkno: 3, PageType: c.pageType, FreeSize: 2048, PageSize: 8192})
			if it.hasChildren != c.wantDrill {
				t.Errorf("hasChildren = %v, want %v — only pages hash_page_items can read may drill",
					it.hasChildren, c.wantDrill)
			}
			if want := heapPageBlockSize - 2048; it.size != want {
				t.Errorf("size = %d, want %d (used bytes)", it.size, want)
			}
			if !strings.Contains(it.name, "3") {
				t.Errorf("name %q does not carry the block number", it.name)
			}
		})
	}
}

// The filter must reach every spelling of the code, the heap pointer, and the
// projected key — the key being the one a user actually knows.
func TestHashItemToItem(t *testing.T) {
	key := "(90125)"
	it := hashItemToItem(pg.HashItem{ItemOffset: 2, Hash: 2147483648, Ctid: "(42,7)", Key: &key})
	for _, want := range []string{"0x80000000", "2147483648", "(42,7)", "(90125)"} {
		if !strings.Contains(it.name, want) {
			t.Errorf("name %q missing %q", it.name, want)
		}
	}
	if !it.hasChildren {
		t.Error("a hash entry must drill: Enter opens the heap tuple its ctid names")
	}
}

// An unresolved key must not stop the row drilling — the heap tuple is still
// where the pointer says it is, and that is what Enter promises.
func TestHashItemToItemUnresolvedKey(t *testing.T) {
	it := hashItemToItem(pg.HashItem{ItemOffset: 1, Hash: 7, Ctid: "(1,1)"})
	if !it.hasChildren {
		t.Error("an entry with no visible heap row must still drill to its ctid")
	}
	if strings.Count(it.name, "(1,1)") != 1 {
		t.Errorf("name %q should carry the ctid once and no key", it.name)
	}
}

// A HOT-redirected entry's key comes from the live tuple, and reads the same as a
// directly projected one — the alternative is a blank column on any table that
// gets updated.
func TestHashItemKey(t *testing.T) {
	direct, hot := "direct", "viaHOT"
	cases := []struct {
		name string
		it   pg.HashItem
		want *string
	}{
		{"direct projection wins", pg.HashItem{Key: &direct, HotKey: &hot}, &direct},
		{"falls back to the HOT redirect", pg.HashItem{HotKey: &hot}, &hot},
		{"neither resolved", pg.HashItem{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hashItemKey(c.it)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("hashItemKey = %q, want nil", *got)
			case c.want != nil && (got == nil || *got != *c.want):
				t.Errorf("hashItemKey = %v, want %q", got, *c.want)
			}
		})
	}
}

func TestHashPageTypeRank(t *testing.T) {
	order := []string{"metapage", "bucket", "overflow", "bitmap", "unused"}
	for i := 1; i < len(order); i++ {
		if hashPageTypeRank(order[i-1]) >= hashPageTypeRank(order[i]) {
			t.Errorf("%s must rank before %s", order[i-1], order[i])
		}
	}
	if hashPageTypeRank("something-new") <= hashPageTypeRank("unused") {
		t.Error("an unrecognised page type must sort last, not alias a known one")
	}
}

// The items cell distinguishes "this page type has no counts" from "zero".
func TestHashPageItemsCell(t *testing.T) {
	if got := hashPageItemsCell(pg.HashPageStat{PageType: "metapage"}); !strings.Contains(got, "—") {
		t.Errorf("absent counts rendered %q, want an em dash", got)
	}
	got := hashPageItemsCell(pg.HashPageStat{PageType: "bucket", LiveItems: new(int32(0)), DeadItems: new(int32(0))})
	if strings.Contains(got, "—") || !strings.Contains(got, "0L") {
		t.Errorf("an empty bucket rendered %q, want a literal 0L", got)
	}
	if got := hashPageItemsCell(pg.HashPageStat{PageType: "bucket", LiveItems: new(int32(9)), DeadItems: new(int32(3))}); !strings.Contains(got, "3D") {
		t.Errorf("dead count missing from %q", got)
	}
}
