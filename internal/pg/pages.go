package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("relpages for %q", t.Qualified()), sqlRelPages, []any{t.OID}, &n); err != nil {
		return 0, err
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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("relpages for %q", qualified), sqlRelPages, []any{oid}, &relpages); err != nil {
		return 0, false, err
	}
	if relpages < minPages || start >= relpages {
		return 0, false, nil
	}
	if start+count > relpages {
		count = relpages - start
	}
	return count, true, nil
}

// listPageWindow is the shared preamble of every List*Pages reader: ensure
// pageinspect, clamp the requested window to the relation's real page count
// (nil result when nothing is browsable), then run the summary query over
// (regclass, start, count) and collect the rows.
func listPageWindow[T any](ctx context.Context, c *Client, db, schema, name string, oid uint32, qualified, op, sql string, start, count, minPages int32, scan func(pgx.CollectableRow) (T, error)) ([]T, error) {
	if err := c.EnsurePageInspect(ctx, db); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(schema, name)
	count, ok, err := c.clampPageWindow(ctx, pool, oid, qualified, start, count, minPages)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return collect(ctx, pool, op, sql, []any{regclass, start, count}, scan)
}

// ListHeapPages returns up to `count` per-page summaries starting at `start`.
func (c *Client) ListHeapPages(ctx context.Context, t Table, start, count int32) ([]HeapPageStat, error) {
	return listPageWindow(ctx, c, t.DB, t.Schema, t.Name, t.OID, t.Qualified(),
		fmt.Sprintf("list heap pages in %q", t.Qualified()), sqlHeapPagesSummary, start, count, 1,
		func(row pgx.CollectableRow) (HeapPageStat, error) {
			var p HeapPageStat
			err := row.Scan(
				&p.Blkno, &p.LSN, &p.Lower, &p.Upper, &p.Special, &p.PageSize, &p.Flags,
				&p.FreeBytes,
				&p.LiveLP, &p.RedirectLP, &p.DeadLP, &p.UnusedLP,
				&p.LiveBytes, &p.DeadBytes, &p.HotUpdated, &p.HasExternal,
			)
			return p, err
		})
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
	return collect(ctx, pool, fmt.Sprintf("read tuple in %q ctid %s", t.Qualified(), ctid), sql, []any{ctid},
		func(row pgx.CollectableRow) (TupleCell, error) {
			var c TupleCell
			err := row.Scan(&c.Name, &c.Value, &c.FullBytes)
			return c, err
		})
}

// ReadToastValue fetches the leading chunks of one out-of-line value from a
// TOAST table (just enough for the hex preview — never the whole value),
// assembles them in chunk_seq order, and returns a small slice of TupleCell
// rows suitable for the row-detail view:
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

	// The view previews at most maxHexBytes of the assembled value, so only
	// the chunks covering that prefix are fetched — TOAST chunks are ~2000 B
	// (TOAST_MAX_CHUNK_SIZE), so 2 always suffice; +1 spare in case the
	// value was toasted with an unusually small chunk size. Totals come from
	// the query's window aggregates, which see every chunk.
	const maxHexBytes = 2048
	const maxChunks = maxHexBytes/1500 + 2

	type chunk struct {
		seq        int32
		data       []byte
		chunks     int32
		totalBytes int64
	}
	chunks, err := collect(ctx, pool, fmt.Sprintf("read toast value in %q chunk %d", t.Qualified(), chunkID), sql, []any{chunkID, maxChunks},
		func(row pgx.CollectableRow) (chunk, error) {
			var ch chunk
			err := row.Scan(&ch.seq, &ch.data, &ch.chunks, &ch.totalBytes)
			return ch, err
		})
	if err != nil {
		return nil, err
	}

	var assembled []byte
	var nChunks int32
	var totalBytes int64
	for _, ch := range chunks {
		if len(assembled) < maxHexBytes {
			assembled = append(assembled, ch.data...)
		}
		nChunks, totalBytes = ch.chunks, ch.totalBytes
	}

	var hexData string
	if int64(len(assembled)) >= totalBytes {
		hexData = fmt.Sprintf(`\x%x`, assembled)
	} else {
		if len(assembled) > maxHexBytes {
			assembled = assembled[:maxHexBytes]
		}
		hexData = fmt.Sprintf(`\x%x…`, assembled)
	}

	str := func(s string) *string { return &s }
	return []TupleCell{
		{Name: "chunk_id", Value: str(strconv.FormatUint(uint64(chunkID), 10))},
		{Name: "chunks", Value: str(strconv.Itoa(int(nChunks)))},
		{Name: "total_bytes", Value: str(strconv.FormatInt(totalBytes, 10))},
		{Name: "data", Value: str(hexData)},
	}, nil
}

