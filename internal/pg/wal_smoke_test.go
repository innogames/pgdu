package pg

import (
	"context"
	"os"
	"testing"

	"pgdu/internal/cli"
)

// Temporary smoke test for the WAL inspector client methods against a live
// local server (peer auth over the unix socket). Run with:
//
//	PGDU_SMOKE_DB=pgdu_test go test ./internal/pg -run TestWALSmoke -v
func TestWALSmoke(t *testing.T) {
	db := os.Getenv("PGDU_SMOKE_DB")
	if db == "" {
		t.Skip("PGDU_SMOKE_DB not set")
	}
	cfg := cli.Config{User: os.Getenv("USER"), Database: db, SSLMode: "disable"}
	c := New(cfg)
	defer c.Close()
	ctx := context.Background()

	start, end, err := c.WALWindow(ctx, db, 16<<20)
	if err != nil {
		t.Fatalf("WALWindow: %v", err)
	}
	t.Logf("window %s .. %s", start, end)

	sum, err := c.WALOverview(ctx, db)
	if err != nil {
		t.Fatalf("WALOverview: %v", err)
	}
	t.Logf("summary: insert=%s seg=%d/%dB recs=%d fpi=%d bytes=%d buffers_full=%d level=%s",
		sum.InsertLSN, sum.SegmentFiles, sum.SegmentBytes, sum.StatRecords, sum.StatFPI, sum.StatBytes, sum.StatBuffersFull, sum.WalLevel)

	// Checkpoint context for the header (best-effort; never errors except on a
	// pool-acquire failure, so any returned err is fatal here).
	cp, err := c.WALCheckpoint(ctx, db)
	if err != nil {
		t.Fatalf("WALCheckpoint: %v", err)
	}
	next, hasNext := cp.NextCheckpointETA()
	t.Logf("checkpoint: since=%dB max_wal=%dB timed=%d req=%d timeout=%ds next=%v(%v) settings=%v",
		cp.BytesSinceCheckpoint, cp.MaxWALBytes, cp.CheckpointsTimed, cp.CheckpointsRequested,
		cp.CheckpointTimeoutSec, next, hasNext, cp.Settings)

	stats, err := c.WALRmgrStats(ctx, db, start, end)
	if err != nil {
		t.Fatalf("WALRmgrStats: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("no rmgr stats")
	}
	for _, s := range stats {
		t.Logf("rmgr %-12s count=%-6d rec=%-9d fpi=%-9d combined=%d", s.Name, s.Count, s.RecordSize, s.FPISize, s.CombinedSize)
	}

	// By-relation breakdown of the same window ("what caused the change"), then
	// drill the busiest relation's block references (PostgreSQL 16+).
	rels, err := c.WALRelStats(ctx, db, start, end)
	if err != nil {
		t.Fatalf("WALRelStats: %v", err)
	}
	for _, r := range rels {
		t.Logf("rel %-24s(%d) data=%-9d fpi=%-9d recs=%-6d pages=%-6d toast=%v db=%q",
			r.RelName, r.RelFileNode, r.DataBytes, r.FPIBytes, r.RecCount, r.BlockCount, r.IsToast, r.DBName)
	}
	if len(rels) > 0 {
		relBlocks, err := c.WALRelBlocks(ctx, db, start, end, rels[0].RelFileNode)
		if err != nil {
			t.Fatalf("WALRelBlocks(%d): %v", rels[0].RelFileNode, err)
		}
		t.Logf("relation %q has %d block refs in the window", rels[0].RelName, len(relBlocks))
	}

	// Per-record-type breakdown of the busiest rmgr (the summary table).
	typeStats, err := c.WALRecordTypeStats(ctx, db, start, end, stats[0].Name)
	if err != nil {
		t.Fatalf("WALRecordTypeStats(%s): %v", stats[0].Name, err)
	}
	for _, s := range typeStats {
		t.Logf("type %-24s count=%-6d rec=%-9d fpi=%-9d combined=%d", s.Name, s.Count, s.RecordSize, s.FPISize, s.CombinedSize)
	}

	// Records of the busiest rmgr.
	recs, err := c.WALRecords(ctx, db, start, end, stats[0].Name)
	if err != nil {
		t.Fatalf("WALRecords(%s): %v", stats[0].Name, err)
	}
	t.Logf("rmgr %s has %d records", stats[0].Name, len(recs))
	if len(recs) == 0 {
		return
	}
	// Find a record with an FPI to exercise block info.
	probe := recs[0]
	for _, r := range recs {
		if r.FPILength > 0 {
			probe = r
			break
		}
	}
	t.Logf("probe record %s (type=%s end=%s fpi=%d)", probe.StartLSN, probe.RecordType, probe.EndLSN, probe.FPILength)
	blocks, err := c.WALBlocks(ctx, db, probe.StartLSN, probe.EndLSN)
	if err != nil {
		t.Fatalf("WALBlocks: %v", err)
	}
	for _, b := range blocks {
		tid, _ := b.HeapTID()
		t.Logf("block id=%d rel=%q(%d)/%s blk=%d tid=%s data=%d fpi=%d info=%v desc=%q",
			b.BlockID, b.RelName, b.RelFileNode, b.ForkName(), b.BlockNumber, tid, b.BlockDataLength, b.FPILength, b.FPIInfo, b.Description)
	}
}
