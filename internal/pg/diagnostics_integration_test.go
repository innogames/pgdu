package pg

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"testing"

	"pgdu/internal/cli"
)

// diagTestClient builds a Client from PGDU_TEST_DSN (skipping the test when
// unset) and returns it with the DSN's database name. Mirrors the parsing in
// TestIntegration_FullChain; an empty host falls back to the Unix socket.
func diagTestClient(t *testing.T) (*Client, string) {
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
	user := ""
	pw := ""
	if u.User != nil {
		user = u.User.Username()
		pw, _ = u.User.Password()
	}
	db := u.Path
	if len(db) > 0 && db[0] == '/' {
		db = db[1:]
	}
	cfg := cli.Config{Host: u.Hostname(), Port: port, User: user, Password: pw, Database: db, SSLMode: u.Query().Get("sslmode")}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}
	c := New(cfg)
	t.Cleanup(c.Close)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return c, db
}

// Every registered diagnostic must execute cleanly and resolve its declared
// Bar/Sort/Kinds column names against the actual result columns — this is the
// SQL smoke test that keeps registry entries from bit-rotting.
func TestIntegration_AllDiagnostics(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()

	for _, d := range Diagnostics {
		t.Run(d.Key, func(t *testing.T) {
			res, err := c.RunDiagnostic(ctx, db, d)
			if err != nil {
				t.Fatalf("RunDiagnostic(%s): %v", d.Key, err)
			}
			if d.Bar != "" && res.BarCol < 0 {
				t.Errorf("bar column %q not in result columns %v", d.Bar, colNames(res.Columns))
			}
			if d.Sort != "" && res.SortCol < 0 {
				t.Errorf("sort column %q not in result columns %v", d.Sort, colNames(res.Columns))
			}
			for name, kind := range d.Kinds {
				found := false
				for _, col := range res.Columns {
					if col.Name == name {
						found = true
						if col.Kind != kind {
							t.Errorf("kind override for %q not applied: got %v want %v", name, col.Kind, kind)
						}
					}
				}
				if !found {
					t.Errorf("Kinds references column %q not in result columns %v", name, colNames(res.Columns))
				}
			}
		})
	}
}

func colNames(cols []DiagColumn) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// diagRowsMatching counts result rows whose cell in column colName equals want.
func diagRowsMatching(res *DiagResult, colName, want string) int {
	idx := -1
	for i, c := range res.Columns {
		if c.Name == colName {
			idx = i
		}
	}
	if idx < 0 {
		return 0
	}
	n := 0
	for _, row := range res.Rows {
		if idx < len(row) && row[idx].Display == want {
			n++
		}
	}
	return n
}

// The FK-without-index detector must flag an unindexed FK column and stop
// flagging it once a supporting index exists — including one whose leading
// columns are a permutation of a multi-column FK.
func TestIntegration_FKMissingIndex(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	setup := []string{
		`DROP SCHEMA IF EXISTS pgdu_diag_test CASCADE`,
		`CREATE SCHEMA pgdu_diag_test`,
		`CREATE TABLE pgdu_diag_test.parent (id int PRIMARY KEY)`,
		`CREATE TABLE pgdu_diag_test.child (
		     id int PRIMARY KEY,
		     parent_id int REFERENCES pgdu_diag_test.parent(id)
		 )`,
	}
	for _, q := range setup {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS pgdu_diag_test CASCADE`)
	})

	diag := diagByKey(t, "fk_missing_index")
	res, err := c.RunDiagnostic(ctx, db, diag)
	if err != nil {
		t.Fatalf("RunDiagnostic: %v", err)
	}
	if got := diagRowsMatching(res, "table_name", "child"); got != 1 {
		t.Fatalf("expected exactly 1 finding for unindexed child FK, got %d (rows: %d)", got, len(res.Rows))
	}

	// A covering index (FK column as the leading key) must clear the finding.
	if _, err := pool.Exec(ctx, `CREATE INDEX ON pgdu_diag_test.child (parent_id, id)`); err != nil {
		t.Fatalf("create index: %v", err)
	}
	res, err = c.RunDiagnostic(ctx, db, diag)
	if err != nil {
		t.Fatalf("RunDiagnostic after index: %v", err)
	}
	if got := diagRowsMatching(res, "table_name", "child"); got != 0 {
		t.Fatalf("indexed FK still flagged (%d rows)", got)
	}
}

// The redundant-prefix detector must flag a single-column index shadowed by a
// wider one with the same leading column, and leave unique / differently-rooted
// indexes alone.
func TestIntegration_IndexRedundantPrefix(t *testing.T) {
	c, db := diagTestClient(t)
	ctx := context.Background()
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	setup := []string{
		`DROP SCHEMA IF EXISTS pgdu_diag_test2 CASCADE`,
		`CREATE SCHEMA pgdu_diag_test2`,
		`CREATE TABLE pgdu_diag_test2.t (a int, b int, c int)`,
		`CREATE INDEX redundant_a ON pgdu_diag_test2.t (a)`,
		`CREATE INDEX wide_ab ON pgdu_diag_test2.t (a, b)`,
		// Not redundant: different leading column, and a unique index.
		`CREATE INDEX lead_b ON pgdu_diag_test2.t (b)`,
		`CREATE UNIQUE INDEX uniq_c ON pgdu_diag_test2.t (c)`,
		`CREATE INDEX wide_cb ON pgdu_diag_test2.t (c, b)`,
	}
	for _, q := range setup {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS pgdu_diag_test2 CASCADE`)
	})

	res, err := c.RunDiagnostic(ctx, db, diagByKey(t, "index_redundant_prefix"))
	if err != nil {
		t.Fatalf("RunDiagnostic: %v", err)
	}
	if got := diagRowsMatching(res, "redundant_index", "redundant_a"); got != 1 {
		t.Fatalf("expected redundant_a flagged once, got %d", got)
	}
	for _, name := range []string{"lead_b", "uniq_c", "wide_ab", "wide_cb"} {
		if got := diagRowsMatching(res, "redundant_index", name); got != 0 {
			t.Fatalf("%s wrongly flagged as redundant (%d rows)", name, got)
		}
	}
}

func diagByKey(t *testing.T, key string) Diagnostic {
	t.Helper()
	for _, d := range Diagnostics {
		if d.Key == key {
			return d
		}
	}
	t.Fatalf("diagnostic %q not registered", key)
	panic("unreachable")
}