// ToastChunkLocation resolves an out-of-line value's TOAST pointer (the toast
// relation's OID and the value's chunk_id) into the Table metadata the heap-page
// inspector needs plus the heap block of the value's first chunk. It backs the
// "ENTER on a TOASTed value jumps to its TOAST relation" shortcut: the pointer
// carries an OID, not a name, so a catalog lookup is required. A missing chunk
// (value vacuumed or updated since) is not an error — block 0 is returned.
func (c *Client) ToastChunkLocation(ctx context.Context, db string, toastOID, chunkID uint32) (Table, int32, error) {
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return Table{}, 0, err
	}
	t := Table{DB: db, OID: toastOID}
	err = pool.QueryRow(ctx, sqlResolveRelByOID, toastOID).
		Scan(&t.Schema, &t.Name, &t.HeapBytes, &t.EstRows)
	if err != nil {
		return Table{}, 0, fmt.Errorf("resolve toast relation %d in %q: %w", toastOID, db, err)
	}
	t.TotalBytes = t.HeapBytes

	var blk int32
	sql := fmt.Sprintf(sqlToastChunkBlock, qualifiedIdent(t.Schema, t.Name))
	err = pool.QueryRow(ctx, sql, chunkID).Scan(&blk)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, 0, nil
	}
	if err != nil {
		return Table{}, 0, fmt.Errorf("locate toast chunk %d in %q: %w", chunkID, t.Qualified(), err)
	}
	return t, blk, nil
}

// ListIndexPages returns up to `count` per-page summaries of a B-tree index
// starting at `start`. Block 0 is the index meta page; bt_page_stats errors
// on it, so the SQL clamps the window to start at 1 internally — callers
// can pass start=0 without surprising failure.
func (c *Client) ListIndexPages(ctx context.Context, r Relation, start, count int32) ([]IndexPageStat, error) {
	// minPages=2: an index's block 0 is the meta page, which bt_page_stats
	// can't summarise, so a one-page index has nothing browsable.
	return listPageWindow(ctx, c, r.DB, r.Schema, r.Name, r.OID, r.Qualified(),
		fmt.Sprintf("list index pages in %q", r.Qualified()), sqlIndexPagesSummary, start, count, 2,
		func(row pgx.CollectableRow) (IndexPageStat, error) {
			var p IndexPageStat
			err := row.Scan(
				&p.Blkno, &p.Type,
				&p.LiveItems, &p.DeadItems,
				&p.AvgItemSize, &p.PageSize, &p.FreeSize,
				&p.BtpoPrev, &p.BtpoNext, &p.BtpoLevel, &p.BtpoFlags,
			)
			return p, err
		})
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
//
// Entries left undecoded get a second chance through fillHotChains: a
// HOT-updated row's index entry points at a redirect line pointer that the
// ctid join can't follow, and resolving that hop is the difference between
// showing the live row and writing the entry off as dead.
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
	var exprs, parent string
	// Only leaf pages carry heap ctids worth decoding; internal pages store
	// downlinks (child block addresses) that would either miss the heap
	// entirely or — worse — match an unrelated row by coincidence, printing
	// bogus keys. Skip the heap join in those cases. Type 'r' reaches here only
	// for a single-page index, whose root is also a leaf: the caller maps a
	// taller tree's (internal) root to 'i' first (see indexTuplePageType), so a
	// non-leaf root never takes the decode path.
	if (pageType == "l" || pageType == "r") && r.ParentOID != 0 && r.ParentName != "" {
		// Fetching the expression list per call avoids a stale cache when
		// the index is redefined under us. It's a one-shot pg_index lookup
		// — cheap next to the per-row heap fetches below.
		if err := pool.QueryRow(ctx, sqlIndexExprList, r.OID).Scan(&exprs); err == nil && exprs != "" {
			parent = qualifiedIdent(r.Schema, r.ParentName)
			sql = fmt.Sprintf(sqlIndexTuplesDecoded, exprs, parent)
		}
	}

	tuples, err := collect(ctx, pool, fmt.Sprintf("list index tuples in %q page %d", r.Qualified(), blkno), sql, []any{regclass, blkno}, scanIndexTuple)
	if err != nil {
		return nil, err
	}
	if pageType == "l" || pageType == "r" {
		fillPostingTids(ctx, pool, exprs, parent, regclass, blkno, tuples)
	}
	if parent != "" {
		fillHotChains(ctx, pool, exprs, parent, tuples)
		fillPostingHotChains(ctx, pool, exprs, parent, tuples)
	}
	return tuples, nil
}

