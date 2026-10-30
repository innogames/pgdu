package tui

import (
	"encoding/binary"
	"strings"
	"testing"

	"pgdu/internal/pg"
)

// le8ptr encodes v as little-endian 8-byte hex, the form pageinspect returns for
// an int8 key.
func le8ptr(v uint64) *string {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return rawBytes(b[:]...)
}

// TestSeekToKeyInternal walks an internal page of int8 separators and checks the
// cursor lands on the downlink whose [key,next) range covers the sought value.
func TestSeekToKeyInternal(t *testing.T) {
	// items deliberately out of offset order; cursor indexes this (visible) slice.
	items := []item{
		tupleItem(3, le8ptr(100)),  // visPos 0
		tupleItem(1, le8ptr(1000)), // visPos 1 — page high key, excluded from seek
		tupleItem(2, nil),          // visPos 2 — minus-infinity leftmost child
		tupleItem(5, le8ptr(300)),  // visPos 3
		tupleItem(4, le8ptr(200)),  // visPos 4
	}
	newScreen := func() *screen {
		return &screen{
			level: levelIndexTuples,
			items: items,
			pages: pageState{indexPageType: "i", indexKeyCols: []pg.IndexKeyColumn{int8Col}}}
	}
	for _, tc := range []struct {
		query      string
		wantCursor int
		wantStatus string
	}{
		{"150", 0, "→ #0003  (100…200)"}, // covered by the [100,200) downlink
		{"50", 2, "→ #0002  (−∞…100)"},   // below the first key → minus-infinity child
		{"500", 3, "→ #0005  (300…+∞)"},  // past the last key → last downlink
		{"100", 0, "→ #0003  (100…200)"}, // exact boundary belongs to its own downlink
	} {
		s := newScreen()
		s.pages.seekQuery = tc.query
		seekToKey(s)
		if s.cursor != tc.wantCursor || s.pages.seekStatus != tc.wantStatus {
			t.Errorf("seek %q: cursor=%d status=%q, want cursor=%d status=%q",
				tc.query, s.cursor, s.pages.seekStatus, tc.wantCursor, tc.wantStatus)
		}
	}
}

// TestSeekToKeyLeafText checks text-leading seek on a leaf page (no minus-infinity
// entry; the high key at offset 1 is excluded).
func TestSeekToKeyLeafText(t *testing.T) {
	items := []item{
		tupleItem(1, hexText("zzz")),   // visPos 0 — high key, excluded
		tupleItem(2, hexText("apple")), // visPos 1
		tupleItem(3, hexText("mango")), // visPos 2
		tupleItem(4, hexText("zebra")), // visPos 3
	}
	newScreen := func() *screen {
		return &screen{
			level: levelIndexTuples,
			items: items,
			pages: pageState{indexPageType: "l", indexKeyCols: []pg.IndexKeyColumn{textCol}}}
	}
	for _, tc := range []struct {
		query      string
		wantCursor int
		wantStatus string
	}{
		{"lemon", 1, "→ #0002"}, // between apple and mango → apple
		{"aaa", 1, "→ #0002"},   // before the first tuple → first tuple
		{"zzz", 3, "→ #0004"},   // past the last tuple → last tuple
	} {
		s := newScreen()
		s.pages.seekQuery = tc.query
		seekToKey(s)
		if s.cursor != tc.wantCursor || s.pages.seekStatus != tc.wantStatus {
			t.Errorf("seek %q: cursor=%d status=%q, want cursor=%d status=%q",
				tc.query, s.cursor, s.pages.seekStatus, tc.wantCursor, tc.wantStatus)
		}
	}
}

// heapPageItem is a heap-page row for the go-to-block seek.
func heapPageItem(blk int64) item {
	return item{data: pg.HeapPageStat{Blkno: blk, PageSize: 8192, Lower: 24, Upper: 8192}}
}

// pagesScreen builds a heap-page list screen plus the Model whose stack holds
// it — seekToPage reaches loadCurrent, which reads m.top().
func pagesScreen(start int32, total int32, blocks ...int64) (*Model, *screen) {
	s := &screen{
		level: levelHeapPages,
		pages: pageState{heapWindowStart: start, heapWindowCount: heapWindowDefault, heapPageCount: total},
	}
	for _, b := range blocks {
		s.items = append(s.items, heapPageItem(b))
	}
	return &Model{stack: []*screen{s}}, s
}

