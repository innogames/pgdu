package pg

import (
	"context"
	"errors"
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
		"pg_qualstats":       c.qualstatsReady,
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

// UpdateExtension runs `ALTER EXTENSION <ext> UPDATE` in db, lifting an
// already-installed extension to the server's default version (e.g. a
// pg_upgrade leftover stuck at pg_stat_statements 1.6 → 1.11). Like
// CreateExtension, ext must be a known constant identifier, never user input.
// Requires extension-owner or superuser privileges; a permission failure
// surfaces as the wrapped error so the TUI can show it.
func (c *Client) UpdateExtension(ctx context.Context, db, ext string) error {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return err
	}
	stmt := "ALTER EXTENSION " + quoteIdent(ext) + " UPDATE"
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("update extension %s: %w", ext, err)
	}
	c.mu.Lock()
	// The installed version changed; drop the cached read so the next
	// statementsVersion re-queries it (and statementsQuery selects the new
	// column layout). Harmless for extensions without a version cache.
	delete(c.statStatementsVerKnown, db)
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
			err := row.Scan(&s.OID, &s.Schema, &s.Name, &s.BufferedBytes, &s.TotalBytes,
				&s.Hits, &s.Reads, &s.DirtyBytes, &s.UsageAvg)
			return s, err
		})
}

// TableBufferStatByOID returns the shared-buffer footprint (buffered/total/
// dirty bytes, cumulative hits/reads, mean usagecount) for one relation by OID,
// summing its heap, toast and indexes — the cache-footprint figures for the
// describe-table view. A relation with no buffered pages (or one that no longer
// exists) yields a zero-value stat and no error, so callers can render "not
// cached" without special-casing pgx.ErrNoRows.
func (c *Client) TableBufferStatByOID(ctx context.Context, db string, oid uint32) (TableBufferStat, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return TableBufferStat{}, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return TableBufferStat{}, err
	}
	s := TableBufferStat{DB: db, OID: oid}
	err = pool.QueryRow(ctx, sqlBufferStatByOID, oid).Scan(
		&s.OID, &s.Schema, &s.Name, &s.BufferedBytes, &s.TotalBytes,
		&s.Hits, &s.Reads, &s.DirtyBytes, &s.UsageAvg)
	if errors.Is(err, pgx.ErrNoRows) {
		return TableBufferStat{DB: db, OID: oid}, nil
	}
	if err != nil {
		return TableBufferStat{}, fmt.Errorf("table buffer stat for oid %d in %q: %w", oid, db, err)
	}
	return s, nil
}

// TableBufferUsageCounts returns the per-table clock-sweep temperature
// histogram (usagecount 0..5 → buffers/dirty/pinned) for one relation, summing
// its heap, toast and index filenodes. Always six rows (empty buckets included)
// so the caller can render a stable bar. Also returns the cluster block_size so
// the caller can express the (page-counted) histogram in bytes — and recompute
// the buffered/dirty footprint from this same fresh snapshot, keeping it
// consistent with the histogram rather than the older list-load snapshot.
// Powers the buffer-detail drill-down.
func (c *Client) TableBufferUsageCounts(ctx context.Context, db string, oid uint32) ([]BufferUsageCount, int64, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return nil, 0, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	var blockSize int64
	if err := pool.QueryRow(ctx, "SELECT current_setting('block_size')::bigint").Scan(&blockSize); err != nil {
		return nil, 0, fmt.Errorf("block size in %q: %w", db, err)
	}
	counts, err := collect(ctx, pool, fmt.Sprintf("table buffer usage counts for oid %d in %q", oid, db),
		sqlBufferTableUsageCounts, []any{oid},
		func(row pgx.CollectableRow) (BufferUsageCount, error) {
			var u BufferUsageCount
			err := row.Scan(&u.Count, &u.Buffers, &u.Dirty, &u.Pinned)
			return u, err
		})
	return counts, blockSize, err
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
	// The temperature histogram is supplementary — if pg_buffercache_usage_counts()
	// is somehow unavailable, keep the occupancy summary rather than failing it.
	if counts, err := collect(ctx, pool, "buffer usage counts", sqlBufferUsageCounts, nil,
		func(row pgx.CollectableRow) (BufferUsageCount, error) {
			var u BufferUsageCount
			err := row.Scan(&u.Count, &u.Buffers, &u.Dirty, &u.Pinned)
			return u, err
		}); err == nil {
		s.UsageCounts = counts
	}
	return s, nil
}

// ShmemAllocations returns the full Postgres shared-memory map from
// pg_shmem_allocations, biggest allocation first. No extension is needed, but
// the view is restricted to pg_read_all_stats / superuser — a permission error
// is wrapped and returned so the TUI can surface it rather than crash. The two
// NULL-name rows are classified into Anonymous / Free (see ShmemAllocation).
func (c *Client) ShmemAllocations(ctx context.Context, db string) ([]ShmemAllocation, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("shmem allocations in %q", db), sqlShmemAllocations, nil,
		func(row pgx.CollectableRow) (ShmemAllocation, error) {
			var (
				name *string
				off  *int64
				a    ShmemAllocation
			)
			if err := row.Scan(&name, &off, &a.Size, &a.AllocatedSize); err != nil {
				return ShmemAllocation{}, err
			}
			switch {
			case name != nil:
				a.Name = *name
			case off == nil:
				a.Anonymous = true // name NULL, off NULL
			default:
				a.Free = true // name NULL, off set: unused segment tail
			}
			if off != nil {
				a.Off = *off
			} else {
				a.Off = -1
			}
			return a, nil
		})
}
