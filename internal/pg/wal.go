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
//
// It first tries the segment-aware sqlWALWindowClamped, which floors the start
// at the oldest segment still on disk so the window never names a recycled
// segment. That needs pg_ls_waldir (pg_monitor / superuser); a privilege error
// there is non-fatal — we fall back to the '0/0'-clamped sqlWALWindow, which
// can still trip "segment already removed" downstream but is the best we can do
// without directory access.
func (c *Client) WALWindow(ctx context.Context, db string, windowBytes int64) (start, end string, err error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return "", "", err
	}
	if err := pool.QueryRow(ctx, sqlWALWindowClamped, windowBytes).Scan(&start, &end); err == nil {
		return start, end, nil
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
		&s.StatRecords, &s.StatFPI, &s.StatBytes, &s.StatBuffersFull,
	); err != nil {
		return WALSummary{}, fmt.Errorf("wal summary in %q: %w", db, err)
	}
	return s, nil
}

// WALCheckpoint reads the checkpoint context for the WAL header. Built-ins only
// (no pg_walinspect gate) so it renders for any monitoring role — but its three
// sources degrade independently: pg_control_checkpoint / pg_stat_checkpointer
// may need superuser, and an older server lacks pg_stat_checkpointer entirely.
// Each scan error is swallowed (the field stays zero / the key stays absent),
// mirroring how Maintenance tolerates partial failure; only a pool-acquire
// failure is returned.
func (c *Client) WALCheckpoint(ctx context.Context, db string) (WALCheckpointInfo, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return WALCheckpointInfo{}, err
	}
	var info WALCheckpointInfo
	_ = pool.QueryRow(ctx, sqlWALCheckpoint).Scan(
		&info.BytesSinceCheckpoint, &info.MaxWALBytes, &info.CheckpointTime, &info.CheckpointTimeoutSec)
	_ = pool.QueryRow(ctx, sqlWALCheckpointer).Scan(&info.CheckpointsTimed, &info.CheckpointsRequested)
	info.Settings = settingsMap(ctx, pool, sqlWALSettings, walSettingsKeys)
	return info, nil
}

// WALRelStats returns the per-relation WAL byte/record breakdown for the window
// [start, end] — which tables/indexes generated the WAL. Requires pg_walinspect
// (gated by EnsureWALInspect) and PostgreSQL 16+ (pg_get_wal_block_info).
func (c *Client) WALRelStats(ctx context.Context, db, start, end string) ([]WALRelStat, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := collect(ctx, pool, fmt.Sprintf("wal rel stats in %q", db), sqlWALRelStats, []any{start, end},
		func(row pgx.CollectableRow) (WALRelStat, error) {
			var r WALRelStat
			err := row.Scan(
				&r.RelDatabase, &r.RelTablespace, &r.RelFileNode, &r.DataBytes, &r.FPIBytes,
				&r.RecCount, &r.BlockCount, &r.OtherForkCount,
				&r.RelName, &r.IsToast, &r.DBName,
			)
			return r, err
		})
	if err != nil {
		return nil, err
	}
	c.resolveForeignFilenodes(ctx, len(rows), func(i int) *walRelRef {
		r := &rows[i]
		return &walRelRef{DBName: r.DBName, Tablespace: r.RelTablespace, Filenode: r.RelFileNode, RelName: &r.RelName, IsToast: &r.IsToast}
	})
	return rows, nil
}

// walRelRef is the resolver's view of one row: where the relation file lives
// and where to write the name once it is known.
type walRelRef struct {
	DBName     string
	Tablespace uint32
	Filenode   uint32
	RelName    *string
	IsToast    *bool
}

