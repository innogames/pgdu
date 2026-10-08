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

// RelationBufferStats lists the shared-buffer footprint and cumulative
// hit/read counters of every storage relation of the tables in one schema:
// heap, TOAST (heap + its index) and each index, one row apiece with Rel set.
// RollUpBufferStats folds them into the per-table view.
func (c *Client) RelationBufferStats(ctx context.Context, db, schema string) ([]TableBufferStat, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("relation buffer stats in %q.%q", db, schema),
		sqlBufferRelStats, []any{schema}, scanBufferRelStat(db))
}

// scanBufferRelStat scans one sqlBufferRelStats row.
func scanBufferRelStat(db string) func(pgx.CollectableRow) (TableBufferStat, error) {
	return func(row pgx.CollectableRow) (TableBufferStat, error) {
		s := TableBufferStat{DB: db, Rel: &BufferRel{}}
		var kind string
		err := row.Scan(&s.OID, &s.Schema, &s.Name, &kind, &s.Rel.OID, &s.Rel.Name, &s.Rel.AccessMethod,
			&s.BufferedBytes, &s.TotalBytes, &s.Hits, &s.Reads, &s.DirtyBytes, &s.UsageAvg)
		switch kind {
		case "toast":
			s.Rel.Kind = PartToast
		case "index":
			s.Rel.Kind = PartIndex
		default:
			s.Rel.Kind = PartHeap
		}
		return s, err
	}
}

// RollUpBufferStats folds per-relation rows into one row per table, in the
// order tables first appear. Bytes and I/O counters sum; the mean usagecount is
// re-weighted by buffered bytes, which reconstructs the pooled per-buffer mean
// exactly since every buffer is one block.
func RollUpBufferStats(rels []TableBufferStat) []TableBufferStat {
	at := make(map[uint32]int, len(rels))
	out := make([]TableBufferStat, 0, len(rels))
	var usage []float64
	for _, r := range rels {
		i, ok := at[r.OID]
		if !ok {
			i = len(out)
			at[r.OID] = i
			out = append(out, TableBufferStat{DB: r.DB, Schema: r.Schema, Name: r.Name, OID: r.OID})
			usage = append(usage, 0)
		}
		t := &out[i]
		t.BufferedBytes += r.BufferedBytes
		t.TotalBytes += r.TotalBytes
		t.Hits += r.Hits
		t.Reads += r.Reads
		t.DirtyBytes += r.DirtyBytes
		usage[i] += r.UsageAvg * float64(r.BufferedBytes)
	}
	for i := range out {
		if out[i].BufferedBytes > 0 {
			out[i].UsageAvg = usage[i] / float64(out[i].BufferedBytes)
		}
	}
	return out
}

// TableBufferStatByOID returns the shared-buffer footprint (buffered/total/
// dirty bytes, cumulative hits/reads, mean usagecount) for one table by OID,
// summing its heap, toast and indexes — the cache-footprint figures for the
// describe-table view. A relation with no buffered pages (or one that no longer
// exists) yields a zero-value stat and no error, so callers can render "not
// cached" without special-casing an empty result.
func (c *Client) TableBufferStatByOID(ctx context.Context, db string, oid uint32) (TableBufferStat, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return TableBufferStat{}, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return TableBufferStat{}, err
	}
	rels, err := collect(ctx, pool, fmt.Sprintf("table buffer stat for oid %d in %q", oid, db),
		sqlBufferRelStatsByOID, []any{oid}, scanBufferRelStat(db))
	if err != nil {
		return TableBufferStat{}, err
	}
	if tabs := RollUpBufferStats(rels); len(tabs) > 0 {
		return tabs[0], nil
	}
	return TableBufferStat{DB: db, OID: oid}, nil
}

// TableBufferUsageCounts returns the clock-sweep temperature histogram
// (usagecount 0..5 → buffers/dirty/pinned) for one relation: with whole set,
// the table's heap, toast and index filenodes together; otherwise oid's alone
// (a TOAST heap with its index). Always six rows (empty buckets included)
// so the caller can render a stable bar. Also returns the cluster block_size so
// the caller can express the (page-counted) histogram in bytes — and recompute
// the buffered/dirty footprint from this same fresh snapshot, keeping it
// consistent with the histogram rather than the older list-load snapshot.
// Powers the buffer-detail drill-down.
func (c *Client) TableBufferUsageCounts(ctx context.Context, db string, oid uint32, whole bool) ([]BufferUsageCount, int64, error) {
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
	counts, err := collect(ctx, pool, fmt.Sprintf("buffer usage counts for oid %d in %q", oid, db),
		sqlBufferTableUsageCounts, []any{oid, whole}, scanBufferUsageCount)
	return counts, blockSize, err
}

// scanBufferUsageCount scans one (usagecount, buffers, dirty, pinned) histogram
// row — shared by the per-table and cluster-wide temperature queries.
func scanBufferUsageCount(row pgx.CollectableRow) (BufferUsageCount, error) {
	var u BufferUsageCount
	err := row.Scan(&u.Count, &u.Buffers, &u.Dirty, &u.Pinned)
	return u, err
}

// PageBuffers maps the blocks of one main-fork window of a relation to their
// shared-buffers state (usagecount, dirty). Blocks not currently cached are
// absent from the map. Powers the page inspector's per-page temperature
// column; callers treat failures as "no temperature data" rather than fatal.
func (c *Client) PageBuffers(ctx context.Context, db string, oid uint32, start, count int32) (map[int64]PageBuffer, error) {
	if err := c.EnsureBufferCache(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collectMap(ctx, pool, fmt.Sprintf("page buffers for oid %d in %q", oid, db), sqlPageBuffers, []any{oid, start, count},
		func(row pgx.CollectableRow) (int64, PageBuffer, error) {
			var blkno int64
			var b PageBuffer
			err := row.Scan(&blkno, &b.UsageCount, &b.Dirty)
			return blkno, b, err
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
	// The temperature histogram is supplementary — if pg_buffercache_usage_counts()
	// is somehow unavailable, keep the occupancy summary rather than failing it.
	if counts, err := collect(ctx, pool, "buffer usage counts", sqlBufferUsageCounts, nil, scanBufferUsageCount); err == nil {
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
