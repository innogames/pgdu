package pg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WALPageHeader is pageinspect's page_header() over a full-page image. LSN is
// the page's own LSN: an FPI is the page as it stood *after* this record's
// change (XLogInsert copies the buffer inside the critical section), so it
// normally equals the record's end LSN.
type WALPageHeader struct {
	LSN      string
	Checksum int32
	Flags    int32
	Lower    int32
	Upper    int32
	Special  int32
	PageSize int32
	Version  int32
	PruneXid string
}

// FreeBytes is the gap between the line-pointer array and the tuple area.
func (h WALPageHeader) FreeBytes() int32 { return max(h.Upper-h.Lower, 0) }

// WALHeapAttr is one physical attribute of the relation a block belongs to —
// the layout information (length, alignment, type) the tuple decoder walks
// with. Dropped columns are kept because their bytes still sit in the tuple.
type WALHeapAttr struct {
	Attnum      int32
	Name        string
	Dropped     bool
	TypLen      int32
	TypAlign    string
	TypName     string
	TypCategory string
}

// WALBlockDetail is one block reference *with its payload*, the drill-down
// behind a block-ref row: what the record actually wrote for this page.
//
// BlockData is the per-block change data (for a heap INSERT the new tuple
// minus its fixed 23-byte header; for a DELETE just a few flag bytes; empty
// when a full-page image replaced it). FPIData is the complete 8 KiB page
// image, decompressed and with the hole restored, or nil when the record
// carried none. PageHeader/PageItems are pageinspect's decode of FPIData,
// best-effort: nil with DecodeNote explaining why (pageinspect missing, index
// page, non-main fork). Attrs is the relation's column layout when it resolved
// to a heap relation in a reachable database — what lets the UI turn raw tuple
// bytes into column values.
type WALBlockDetail struct {
	Ref            WALBlockRef
	PrevLSN        string
	Xid            string
	RecordLength   int32
	MainDataLength int32
	BlockData      []byte
	FPIData        []byte

	RelOID  uint32
	RelKind string
	RelAM   string
	Attrs   []WALHeapAttr

	PageHeader *WALPageHeader
	PageItems  []HeapTuple
	// IndexCols / IndexItems are the B-tree counterparts of Attrs / PageItems:
	// the index's key layout (for decoding tuple bytes) and bt_page_items over
	// the page image. Only filled for relkind i with the btree AM.
	IndexCols  []IndexKeyColumn
	IndexItems []IndexTuple
	DecodeNote string
	// PageInspectMissing is set when the page image could not be decoded
	// because pageinspect is not installed in the connected database — the
	// typed error the UI turns into its install affordance, since the record
	// itself loaded fine and DecodeNote alone would leave the user stranded.
	PageInspectMissing *MissingExtensionError
}

// IsBtree reports whether the block is a main-fork page of a B-tree index.
func (d WALBlockDetail) IsBtree() bool {
	return d.Ref.ForkNumber == 0 && d.RelOID != 0 && d.RelKind == "i" && d.RelAM == "btree"
}

// BtreeOpaque decodes the page image's special area as a BTPageOpaqueData
// (nbtree.h): sibling links, tree level and flags. ok is false when the image
// is missing or the special pointer doesn't leave room for the 16-byte struct.
func (d WALBlockDetail) BtreeOpaque() (prev, next, level uint32, flags uint16, ok bool) {
	if d.PageHeader == nil || !d.IsBtree() {
		return 0, 0, 0, 0, false
	}
	sp := int(d.PageHeader.Special)
	if sp <= 0 || sp+16 > len(d.FPIData) {
		return 0, 0, 0, 0, false
	}
	b := d.FPIData[sp:]
	return binary.LittleEndian.Uint32(b[0:4]), binary.LittleEndian.Uint32(b[4:8]),
		binary.LittleEndian.Uint32(b[8:12]), binary.LittleEndian.Uint16(b[12:14]), true
}

// IsHeap reports whether the block's relation resolved to a heap-organised
// table (ordinary, TOAST, materialized view) on its main fork — the only case
// where the payload and page image hold heap tuples worth decoding.
func (d WALBlockDetail) IsHeap() bool {
	if d.Ref.ForkNumber != 0 || d.RelOID == 0 {
		return false
	}
	switch d.RelKind {
	case "r", "t", "m", "p":
		return d.RelAM == "" || d.RelAM == "heap"
	}
	return false
}

// WALBlockDetail fetches the payload of one block reference (the record's
// [StartLSN, EndLSN) range and BlockID come from ref, which the list already
// resolved names for). The record bytes themselves are mandatory; everything
// that helps interpret them — relation kind, column layout, the pageinspect
// decode of the page image — is best-effort and degrades into DecodeNote.
func (c *Client) WALBlockDetail(ctx context.Context, db string, ref WALBlockRef) (WALBlockDetail, error) {
	d := WALBlockDetail{Ref: ref}
	if err := c.EnsureWALInspect(ctx, db); err != nil {
		return d, err
	}
	pool, err := c.PoolFor(ctx, db)
	if err != nil {
		return d, err
	}
	op := fmt.Sprintf("wal block %d of record %s in %q", ref.BlockID, ref.StartLSN, db)
	var b WALBlockRef
	err = pool.QueryRow(ctx, sqlWALBlockDetail, ref.StartLSN, ref.EndLSN, ref.BlockID).Scan(
		&b.StartLSN, &b.EndLSN, &d.PrevLSN, &d.Xid, &d.RecordLength, &d.MainDataLength,
		&b.BlockID, &b.RelTablespace, &b.RelDatabase, &b.RelFileNode,
		&b.ForkNumber, &b.BlockNumber, &b.Rmgr, &b.RecordType,
		&b.BlockDataLength, &b.FPILength, &b.FPIInfo, &b.Description,
		&d.BlockData, &d.FPIData,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("%s: record no longer readable (WAL segment recycled?)", op)
	}
	if err != nil {
		return d, fmt.Errorf("%s: %w", op, err)
	}
	// Keep the list's resolved names/flags; refresh only what show_data added.
	b.RelName, b.IsToast, b.DBName = ref.RelName, ref.IsToast, ref.DBName
	d.Ref = b

	c.fillWALRelKind(ctx, db, &d)
	c.fillWALPageImage(ctx, db, pool, &d)
	return d, nil
}