// scanIndexTuple scans one bt_page_items row (sqlIndexTuples' column list) —
// shared with the WAL page-image decode.
func scanIndexTuple(row pgx.CollectableRow) (IndexTuple, error) {
	var it IndexTuple
	err := row.Scan(&it.ItemOffset, &it.Ctid, &it.ItemLen, &it.Nulls, &it.Vars, &it.Data, &it.Dead, &it.Decoded)
	return it, err
}

// fillPostingTids attaches each posting-list tuple's member heap tids
// (IndexTuple.Posting) on a leaf page, projected through the heap when the
// parent table is known. Best-effort: a pageinspect too old to have the tids
// column, or any other failure, just leaves the posting tuples collapsed to
// their "×N" summary.
func fillPostingTids(ctx context.Context, pool *pgxpool.Pool, exprs, parent, regclass string, blkno int32, tuples []IndexTuple) {
	sql := sqlIndexPostingTids
	if parent != "" {
		sql = fmt.Sprintf(sqlIndexPostingTidsDecoded, exprs, parent)
	}
	byOff := make(map[int32]int, len(tuples))
	for i, t := range tuples {
		byOff[t.ItemOffset] = i
	}
	members := collectBestEffort(ctx, pool, sql, []any{regclass, blkno},
		func(rows pgx.Rows) (IndexTuple, bool) {
			var m IndexTuple
			if err := rows.Scan(&m.ItemOffset, &m.Ctid, &m.Decoded); err != nil {
				return m, false
			}
			return m, true
		})
	for _, m := range members {
		if i, ok := byOff[m.ItemOffset]; ok {
			tuples[i].Posting = append(tuples[i].Posting, m)
		}
	}
}

// fillPostingHotChains runs the HOT-redirect pass over every posting member on
// the page in one batch (fillHotChains dedups the heap blocks, so batching
// keeps it to one query) and writes the results back under their parents.
func fillPostingHotChains(ctx context.Context, pool *pgxpool.Pool, exprs, parent string, tuples []IndexTuple) {
	var flat []IndexTuple
	for _, t := range tuples {
		flat = append(flat, t.Posting...)
	}
	if len(flat) == 0 {
		return
	}
	fillHotChains(ctx, pool, exprs, parent, flat)
	k := 0
	for i := range tuples {
		for j := range tuples[i].Posting {
			tuples[i].Posting[j] = flat[k]
			k++
		}
	}
}

