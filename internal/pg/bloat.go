package pg

import (
	"context"
	"fmt"
	"sync"
	"time"

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

// bloatExactMaxBytes is the relation size above which we skip the page-reading
// statistics functions and fall back to the cheap estimate (or to nothing).
// pgstattuple_approx samples enough pages that its runtime scales with
// relation size, and pgstatindex walks the whole index — a single call on a
// multi-hundred-GB relation can block its part of the scan for minutes.
const bloatExactMaxBytes int64 = 50 * 1024 * 1024 * 1024

const (
	// bloatPartTimeout bounds one part's measurement. A part that overruns it
	// stays unmeasured and renders blank cells; it no longer takes the rest of
	// the table's scan with it.
	bloatPartTimeout = 10 * time.Second
	// bloatFanout is how many measurements run at once. These functions read
	// pages, so it is deliberately the same modest number the schema-health
	// sweep uses: enough that a table with twenty indexes doesn't cost twenty
	// sequential round-trips, without stampeding a server that is already busy.
	bloatFanout = 3
)

// FillBloat populates WastedBytes/HasBloat/BloatEstimated on each part of a
// table. Parts are measured concurrently and independently: an error, a
// timeout or an access method with no estimator leaves that one part
// unmeasured (HasBloat false) and the others intact, so the view degrades row
// by row instead of all at once. The returned error covers only the
// per-database setup — a failure to connect or to probe the extension.
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
			pctx, cancel := context.WithTimeout(ctx, bloatPartTimeout)
			defer cancel()
			fillPartBloat(pctx, pool, t, qualified, mode, p)
		}(&parts[i])
	}
	wg.Wait()
	return nil
}

// fillPartBloat measures one part. Every failure is silent by design: the
// result is "no number", which the parts view renders as blank cells and
// bloatScanLabel counts as unscanned.
func fillPartBloat(ctx context.Context, pool *pgxpool.Pool, t Table, qualified string, mode BloatMode, p *Part) {
	switch p.Kind {
	case PartHeap:
		if mode == BloatExact && p.SizeBytes <= bloatExactMaxBytes {
			if err := pool.QueryRow(ctx, sqlBloatHeapApprox, qualified).Scan(&p.WastedBytes); err == nil {
				p.HasBloat = true
				return
			}
			// Fall through: a refused or failed sample still leaves the
			// statistics estimate worth showing, flagged as an estimate.
		}
		if err := pool.QueryRow(ctx, sqlBloatHeapEstimate, t.OID).Scan(&p.WastedBytes); err == nil {
			p.HasBloat, p.BloatEstimated = true, true
		}
	case PartToast:
		// Toast is opaque without pgstattuple. Above the size cap the sample
		// would block the scan, and toast bloat is rarely actionable anyway
		// (vacuum reclaims it), so it simply goes unmeasured.
		if mode != BloatExact || p.SizeBytes > bloatExactMaxBytes {
			return
		}
		toastRef := fmt.Sprintf("pg_toast.pg_toast_%d", t.OID)
		if err := pool.QueryRow(ctx, sqlBloatHeapApprox, toastRef).Scan(&p.WastedBytes); err == nil {
			p.HasBloat = true
		}
	case PartIndex:
		// btree is the only access method with a density pgdu can read without
		// a full scan (pgstatindex). GIN, GiST, BRIN, SP-GiST and hash would
		// need pgstattuple over the whole index, so they stay unmeasured — a
		// blank cell is honest, where the flat 10 % guess this used to emit
		// sorted, summed into the Σ row and armed REINDEX exactly like a
		// measured value.
		if mode != BloatExact || p.AccessMethod != "btree" || p.SizeBytes > bloatExactMaxBytes {
			return
		}
		indexRef := qualifiedIdent(t.Schema, p.Name)
		if err := pool.QueryRow(ctx, sqlBloatIndex, indexRef).Scan(&p.WastedBytes); err == nil {
			p.HasBloat = true
		}
	}
}
