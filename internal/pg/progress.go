package pg

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// ListProgress returns every operation currently reporting into a
// pg_stat_progress_* view. The views are cluster-wide, so db only selects the
// pool to query through; an empty result means nothing is in flight.
// Relations in other databases (which regclass can't see from db) are
// resolved best-effort through their own database's pool.
func (c *Client) ListProgress(ctx context.Context, db string) ([]ProgressRow, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("list progress in %q: %w", db, err)
	}
	rows, err := collect(ctx, pool, fmt.Sprintf("list progress in %q", db), sqlProgressOps, nil,
		func(row pgx.CollectableRow) (ProgressRow, error) {
			var r ProgressRow
			err := row.Scan(
				&r.PID, &r.Command, &r.Relation, &r.RelID, &r.Database, &r.Phase, &r.Unit,
				&r.Done, &r.Total, &r.Approx, &r.RunningMs, &r.Username,
			)
			return r, err
		})
	if err != nil {
		return nil, err
	}
	c.resolveProgressRelations(ctx, db, rows)
	return rows, nil
}

// resolveProgressRelations fills Relation for operations running in a
// different database than the one queried: regclass only sees the current
// database's catalog, so look those OIDs up through each operation's own
// pool (lazy, cached — the same pools every other per-database view uses).
// Best-effort by design: no CONNECT privilege, a connection failure, or a
// relation dropped mid-operation falls back to the bare OID digits.
func (c *Client) resolveProgressRelations(ctx context.Context, db string, rows []ProgressRow) {
	byDB := make(map[string][]int)
	for i, r := range rows {
		if r.Relation == "" && r.RelID != 0 && r.Database != "" && r.Database != db {
			byDB[r.Database] = append(byDB[r.Database], i)
		}
	}
	for other, idxs := range byDB {
		names := make(map[uint32]string, len(idxs))
		if pool, err := c.PoolFor(ctx, other); err == nil {
			oids := make([]uint32, 0, len(idxs))
			for _, i := range idxs {
				oids = append(oids, rows[i].RelID)
			}
			rs, err := pool.Query(ctx, sqlProgressRelNames, oids)
			if err == nil {
				var oid uint32
				var name string
				_, _ = pgx.ForEachRow(rs, []any{&oid, &name}, func() error {
					names[oid] = name
					return nil
				})
			}
		}
		for _, i := range idxs {
			if n, ok := names[rows[i].RelID]; ok {
				rows[i].Relation = n
			} else {
				rows[i].Relation = strconv.FormatUint(uint64(rows[i].RelID), 10)
			}
		}
	}
}