// btAltTIDOffsetBase is the lowest offset value nbtree steals for its own
// markers: BT_PIVOT_HEAP_TID_ATTR (0x1000) and INDEX_ALT_TID_MASK (0x2000) sit
// in the high bits of a pivot's or posting tuple's ctid offset word. A real heap
// offset never comes close — MaxHeapTuplesPerPage is 291 on an 8K page — so an
// offset at or above this is a marker, not an address worth resolving.
const btAltTIDOffsetBase = 0x1000

// fillHotChains fills HotCtid/HotDecoded for the leaf entries whose ctid didn't
// resolve to a visible row, by following the HOT redirect line pointer their
// ctid names (see sqlHeapRedirectKeys for why the plain ctid join can't).
//
// Best-effort by design: this only adds detail to rows that already render, so
// any failure — a truncated relation, a lost privilege, the caller's timeout —
// leaves the tuples exactly as the main query returned them.
func fillHotChains(ctx context.Context, pool *pgxpool.Pool, exprs, parent string, tuples []IndexTuple) {
	roots := make([]string, 0, len(tuples))
	blocks := make([]int32, 0, len(tuples))
	byRoot := make(map[string][]int, len(tuples))
	for i, t := range tuples {
		if t.Decoded != nil || t.Ctid == nil {
			continue
		}
		blk, off, ok := parseTidText(*t.Ctid)
		if !ok || off >= btAltTIDOffsetBase {
			continue
		}
		if _, seen := byRoot[*t.Ctid]; !seen {
			roots = append(roots, *t.Ctid)
			blocks = append(blocks, blk)
		}
		byRoot[*t.Ctid] = append(byRoot[*t.Ctid], i)
	}
	if len(roots) == 0 {
		return
	}
	type redirect struct {
		root, live string
		decoded    *string
	}
	hits := collectBestEffort(ctx, pool, fmt.Sprintf(sqlHeapRedirectKeys, exprs, parent),
		[]any{roots, parent, blocks},
		func(rows pgx.Rows) (redirect, bool) {
			var rd redirect
			if err := rows.Scan(&rd.root, &rd.live, &rd.decoded); err != nil {
				return rd, false
			}
			return rd, true
		})
	for _, rd := range hits {
		live := rd.live
		for _, i := range byRoot[rd.root] {
			tuples[i].HotCtid = &live
			tuples[i].HotDecoded = rd.decoded
		}
	}
}

// parseTidText splits pageinspect's "(blk,off)" tid rendering. Returns false for
// anything that isn't that shape, so a NULL-ish or unexpected value is skipped
// rather than resolved against block 0.
func parseTidText(s string) (blk int32, off int32, ok bool) {
	if _, err := fmt.Sscanf(s, "(%d,%d)", &blk, &off); err != nil {
		return 0, 0, false
	}
	return blk, off, true
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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("bt_metap for %q", r.Qualified()), sqlBtreeMeta, []any{regclass},
		&m.Magic, &m.Version, &m.Root, &m.Level, &m.FastRoot, &m.FastLevel, &m.AllEqualImage,
	); err != nil {
		return m, err
	}
	return m, nil
}

