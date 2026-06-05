package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsurePageInspect makes sure pageinspect is installed in db. Mirrors
// EnsureBufferCache: returns *MissingExtensionError when the extension is
// missing so the TUI can offer an interactive install instead of failing
// with an opaque error.
func (c *Client) EnsurePageInspect(ctx context.Context, db string) error {
	return c.ensureExtension(ctx, db, "pageinspect", c.pageInspectReady)
}

// RelPages returns pg_class.relpages for a table — used to clamp the
// page-window the user is scrolling through so we never call get_raw_page
// past EOF. ANALYZE-accurate; close enough for clamping without taking the
// exclusive lock pg_relation_size_blocks would need.
func (c *Client) RelPages(ctx context.Context, t Table) (int32, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return 0, err
	}
	var n int32
	if err := pool.QueryRow(ctx, sqlRelPages, t.OID).Scan(&n); err != nil {
		return 0, fmt.Errorf("relpages for %q: %w", t.Qualified(), err)
	}
	return n, nil
}

// clampPageWindow caps the half-open window [start, start+count) to the
// relation's real page count, since get_raw_page / bt_page_stats error hard
// past EOF. relpages comes from sqlRelPages (pg_class — ANALYZE-accurate, no
// exclusive lock). ok is false when the window is entirely past EOF or the
// relation has fewer than minPages browsable pages (1 for a heap; 2 for an
// index, whose block 0 is the un-listable meta page). On ok the adjusted count
// is returned.
func (c *Client) clampPageWindow(ctx context.Context, pool *pgxpool.Pool, oid uint32, qualified string, start, count, minPages int32) (int32, bool, error) {
	var relpages int32
	if err := pool.QueryRow(ctx, sqlRelPages, oid).Scan(&relpages); err != nil {
		return 0, false, fmt.Errorf("relpages for %q: %w", qualified, err)
	}
	if relpages < minPages || start >= relpages {
		return 0, false, nil
	}
	if start+count > relpages {
		count = relpages - start
	}
	return count, true, nil
}

