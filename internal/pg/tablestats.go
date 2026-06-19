package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TableMaintStats returns the maintenance snapshot for t, or (nil, nil) when
// the relation no longer exists (e.g., dropped while pgdu was open).
func (c *Client) TableMaintStats(ctx context.Context, t Table) (*TableMaintStats, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	var s TableMaintStats
	err = pool.QueryRow(ctx, sqlTableMaintStats, t.OID).Scan(
		&s.NLive, &s.NDead,
		&s.LastVacuum, &s.LastAutovacuum,
		&s.LastAnalyze, &s.LastAutoanalyze,
		&s.VacuumCount, &s.AutovacuumCount,
		&s.AnalyzeCount, &s.AutoanalyzeCount,
		&s.NModSinceAnalyze, &s.NInsSinceVacuum,
		&s.LastSeqScan, &s.LastIdxScan,
		&s.SeqScans, &s.IdxScans,
		&s.RelTuples, &s.FrozenXIDAge, &s.RelKind, &s.RelOptions,
		&s.AvacEnabled,
		&s.AvacVacuumThreshold, &s.AvacVacuumScale,
		&s.AvacInsertThreshold, &s.AvacInsertScale,
		&s.AvacAnalyzeThreshold, &s.AvacAnalyzeScale,
		&s.FreezeMaxAge,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("maint stats for %q.%q: %w", t.Schema, t.Name, err)
	}
	return &s, nil
}

// TableHotStats holds the cumulative (since the last stats reset) update
// counters pg_stat_user_tables tracks for one relation, used to show its HOT
// (heap-only tuple) update ratio. A HOT update reuses free space on the same
// page and touches no index, so a high ratio means cheap updates; a low one
// points at index write amplification (FILLFACTOR / over-indexing candidates).
type TableHotStats struct {
	Updates    int64 // n_tup_upd: all row updates
	HotUpdates int64 // n_tup_hot_upd: the subset that were HOT
}

// NonHotUpdates is the count of updates that had to touch every index.
func (s TableHotStats) NonHotUpdates() int64 { return s.Updates - s.HotUpdates }

// HotRatio returns the HOT update percentage (0–100); ok is false when the
// table has recorded no updates, leaving the ratio undefined.
func (s TableHotStats) HotRatio() (float64, bool) {
	if s.Updates <= 0 {
		return 0, false
	}
	return 100 * float64(s.HotUpdates) / float64(s.Updates), true
}

// TableHotStats fetches the update / HOT-update counters for the relation named
// name (optionally schema-qualified) from pg_stat_user_tables, or (nil, nil)
// when the name doesn't resolve to a stats-tracked user table. The
// statement-detail view uses it to show a query's main-table HOT update ratio.
func (c *Client) TableHotStats(ctx context.Context, db, name string) (*TableHotStats, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	var s TableHotStats
	err = pool.QueryRow(ctx, sqlTableHotStats, name).Scan(&s.Updates, &s.HotUpdates)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hot stats for %q in %q: %w", name, db, err)
	}
	return &s, nil
}
