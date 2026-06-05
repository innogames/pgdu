package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// EnsureWALInspect makes sure pg_walinspect is installed in db. Mirrors
// EnsurePageInspect: returns *MissingExtensionError when missing so the TUI
// can offer an interactive install. Note the functions additionally require
// superuser or pg_read_server_files at execution time — that surfaces later
// as an ordinary permission error from the query itself, not here.
func (c *Client) EnsureWALInspect(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pg_walinspect", c.walInspectReady)
}

// WALWindow resolves the [start, end] LSN window the inspector analyses: the
// most recent windowBytes of WAL up to the current write position. Built-ins
// only, so it works without pg_walinspect.
func (c *Client) WALWindow(ctx context.Context, db string, windowBytes int64) (start, end string, err error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", "", err
	}
	if err := pool.QueryRow(ctx, sqlWALWindow, windowBytes).Scan(&start, &end); err != nil {
		return "", "", fmt.Errorf("resolve wal window in %q: %w", db, err)
	}
	return start, end, nil
}

// WALOverview reads the header snapshot. A failure here is non-fatal to the
// caller — the rmgr breakdown can still render — because pg_ls_waldir and
// pg_stat_wal need a monitoring role the user might lack even when they can
// run pg_walinspect. The resolved window is filled in by the caller.
func (c *Client) WALOverview(ctx context.Context, db string) (WALSummary, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return WALSummary{}, err
	}
	var s WALSummary
	if err := pool.QueryRow(ctx, sqlWALSummary).Scan(
		&s.InsertLSN, &s.FlushLSN, &s.CurrentFile, &s.WalLevel,
		&s.SegmentFiles, &s.SegmentBytes,
		&s.StatRecords, &s.StatFPI, &s.StatBytes,
	); err != nil {
		return WALSummary{}, fmt.Errorf("wal summary in %q: %w", db, err)
	}
	return s, nil
}

// WALRmgrStats returns the per-resource-manager byte breakdown for the window
// [start, end]. Requires pg_walinspect (gated by EnsureWALInspect).
func (c *Client) WALRmgrStats(ctx context.Context, db, start, end string) ([]WALRmgrStat, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("wal rmgr stats in %q", db), sqlWALRmgrStats, []any{start, end},
		func(row pgx.CollectableRow) (WALRmgrStat, error) {
			var r WALRmgrStat
			err := row.Scan(&r.Name, &r.Count, &r.RecordSize, &r.FPISize, &r.CombinedSize)
			return r, err
		})
}

// WALRecordTypeStats returns the per-record-type byte/count breakdown for one
// resource manager within the window [start, end] — the data behind the
// summary table above the records list. Reuses WALRmgrStat (Name holds the
// "Rmgr/RecordType" label). Requires pg_walinspect (gated by EnsureWALInspect).
func (c *Client) WALRecordTypeStats(ctx context.Context, db, start, end, rmgr string) ([]WALRmgrStat, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("wal record-type stats in %q", db), sqlWALRecordTypeStats, []any{start, end, rmgr},
		func(row pgx.CollectableRow) (WALRmgrStat, error) {
			var r WALRmgrStat
			err := row.Scan(&r.Name, &r.Count, &r.RecordSize, &r.FPISize, &r.CombinedSize)
			return r, err
		})
}

// WALRecords lists the individual records of one resource manager within the
// window [start, end], in chronological (LSN) order.
func (c *Client) WALRecords(ctx context.Context, db, start, end, rmgr string) ([]WALRecord, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("wal records in %q", db), sqlWALRecords, []any{start, end, rmgr},
		func(row pgx.CollectableRow) (WALRecord, error) {
			var r WALRecord
			err := row.Scan(
				&r.StartLSN, &r.EndLSN, &r.PrevLSN, &r.Xid,
				&r.Rmgr, &r.RecordType,
				&r.RecordLength, &r.MainDataLength, &r.FPILength,
				&r.Description, &r.BlockRef,
			)
			return r, err
		})
}

// WALBlocks lists the block references of the single record spanning
// [start, end) — the record's own start/end LSN — via pg_get_wal_block_info
// (PostgreSQL 16+). On a 15-series server the function does not exist and the
// query errors, which is surfaced to the user.
func (c *Client) WALBlocks(ctx context.Context, db, start, end string) ([]WALBlockRef, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("wal block info at %s in %q", start, db), sqlWALBlocks, []any{start, end},
		func(row pgx.CollectableRow) (WALBlockRef, error) {
			var b WALBlockRef
			err := row.Scan(
				&b.BlockID, &b.RelTablespace, &b.RelDatabase, &b.RelFileNode,
				&b.ForkNumber, &b.BlockNumber, &b.Rmgr, &b.RecordType,
				&b.BlockDataLength, &b.FPILength, &b.FPIInfo, &b.Description, &b.RelName,
				&b.IsToast, &b.DBName,
			)
			return b, err
		})
}
