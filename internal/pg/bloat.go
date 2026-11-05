package pg

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProbeBloat decides which bloat path to use for a given database. The result
// is cached on the Client.
func (c *Client) ProbeBloat(ctx context.Context, db string) (BloatMode, error) {
	c.mu.Lock()
	if m, ok := c.bloatProbed[db]; ok {
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()

	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return BloatUnknown, err
	}
	var available bool
	if err := pool.QueryRow(ctx, sqlBloatProbe).Scan(&available); err != nil {
		return BloatUnknown, fmt.Errorf("probe bloat in %q: %w", db, err)
	}
	mode := BloatEstimate
	if available {
		mode = BloatExact
	}
	c.mu.Lock()
	c.bloatProbed[db] = mode
	c.mu.Unlock()
	return mode, nil
}

// bloatExactMaxBytes is the index size above which the page-reading statistics
// functions are skipped outright. pgstatindex and pgstattuple walk every page
// of the index — there is no visibility map to shorten the work, as there is
// for a heap — so a single call on a multi-hundred-GB index can block its part
// of the scan for minutes.
const bloatExactMaxBytes int64 = 50 * 1024 * 1024 * 1024

// bloatApproxDirtyMaxBytes is the heap equivalent, and it bounds the pages
// pgstattuple_approx would actually have to read rather than the relation's
// size: everything all-visible comes from the FSM (see sqlBloatHeapApprox).
// Sized against bloatPartTimeout at the rate a plain sequential read manages,
// so a relation that passes the gate can finish inside it.
const bloatApproxDirtyMaxBytes int64 = 2 * 1024 * 1024 * 1024

// bloatIndexExactMaxBytes is the index size above which a btree skips
// pgstatindex and takes the statistics estimate instead. pgstatindex walks
// every page: a 7 GB index measures in tens of seconds, far past
// bloatPartTimeout, so the exact path there only ever buys a blank cell after
// a long wait. The threshold is chosen so an index that can plausibly finish
// inside the timeout still gets a real measurement.
const bloatIndexExactMaxBytes int64 = 2 * 1024 * 1024 * 1024

const (
	// bloatPartTimeout bounds one measurement. A statement that overruns it
	// gives up alone: the part can still fall back to an estimate, and the
	// rest of the table's scan is untouched.
	bloatPartTimeout = 10 * time.Second
	// bloatFanout is how many measurements run at once. These functions read
	// pages, so it is deliberately the same modest number the schema-health
	// sweep uses: enough that a table with twenty indexes doesn't cost twenty
	// sequential round-trips, without stampeding a server that is already busy.
	bloatFanout = 3
)

// FillBloat populates WastedBytes/HasBloat/BloatEstimated on each part of a
// table. Parts are measured concurrently and independently, and each one
// degrades on its own: heap and btree fall back from the page scan to a
// statistics estimate (BloatEstimated), and where there is no estimator either
// — toast, a non-btree index, an error on both paths — that one part stays
// unmeasured (HasBloat false) while the others keep their figures, so the view
// degrades row by row instead of all at once. The returned error covers only
// the per-database setup — a failure to connect or to probe the extension.
func (c *Client) FillBloat(ctx context.Context, t Table, parts []Part) error {
	mode, err := c.ProbeBloat(ctx, t.DB)
	if err != nil {
		return err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return err
	}
	qualified := qualifiedIdent(t.Schema, t.Name)

	sem := make(chan struct{}, bloatFanout)
	var wg sync.WaitGroup
	for i := range parts {
		wg.Add(1)
		// Each goroutine owns exactly one element of parts, so the writes
		// don't need a lock; wg.Wait is the only synchronisation needed before
		// the caller reads the slice back.
		go func(p *Part) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fillPartBloat(ctx, pool, t, qualified, mode, p)
		}(&parts[i])
	}
	wg.Wait()
	return nil
}

// bloatQuery runs one measurement under bloatPartTimeout. The bound is per
// statement rather than per part because a part that gives up on the page scan
// still has a fallback to run, and it needs a live context to run it on.
func bloatQuery(ctx context.Context, pool *pgxpool.Pool, stmt string, out *int64, args ...any) error {
	qctx, cancel := context.WithTimeout(ctx, bloatPartTimeout)
	defer cancel()
	return pool.QueryRow(qctx, stmt, args...).Scan(out)
}

