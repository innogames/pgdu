package pg

import (
	"fmt"
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

// TupleCell is one column of a heap row decoded for the row-detail view.
// Value is nil for SQL NULLs so the renderer can show them distinctly from
// empty strings or zero values.
type TupleCell struct {
	Name  string
	Value *string
}