// BtreeLevelCounts scans the whole index (every page, once) and returns the
// per-(level, type) page counts for the tree-shape banner. Expensive on big
// indexes — a full-index read — so the TUI runs it asynchronously and caches
// the result for the screen's lifetime. Best-effort like BtreeMeta: a
// privilege error or the client-side timeout just hides the banner line.
func (c *Client) BtreeLevelCounts(ctx context.Context, r Relation) ([]BtreeLevelCount, error) {
	// Ensure pageinspect ourselves: this runs concurrently with the page-list
	// load, not after it, so we can't lean on that loader's ensure — and the
	// typed MissingExtensionError lets the TUI retry the census after the
	// user installs the extension through the prompt.
	if err := c.EnsurePageInspect(ctx, r.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(r.Schema, r.Name)
	var nblocks int64
	if err := pool.QueryRow(ctx, sqlRelationBlocks, regclass).Scan(&nblocks); err != nil {
		return nil, fmt.Errorf("btree level counts for %q: %w", r.Qualified(), err)
	}
	if nblocks <= 1 {
		return nil, nil // just the metapage (or an empty file): nothing to census
	}
	return collect(ctx, pool, fmt.Sprintf("btree level counts for %q", r.Qualified()), sqlBtreeLevelCounts, []any{regclass, nblocks - 1},
		func(row pgx.CollectableRow) (BtreeLevelCount, error) {
			var lc BtreeLevelCount
			err := row.Scan(&lc.Level, &lc.Type, &lc.Pages)
			return lc, err
		})
}

// BtreePageType returns one B-tree page's bt_page_stats type ('l' leaf, 'r'
// root, 'i' internal, 'd' deleted) and its btpo_level (0 = leaf). Used to
// resolve a child page's type when the user descends through an internal-page
// downlink (the parent page only gave us the child's block number, not its
// role) and to label the page's tree depth. Best-effort at the call site: a
// failure leaves the page type unknown and the tuple loader falls back to the
// raw, non-decoded path.
func (c *Client) BtreePageType(ctx context.Context, r Relation, blkno int32) (string, int32, error) {
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return "", 0, err
	}
	var (
		t     string
		level int32
	)
	regclass := qualifiedIdent(r.Schema, r.Name)
	if err := queryRelRow(ctx, pool, fmt.Sprintf("bt_page_stats type for %q page %d", r.Qualified(), blkno),
		sqlBtreePageType, []any{regclass, blkno}, &t, &level); err != nil {
		return "", 0, err
	}
	return t, level, nil
}

// IndexKeyColumns returns the index's columns in definition order, split into
// key vs INCLUDE columns. Used to render the "keys: (…) include: (…)" banner
// above the index page/tuple views.
func (c *Client) IndexKeyColumns(ctx context.Context, r Relation) ([]IndexKeyColumn, error) {
	pool, err := c.PoolFor(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	return collect(ctx, pool, fmt.Sprintf("index key columns for %q", r.Qualified()), sqlIndexKeyColumns, []any{r.OID}, scanIndexKeyColumn)
}

func scanIndexKeyColumn(row pgx.CollectableRow) (IndexKeyColumn, error) {
	var k IndexKeyColumn
	err := row.Scan(&k.Ordinal, &k.Def, &k.IsKey,
		&k.TypLen, &k.TypAlign, &k.TypName, &k.TypCategory)
	return k, err
}

// ListHeapTuples returns the line-pointer array for one heap page together with
// the row content the tuple list shows next to it: the primary key (HeapTuple.PK)
// and the picked table columns (Vals/Raws, described by HeapPageTuples.Columns
// and Shown). pick names the columns to project; nil means the default — the
// primary-key columns — and names that don't exist are dropped silently (the
// user's pick outlives an ALTER TABLE). The page must exist (caller already saw
// it in ListHeapPages); a missing block here surfaces as a pageinspect error
// from the server.
//
// For TOAST tables (t.Schema == "pg_toast") the query joins back into the toast
// relation to project chunk_id/chunk_seq per live row instead — those relations
// have no primary key, and the chunk identity is the useful handle there. Each
// HeapTuple's ChunkID/ChunkSeq fields are populated only in that case, and
// Columns stays nil (nothing to pick).
//
// The row content is best-effort in three tiers: a failed catalog lookup costs
// the columns; a relation join the server rejects (a role granted pageinspect
// but not SELECT on the table) falls back to the page-bytes-only variant; and
// if even that fails, to the plain line-pointer query. The line pointers are
// the half worth having, so they never fail for the content's sake.
func (c *Client) ListHeapTuples(ctx context.Context, t Table, blkno int32, pick []string) (HeapPageTuples, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return HeapPageTuples{}, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return HeapPageTuples{}, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)
	op := fmt.Sprintf("list heap tuples in %q page %d", t.Qualified(), blkno)
	args := []any{regclass, blkno}

	if t.Schema == "pg_toast" {
		tuples, err := collect(ctx, pool, op, fmt.Sprintf(sqlToastTuples, regclass), args, scanHeapTuple(heapTupleExtraChunk))
		return HeapPageTuples{Tuples: tuples}, err
	}

	var page HeapPageTuples
	page.PKCols, _ = primaryKeyColumns(ctx, pool, regclass)
	page.Columns, _ = heapColumns(ctx, pool, regclass, page.PKCols)
	page.Shown = resolveShown(page.Columns, pick)
	names := page.ShownNames()
	attnums := make([]int32, len(page.Shown))
	for i, ci := range page.Shown {
		attnums[i] = page.Columns[ci].Attnum
	}

	keyExpr := heapKeyProjection("src", page.PKCols)
	if keyExpr == "" {
		keyExpr = "NULL"
	}
	if page.Columns != nil {
		sql := fmt.Sprintf(sqlHeapTuplesCols, keyExpr, heapColProjection("src", names), heapAttrProjection("hpa", attnums), regclass)
		if tuples, err := collect(ctx, pool, op, sql, args, scanHeapTuple(heapTupleExtraCols)); err == nil {
			page.Tuples = tuples
			return page, nil
		}
		sql = fmt.Sprintf(sqlHeapTuplesDecode, heapAttrProjection("hpa", attnums))
		if tuples, err := collect(ctx, pool, op, sql, args, scanHeapTuple(heapTupleExtraDecode)); err == nil {
			page.Tuples = tuples
			return page, nil
		}
	}
	page.Tuples, err = collect(ctx, pool, op, sqlHeapTuples, args, scanHeapTuple(heapTupleExtraNone))
	// Without a row-content query the pick is moot; report nothing shown so
	// the list renders the plain physical columns.
	page.Shown = nil
	return page, err
}

