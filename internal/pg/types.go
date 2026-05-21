package pg

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
}

func (b BufferCacheSummary) FreeBytes() int64 {
	free := b.TotalBytes - b.ThisDBBytes - b.OtherDBBytes
	if free < 0 {
		return 0
	}
	return free
}
