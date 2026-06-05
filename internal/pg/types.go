package pg

import (
	"fmt"
	"strings"
	"time"
)

// MissingExtensionError signals that an optional Postgres extension pgdu
// would like to use isn't installed in the target database. The TUI uses
// the typed error to offer an interactive `CREATE EXTENSION` instead of
// either silently degrading or failing with an opaque message.
type MissingExtensionError struct {
	Extension string
	DB        string
	// Installable is true when the extension shows up in pg_available_extensions
	// (i.e. CREATE EXTENSION would succeed given sufficient privileges).
	Installable bool
}

func (e *MissingExtensionError) Error() string {
	if e.Installable {
		return fmt.Sprintf("extension %q is not installed in %q (can be installed)", e.Extension, e.DB)
	}
	return fmt.Sprintf("extension %q is not installed in %q and not available on the server", e.Extension, e.DB)
}

// Database row from sqlDatabases.
type Database struct {
	Name      string
	SizeBytes int64
}

// Schema row from sqlSchemas.
type Schema struct {
	DB         string
	Name       string
	SizeBytes  int64
	TableCount int64
}

// Table row from sqlTables.
type Table struct {
	DB           string
	Schema       string
	OID          uint32
	Name         string
	HeapBytes    int64
	IndexesBytes int64
	ToastBytes   int64
	TotalBytes   int64
	EstRows      int64

	// ToastOID is c.reltoastrelid — non-zero whenever the table has a TOAST
	// relation, even when ToastBytes is 0 (toast exists but currently holds
	// no out-of-line values). ToastName is the qualified relation name
	// ("pg_toast.pg_toast_<oid>") so it can be surfaced as metadata.
	ToastOID  uint32
	ToastName string
}

func (t Table) Qualified() string { return t.Schema + "." + t.Name }

// PartKind classifies a row in the per-table parts view.
type PartKind int

const (
	PartHeap PartKind = iota
	PartToast
	PartIndex
)

func (k PartKind) String() string {
	switch k {
	case PartHeap:
		return "heap"
	case PartToast:
		return "toast"
	case PartIndex:
		return "index"
	}
	return "?"
}

// Part is one piece of a table's storage: the heap, the toast relation, or
// one of its indexes. WastedBytes is populated for the kinds we can measure.
type Part struct {
	Kind         PartKind
	OID          uint32 // index relation oid; 0 for heap/toast
	Name         string // e.g. "heap", "toast", "idx_users_email"
	SizeBytes    int64
	WastedBytes  int64 // bloat, when known
	HasBloat     bool  // true once bloat has been computed (even if 0)
	IsPrimary    bool
	IsUnique     bool
	AccessMethod string // for indexes

	// HeapStats is populated only for PartHeap (from pg_stat_all_tables).
	// Nil for other kinds, or for the heap when stats are unavailable.
	HeapStats *HeapStats

	// ToastName is the underlying TOAST relation name (e.g.
	// "pg_toast.pg_toast_16438"). Populated only for PartToast — shown as
	// metadata so users can correlate to pg_class entries.
	ToastName string
}

// RelationKind discriminates a row in the page-inspector relation list: heap
// table vs. B-tree index vs. TOAST heap. Other access methods aren't drillable
// here so they don't get a kind.
type RelationKind int

const (
	RelTable RelationKind = iota
	RelBTreeIndex
	// RelToast is a TOAST storage heap (relkind 't'). Its heap pages drill the
	// same way as RelTable; the line-pointer list additionally decodes
	// chunk_id/chunk_seq for each live chunk.
	RelToast
)

// Relation is one entry in the page-inspector tool's mixed table+index+toast
// list. Carries everything later screens need (OID for get_raw_page, qualified
// name for regclass binds, est-rows/pages for the row summary). For
// RelBTreeIndex and RelToast rows, ParentOID/ParentName name the owning table
// so the list stays comprehensible after sort interleaves rows.
type Relation struct {
	Kind         RelationKind
	DB           string
	Schema       string
	OID          uint32
	Name         string
	SizeBytes    int64
	EstRows      int64
	Pages        int32
	AccessMethod string // "btree" for indexes, "" for tables
	ParentOID    uint32
	ParentName   string
}

