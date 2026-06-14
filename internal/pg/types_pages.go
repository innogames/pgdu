package pg

// HeapPageStat is one row of the page-inspector view: a heap page summarised
// by its line-pointer counts, bytes occupied by live/dead tuples, and the
// per-page flags that drive the bar overlays.
type HeapPageStat struct {
	Blkno       int64
	LSN         string
	Lower       int32 // pd_lower: end of LP array
	Upper       int32 // pd_upper: start of tuple data (free space lives between)
	Special     int32
	PageSize    int32
	Flags       int32 // pd_flags (e.g. PD_HAS_FREE_LINES, PD_ALL_VISIBLE)
	FreeBytes   int32 // upper - lower
	LiveLP      int32
	RedirectLP  int32
	DeadLP      int32
	UnusedLP    int32
	LiveBytes   int64
	DeadBytes   int64
	HotUpdated  int32
	HasExternal int32
}

// DeadFrac returns DeadLP / (LiveLP + DeadLP) in [0,1], or -1 when the page
// has no live or dead tuples (e.g. a freshly-allocated page).
func (p HeapPageStat) DeadFrac() float64 {
	total := p.LiveLP + p.DeadLP
	if total <= 0 {
		return -1
	}
	return float64(p.DeadLP) / float64(total)
}

// HeapTuple is one entry in the line-pointer array of a heap page. Pointer
// fields (Xmin/Xmax/Oid/Bits/Data) are nil when pageinspect reports NULL —
// chiefly for LP_UNUSED and LP_REDIRECT line pointers.
// ChunkID/ChunkSeq are non-nil only for TOAST tables (schema = "pg_toast"),
// and only when the line pointer holds a live chunk row — DEAD/UNUSED/REDIRECT
// entries yield nil here too.
type HeapTuple struct {
	LP        int32
	LPOff     int32
	LPFlags   int32 // 0=UNUSED 1=NORMAL 2=REDIRECT 3=DEAD
	LPLen     int32
	Xmin      *uint32
	Xmax      *uint32
	Field3    *int32
	Ctid      *string // "(blk,off)" — NULL for LP_REDIRECT / LP_UNUSED
	Infomask2 int32
	Infomask  int32
	Hoff      *int32
	Bits      *string
	Oid       *uint32
	Data      []byte
	ChunkID   *uint32 // TOAST only: chunk_id of this chunk row
	ChunkSeq  *int32  // TOAST only: chunk_seq within its chunk_id
}

// Line-pointer flag values from src/include/storage/itemid.h.
const (
	LPUnused   = 0
	LPNormal   = 1
	LPRedirect = 2
	LPDead     = 3
)

// t_infomask flag bits from access/htup_details.h. Bits not surfaced in the
// TUI (heap-only OID, frozen-via-multi, etc.) are intentionally omitted to
// keep the badge list scannable.
const (
	HeapHasNull       = 0x0001
	HeapHasVarWidth   = 0x0002
	HeapHasExternal   = 0x0004
	HeapHasOid        = 0x0008
	HeapXminCommitted = 0x0100
	HeapXminInvalid   = 0x0200
	HeapXmaxCommitted = 0x0400
	HeapXmaxInvalid   = 0x0800
	HeapXmaxIsMulti   = 0x1000
	HeapUpdated       = 0x2000
	HeapMovedOff      = 0x4000
	HeapMovedIn       = 0x8000
)

// t_infomask2 flag bits — numerically overlap with t_infomask, so they get
// distinct Go names.
const (
	HeapKeysUpdated2 = 0x2000
	HeapHotUpdated2  = 0x4000
	HeapOnlyTuple2   = 0x8000
)

// IndexPageStat is one row of the B-tree page-inspector view: a page
// summarised by its bt_page_stats output. Type is the single-character
// page type from pageinspect — 'l' leaf, 'r' root, 'i' internal, 'd'
// deleted. BtpoLevel is the page's depth in the tree (0 = leaf).
type IndexPageStat struct {
	Blkno       int32
	Type        string
	LiveItems   int32
	DeadItems   int32
	AvgItemSize int32
	PageSize    int32
	FreeSize    int32
	BtpoPrev    int32
	BtpoNext    int32
	BtpoLevel   int32
	BtpoFlags   int32
}

// DeadFrac returns DeadItems / (LiveItems + DeadItems) in [0,1], or -1 when
// the page has no items (typically a meta or deleted page).
func (p IndexPageStat) DeadFrac() float64 {
	total := p.LiveItems + p.DeadItems
	if total <= 0 {
		return -1
	}
	return float64(p.DeadItems) / float64(total)
}

// IndexTuple is one entry on a B-tree page from bt_page_items. On leaf
// pages Ctid points to the heap row; on internal pages it's a downlink
// (block,0) referring to a child index page. Data is pageinspect's
// raw key bytes as a hex string — a fallback when the structured
// Decoded value isn't available.
//
// Decoded is the per-item projection of the index's column expressions
// from the heap (e.g. "(42,alice)" for a (id,name) index). Populated
// only on leaf/root pages whose ctid still resolves to a live heap row;
// nil for internal-page downlinks and entries whose heap tuple is gone
// (vacuumed away after the page snapshot, or beyond the snapshot's
// visibility horizon).
type IndexTuple struct {
	ItemOffset int32
	Ctid       *string
	ItemLen    int32
	Nulls      *bool
	Vars       *bool
	Data       *string
	Decoded    *string
}

// TupleCell is one column of a heap row decoded for the row-detail view.
// Value is nil for SQL NULLs so the renderer can show them distinctly from
// empty strings or zero values.
type TupleCell struct {
	Name  string
	Value *string
}