// ListHeapPages returns up to `count` per-page summaries starting at `start`.
func (c *Client) ListHeapPages(ctx context.Context, t Table, start, count int32) ([]HeapPageStat, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)

	count, ok, err := c.clampPageWindow(ctx, pool, t.OID, t.Qualified(), start, count, 1)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	rows, err := pool.Query(ctx, sqlHeapPagesSummary, regclass, start, count)
	if err != nil {
		return nil, fmt.Errorf("list heap pages in %q: %w", t.Qualified(), err)
	}
	defer rows.Close()
	var out []HeapPageStat
	for rows.Next() {
		var p HeapPageStat
		if err := rows.Scan(
			&p.Blkno, &p.LSN, &p.Lower, &p.Upper, &p.Special, &p.PageSize, &p.Flags,
			&p.FreeBytes,
			&p.LiveLP, &p.RedirectLP, &p.DeadLP, &p.UnusedLP,
			&p.LiveBytes, &p.DeadBytes, &p.HotUpdated, &p.HasExternal,
		); err != nil {
			return nil, fmt.Errorf("list heap pages in %q: %w", t.Qualified(), err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list heap pages in %q: %w", t.Qualified(), err)
	}
	return out, nil
}

// ListTupleRow returns the column-by-column decoding of one heap row,
// identified by ctid. Used by the row-detail view the user reaches by
// pressing Enter on a NORMAL line pointer. Returns an empty slice (not an
// error) when the ctid points to a row that's gone — e.g. the tuple was
// updated or vacuumed after the page snapshot was taken.
func (c *Client) ListTupleRow(ctx context.Context, t Table, ctid string) ([]TupleCell, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)
	tmpl := sqlTupleRow
	if t.Schema == "pg_toast" {
		// TOAST tables lack a composite type so row_to_json fails; use the
		// fixed-column query instead.
		tmpl = sqlToastTupleRow
	}
	sql := fmt.Sprintf(tmpl, regclass)
	rows, err := pool.Query(ctx, sql, ctid)
	if err != nil {
		return nil, fmt.Errorf("read tuple in %q ctid %s: %w", t.Qualified(), ctid, err)
	}
	defer rows.Close()
	var out []TupleCell
	for rows.Next() {
		var c TupleCell
		if err := rows.Scan(&c.Name, &c.Value); err != nil {
			return nil, fmt.Errorf("read tuple in %q ctid %s: %w", t.Qualified(), ctid, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tuple in %q ctid %s: %w", t.Qualified(), ctid, err)
	}
	return out, nil
}

// ReadToastValue fetches all chunks for one out-of-line value from a TOAST
// table, assembles them in chunk_seq order, and returns a small slice of
// TupleCell rows suitable for the row-detail view:
//
//	chunk_id   – the OID of the out-of-line value
//	chunks     – number of chunks stored on disk
//	total_bytes – assembled size in bytes
//	data       – hex-encoded assembled bytes (truncated at 2 048 bytes)
func (c *Client) ReadToastValue(ctx context.Context, t Table, chunkID uint32) ([]TupleCell, error) {
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)
	sql := fmt.Sprintf(sqlToastValueChunks, regclass)
	rows, err := pool.Query(ctx, sql, chunkID)
	if err != nil {
		return nil, fmt.Errorf("read toast value in %q chunk %d: %w", t.Qualified(), chunkID, err)
	}
	defer rows.Close()

	type chunk struct {
		seq  int32
		data []byte
	}
	var chunks []chunk
	for rows.Next() {
		var ch chunk
		if err := rows.Scan(&ch.seq, &ch.data); err != nil {
			return nil, fmt.Errorf("read toast value in %q chunk %d: %w", t.Qualified(), chunkID, err)
		}
		chunks = append(chunks, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read toast value in %q chunk %d: %w", t.Qualified(), chunkID, err)
	}

	var assembled []byte
	for _, ch := range chunks {
		assembled = append(assembled, ch.data...)
	}

	const maxHexBytes = 2048
	var hexData string
	if len(assembled) <= maxHexBytes {
		hexData = fmt.Sprintf(`\x%x`, assembled)
	} else {
		hexData = fmt.Sprintf(`\x%x…`, assembled[:maxHexBytes])
	}

	str := func(s string) *string { return &s }
	return []TupleCell{
		{Name: "chunk_id", Value: str(fmt.Sprintf("%d", chunkID))},
		{Name: "chunks", Value: str(fmt.Sprintf("%d", len(chunks)))},
		{Name: "total_bytes", Value: str(fmt.Sprintf("%d", len(assembled)))},
		{Name: "data", Value: str(hexData)},
	}, nil
}

// ListIndexPages returns up to `count` per-page summaries of a B-tree index
// starting at `start`. Block 0 is the index meta page; bt_page_stats errors
// on it, so the SQL clamps the window to start at 1 internally — callers
// can pass start=0 without surprising failure.
func (c *Client) ListIndexPages(ctx context.Context, r Relation, start, count int32) ([]IndexPageStat, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)

	// minPages=2: an index's block 0 is the meta page, which bt_page_stats
	// can't summarise, so a one-page index has nothing browsable.
	count, ok, err := c.clampPageWindow(ctx, pool, r.OID, r.Qualified(), start, count, 2)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	rows, err := pool.Query(ctx, sqlIndexPagesSummary, regclass, start, count)
	if err != nil {
		return nil, fmt.Errorf("list index pages in %q: %w", r.Qualified(), err)
	}
	defer rows.Close()
	var out []IndexPageStat
	for rows.Next() {
		var p IndexPageStat
		if err := rows.Scan(
			&p.Blkno, &p.Type,
			&p.LiveItems, &p.DeadItems,
			&p.AvgItemSize, &p.PageSize, &p.FreeSize,
			&p.BtpoPrev, &p.BtpoNext, &p.BtpoLevel, &p.BtpoFlags,
		); err != nil {
			return nil, fmt.Errorf("list index pages in %q: %w", r.Qualified(), err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list index pages in %q: %w", r.Qualified(), err)
	}
	return out, nil
}

// ListIndexTuples returns the items on one B-tree page via bt_page_items.
// Caller already saw the page in ListIndexPages and passes its type
// ('l'/'r'/'i'/'d'); a missing block here surfaces as a pageinspect
// error from the server.
//
// For leaf and (single-page) root pages whose items point at heap rows,
// each row also gets a Decoded column projected from the parent table —
// the user sees the actual key value (e.g. "(42,alice)") instead of a
// hex blob. Internal-page downlinks and DEAD/empty entries return
// Decoded = nil; the renderer falls back to the raw hex `data`.
func (c *Client) ListIndexTuples(ctx context.Context, r Relation, blkno int32, pageType string) ([]IndexTuple, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)

	sql := sqlIndexTuples
	// Only leaf and root pages carry heap ctids worth decoding; internal
	// pages store downlinks (child block addresses) that would either
	// miss the heap entirely or — worse — match an unrelated row by
	// coincidence. Skip the heap join in those cases. A single-page
	// index is type 'r' (root) and is also effectively a leaf.
	if (pageType == "l" || pageType == "r") && r.ParentOID != 0 && r.ParentName != "" {
		var exprs string
		// Fetching the expression list per call avoids a stale cache when
		// the index is redefined under us. It's a one-shot pg_index lookup
		// — cheap next to the per-row heap fetches below.
		if err := pool.QueryRow(ctx, sqlIndexExprList, r.OID).Scan(&exprs); err == nil && exprs != "" {
			parent := qualifiedIdent(r.Schema, r.ParentName)
			sql = fmt.Sprintf(sqlIndexTuplesDecoded, exprs, parent)
		}
	}

	rows, err := pool.Query(ctx, sql, regclass, blkno)
	if err != nil {
		return nil, fmt.Errorf("list index tuples in %q page %d: %w", r.Qualified(), blkno, err)
	}
	defer rows.Close()
	var out []IndexTuple
	for rows.Next() {
		var it IndexTuple
		if err := rows.Scan(&it.ItemOffset, &it.Ctid, &it.ItemLen, &it.Nulls, &it.Vars, &it.Data, &it.Decoded); err != nil {
			return nil, fmt.Errorf("list index tuples in %q page %d: %w", r.Qualified(), blkno, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list index tuples in %q page %d: %w", r.Qualified(), blkno, err)
	}
	return out, nil
}

// ListHeapTuples returns the line-pointer array for one heap page. The page
// must exist (caller already saw it in ListHeapPages); a missing block here
// surfaces as a pageinspect error from the server.
//
// For TOAST tables (t.Schema == "pg_toast") the query also joins back into the
// toast relation to project chunk_id/chunk_seq per live row; each HeapTuple's
// ChunkID/ChunkSeq fields are populated only in that case.
func (c *Client) ListHeapTuples(ctx context.Context, t Table, blkno int32) ([]HeapTuple, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)

	isToast := t.Schema == "pg_toast"
	var rows pgx.Rows
	var queryErr error
	if isToast {
		sql := fmt.Sprintf(sqlToastTuples, regclass)
		rows, queryErr = pool.Query(ctx, sql, regclass, blkno)
	} else {
		rows, queryErr = pool.Query(ctx, sqlHeapTuples, regclass, blkno)
	}
	if queryErr != nil {
		return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, queryErr)
	}
	defer rows.Close()
	var out []HeapTuple
	for rows.Next() {
		var h HeapTuple
		if isToast {
			if err := rows.Scan(
				&h.LP, &h.LPOff, &h.LPFlags, &h.LPLen,
				&h.Xmin, &h.Xmax, &h.Field3, &h.Ctid,
				&h.Infomask2, &h.Infomask, &h.Hoff,
				&h.Bits, &h.Oid, &h.Data,
				&h.ChunkID, &h.ChunkSeq,
			); err != nil {
				return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
			}
		} else {
			if err := rows.Scan(
				&h.LP, &h.LPOff, &h.LPFlags, &h.LPLen,
				&h.Xmin, &h.Xmax, &h.Field3, &h.Ctid,
				&h.Infomask2, &h.Infomask, &h.Hoff,
				&h.Bits, &h.Oid, &h.Data,
			); err != nil {
				return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
			}
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
	}
	return out, nil
}