// heapTupleExtra names the trailing column(s) a heap-tuple query projects on
// top of the heap_page_items set every variant shares.
type heapTupleExtra int

const (
	heapTupleExtraNone heapTupleExtra = iota
	// heapTupleExtraChunk: chunk_id, chunk_seq (TOAST relations).
	heapTupleExtraChunk
	// heapTupleExtraCols: the rendered primary key, the picked columns' text
	// (visible row) and their on-page bytes.
	heapTupleExtraCols
	// heapTupleExtraDecode: the picked columns' on-page bytes only.
	heapTupleExtraDecode
)

func scanHeapTuple(extra heapTupleExtra) func(pgx.CollectableRow) (HeapTuple, error) {
	return func(row pgx.CollectableRow) (HeapTuple, error) {
		var h HeapTuple
		dest := []any{
			&h.LP, &h.LPOff, &h.LPFlags, &h.LPLen,
			&h.Xmin, &h.Xmax, &h.Field3, &h.Ctid,
			&h.Infomask2, &h.Infomask, &h.Hoff,
			&h.Bits, &h.Oid, &h.Data,
		}
		switch extra {
		case heapTupleExtraChunk:
			dest = append(dest, &h.ChunkID, &h.ChunkSeq)
		case heapTupleExtraCols:
			dest = append(dest, &h.PK, &h.Vals, &h.Raws)
		case heapTupleExtraDecode:
			dest = append(dest, &h.Raws)
		}
		err := row.Scan(dest...)
		return h, err
	}
}

// heapColumns lists the relation's live columns (sqlHeapColumns) and flags the
// primary-key members by name.
func heapColumns(ctx context.Context, pool *pgxpool.Pool, regclass string, pkCols []string) ([]HeapColumn, error) {
	cols, err := collect(ctx, pool, "columns of "+regclass, sqlHeapColumns, []any{regclass},
		func(row pgx.CollectableRow) (HeapColumn, error) {
			var c HeapColumn
			err := row.Scan(&c.Attnum, &c.Name, &c.TypeName, &c.Len, &c.Align, &c.TypName, &c.TypCategory)
			return c, err
		})
	if err != nil {
		return nil, err
	}
	for i := range cols {
		cols[i].PK = slices.Contains(pkCols, cols[i].Name)
	}
	return cols, nil
}

