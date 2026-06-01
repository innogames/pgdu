package pg

import (
	"context"
	"fmt"
)

// EnsurePageInspect makes sure pageinspect is installed in db. Mirrors
// EnsureBufferCache: returns *MissingExtensionError when the extension is
// missing so the TUI can offer an interactive install instead of failing
// with an opaque error.
func (c *Client) EnsurePageInspect(ctx context.Context, db string) error {
	c.mu.Lock()
	if c.pageInspectReady[db] {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	st, err := c.ProbeExtension(ctx, db, "pageinspect")
	if err != nil {
		return err
	}
	if !st.Installed {
		return &MissingExtensionError{Extension: "pageinspect", DB: db, Installable: st.Available}
	}
	c.mu.Lock()
	c.pageInspectReady[db] = true
	c.mu.Unlock()
	return nil
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

// ListHeapPages returns up to `count` per-page summaries starting at `start`.
func (c *Client) ListHeapPages(ctx context.Context, t Table, start, count int32) ([]HeapPageStat, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := quoteIdent(t.Schema) + "." + quoteIdent(t.Name)

	// Clamp the window to the real relation size — get_raw_page errors hard
	// when asked for a block past EOF, and pg_class.relpages is the cheap
	// source of truth. relpages can be 0 (empty heap, or a partitioned-root
	// with no storage of its own), in which case we return an empty list
	// without issuing the page query at all.
	var relpages int32
	if err := pool.QueryRow(ctx, sqlRelPages, t.OID).Scan(&relpages); err != nil {
		return nil, fmt.Errorf("relpages for %q: %w", t.Qualified(), err)
	}
	if relpages <= 0 || start >= relpages {
		return nil, nil
	}
	if start+count > relpages {
		count = relpages - start
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
	regclass := quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
	sql := fmt.Sprintf(sqlTupleRow, regclass)
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

// ListHeapTuples returns the line-pointer array for one heap page. The page
// must exist (caller already saw it in ListHeapPages); a missing block here
// surfaces as a pageinspect error from the server.
func (c *Client) ListHeapTuples(ctx context.Context, t Table, blkno int32) ([]HeapTuple, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
	rows, err := pool.Query(ctx, sqlHeapTuples, regclass, blkno)
	if err != nil {
		return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
	}
	defer rows.Close()
	var out []HeapTuple
	for rows.Next() {
		var h HeapTuple
		if err := rows.Scan(
			&h.LP, &h.LPOff, &h.LPFlags, &h.LPLen,
			&h.Xmin, &h.Xmax, &h.Field3, &h.Ctid,
			&h.Infomask2, &h.Infomask, &h.Hoff,
			&h.Bits, &h.Oid, &h.Data,
		); err != nil {
			return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list heap tuples in %q page %d: %w", t.Qualified(), blkno, err)
	}
	return out, nil
}