func (r Relation) Qualified() string { return r.Schema + "." + r.Name }

// HeapStats summarises the autovacuum-relevant counters for one table's heap.
// All fields come from pg_stat_all_tables; *time.Time fields are nil when the
// table has never been (auto)vacuumed/(auto)analyzed.
type HeapStats struct {
	NLive           int64
	NDead           int64
	LastVacuum      *time.Time
	LastAutovacuum  *time.Time
	LastAnalyze     *time.Time
	LastAutoanalyze *time.Time
}

// LastVacuumed returns the more recent of LastVacuum / LastAutovacuum, or nil.
func (h *HeapStats) LastVacuumed() *time.Time {
	return latest(h.LastVacuum, h.LastAutovacuum)
}

// LastAnalyzed returns the more recent of LastAnalyze / LastAutoanalyze, or nil.
func (h *HeapStats) LastAnalyzed() *time.Time {
	return latest(h.LastAnalyze, h.LastAutoanalyze)
}

// DeadFrac returns NDead / (NLive + NDead) in [0,1], or -1 when no rows.
func (h *HeapStats) DeadFrac() float64 {
	total := h.NLive + h.NDead
	if total <= 0 {
		return -1
	}
	return float64(h.NDead) / float64(total)
}

func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.After(*b):
		return a
	}
	return b
}

// Column is one row of the per-column space view: an estimate of how much
// heap space the column occupies, derived entirely from planner statistics
// (no table scan).
type Column struct {
	Name      string
	Type      string  // e.g. "text", "varchar(64)", "integer"
	AvgWidth  int     // pg_stats.avg_width, bytes per non-null value
	NullFrac  float64 // pg_stats.null_frac, [0,1]
	EstBytes  int64   // avg_width × (1 − null_frac) × reltuples
	Toastable bool    // column has TOAST-eligible storage AND its table has a TOAST relation
}

// TableBufferStat is one row of the shared-buffers view: how much of
// shared_buffers a table currently occupies (heap + toast + indexes summed)
// and its cumulative cache hit ratio.
type TableBufferStat struct {
	DB            string
	Schema        string
	Name          string
	OID           uint32
	BufferedBytes int64 // pages in shared_buffers * block_size
	TotalBytes    int64 // pg_total_relation_size(oid), for "% cached" context
	Hits          int64 // heap_blks_hit + idx_blks_hit
	Reads         int64 // heap_blks_read + idx_blks_read
}

// HitRatio returns hits / (hits + reads) in [0,1], or -1 when the table has
// not been read from since stats were last reset.
func (s TableBufferStat) HitRatio() float64 {
	total := s.Hits + s.Reads
	if total <= 0 {
		return -1
	}
	return float64(s.Hits) / float64(total)
}

// BufferCacheSummary is a cluster-wide snapshot of shared_buffers occupancy,
// split by who is using each page: the database the user is currently viewing,
// any other database (including shared catalogs), or unused.
type BufferCacheSummary struct {
	TotalBytes   int64
	ThisDBBytes  int64
	OtherDBBytes int64
	// ServerMemBytes is the host's total RAM (MemTotal from /proc/meminfo),
	// read locally by pgdu — not from Postgres. Zero when unavailable.
	ServerMemBytes int64
	// ServerMemAvailableBytes is MemAvailable from /proc/meminfo: free pages
	// plus reclaimable cache, i.e. what's actually usable by new workloads.
	// Zero on kernels too old to expose MemAvailable, or when not readable.
	ServerMemAvailableBytes int64
	// ServerMemFreeBytes is MemFree from /proc/meminfo — strictly unallocated
	// memory, excluding the kernel page cache. Zero when unavailable.
	ServerMemFreeBytes int64
}

func (b BufferCacheSummary) FreeBytes() int64 {
	free := b.TotalBytes - b.ThisDBBytes - b.OtherDBBytes
	if free < 0 {
		return 0
	}
	return free
}

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

// --- WAL inspector types (toolWAL) ---

