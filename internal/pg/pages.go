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
	// Only leaf pages carry heap ctids worth decoding; internal pages store
	// downlinks (child block addresses) that would either miss the heap
	// entirely or — worse — match an unrelated row by coincidence, printing
	// bogus keys. Skip the heap join in those cases. Type 'r' reaches here only
	// for a single-page index, whose root is also a leaf: the caller maps a
	// taller tree's (internal) root to 'i' first (see indexTuplePageType), so a
	// non-leaf root never takes the decode path.
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

// BtreeMeta reads the B-tree metapage for the index page-list banner (root
// block, tree height, dedup-capable). Best-effort: callers render the page list
// regardless of whether this succeeds, so a permission/version error here just
// hides the banner line. Requires pageinspect (already ensured by the page
// loader that calls this).
func (c *Client) BtreeMeta(ctx context.Context, r Relation) (BtreeMeta, error) {
	var m BtreeMeta
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return m, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := pool.QueryRow(ctx, sqlBtreeMeta, regclass).Scan(
		&m.Magic, &m.Version, &m.Root, &m.Level, &m.FastRoot, &m.FastLevel, &m.AllEqualImage,
	); err != nil {
		return m, fmt.Errorf("bt_metap for %q: %w", r.Qualified(), err)
	}
	return m, nil
}

// BtreePageType returns one B-tree page's bt_page_stats type ('l' leaf, 'r'
// root, 'i' internal, 'd' deleted). Used to resolve a child page's type when
// the user descends through an internal-page downlink (the parent page only
// gave us the child's block number, not its role). Best-effort at the call
// site: a failure leaves the page type unknown and the tuple loader falls back
// to the raw, non-decoded path.
func (c *Client) BtreePageType(ctx context.Context, r Relation, blkno int32) (string, error) {
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return "", err
	}
	var t string
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := pool.QueryRow(ctx, sqlBtreePageType, regclass, blkno).Scan(&t); err != nil {
		return "", fmt.Errorf("bt_page_stats type for %q page %d: %w", r.Qualified(), blkno, err)
	}
	return t, nil
}

