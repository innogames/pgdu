package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TableParts returns the heap, the toast relation (if non-zero), and every
// index of a table as a slice of Parts. Sizes come from the parent Table row
// (we don't re-issue pg_relation_size for heap/toast). Bloat is filled in
// separately by FillBloat. The heap row carries optional HeapStats
// (n_live/n_dead/last_vacuum from pg_stat_all_tables) when available.
func (c *Client) TableParts(ctx context.Context, t Table) ([]Part, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	heap := Part{Kind: PartHeap, Name: "heap", SizeBytes: t.HeapBytes}
	var hs HeapStats
	err = pool.QueryRow(ctx, sqlHeapStats, t.OID).Scan(
		&hs.NLive, &hs.NDead,
		&hs.LastVacuum, &hs.LastAutovacuum,
		&hs.LastAnalyze, &hs.LastAutoanalyze,
	)
	switch {
	case err == nil:
		heap.HeapStats = &hs
	case errors.Is(err, pgx.ErrNoRows):
		// No stats row (e.g. matview) — leave HeapStats nil.
	default:
		return nil, fmt.Errorf("heap stats for %q.%q: %w", t.Schema, t.Name, err)
	}
	parts := []Part{heap}
	if t.ToastOID != 0 {
		parts = append(parts, Part{
			Kind:      PartToast,
			Name:      "toast",
			SizeBytes: t.ToastBytes,
			ToastName: t.ToastName,
		})
	}
	rows, err := pool.Query(ctx, sqlIndexes, t.OID)
	if err != nil {
		return nil, fmt.Errorf("list indexes for %q.%q: %w", t.Schema, t.Name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid uint32
		var p Part
		p.Kind = PartIndex
		if err := rows.Scan(&oid, &p.Name, &p.SizeBytes, &p.IsPrimary, &p.IsUnique, &p.AccessMethod); err != nil {
			return nil, fmt.Errorf("list indexes for %q.%q: %w", t.Schema, t.Name, err)
		}
		parts = append(parts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list indexes for %q.%q: %w", t.Schema, t.Name, err)
	}
	return parts, nil
}
