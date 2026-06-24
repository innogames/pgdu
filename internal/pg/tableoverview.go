package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListTableStats returns one TableStat per base / partitioned / materialized
// table in db.schema, with size, write/scan activity, cache-hit counters,
// maintenance counters and storage options gathered in a single query. Result
// is ordered by total size; the TUI re-sorts by the active column.
func (c *Client) ListTableStats(ctx context.Context, db, schema string) ([]TableStat, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("list table stats in %q.%q", db, schema), sqlTableStats, []any{schema},
		func(row pgx.CollectableRow) (TableStat, error) {
			t := TableStat{DB: db, Schema: schema}
			err := row.Scan(
				&t.OID, &t.Name, &t.RelKind, &t.RelPersistence, &t.RelOptions,
				&t.EstRows, &t.ToastOID, &t.ToastName,
				&t.HeapBytes, &t.IndexesBytes, &t.ToastBytes, &t.TotalBytes,
				&t.FrozenXIDAge,
				&t.NLive, &t.NDead,
				&t.NInsert, &t.NUpdate, &t.NDelete, &t.NHotUpdate,
				&t.NModSinceAnalyze, &t.NInsSinceVacuum,
				&t.SeqScan, &t.IdxScan, &t.SeqTupRead, &t.IdxTupFetch,
				&t.VacuumCount, &t.AutovacuumCount, &t.AnalyzeCount, &t.AutoanalyzeCount,
				&t.VacAgeMs, &t.AnaAgeMs,
				&t.HeapBlksRead, &t.HeapBlksHit, &t.IdxBlksRead, &t.IdxBlksHit,
			)
			return t, err
		})
}