// IndexKeyColumns returns the index's columns in definition order, split into
// key vs INCLUDE columns. Used to render the "keys: (…) include: (…)" banner
// above the index page/tuple views.
func (c *Client) IndexKeyColumns(ctx context.Context, r Relation) ([]IndexKeyColumn, error) {
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, sqlIndexKeyColumns, r.OID)
	if err != nil {
		return nil, fmt.Errorf("index key columns for %q: %w", r.Qualified(), err)
	}
	defer rows.Close()
	var out []IndexKeyColumn
	for rows.Next() {
		var k IndexKeyColumn
		if err := rows.Scan(&k.Ordinal, &k.Def, &k.IsKey,
			&k.TypLen, &k.TypAlign, &k.TypName, &k.TypCategory); err != nil {
			return nil, fmt.Errorf("index key columns for %q: %w", r.Qualified(), err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index key columns for %q: %w", r.Qualified(), err)
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

// --- GiST ---

// ListGistPages returns up to `count` per-page summaries of a GiST index from
// `start`. GiST has no metapage (block 0 is the root), so minPages=1 and the
// window starts wherever the caller asks.
func (c *Client) ListGistPages(ctx context.Context, r Relation, start, count int32) ([]GistPageStat, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	count, ok, err := c.clampPageWindow(ctx, pool, r.OID, r.Qualified(), start, count, 1)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := pool.Query(ctx, sqlGistPagesSummary, regclass, start, count)
	if err != nil {
		return nil, fmt.Errorf("list gist pages in %q: %w", r.Qualified(), err)
	}
	defer rows.Close()
	var out []GistPageStat
	for rows.Next() {
		var p GistPageStat
		if err := rows.Scan(&p.Blkno, &p.IsLeaf, &p.IsDeleted, &p.Items,
			&p.FreeSize, &p.PageSize, &p.RightLink); err != nil {
			return nil, fmt.Errorf("list gist pages in %q: %w", r.Qualified(), err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gist pages in %q: %w", r.Qualified(), err)
	}
	return out, nil
}

// ListGistItems lists one GiST page's items. It prefers gist_page_items, whose
// keys column is opclass-decoded, but falls back to gist_page_items_bytea (raw
// key bytes, rendered as hex) when the decoded variant fails — many opclasses,
// notably btree_gist's gbtreekey* types, have no output function and raise
// "cannot display a value of type gbtreekeyNN" (SQLSTATE 0A000).
func (c *Client) ListGistItems(ctx context.Context, r Relation, blkno int32) ([]GistItem, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	out, err := scanGistItems(ctx, pool, sqlGistItems, regclass, blkno, false)
	if err != nil {
		// Re-run with the raw-bytes variant. If that fails too, surface the
		// original (decoded-path) error — it's the more informative one.
		if raw, rawErr := scanGistItems(ctx, pool, sqlGistItemsBytea, regclass, blkno, true); rawErr == nil {
			return raw, nil
		}
		return nil, fmt.Errorf("list gist items in %q page %d: %w", r.Qualified(), blkno, err)
	}
	return out, nil
}

// scanGistItems runs one of the two GiST item queries and scans the rows. When
// raw is true the final column is gist_page_items_bytea's key_data (bytea),
// hex-encoded into GistItem.Keys; otherwise it's gist_page_items' keys (text).
func scanGistItems(ctx context.Context, pool *pgxpool.Pool, sql, regclass string, blkno int32, raw bool) ([]GistItem, error) {
	rows, err := pool.Query(ctx, sql, regclass, blkno)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GistItem
	for rows.Next() {
		var it GistItem
		if raw {
			var keyData []byte
			if err := rows.Scan(&it.ItemOffset, &it.Ctid, &it.ItemLen, &it.Dead, &keyData); err != nil {
				return nil, err
			}
			if len(keyData) > 0 {
				s := fmt.Sprintf(`\x%x`, keyData)
				it.Keys = &s
			}
		} else {
			if err := rows.Scan(&it.ItemOffset, &it.Ctid, &it.ItemLen, &it.Dead, &it.Keys); err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GistPageFlags resolves a GiST child page's leaf/deleted role mid-descent
// (mirrors BtreePageType). Best-effort at the call site: a failure leaves the
// role unknown and the renderer/drill fall back to treating it conservatively.
func (c *Client) GistPageFlags(ctx context.Context, r Relation, blkno int32) (isLeaf, isDeleted bool, err error) {
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return false, false, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := pool.QueryRow(ctx, sqlGistPageFlags, regclass, blkno).Scan(&isLeaf, &isDeleted); err != nil {
		return false, false, fmt.Errorf("gist_page_opaque_info for %q page %d: %w", r.Qualified(), blkno, err)
	}
	return isLeaf, isDeleted, nil
}

// --- BRIN ---

// BrinMeta reads the BRIN metapage for the page-list banner (pages-per-range,
// version, last revmap block). Best-effort, like BtreeMeta.
func (c *Client) BrinMeta(ctx context.Context, r Relation) (BrinMeta, error) {
	var m BrinMeta
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return m, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := pool.QueryRow(ctx, sqlBrinMeta, regclass).Scan(
		&m.Magic, &m.Version, &m.PagesPerRange, &m.LastRevmapPage); err != nil {
		return m, fmt.Errorf("brin_metapage_info for %q: %w", r.Qualified(), err)
	}
	return m, nil
}

// ListBrinPages returns up to `count` BRIN page summaries from `start`. Block 0
// (meta) is browsable — brin_page_type handles every page type — so minPages=1.
func (c *Client) ListBrinPages(ctx context.Context, r Relation, start, count int32) ([]BrinPageStat, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	count, ok, err := c.clampPageWindow(ctx, pool, r.OID, r.Qualified(), start, count, 1)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := pool.Query(ctx, sqlBrinPagesSummary, regclass, start, count)
	if err != nil {
		return nil, fmt.Errorf("list brin pages in %q: %w", r.Qualified(), err)
	}
	defer rows.Close()
	var out []BrinPageStat
	for rows.Next() {
		var p BrinPageStat
		if err := rows.Scan(&p.Blkno, &p.PageType, &p.FreeSize, &p.PageSize); err != nil {
			return nil, fmt.Errorf("list brin pages in %q: %w", r.Qualified(), err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list brin pages in %q: %w", r.Qualified(), err)
	}
	return out, nil
}

// ListBrinItems lists one BRIN regular page's range-summary tuples. A non-regular
// page (meta/revmap) surfaces as a pageinspect error from the server; callers
// only drill regular pages.
func (c *Client) ListBrinItems(ctx context.Context, r Relation, blkno int32) ([]BrinItem, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	rows, err := pool.Query(ctx, sqlBrinItems, regclass, blkno)
	if err != nil {
		return nil, fmt.Errorf("list brin items in %q page %d: %w", r.Qualified(), blkno, err)
	}
	defer rows.Close()
	var out []BrinItem
	for rows.Next() {
		var it BrinItem
		if err := rows.Scan(&it.ItemOffset, &it.BlockNum, &it.AttNum,
			&it.AllNulls, &it.HasNulls, &it.Placeholder, &it.Empty, &it.Value); err != nil {
			return nil, fmt.Errorf("list brin items in %q page %d: %w", r.Qualified(), blkno, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list brin items in %q page %d: %w", r.Qualified(), blkno, err)
	}
	return out, nil
}

// --- GIN ---

// GinMeta reads the GIN metapage for the page-list banner. Best-effort.
func (c *Client) GinMeta(ctx context.Context, r Relation) (GinMeta, error) {
	var m GinMeta
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return m, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := pool.QueryRow(ctx, sqlGinMeta, regclass).Scan(
		&m.PendingPages, &m.PendingTuples, &m.TotalPages,
		&m.EntryPages, &m.DataPages, &m.Entries, &m.Version); err != nil {
		return m, fmt.Errorf("gin_metapage_info for %q: %w", r.Qualified(), err)
	}
	return m, nil
}

// ListGinPages returns up to `count` GIN page summaries from `start`. The
// metapage (block 0) is skipped (minPages=2; the SQL clamps the lower bound to
// block 1) since its opaque area differs and it's covered by the banner.
func (c *Client) ListGinPages(ctx context.Context, r Relation, start, count int32) ([]GinPageStat, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	count, ok, err := c.clampPageWindow(ctx, pool, r.OID, r.Qualified(), start, count, 2)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	rows, err := pool.Query(ctx, sqlGinPagesSummary, regclass, start, count)
	if err != nil {
		return nil, fmt.Errorf("list gin pages in %q: %w", r.Qualified(), err)
	}
	defer rows.Close()
	var out []GinPageStat
	for rows.Next() {
		var p GinPageStat
		if err := rows.Scan(&p.Blkno, &p.Flags, &p.MaxOff, &p.FreeSize, &p.PageSize); err != nil {
			return nil, fmt.Errorf("list gin pages in %q: %w", r.Qualified(), err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gin pages in %q: %w", r.Qualified(), err)
	}
	return out, nil
}

// ListGinItems lists posting-list segments on a compressed GIN data-leaf page.
// Returns a pageinspect error from the server for non-leaf/entry pages; callers
// only drill data-leaf pages.
func (c *Client) ListGinItems(ctx context.Context, r Relation, blkno int32) ([]GinItem, error) {
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	rows, err := pool.Query(ctx, sqlGinItems, regclass, blkno)
	if err != nil {
		return nil, fmt.Errorf("list gin items in %q page %d: %w", r.Qualified(), blkno, err)
	}
	defer rows.Close()
	var out []GinItem
	for rows.Next() {
		var it GinItem
		if err := rows.Scan(&it.FirstTid, &it.NBytes, &it.TidCount, &it.TidsText); err != nil {
			return nil, fmt.Errorf("list gin items in %q page %d: %w", r.Qualified(), blkno, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list gin items in %q page %d: %w", r.Qualified(), blkno, err)
	}
	return out, nil
}