// resolveForeignFilenodes fills in names the main query could not: WAL is
// cluster-wide but pg_filenode_relation is database-local, so a filenode from
// another database only resolves through a connection *to* that database. The
// client already keeps a lazy pool per database, so each foreign db costs one
// connection and one query. Strictly best effort — a database that refuses the
// connection (datallowconn=false, missing grant) or a dropped relation simply
// keeps its numeric fallback; nothing here can fail the caller.
func (c *Client) resolveForeignFilenodes(ctx context.Context, n int, ref func(i int) *walRelRef) {
	type key struct{ ts, fn uint32 }
	byDB := map[string]map[key][]*walRelRef{}
	for i := range n {
		r := ref(i)
		// DBName "" is a shared relation (reldatabase 0) — no db to ask.
		if *r.RelName != "" || r.DBName == "" {
			continue
		}
		if byDB[r.DBName] == nil {
			byDB[r.DBName] = map[key][]*walRelRef{}
		}
		k := key{r.Tablespace, r.Filenode}
		byDB[r.DBName][k] = append(byDB[r.DBName][k], r)
	}
	for db, refs := range byDB {
		pool, err := c.PoolFor(ctx, db)
		if err != nil {
			continue
		}
		tss := make([]uint32, 0, len(refs))
		fns := make([]uint32, 0, len(refs))
		for k := range refs {
			tss = append(tss, k.ts)
			fns = append(fns, k.fn)
		}
		rows, err := pool.Query(ctx, sqlWALResolveFilenodes, tss, fns)
		if err != nil {
			continue
		}
		for rows.Next() {
			var fn uint32
			var name string
			var toast bool
			if rows.Scan(&fn, &name, &toast) != nil {
				continue
			}
			for k, rs := range refs {
				if k.fn != fn {
					continue
				}
				for _, r := range rs {
					*r.RelName, *r.IsToast = name, toast
				}
			}
		}
		rows.Close()
	}
}

// WALRelBlocks lists every block reference of one relation across the window
// [start, end], full-page-image-heaviest first — the drill-down behind a
// WALRelStat row. Requires pg_walinspect and PostgreSQL 16+.
func (c *Client) WALRelBlocks(ctx context.Context, db, start, end string, relfilenode uint32) ([]WALBlockRef, error) {
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	blocks, err := collect(ctx, pool, fmt.Sprintf("wal rel blocks in %q", db), sqlWALRelBlocks, []any{start, end, int64(relfilenode)}, scanWALBlockRef)
	if err != nil {
		return nil, err
	}
	c.resolveBlockRefNames(ctx, blocks)
	return blocks, nil
}

// resolveBlockRefNames runs the foreign-database name pass over block refs.
func (c *Client) resolveBlockRefNames(ctx context.Context, blocks []WALBlockRef) {
	c.resolveForeignFilenodes(ctx, len(blocks), func(i int) *walRelRef {
		b := &blocks[i]
		return &walRelRef{DBName: b.DBName, Tablespace: b.RelTablespace, Filenode: b.RelFileNode, RelName: &b.RelName, IsToast: &b.IsToast}
	})
}

// scanWALBlockRef scans one pg_get_wal_block_info row — shared by the
// per-relation and per-record block listings.
func scanWALBlockRef(row pgx.CollectableRow) (WALBlockRef, error) {
	var b WALBlockRef
	err := row.Scan(
		&b.StartLSN, &b.EndLSN, &b.BlockID, &b.RelTablespace, &b.RelDatabase, &b.RelFileNode,
		&b.ForkNumber, &b.BlockNumber, &b.Rmgr, &b.RecordType,
		&b.BlockDataLength, &b.FPILength, &b.FPIInfo, &b.Description, &b.RelName,
		&b.IsToast, &b.DBName,
	)
	return b, err
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
	return collect(ctx, pool, fmt.Sprintf("wal rmgr stats in %q", db), sqlWALRmgrStats, []any{start, end}, scanWALRmgrStat)
}

// scanWALRmgrStat scans one (name, count, record/FPI/combined size) stats row —
// shared by the per-rmgr and per-record-type breakdowns.
func scanWALRmgrStat(row pgx.CollectableRow) (WALRmgrStat, error) {
	var r WALRmgrStat
	err := row.Scan(&r.Name, &r.Count, &r.RecordSize, &r.FPISize, &r.CombinedSize)
	return r, err
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
	return collect(ctx, pool, fmt.Sprintf("wal record-type stats in %q", db), sqlWALRecordTypeStats, []any{start, end, rmgr}, scanWALRmgrStat)
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
	blocks, err := collect(ctx, pool, fmt.Sprintf("wal block info at %s in %q", start, db), sqlWALBlocks, []any{start, end}, scanWALBlockRef)
	if err != nil {
		return nil, err
	}
	c.resolveBlockRefNames(ctx, blocks)
	return blocks, nil
}
