package pg

import (
	"context"
	"fmt"
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

// bloatExactMaxBytes is the relation size above which we skip
// pgstattuple_approx and fall back to the cheap estimate (or skip
// altogether for TOAST). pgstattuple_approx samples enough pages that its
// runtime scales with relation size — a single call on a multi-hundred-GB
// relation can block the parts view for minutes.
const bloatExactMaxBytes int64 = 50 * 1024 * 1024 * 1024

// FillBloat populates WastedBytes/HasBloat on each part of a table.
// For the heap, runs pgstattuple_approx or the estimate query.
// For each index, runs pgstatindex (when available) or a 10% heuristic.
// Toast bloat is reported via the toast table itself when pgstattuple is
// available; otherwise left at zero.
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

	for i := range parts {
		p := &parts[i]
		switch p.Kind {
		case PartHeap:
			useExact := mode == BloatExact && p.SizeBytes <= bloatExactMaxBytes
			if useExact {
				if err := pool.QueryRow(ctx, sqlBloatHeapApprox, qualified).Scan(&p.WastedBytes); err != nil {
					return fmt.Errorf("heap bloat (approx) %s: %w", qualified, err)
				}
			} else {
				if err := pool.QueryRow(ctx, sqlBloatHeapEstimate, t.OID).Scan(&p.WastedBytes); err != nil {
					p.WastedBytes = 0
				}
			}
			p.HasBloat = true
		case PartToast:
			// Toast is opaque without pgstattuple. Leave zero in estimate
			// mode, and above the size cap — pgstattuple_approx on a
			// multi-hundred-GB toast blocks the whole scan, and toast bloat
			// is rarely actionable anyway (vacuum reclaims it).
			if mode == BloatExact && p.SizeBytes <= bloatExactMaxBytes {
				toastRef := fmt.Sprintf("pg_toast.pg_toast_%d", t.OID)
				if err := pool.QueryRow(ctx, sqlBloatHeapApprox, toastRef).Scan(&p.WastedBytes); err == nil {
					p.HasBloat = true
				}
			}
		case PartIndex:
			indexRef := qualifiedIdent(t.Schema, p.Name)
			if mode == BloatExact && p.AccessMethod == "btree" {
				if err := pool.QueryRow(ctx, sqlBloatIndex, indexRef).Scan(&p.WastedBytes); err == nil {
					p.HasBloat = true
				}
			} else {
				if err := pool.QueryRow(ctx, sqlBloatIndexEstimate, indexRef).Scan(&p.WastedBytes); err == nil {
					p.HasBloat = true
				}
			}
		}
	}
	return nil
}