// WALSummary is the header snapshot for the WAL inspector overview: the
// current write position, the segment file it lands in, wal_level, the
// pg_wal directory's file count and size, and the cluster-wide pg_stat_wal
// generation counters. StartLSN/EndLSN/WindowBytes describe the LSN window
// the rmgr breakdown below was computed over. All built-in sources, so it
// renders even without pg_walinspect — but a privilege error on pg_ls_waldir
// / pg_stat_wal is treated as non-fatal by the caller (summary "unavailable").
type WALSummary struct {
	InsertLSN    string
	FlushLSN     string
	CurrentFile  string
	WalLevel     string
	SegmentFiles int64
	SegmentBytes int64
	StatRecords  int64
	StatFPI      int64
	StatBytes    int64

	// Window the rmgr stats were computed over (resolved by sqlWALWindow).
	StartLSN    string
	EndLSN      string
	WindowBytes int64
}

// WALRmgrStat is one resource-manager row of the WAL overview: how many
// records that manager wrote in the window and how those bytes split between
// record data and full-page images (FPI). CombinedSize = RecordSize + FPISize.
type WALRmgrStat struct {
	Name         string
	Count        int64
	RecordSize   int64
	FPISize      int64
	CombinedSize int64
}

// WALRecord is one entry from pg_get_wal_records_info: a single WAL record's
// position, owning xid, type, byte breakdown and human-readable description.
// LSN/xid fields are kept as text (pg_lsn/xid have no pgx codec; cast ::text
// in SQL). Xid is "0" for non-transactional records (checkpoints, etc.).
type WALRecord struct {
	StartLSN       string
	EndLSN         string
	PrevLSN        string
	Xid            string
	Rmgr           string
	RecordType     string
	RecordLength   int32
	MainDataLength int32
	FPILength      int32
	Description    string
	BlockRef       string
}

// CombinedSize is record bytes plus full-page-image bytes — what the bar in
// the records view scales against.
func (r WALRecord) CombinedSize() int64 { return int64(r.RecordLength) + int64(r.FPILength) }

// WALBlockRef is one block reference of a record, from pg_get_wal_block_info
// (PostgreSQL 16+). It ties the record back to a concrete relation block:
// (RelDatabase, RelFileNode, ForkNumber, BlockNumber). FPILength > 0 means
// this record carried a full-page image of the block — the dominant source
// of WAL write amplification.
type WALBlockRef struct {
	BlockID         int32
	RelTablespace   uint32
	RelDatabase     uint32
	RelFileNode     uint32
	ForkNumber      int32
	BlockNumber     int64
	Rmgr            string
	RecordType      string
	BlockDataLength int32
	FPILength       int32
	// FPIInfo is the text[] of full-page-image flag names (e.g. {APPLY},
	// {APPLY,COMPRESSED}); nil when this block carried no page image.
	FPIInfo     []string
	Description string
	// RelName is the relation this block belongs to, resolved from
	// relfilenode via pg_filenode_relation. For a TOAST relation this is the
	// owning table's name, not the pg_toast.pg_toast_<oid> internal name.
	// Empty when the relation is in another database or has been dropped
	// (relfilenode no longer maps).
	RelName string
	// IsToast reports that the block belongs to a TOAST relation; RelName then
	// names the owning table and the UI tags the row with "(toast)".
	IsToast bool
	// DBName is reldatabase resolved against pg_database. Empty for shared
	// relations (reldatabase 0) or an unknown OID, in which case the UI falls
	// back to the numeric database OID.
	DBName string
}

// HeapTID best-effort-extracts the tuple id (block, offset) this block
// reference touched. The block number is RelBlockNumber; the offset is parsed
// from the record description, which for heap records reads like
// "off 15 flags 0x00". Only meaningful on the main fork — index/fsm/vm forks
// have no heap tuple — so it returns ("", false) elsewhere or when the
// description carries no "off N". Multi-block records repeat one description,
// so the offset is the record's primary tuple, not necessarily this block's.
func (b WALBlockRef) HeapTID() (string, bool) {
	if b.ForkNumber != 0 {
		return "", false
	}
	const marker = "off "
	i := strings.Index(b.Description, marker)
	if i < 0 {
		return "", false
	}
	rest := b.Description[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return "", false
	}
	return fmt.Sprintf("(%d,%s)", b.BlockNumber, rest[:j]), true
}

