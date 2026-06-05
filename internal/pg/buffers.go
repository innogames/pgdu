package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ExtensionStatus reports whether ext is installed in db, and (if not)
// whether it's available on the server so CREATE EXTENSION would succeed
// given sufficient privileges.
type ExtensionStatus struct {
	Installed bool
	Available bool
}

// ProbeExtension queries pg_extension / pg_available_extensions for one
// optional extension.
func (c *Client) ProbeExtension(ctx context.Context, db, ext string) (ExtensionStatus, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return ExtensionStatus{}, err
	}
	var s ExtensionStatus
	if err := pool.QueryRow(ctx, sqlExtensionProbe, ext).Scan(&s.Installed, &s.Available); err != nil {
		return ExtensionStatus{}, fmt.Errorf("probe extension %s: %w", ext, err)
	}
	return s, nil
}

// CreateExtension runs `CREATE EXTENSION IF NOT EXISTS <ext>` in db. The
// extension name is identifier-quoted but not free-form — callers must pass
// a known constant (e.g. "pg_buffercache", "pgstattuple"), never user input.
func (c *Client) CreateExtension(ctx context.Context, db, ext string) error {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return err
	}
	// CREATE EXTENSION doesn't accept parameters; we trust the caller to pass
	// a constant identifier. quoteIdent guards against any accidental injection.
	stmt := "CREATE EXTENSION IF NOT EXISTS " + quoteIdent(ext)
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create extension %s: %w", ext, err)
	}
	c.mu.Lock()
	// Mark the just-installed extension ready so the matching Ensure* skips its
	// probe; ready maps mirror the ones ensureExtension caches into.
	if ready := map[string]map[string]bool{
		"pg_buffercache":     c.bufCacheReady,
		"pageinspect":        c.pageInspectReady,
		"pg_walinspect":      c.walInspectReady,
		"pg_stat_statements": c.statStatementsReady,
	}[ext]; ready != nil {
		ready[db] = true
	}
	if ext == "pgstattuple" {
		// Force ProbeBloat to re-evaluate on next call.
		delete(c.bloatProbed, db)
	}
	c.mu.Unlock()
	return nil
}

// EnsureBufferCache makes sure pg_buffercache is installed in db. When the
// extension is missing we return a *MissingExtensionError so the TUI can
// offer the user an interactive install instead of guessing — previous
// versions of this code auto-ran CREATE EXTENSION, which silently masked
// permission problems and surprised people who didn't realise pgdu was
// taking that liberty.
func (c *Client) EnsureBufferCache(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_buffercache", c.bufCacheReady)
}

// TableBufferStats lists per-table shared-buffer footprint and cumulative
// hit/read counters for one schema. Buffer footprint sums heap + toast +
// indexes — the natural answer to "which table is using the cache".
func (c *Client) TableBufferStats(ctx context.Context, db, schema string) ([]TableBufferStat, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("table buffer stats in %q.%q", db, schema), sqlBufferStats, []any{schema},
		func(row pgx.CollectableRow) (TableBufferStat, error) {
			s := TableBufferStat{DB: db, Schema: schema}
			err := row.Scan(&s.OID, &s.Schema, &s.Name, &s.BufferedBytes, &s.TotalBytes, &s.Hits, &s.Reads)
			return s, err
		})
}

// BufferCacheSummary returns the cluster-wide shared_buffers occupancy split
// between the current database, anything else, and free pages.
func (c *Client) BufferCacheSummary(ctx context.Context, db string) (BufferCacheSummary, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return BufferCacheSummary{}, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return BufferCacheSummary{}, err
	}
	var s BufferCacheSummary
	if err := pool.QueryRow(ctx, sqlBufferCacheSummary).Scan(&s.TotalBytes, &s.ThisDBBytes, &s.OtherDBBytes); err != nil {
		return BufferCacheSummary{}, fmt.Errorf("buffer cache summary: %w", err)
	}
	return s, nil
}
