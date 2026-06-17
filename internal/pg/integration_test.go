package pg

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"

	"pgdu/internal/cli"
)

// Runs the full database → schemas → tables → parts → bloat chain.
// Skipped unless PGDU_TEST_DSN is set, e.g.
//
//	PGDU_TEST_DSN=postgres://postgres:pw@127.0.0.1:55432/shop?sslmode=disable go test ./internal/pg
func TestIntegration_FullChain(t *testing.T) {
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
	host := u.Hostname()
	user := u.User.Username()
	pw, _ := u.User.Password()
	db := u.Path
	if len(db) > 0 && db[0] == '/' {
		db = db[1:]
	}

	cfg := cli.Config{Host: host, Port: port, User: user, Password: pw, Database: db, SSLMode: u.Query().Get("sslmode")}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}

	c := New(cfg)
	defer c.Close()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	dbs, err := c.ListDatabases(ctx)
	if err != nil || len(dbs) == 0 {
		t.Fatalf("ListDatabases: err=%v len=%d", err, len(dbs))
	}

	schemas, err := c.ListSchemas(ctx, db)
	if err != nil || len(schemas) == 0 {
		t.Fatalf("ListSchemas: err=%v len=%d", err, len(schemas))
	}

	// Find the public schema (seeded by hand).
	var pubFound bool
	for _, s := range schemas {
		if s.Name == "public" {
			pubFound = true
			break
		}
	}
	if !pubFound {
		t.Fatalf("public schema not in results: %+v", schemas)
	}

	tables, err := c.ListTables(ctx, db, "public")
	if err != nil || len(tables) == 0 {
		t.Fatalf("ListTables: err=%v len=%d", err, len(tables))
	}

	// Run bloat probe + fill on the largest table.
	t0 := tables[0]
	mode, err := c.ProbeBloat(ctx, db)
	if err != nil {
		t.Fatalf("ProbeBloat: %v", err)
	}
	t.Logf("bloat mode for %q: %d", db, mode)

	parts, err := c.TableParts(ctx, t0)
	if err != nil || len(parts) == 0 {
		t.Fatalf("TableParts(%s): err=%v", t0.Qualified(), err)
	}
	if err := c.FillBloat(ctx, t0, parts); err != nil {
		t.Fatalf("FillBloat: %v", err)
	}

	// The heap must have HasBloat=true after FillBloat, regardless of mode.
	if !parts[0].HasBloat || parts[0].Kind != PartHeap {
		t.Fatalf("expected heap with bloat populated, got %+v", parts[0])
	}
	t.Logf("heap size=%d wasted=%d", parts[0].SizeBytes, parts[0].WastedBytes)

	// Per-column space estimate must come back with at least one row and the
	// largest column's est_bytes should be > 0 on a non-empty seeded table.
	cols, err := c.ListColumns(ctx, t0)
	if err != nil || len(cols) == 0 {
		t.Fatalf("ListColumns: err=%v len=%d", err, len(cols))
	}
	if cols[0].EstBytes <= 0 {
		t.Fatalf("expected positive est_bytes on top column, got %+v", cols[0])
	}
	t.Logf("top column %s (%s) est=%d avg_width=%d null_frac=%.2f",
		cols[0].Name, cols[0].Type, cols[0].EstBytes, cols[0].AvgWidth, cols[0].NullFrac)

	// B-tree deep-dive: find a btree index in public and exercise the
	// key-column + metapage queries that back the page-inspector banners.
	// pageinspect (and superuser, for bt_metap) may be absent on the test DSN,
	// so those steps log-and-skip rather than fail.
	rels, err := c.ListRelations(ctx, db, "public")
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	var idx *Relation
	for i := range rels {
		if rels[i].Kind == RelBTreeIndex {
			idx = &rels[i]
			break
		}
	}
	if idx == nil {
		t.Log("no btree index in public; skipping index deep-dive checks")
		return
	}

	// Key columns need no special privilege and must come back for any index.
	keyCols, err := c.IndexKeyColumns(ctx, *idx)
	if err != nil {
		t.Fatalf("IndexKeyColumns(%s): %v", idx.Qualified(), err)
	}
	var nKey int
	for _, k := range keyCols {
		if k.IsKey {
			nKey++
		}
	}
	if nKey == 0 {
		t.Fatalf("expected ≥1 key column for %s, got %+v", idx.Qualified(), keyCols)
	}
	t.Logf("index %s key columns: %+v", idx.Qualified(), keyCols)

	// bt_metap requires pageinspect + superuser; a failure is a skip, not a fail.
	meta, err := c.BtreeMeta(ctx, *idx)
	if err != nil {
		t.Logf("BtreeMeta(%s) unavailable (%v); skipping metapage check", idx.Qualified(), err)
		return
	}
	if meta.Level < 0 {
		t.Fatalf("nonsensical tree height %d for %s", meta.Level, idx.Qualified())
	}
	t.Logf("index %s metapage: root blk %d height %d dedup=%v v%d",
		idx.Qualified(), meta.Root, meta.Level, meta.AllEqualImage, meta.Version)
}