// A block inside the loaded window only moves the cursor — no reload.
func TestSeekToPageWithinWindow(t *testing.T) {
	m, s := pagesScreen(0, 10000, 0, 1, 2, 3, 4)
	s.pages.seekQuery = "3"
	if cmd := m.seekToPage(s); cmd != nil {
		t.Error("a block already loaded must not trigger a reload")
	}
	if s.cursor != 3 {
		t.Errorf("cursor = %d, want 3", s.cursor)
	}
	if s.pages.heapWindowStart != 0 {
		t.Errorf("window moved to %d for an in-window block", s.pages.heapWindowStart)
	}
	if s.pages.seekStatus != "→ block 3" {
		t.Errorf("status = %q", s.pages.seekStatus)
	}
}

// A block outside the window slides it to the grid-aligned span holding that
// block and arms focusBlkno so the load lands the cursor on it.
func TestSeekToPageMovesWindow(t *testing.T) {
	m, s := pagesScreen(0, 100000, 0, 1, 2)
	s.pages.seekQuery = "54321"
	_ = m.seekToPage(s)

	wantStart := (54321 / heapWindowDefault) * heapWindowDefault
	if s.pages.heapWindowStart != wantStart {
		t.Errorf("window start = %d, want %d", s.pages.heapWindowStart, wantStart)
	}
	if s.pages.focusBlkno != 54321 {
		t.Errorf("focusBlkno = %d, want 54321", s.pages.focusBlkno)
	}
	if s.cursor != 0 {
		t.Errorf("cursor = %d, want it reset for the incoming window", s.cursor)
	}
}

// The window never runs past EOF (get_raw_page errors there), and a block
// beyond the relation is refused outright rather than silently clamped.
func TestSeekToPageClampsToEOF(t *testing.T) {
	m, s := pagesScreen(0, 5000, 0, 1, 2)
	s.pages.seekQuery = "4999"
	_ = m.seekToPage(s)
	if got := s.pages.heapWindowStart + heapWindowDefault; got > 5000 {
		t.Errorf("window [%d,%d) runs past the 5000-page relation", s.pages.heapWindowStart, got)
	}

	m, s = pagesScreen(0, 5000, 0, 1, 2)
	s.pages.seekQuery = "5000"
	if cmd := m.seekToPage(s); cmd != nil {
		t.Error("a block past EOF must not issue a load")
	}
	if !strings.Contains(s.pages.seekStatus, "past the end") {
		t.Errorf("status = %q, want it to name the end of the relation", s.pages.seekStatus)
	}

	m, s = pagesScreen(0, 5000, 0, 1, 2)
	s.pages.seekQuery = "not a number"
	if cmd := m.seekToPage(s); cmd != nil || s.pages.seekStatus != "enter a block number" {
		t.Errorf("non-numeric query: cmd %v status %q", cmd != nil, s.pages.seekStatus)
	}
}

// focusPageBlkno is consumed once, so a later reload doesn't yank the cursor
// back to a block the user has since scrolled away from.
func TestFocusPageBlknoConsumedOnce(t *testing.T) {
	_, s := pagesScreen(2000, 100000, 2000, 2001, 2002)
	s.loaded = true
	s.pages.focusBlkno = 2002
	focusPageBlkno(s)
	if s.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", s.cursor)
	}
	if s.pages.focusBlkno != 0 {
		t.Error("focusBlkno not consumed")
	}
	s.cursor = 0
	focusPageBlkno(s)
	if s.cursor != 0 {
		t.Error("a consumed focusBlkno moved the cursor again")
	}
}

// A BRIN range has one item per indexed attribute, so BlockNum is not unique;
// the seek must land on the first row of the covering range every time.
func TestSeekToBlockStableAcrossAttributes(t *testing.T) {
	mk := func(blk int64, att int32) item {
		return item{data: pg.BrinItem{BlockNum: blk, AttNum: att}}
	}
	for range 20 { // an unstable sort only shows up over repeated runs
		s := &screen{
			level: levelIndexTuples,
			items: []item{mk(0, 1), mk(0, 2), mk(0, 3), mk(128, 1), mk(128, 2)},
			pages: pageState{index: pg.Relation{AccessMethod: "brin"}, seekQuery: "10"},
		}
		seekApply(s)
		if s.cursor != 0 {
			t.Fatalf("cursor = %d, want the first row of the covering range", s.cursor)
		}
	}
}
