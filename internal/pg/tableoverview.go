package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListTableStats returns one TableStat per base / partitioned / materialized
// table in db.schema, with size, write/scan activity, cache-hit counters,
// maintenance counters and storage options gathered in a single query. Result
// is ordered by total size; the TUI re-sorts by the active column.
//
// Two best-effort extras never fail the load: the current shared-buffer
// footprint per table (pg_buffercache — extension, needs pg_monitor; absent →
// BufsKnown stays false) and the database's stats_reset timestamp dating the
// cumulative counters (zero when unavailable or never reset).
func (c *Client) ListTableStats(ctx context.Context, db, schema string) ([]TableStat, time.Time, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, time.Time{}, err
	}
	rows, err := collect(ctx, pool, fmt.Sprintf("list table stats in %q.%q", db, schema), sqlTableStats, []any{schema},
		func(row pgx.CollectableRow) (TableStat, error) {
			t := TableStat{DB: db, Schema: schema}
			err := row.Scan(
				&t.OID, &t.Name, &t.RelKind, &t.RelOptions,
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
	if err != nil {
		return nil, time.Time{}, err
	}

	type bufRow struct {
		oid             uint32
		buffered, dirty int64
	}
	bufs := collectBestEffort(ctx, pool, sqlTableStatsBuffers, []any{schema},
		func(r pgx.Rows) (bufRow, bool) {
			var b bufRow
			return b, r.Scan(&b.oid, &b.buffered, &b.dirty) == nil
		})
	if bufs != nil {
		byOID := make(map[uint32]bufRow, len(bufs))
		for _, b := range bufs {
			byOID[b.oid] = b
		}
		// Every table in the schema gets a row from the LEFT JOIN, so a merged
		// zero is a real "nothing cached", not missing data.
		for i := range rows {
			if b, ok := byOID[rows[i].OID]; ok {
				rows[i].BufferedBytes = b.buffered
				rows[i].DirtyBytes = b.dirty
				rows[i].BufsKnown = true
			}
		}
	}

	var reset time.Time
	var resetPtr *time.Time
	if pool.QueryRow(ctx, sqlMaintTableStatsReset).Scan(&resetPtr) == nil && resetPtr != nil {
		reset = *resetPtr
	}
	return rows, reset, nil
}