// resolveShown maps a column pick to indexes into cols, in cols (attnum) order.
// A nil pick means the default — the primary-key columns; an explicit empty
// pick means none. Names cols doesn't have are dropped.
func resolveShown(cols []HeapColumn, pick []string) []int {
	var shown []int
	for i, c := range cols {
		if (pick == nil && c.PK) || (pick != nil && slices.Contains(pick, c.Name)) {
			shown = append(shown, i)
		}
	}
	return shown
}

// primaryKeyColumns returns the relation's primary-key column names in key
// order, or nil when it has no primary key.
func primaryKeyColumns(ctx context.Context, pool *pgxpool.Pool, regclass string) ([]string, error) {
	return collect(ctx, pool, "primary key columns of "+regclass, sqlPrimaryKeyColumns, []any{regclass},
		func(row pgx.CollectableRow) (string, error) {
			var name string
			err := row.Scan(&name)
			return name, err
		})
}

// heapColProjection lists the picked columns as capped text expressions for
// the vals array of sqlHeapTuplesCols: `left(src."a"::text, 64), left(...)`.
// Empty for no columns (ARRAY[]::text[] is valid SQL).
func heapColProjection(alias string, cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = "left(" + alias + "." + quoteIdent(c) + "::text, " + heapKeyColCap + ")"
	}
	return strings.Join(parts, ", ")
}

// heapAttrProjection lists the picked columns' t_attrs elements for the raws
// array of sqlHeapTuplesCols: `hpa.t_attrs[3], hpa.t_attrs[7]`. Attnums come
// from pg_attribute, never from user input.
func heapAttrProjection(alias string, attnums []int32) string {
	parts := make([]string, len(attnums))
	for i, n := range attnums {
		parts[i] = alias + ".t_attrs[" + strconv.Itoa(int(n)) + "]"
	}
	return strings.Join(parts, ", ")
}

// heapKeyProjection builds the SQL text expression that renders one heap row's
// primary key for the tuple list: a single column projects bare ("42"), a
// composite key as a tuple literal ("(7, 42)") so it reads the same way the
// user would write it in a WHERE clause. Every column is capped at
// heapKeyColCap characters *before* concatenation, bounding what a wide text
// key can ship per line pointer. Returns "" when there is no key to project,
// which is the caller's signal to use the plain (pk-less) query.
func heapKeyProjection(alias string, cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = "left(" + alias + "." + quoteIdent(c) + "::text, " + heapKeyColCap + ")"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "'(' || concat_ws(', ', " + strings.Join(parts, ", ") + ") || ')'"
}

// ListTupleAttrs splits one heap tuple (identified by block + line pointer)
// into its per-attribute raw bytes plus pg_attribute physical metadata, in
// attnum order. Feeds the tuple byte-layout overlay. Returns an empty slice
// (not an error) when the lp no longer exists — the page was rewritten after
// the tuple list loaded.
func (c *Client) ListTupleAttrs(ctx context.Context, t Table, blkno, lp int32) ([]TupleAttr, error) {
	if err := c.EnsurePageInspect(ctx, t.DB); err != nil {
		return nil, err
	}
	pool, err := c.PoolFor(ctx, t.DB)
	if err != nil {
		return nil, err
	}
	regclass := qualifiedIdent(t.Schema, t.Name)
	return collect(ctx, pool, fmt.Sprintf("tuple attrs in %q page %d lp %d", t.Qualified(), blkno, lp), sqlTupleAttrs, []any{regclass, blkno, lp},
		func(row pgx.CollectableRow) (TupleAttr, error) {
			var a TupleAttr
			err := row.Scan(&a.Attnum, &a.Name, &a.TypeName,
				&a.Len, &a.Align, &a.Dropped, &a.Stored,
				&a.TypName, &a.TypCategory, &a.EnumLabel, &a.Value)
			return a, err
		})
}

// --- GiST ---

