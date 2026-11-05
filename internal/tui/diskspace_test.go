package tui

import (
	"strings"
	"testing"

	"pgdu/internal/pg"
	"pgdu/internal/procfs"
)

const gb = int64(1) << 30

func partsScreen() *screen {
	return &screen{level: levelParts, tool: toolDisk, table: pg.Table{OID: 1, Schema: "public", Name: "t"},
		items: []item{
			{name: "heap", size: 40 * gb, data: pg.Part{Kind: pg.PartHeap, SizeBytes: 40 * gb}},
			{name: "i1", size: 6 * gb, data: pg.Part{Kind: pg.PartIndex, SizeBytes: 6 * gb, Name: "i1"}},
			{name: "i2", size: 5 * gb, data: pg.Part{Kind: pg.PartIndex, SizeBytes: 5 * gb, Name: "i2"}},
		}}
}

func diskOf(avail int64) *pg.DiskSpace {
	return &pg.DiskSpace{Path: "/var/lib/postgresql/17/main",
		FS: procfs.FS{Total: 100 * gb, Free: avail, Avail: avail}}
}

// The whole-table rebuild needs room for a second copy of *every* index at
// once, not just the largest one: REINDEX TABLE CONCURRENTLY swaps them all in
// at the end.
func TestReindexNeedBytes(t *testing.T) {
	s := partsScreen()
	if got := reindexNeedBytes(s, true); got != 11*gb {
		t.Errorf("whole-table need = %d GB, want 11", got/gb)
	}
	s.cursor = 1
	if got := reindexNeedBytes(s, false); got != 6*gb {
		t.Errorf("single-index need = %d GB, want 6", got/gb)
	}
}

// R refuses when the copy it would build does not fit, and says why instead of
// arming a confirm.
func TestReindexTableBlockedWhenDiskTooFull(t *testing.T) {
	s := partsScreen()
	m := newTestModel(s)
	m.disk = diskOf(8 * gb) // < the 11 GB of indexes

	m.armReindexTable(s)
	if s.reindex.pending != "" {
		t.Fatalf("armed a rebuild that cannot fit: pending=%q", s.reindex.pending)
	}
	if !strings.Contains(s.reindex.blocked, "11.00 GB") || !strings.Contains(s.reindex.blocked, "8.00 GB") {
		t.Errorf("blocked reason = %q, want the need and the free space", s.reindex.blocked)
	}
	if banner := m.renderReindexBanner(s); !strings.Contains(banner, "reindex blocked") {
		t.Errorf("banner = %q, want a blocked notice", banner)
	}

	// Room again: the same press arms normally.
	s.reindex.blocked = ""
	m.disk = diskOf(60 * gb)
	m.armReindexTable(s)
	if s.reindex.pending == "" || !s.reindex.whole || s.reindex.blocked != "" {
		t.Errorf("rebuild not armed with room to spare: pending=%q blocked=%q", s.reindex.pending, s.reindex.blocked)
	}
}

// With no reading of the server's disk nothing is refused — the gate is best
// effort, and pgdu is routinely run from somewhere other than the DB host.
func TestReindexNotBlockedWithoutDiskReading(t *testing.T) {
	s := partsScreen()
	m := newTestModel(s)
	m.armReindexTable(s)
	if s.reindex.pending == "" || s.reindex.blocked != "" {
		t.Errorf("unknown free space must not block: pending=%q blocked=%q", s.reindex.pending, s.reindex.blocked)
	}
}

// Enter on a bloated index row is gated the same way.
func TestReindexEnterBlocked(t *testing.T) {
	s := partsScreen()
	s.items[1].bloat = 4 * gb
	s.items[1].hasBloat = true
	s.cursor = 1
	m := newTestModel(s)
	if reindexCandidate(s) != "i1" {
		t.Fatalf("test row is not a reindex candidate")
	}
	m.disk = diskOf(3 * gb)
	m.handleReindexEnter(s)
	if s.reindex.pending != "" || s.reindex.blocked == "" {
		t.Errorf("Enter armed a rebuild needing 6 GB with 3 GB free: pending=%q blocked=%q",
			s.reindex.pending, s.reindex.blocked)
	}
}

// A rebuild that fits but takes more than half of what is free still runs —
// with a warning next to the confirm.
func TestReindexTightNote(t *testing.T) {
	s := partsScreen()
	m := newTestModel(s)
	m.disk = diskOf(15 * gb)
	m.armReindexTable(s)
	banner := m.renderReindexBanner(s)
	if !strings.Contains(banner, "confirm:") || !strings.Contains(banner, "11.00 GB") {
		t.Errorf("banner = %q, want a confirm carrying the tight-space warning", banner)
	}
	m.disk = diskOf(80 * gb)
	if note := m.reindexTightNote(s); note != "" {
		t.Errorf("tight note on a roomy disk = %q, want none", note)
	}
}

// The status row carries the filesystem the data directory sits on, and says
// nothing at all when it could not be read.
func TestDiskSpaceLabel(t *testing.T) {
	m := newTestModel(partsScreen())
	if got := m.diskSpaceLabel(); got != "" {
		t.Errorf("label without a reading = %q, want empty", got)
	}
	m.disk = diskOf(25 * gb)
	got := squashSpaces(m.diskSpaceLabel())
	if !strings.Contains(got, "75% full") || !strings.Contains(got, "25.00 GB free of 100.00 GB") {
		t.Errorf("label = %q, want fullness and free/total", got)
	}
}
