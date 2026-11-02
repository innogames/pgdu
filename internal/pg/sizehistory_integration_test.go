package pg

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"pgdu/internal/cli"
)

// sizeTestClient builds a client from PGDU_TEST_DSN, skipping when it is unset.
func sizeTestClient(t *testing.T) *Client {
	t.Helper()
	dsn := os.Getenv("PGDU_TEST_DSN")
	if dsn == "" {
		t.Skip("PGDU_TEST_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 5432
	}
	pw, _ := u.User.Password()
	db := u.Path
	if len(db) > 0 && db[0] == '/' {
		db = db[1:]
	}
	cfg := cli.Config{
		Host: u.Hostname(), Port: port, User: u.User.Username(),
		Password: pw, Database: db, SSLMode: u.Query().Get("sslmode"),
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}
	c := New(cfg)
	t.Cleanup(c.Close)
	return c
}

// Exercises the capture SQL against a real server — the one thing the unit
// tests cannot cover, since sqlSizeSample is a const string until a server
// parses it.
func TestIntegration_CaptureSizes(t *testing.T) {
	c := sizeTestClient(t)
	ctx := context.Background()

	s, err := c.CaptureSizes(ctx, "", 0)
	if err != nil {
		t.Fatalf("CaptureSizes: %v", err)
	}
	if s.RowCount != len(s.Rows) {
		t.Errorf("RowCount %d != len(Rows) %d", s.RowCount, len(s.Rows))
	}
	if len(s.Rows) == 0 {
		t.Fatal("no relations captured — the test database looks empty")
	}
	if s.Database == "" || s.Target == "" {
		t.Errorf("identity missing: target %q db %q", s.Target, s.Database)
	}
	if s.DatabaseOID == 0 {
		t.Error("DatabaseOID = 0; pg_database is readable by any role that can connect")
	}
	if s.BlockSize != 8192 {
		t.Errorf("BlockSize = %d, want 8192 on a default build", s.BlockSize)
	}
	// SystemID degrades to 0 for a role without EXECUTE on pg_control_system,
	// so it is logged rather than asserted.
	t.Logf("SystemID = %d (0 means the read was not permitted)", s.SystemID)

	for _, r := range s.Rows {
		if r.Schema == "" || r.Name == "" {
			t.Fatalf("row without identity: %+v", r)
		}
		if r.OID == 0 {
			t.Fatalf("row without oid: %+v", r)
		}
		switch r.RelKind {
		case "r", "m", "p":
		default:
			t.Fatalf("unexpected relkind %q on %s", r.RelKind, r.Key())
		}
		if r.TotalBytes < r.HeapBytes {
			t.Errorf("%s: total %d < heap %d", r.Key(), r.TotalBytes, r.HeapBytes)
		}
		// A partitioned parent holds no storage of its own.
		if r.RelKind == "p" && r.RelFilenode != 0 {
			t.Errorf("%s: partitioned parent has relfilenode %d", r.Key(), r.RelFilenode)
		}
	}

	// Round trip through the on-disk format.
	dir := t.TempDir()
	path, err := SaveSizeSample(dir, s)
	if err != nil {
		t.Fatalf("SaveSizeSample: %v", err)
	}
	metas, err := ListSizeSamples(dir)
	if err != nil || len(metas) != 1 {
		t.Fatalf("ListSizeSamples: %v / %d", err, len(metas))
	}
	if metas[0].RowCount != len(s.Rows) || metas[0].TotalBytes != s.TotalBytes {
		t.Errorf("header disagrees with the sample: %+v", metas[0])
	}
	back, err := LoadSizeSample(path)
	if err != nil {
		t.Fatalf("LoadSizeSample: %v", err)
	}
	if len(back.Rows) != len(s.Rows) {
		t.Errorf("round trip lost rows: %d != %d", len(back.Rows), len(s.Rows))
	}

	// Two captures back to back still diff — they are only flagged as a short
	// window — which is the cheapest real test of that path.
	s2, err := c.CaptureSizes(ctx, "", 0)
	if err != nil {
		t.Fatalf("CaptureSizes (second): %v", err)
	}
	switch immediate, err := DiffSizes(s, s2); {
	case err != nil:
		t.Errorf("DiffSizes of two immediate captures: %v, want the diff anyway", err)
	case !immediate.Window.ShortWindow:
		t.Error("two immediate captures were not flagged as a short window")
	}

	// A synthetic window over the same two captures must diff cleanly, which
	// exercises the matcher over real catalog data.
	s2.CapturedAt = s.CapturedAt.Add(24 * time.Hour)
	res, err := DiffSizes(s, s2)
	if err != nil {
		t.Fatalf("DiffSizes: %v", err)
	}
	if len(res.Rows) < len(s.Rows) {
		t.Errorf("diff dropped rows: %d < %d", len(res.Rows), len(s.Rows))
	}
	for _, r := range res.Rows {
		if r.Tag != GrowthSteady {
			t.Errorf("%s tagged %v between two captures seconds apart", r.Qualified(), r.Tag)
		}
	}
	// Every scope must fold real data without losing bytes.
	for _, sc := range []GrowthScope{GrowthBySchema, GrowthByDatabase, GrowthByPartition} {
		folded := RollUpGrowth(res.Rows, sc)
		var leaf, rolled int64
		for _, r := range res.Rows {
			leaf += r.EndBytes
		}
		for _, r := range folded {
			rolled += r.EndBytes
		}
		if leaf != rolled {
			t.Errorf("scope %d: folded bytes %d != leaf bytes %d", sc, rolled, leaf)
		}
	}
}

// A size floor must not change the sample's reported total.
func TestIntegration_CaptureSizesMinBytes(t *testing.T) {
	c := sizeTestClient(t)
	ctx := context.Background()

	full, err := c.CaptureSizes(ctx, "", 0)
	if err != nil {
		t.Fatalf("CaptureSizes: %v", err)
	}
	floored, err := c.CaptureSizes(ctx, "", 1<<20)
	if err != nil {
		t.Fatalf("CaptureSizes with a floor: %v", err)
	}
	dir := t.TempDir()
	if _, err := SaveSizeSample(dir, full); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := SaveSizeSample(dir, floored); err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(floored.Rows) > len(full.Rows) {
		t.Errorf("the floor kept more rows than no floor: %d > %d", len(floored.Rows), len(full.Rows))
	}
	if floored.OtherCount != len(full.Rows)-len(floored.Rows) {
		t.Errorf("OtherCount %d does not account for the %d folded rows",
			floored.OtherCount, len(full.Rows)-len(floored.Rows))
	}
	// Sizes drift a little between two captures, so compare with tolerance.
	diff := full.TotalBytes - floored.TotalBytes
	if diff < 0 {
		diff = -diff
	}
	if full.TotalBytes > 0 && diff*100/full.TotalBytes > 5 {
		t.Errorf("floored total %d differs from full total %d by more than 5%%",
			floored.TotalBytes, full.TotalBytes)
	}
}