// ForkName maps relforknumber to its short fork name (main/fsm/vm/init).
func (b WALBlockRef) ForkName() string {
	switch b.ForkNumber {
	case 0:
		return "main"
	case 1:
		return "fsm"
	case 2:
		return "vm"
	case 3:
		return "init"
	}
	return fmt.Sprintf("fork%d", b.ForkNumber)
}

// --- describe types (psql \d-style object description) ---

// DescribeKind discriminates a Description: a table or a single index.
type DescribeKind int

const (
	DescribeTable DescribeKind = iota
	DescribeIndex
)

// DescribeColumn is one row of a table's column list in the describe view.
type DescribeColumn struct {
	Name    string
	Type    string // format_type output, e.g. "text", "varchar(64)"
	NotNull bool
	Default string // pg_get_expr output; "" when there is no default
}

// DescribeIndexDef is one index entry in the describe-table view.
type DescribeIndexDef struct {
	Name      string
	Def       string // pg_get_indexdef full CREATE INDEX text
	IsPrimary bool
	IsUnique  bool
}

// DescribeConstraint is one row from pg_constraint in the describe-table view.
type DescribeConstraint struct {
	Name string
	Def  string // pg_get_constraintdef output
}

// Description is the fully-loaded \d-style payload for one object.
// Kind discriminates which fields are populated: DescribeTable uses
// Columns/Indexes/Constraints/SizeBytes/EstRows; DescribeIndex uses the
// Index* fields.
type Description struct {
	Kind  DescribeKind
	OID   uint32 // target relation oid; used to guard the loaded message
	Title string // qualified object name for the panel header

	// Table describe fields.
	Columns     []DescribeColumn
	Indexes     []DescribeIndexDef
	Constraints []DescribeConstraint
	SizeBytes   int64
	EstRows     int64

	// Index describe fields.
	IndexDef     string // pg_get_indexdef(oid) — full CREATE INDEX statement
	AccessMethod string // amname, e.g. "btree"
	IdxUnique    bool
	IdxPrimary   bool
	Predicate    string // pg_get_expr(indpred) for partial indexes; "" otherwise
	ParentTable  string // indrelid::regclass::text
}

// QueryStat is one row of pg_stat_statements. We read the 1.11 column set,
// which exists on PostgreSQL 17 (1.11) and is a subset of 18 (1.12), so the
// same query works on both. Counter fields (Calls, Rows, *Blks*, WAL*, the
// total/exec times) are cumulative since the last stats reset — the TUI takes
// a baseline snapshot on entry and shows the delta against it, which is how a
// time window is fabricated without storing history (see DiffStatements).
//
// Min/Max/Stddev exec time are kept for completeness but are NOT meaningful on
// a delta (you can't subtract two extrema), so the diff zeroes them and the
// detail view shows only window-decomposable metrics.
type QueryStat struct {
	QueryID int64
	UserID  uint32
	DBID    uint32
	Query   string

	Calls int64
	Rows  int64

	TotalExecTime  float64 // milliseconds
	MinExecTime    float64
	MaxExecTime    float64
	MeanExecTime   float64
	StddevExecTime float64

	Plans         int64
	TotalPlanTime float64 // milliseconds; 0 when track_planning is off

	SharedBlksHit     int64
	SharedBlksRead    int64
	SharedBlksDirtied int64
	SharedBlksWritten int64
	LocalBlksHit      int64
	LocalBlksRead     int64
	LocalBlksDirtied  int64
	LocalBlksWritten  int64
	TempBlksRead      int64
	TempBlksWritten   int64

	SharedBlkReadTime  float64 // milliseconds
	SharedBlkWriteTime float64
	LocalBlkReadTime   float64
	LocalBlkWriteTime  float64
	TempBlkReadTime    float64
	TempBlkWriteTime   float64

	WALRecords int64
	WALFPI     int64
	WALBytes   int64
}

