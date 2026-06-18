package tui

import (
	"encoding/binary"
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
			level:         levelIndexTuples,
			items:         items,
			indexPageType: "i",
			indexKeyCols:  []pg.IndexKeyColumn{int8Col},
		}
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
		s.seekQuery = tc.query
		seekToKey(s)
		if s.cursor != tc.wantCursor || s.seekStatus != tc.wantStatus {
			t.Errorf("seek %q: cursor=%d status=%q, want cursor=%d status=%q",
				tc.query, s.cursor, s.seekStatus, tc.wantCursor, tc.wantStatus)
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
			level:         levelIndexTuples,
			items:         items,
			indexPageType: "l",
			indexKeyCols:  []pg.IndexKeyColumn{textCol},
		}
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
		s.seekQuery = tc.query
		seekToKey(s)
		if s.cursor != tc.wantCursor || s.seekStatus != tc.wantStatus {
			t.Errorf("seek %q: cursor=%d status=%q, want cursor=%d status=%q",
				tc.query, s.cursor, s.seekStatus, tc.wantCursor, tc.wantStatus)
		}
	}
}
