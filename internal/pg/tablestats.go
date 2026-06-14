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