// fillWALRelKind resolves the block's relation to (oid, relkind, am) and, for
// heap relations, its column layout. Catalog lookups must run in the database
// that owns the relation; shared relations (reldatabase 0) live in every
// database's catalog, so the connected one serves. A foreign database that
// refuses the connection just leaves the relation unresolved.
func (c *Client) fillWALRelKind(ctx context.Context, db string, d *WALBlockDetail) {
	relDB := db
	if d.Ref.RelDatabase != 0 {
		if d.Ref.DBName == "" {
			d.DecodeNote = "relation's database unknown — payload shown raw"
			return
		}
		relDB = d.Ref.DBName
	}
	pool, err := c.PoolFor(ctx, relDB)
	if err != nil {
		d.DecodeNote = fmt.Sprintf("cannot connect to %q to resolve the relation — payload shown raw", relDB)
		return
	}
	err = pool.QueryRow(ctx, sqlWALRelKind, d.Ref.RelTablespace, d.Ref.RelFileNode).Scan(&d.RelOID, &d.RelKind, &d.RelAM)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			d.DecodeNote = "relation not in the catalog (dropped or rewritten since) — payload shown raw"
		} else {
			d.DecodeNote = "relation lookup failed: " + err.Error()
		}
		return
	}
	if d.IsBtree() {
		cols, err := collect(ctx, pool, fmt.Sprintf("index key columns for %d in %q", d.RelOID, relDB), sqlIndexKeyColumns, []any{d.RelOID}, scanIndexKeyColumn)
		if err != nil {
			d.DecodeNote = "index key layout unavailable: " + err.Error()
			return
		}
		d.IndexCols = cols
		return
	}
	if !d.IsHeap() {
		return
	}
	attrs, err := collect(ctx, pool, fmt.Sprintf("attributes of relation %d in %q", d.RelOID, relDB), sqlWALHeapAttrs, []any{d.RelOID},
		func(row pgx.CollectableRow) (WALHeapAttr, error) {
			var a WALHeapAttr
			err := row.Scan(&a.Attnum, &a.Name, &a.Dropped, &a.TypLen, &a.TypAlign, &a.TypName, &a.TypCategory)
			return a, err
		})
	if err != nil {
		d.DecodeNote = "column layout unavailable: " + err.Error()
		return
	}
	d.Attrs = attrs
}

// fillWALPageImage decodes the full-page image with pageinspect in the
// connected database — its bytea-taking functions need no relation, just the
// extension. The header decodes for any page; line pointers only for heap
// main-fork pages, where heap_page_items' interpretation is valid.
func (c *Client) fillWALPageImage(ctx context.Context, db string, pool *pgxpool.Pool, d *WALBlockDetail) {
	if len(d.FPIData) == 0 {
		return
	}
	if err := c.EnsurePageInspect(ctx, db); err != nil {
		if ext, ok := errors.AsType[*MissingExtensionError](err); ok {
			d.PageInspectMissing = ext
			d.appendNote("page image not decoded: pageinspect is not installed in " + ext.DB)
		} else {
			d.appendNote("page image not decoded: " + err.Error())
		}
		return
	}
	var h WALPageHeader
	err := pool.QueryRow(ctx, sqlWALPageHeader, d.FPIData).Scan(
		&h.LSN, &h.Checksum, &h.Flags, &h.Lower, &h.Upper, &h.Special, &h.PageSize, &h.Version, &h.PruneXid)
	if err != nil {
		d.appendNote("page header not decoded: " + err.Error())
		return
	}
	d.PageHeader = &h
	if d.IsBtree() {
		items, err := collect(ctx, pool, "bt_page_items over page image", sqlWALBtreePageItems, []any{d.FPIData}, scanIndexTuple)
		if err != nil {
			d.appendNote("index items not decoded: " + err.Error())
			return
		}
		d.IndexItems = items
		return
	}
	if !d.IsHeap() {
		switch {
		case d.Ref.ForkNumber != 0:
			d.appendNote(d.Ref.ForkName() + " fork page: no tuples to list")
		case d.RelOID != 0:
			d.appendNote(fmt.Sprintf("%s page (relkind %s): tuples not decoded", d.RelAM, d.RelKind))
		}
		return
	}
	rows, err := pool.Query(ctx, sqlWALHeapPageItems, d.FPIData)
	if err != nil {
		d.appendNote("page items not decoded: " + err.Error())
		return
	}
	defer rows.Close()
	items, err := pgx.CollectRows(rows, scanHeapTuple(heapTupleExtraNone))
	if err != nil {
		d.appendNote("page items not decoded: " + err.Error())
		return
	}
	d.PageItems = items
}

func (d *WALBlockDetail) appendNote(s string) {
	if d.DecodeNote != "" {
		d.DecodeNote += "; "
	}
	d.DecodeNote += s
}