// sub returns the window delta of q relative to a baseline snapshot b. Counter
// fields are subtracted; identity (QueryID/Query/ids) comes from q (the newer
// snapshot, in case the query text was re-normalised). MeanExecTime is
// recomputed from the delta; the extrema are not subtractable so they're zero.
func (q QueryStat) sub(b QueryStat) QueryStat {
	d := q
	d.Calls = q.Calls - b.Calls
	d.Rows = q.Rows - b.Rows
	d.TotalExecTime = q.TotalExecTime - b.TotalExecTime
	d.Plans = q.Plans - b.Plans
	d.TotalPlanTime = q.TotalPlanTime - b.TotalPlanTime
	d.SharedBlksHit = q.SharedBlksHit - b.SharedBlksHit
	d.SharedBlksRead = q.SharedBlksRead - b.SharedBlksRead
	d.SharedBlksDirtied = q.SharedBlksDirtied - b.SharedBlksDirtied
	d.SharedBlksWritten = q.SharedBlksWritten - b.SharedBlksWritten
	d.LocalBlksHit = q.LocalBlksHit - b.LocalBlksHit
	d.LocalBlksRead = q.LocalBlksRead - b.LocalBlksRead
	d.LocalBlksDirtied = q.LocalBlksDirtied - b.LocalBlksDirtied
	d.LocalBlksWritten = q.LocalBlksWritten - b.LocalBlksWritten
	d.TempBlksRead = q.TempBlksRead - b.TempBlksRead
	d.TempBlksWritten = q.TempBlksWritten - b.TempBlksWritten
	d.SharedBlkReadTime = q.SharedBlkReadTime - b.SharedBlkReadTime
	d.SharedBlkWriteTime = q.SharedBlkWriteTime - b.SharedBlkWriteTime
	d.LocalBlkReadTime = q.LocalBlkReadTime - b.LocalBlkReadTime
	d.LocalBlkWriteTime = q.LocalBlkWriteTime - b.LocalBlkWriteTime
	d.TempBlkReadTime = q.TempBlkReadTime - b.TempBlkReadTime
	d.TempBlkWriteTime = q.TempBlkWriteTime - b.TempBlkWriteTime
	d.WALRecords = q.WALRecords - b.WALRecords
	d.WALFPI = q.WALFPI - b.WALFPI
	d.WALBytes = q.WALBytes - b.WALBytes
	d.MinExecTime, d.MaxExecTime, d.StddevExecTime = 0, 0, 0
	if d.Calls > 0 {
		d.MeanExecTime = d.TotalExecTime / float64(d.Calls)
	} else {
		d.MeanExecTime = 0
	}
	return d
}

// MeanTime is the average execution time per call in milliseconds.
func (q QueryStat) MeanTime() float64 {
	if q.Calls <= 0 {
		return 0
	}
	return q.TotalExecTime / float64(q.Calls)
}

// HitRatio is the shared-buffer cache hit ratio as a percentage. The bool is
// false when there was no block access at all (ratio undefined → render "—").
func (q QueryStat) HitRatio() (float64, bool) {
	total := q.SharedBlksHit + q.SharedBlksRead
	if total <= 0 {
		return 0, false
	}
	return float64(q.SharedBlksHit) / float64(total) * 100, true
}

// IOTime is the total block read+write time (shared+local+temp) in milliseconds.
func (q QueryStat) IOTime() float64 {
	return q.SharedBlkReadTime + q.SharedBlkWriteTime +
		q.LocalBlkReadTime + q.LocalBlkWriteTime +
		q.TempBlkReadTime + q.TempBlkWriteTime
}

// RowsPerCall is the average rows returned/affected per call.
func (q QueryStat) RowsPerCall() float64 {
	if q.Calls <= 0 {
		return 0
	}
	return float64(q.Rows) / float64(q.Calls)
}

// DiffStatements computes the window deltas of a fresh snapshot against a
// baseline keyed by queryid. Queries with no activity in the window (≤0 calls)
// are dropped; queries new since the baseline keep their full counters.
func DiffStatements(baseline map[int64]QueryStat, current []QueryStat) []QueryStat {
	out := make([]QueryStat, 0, len(current))
	for _, c := range current {
		d := c
		if b, ok := baseline[c.QueryID]; ok {
			d = c.sub(b)
		}
		if d.Calls <= 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ParamType describes one positional parameter ($1, $2, …) of a normalized
// query, as inferred by PREPARE. Type is the regtype name, e.g. "integer".
type ParamType struct {
	Ordinal int
	Type    string
}