// ListGistPages returns up to `count` per-page summaries of a GiST index from
// `start`. GiST has no metapage (block 0 is the root), so minPages=1 and the
// window starts wherever the caller asks.
func (c *Client) ListGistPages(ctx context.Context, r Relation, start, count int32) ([]GistPageStat, error) {
	return listPageWindow(ctx, c, r.DB, r.Schema, r.Name, r.OID, r.Qualified(),
		fmt.Sprintf("list gist pages in %q", r.Qualified()), sqlGistPagesSummary, start, count, 1,
		func(row pgx.CollectableRow) (GistPageStat, error) {
			var p GistPageStat
			err := row.Scan(&p.Blkno, &p.IsLeaf, &p.IsDeleted, &p.Items,
				&p.FreeSize, &p.PageSize, &p.RightLink)
			return p, err
		})
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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("gist_page_opaque_info for %q page %d", r.Qualified(), blkno),
		sqlGistPageFlags, []any{regclass, blkno}, &isLeaf, &isDeleted); err != nil {
		return false, false, err
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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("brin_metapage_info for %q", r.Qualified()), sqlBrinMeta, []any{regclass},
		&m.Magic, &m.Version, &m.PagesPerRange, &m.LastRevmapPage); err != nil {
		return m, err
	}
	return m, nil
}

// ListBrinPages returns up to `count` BRIN page summaries from `start`. Block 0
// (meta) is browsable — brin_page_type handles every page type — so minPages=1.
func (c *Client) ListBrinPages(ctx context.Context, r Relation, start, count int32) ([]BrinPageStat, error) {
	return listPageWindow(ctx, c, r.DB, r.Schema, r.Name, r.OID, r.Qualified(),
		fmt.Sprintf("list brin pages in %q", r.Qualified()), sqlBrinPagesSummary, start, count, 1,
		func(row pgx.CollectableRow) (BrinPageStat, error) {
			var p BrinPageStat
			err := row.Scan(&p.Blkno, &p.PageType, &p.FreeSize, &p.PageSize)
			return p, err
		})
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
	return collect(ctx, pool, fmt.Sprintf("list brin items in %q page %d", r.Qualified(), blkno), sqlBrinItems, []any{regclass, blkno},
		func(row pgx.CollectableRow) (BrinItem, error) {
			var it BrinItem
			err := row.Scan(&it.ItemOffset, &it.BlockNum, &it.AttNum,
				&it.AllNulls, &it.HasNulls, &it.Placeholder, &it.Empty, &it.Value)
			return it, err
		})
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
	if err := queryRelRow(ctx, pool, fmt.Sprintf("gin_metapage_info for %q", r.Qualified()), sqlGinMeta, []any{regclass},
		&m.PendingPages, &m.PendingTuples, &m.TotalPages,
		&m.EntryPages, &m.DataPages, &m.Entries, &m.Version); err != nil {
		return m, err
	}
	return m, nil
}

// ListGinPages returns up to `count` GIN page summaries from `start`. The
// metapage (block 0) is skipped (minPages=2; the SQL clamps the lower bound to
// block 1) since its opaque area differs and it's covered by the banner.
func (c *Client) ListGinPages(ctx context.Context, r Relation, start, count int32) ([]GinPageStat, error) {
	return listPageWindow(ctx, c, r.DB, r.Schema, r.Name, r.OID, r.Qualified(),
		fmt.Sprintf("list gin pages in %q", r.Qualified()), sqlGinPagesSummary, start, count, 2,
		func(row pgx.CollectableRow) (GinPageStat, error) {
			var p GinPageStat
			err := row.Scan(&p.Blkno, &p.Flags, &p.MaxOff, &p.FreeSize, &p.PageSize)
			return p, err
		})
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
	return collect(ctx, pool, fmt.Sprintf("list gin items in %q page %d", r.Qualified(), blkno), sqlGinItems, []any{regclass, blkno},
		func(row pgx.CollectableRow) (GinItem, error) {
			var it GinItem
			err := row.Scan(&it.FirstTid, &it.NBytes, &it.TidCount, &it.TidsText)
			return it, err
		})
}
