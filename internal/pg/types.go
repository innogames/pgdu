package pg

import "time"

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
	BufferedBytes int64   // pages in shared_buffers * block_size
	TotalBytes    int64   // pg_total_relation_size(oid), for "% cached" context
	Hits          int64   // heap_blks_hit + idx_blks_hit
	Reads         int64   // heap_blks_read + idx_blks_read
	DirtyBytes    int64   // buffered pages flagged isdirty * block_size
	UsageAvg      float64 // mean clock-sweep usagecount across this table's buffers (0..5)
}

// CachedFrac returns BufferedBytes / TotalBytes in [0,1], or -1 when the
// table's on-disk size is unknown (zero) so callers can show "—".
func (s TableBufferStat) CachedFrac() float64 {
	if s.TotalBytes <= 0 {
		return -1
	}
	return float64(s.BufferedBytes) / float64(s.TotalBytes)
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
	// UsageCounts is the cluster-wide clock-sweep "temperature" histogram from
	// pg_buffercache_usage_counts(): one entry per usagecount (0 = cold/evictable,
	// up to 5 = hot). Nil when the function is unavailable. Drives the temperature
	// bar in the shared-buffers summary.
	UsageCounts []BufferUsageCount
}

// BufferUsageCount is one bucket of the clock-sweep usage histogram: how many
// shared_buffers pages currently sit at this usagecount, and how many of those
// are dirty (modified, awaiting flush) or pinned (in use right now). Returned
// both cluster-wide (pg_buffercache_usage_counts) and per-table.
type BufferUsageCount struct {
	Count   int
	Buffers int64
	Dirty   int64
	Pinned  int64
}

func (b BufferCacheSummary) FreeBytes() int64 {
	free := b.TotalBytes - b.ThisDBBytes - b.OtherDBBytes
	if free < 0 {
		return 0
	}
	return free
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
	Clustered bool // pg_index.indisclustered: table is CLUSTERed on this index
}

// Description is the fully-loaded \d-style payload for one object.
// Kind discriminates which fields are populated: DescribeTable uses
// Columns/Indexes/SizeBytes/EstRows; DescribeIndex uses the Index* fields.
type Description struct {
	Kind  DescribeKind
	OID   uint32 // target relation oid; used to guard the loaded message
	Title string // qualified object name for the panel header

	// Table describe fields.
	Columns   []DescribeColumn
	Indexes   []DescribeIndexDef
	SizeBytes int64
	EstRows   int64

	// Index describe fields.
	IndexDef     string // pg_get_indexdef(oid) — full CREATE INDEX statement
	AccessMethod string // amname, e.g. "btree"
	IdxUnique    bool
	IdxPrimary   bool
	Predicate    string // pg_get_expr(indpred) for partial indexes; "" otherwise
	ParentTable  string // indrelid::regclass::text
}