// fillPartBloat measures one part. Every failure is silent by design: the
// result is "no number", which the parts view renders as blank cells and
// bloatScanLabel counts as unscanned.
func fillPartBloat(ctx context.Context, pool *pgxpool.Pool, t Table, qualified string, mode BloatMode, p *Part) {
	switch p.Kind {
	case PartHeap:
		if mode == BloatExact {
			if err := bloatQuery(ctx, pool, sqlBloatHeapApprox, &p.WastedBytes, qualified, bloatApproxDirtyMaxBytes); err == nil {
				p.HasBloat = true
				return
			}
			// Fall through: a refused or failed sample still leaves the
			// statistics estimate worth showing, flagged as an estimate.
		}
		if err := bloatQuery(ctx, pool, sqlBloatHeapEstimate, &p.WastedBytes, t.OID); err == nil {
			p.HasBloat, p.BloatEstimated = true, true
		}
	case PartToast:
		// A toast table is a heap, so it takes the same sample under the same
		// gate — and on the tables where toast dominates the row it is usually
		// the part that passes it most easily, being written once and then
		// left alone until a vacuum marks it all-visible. There is no estimate
		// behind it the way there is for the heap: ANALYZE does not collect
		// pg_stats for toast relations, so sqlBloatHeapEstimate would have no
		// column widths to work from and would read the whole relation as
		// bloat. Unmeasured is the honest answer there.
		if mode != BloatExact {
			return
		}
		toastRef := fmt.Sprintf("pg_toast.pg_toast_%d", t.OID)
		if err := bloatQuery(ctx, pool, sqlBloatHeapApprox, &p.WastedBytes, toastRef, bloatApproxDirtyMaxBytes); err == nil {
			p.HasBloat = true
		}
	case PartIndex:
		indexRef := qualifiedIdent(t.Schema, p.Name)
		btree := p.AccessMethod == "btree"
		// Without pgstattuple there is nothing to read, and above either size
		// cap the read would cost more than the answer is worth: pgstatindex
		// and pgstattuple both walk the whole index, so a big one burns the
		// timeout and comes back empty. btree has an estimate to fall back on
		// below; everything else stays blank, which is honest — where the flat
		// 10 % guess this used to emit sorted, summed into the Σ row and armed
		// REINDEX exactly like a measured value.
		if mode == BloatExact && p.SizeBytes <= bloatExactMaxBytes &&
			(!btree || p.SizeBytes <= bloatIndexExactMaxBytes) {
			// btree has a density in its metapage-level statistics; everything
			// else has to be walked. Both are real measurements, so neither is
			// marked as an estimate.
			stmt := sqlBloatIndexFull
			if btree {
				stmt = sqlBloatIndex
			}
			if err := bloatQuery(ctx, pool, stmt, &p.WastedBytes, indexRef); err == nil {
				p.HasBloat = true
			}
		}
		if !p.HasBloat && btree {
			// Same fallback the heap takes, for the same reason: an order of
			// magnitude under a "~" beats a blank cell. The estimate reads
			// catalogs only, so it costs nothing even when the walk above was
			// skipped for being too expensive.
			if err := bloatQuery(ctx, pool, sqlBloatIndexEstimate, &p.WastedBytes, indexRef); err == nil {
				p.HasBloat, p.BloatEstimated = true, true
			}
		}
		if p.AccessMethod == "gin" {
			// Cheap (metapage only) and only meaningful for GIN, so it is read
			// here rather than given a probe of its own.
			_ = bloatQuery(ctx, pool, sqlGinPendingPages, &p.GinPendingPages, indexRef)
		}
	}
}

// TableBloat is one table's estimated wasted bytes for the tables list.
// CanEstimate false means the inputs were missing (never analyzed, or a
// partitioned parent with no storage of its own), so WastedBytes says nothing
// and the row renders blank rather than clean.
type TableBloat struct {
	OID         uint32
	Name        string
	WastedBytes int64
	CanEstimate bool
}

// TableBloatEstimates returns a statistics-only bloat estimate for every table
// in one schema. It reads pg_class and pg_stats only — no page sampling — so it
// is cheap enough to chain off the tables list, unlike the pgstattuple scan
// FillBloat runs once a single table is open. The figures are estimates and the
// caller must mark them as such.
func (c *Client) TableBloatEstimates(ctx context.Context, db, schema string) ([]TableBloat, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("estimate table bloat in %q.%q", db, schema),
		sqlBloatTablesEstimate, []any{schema},
		func(row pgx.CollectableRow) (TableBloat, error) {
			var t TableBloat
			err := row.Scan(&t.OID, &t.Name, &t.CanEstimate, &t.WastedBytes)
			return t, err
		})
}
