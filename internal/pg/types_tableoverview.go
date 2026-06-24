package pg

// TableStat is one row of the Table overview tool (toolTableStats): per-table
// size, write/scan activity, shared-buffer hit counters, maintenance counters
// and storage options, gathered in a single pass over a schema (see
// ListTableStats). All counters are cumulative since the last stats reset
// (pg_stat reset semantics); the TUI presents them as-is and derives the ratios
// below. The Age fields are pre-computed server-side as milliseconds.
type TableStat struct {
	DB     string
	Schema string
	OID    uint32
	Name   string

	RelKind        string // 'r' table, 'm' matview, 'p' partitioned
	RelPersistence string // 'p' permanent, 'u' unlogged, 't' temp
	RelOptions     []string
	EstRows        int64 // reltuples (planner estimate)

	// Sizes (bytes). ToastOID/ToastName let drill-in reconstruct a pg.Table for
	// the disk "parts" / describe views without a second catalog lookup.
	HeapBytes    int64
	IndexesBytes int64
	ToastBytes   int64
	TotalBytes   int64
	ToastOID     uint32
	ToastName    string

	FrozenXIDAge int64 // age(relfrozenxid); 0 for partitioned parents (no storage)

	// pg_stat_all_tables counters.
	NLive, NDead                      int64
	NInsert, NUpdate, NDelete         int64
	NHotUpdate                        int64
	NModSinceAnalyze, NInsSinceVacuum int64
	SeqScan, IdxScan                  int64
	SeqTupRead, IdxTupFetch           int64
	VacuumCount, AutovacuumCount      int64
	AnalyzeCount, AutoanalyzeCount    int64

	// Milliseconds since the most recent (auto)vacuum / (auto)analyze; nil when
	// the table has never been vacuumed / analyzed (GREATEST of NULLs → NULL).
	VacAgeMs *float64
	AnaAgeMs *float64

	// pg_statio_all_tables block counters (shared-buffer hits vs disk reads).
	HeapBlksRead, HeapBlksHit int64
	IdxBlksRead, IdxBlksHit   int64
}

// AsTable reconstructs the foundation Table for this row so drill-in can reuse
// the disk "parts" view and `d` (describe) without another catalog round-trip.
func (s TableStat) AsTable() Table {
	return Table{
		DB: s.DB, Schema: s.Schema, OID: s.OID, Name: s.Name,
		HeapBytes: s.HeapBytes, IndexesBytes: s.IndexesBytes,
		ToastBytes: s.ToastBytes, TotalBytes: s.TotalBytes,
		EstRows:  s.EstRows,
		ToastOID: s.ToastOID, ToastName: s.ToastName,
	}
}

// Writes is total row churn (ins+upd+del) — a quick "how active" measure.
func (s TableStat) Writes() int64 { return s.NInsert + s.NUpdate + s.NDelete }

// DeadPct is dead tuples as a percentage of live+dead (0 when empty). Higher is
// worse (bloat / vacuum lag).
func (s TableStat) DeadPct() float64 {
	tot := s.NLive + s.NDead
	if tot <= 0 {
		return 0
	}
	return 100 * float64(s.NDead) / float64(tot)
}

// HotPct is the HOT-update share of all updates; ok is false when there were no
// updates. Higher is better (cheap updates, no index write amplification).
func (s TableStat) HotPct() (float64, bool) {
	if s.NUpdate <= 0 {
		return 0, false
	}
	return 100 * float64(s.NHotUpdate) / float64(s.NUpdate), true
}

// SeqPct is the share of scans that were sequential; ok is false when the table
// has never been scanned. Higher is worse (possible missing index).
func (s TableStat) SeqPct() (float64, bool) {
	tot := s.SeqScan + s.IdxScan
	if tot <= 0 {
		return 0, false
	}
	return 100 * float64(s.SeqScan) / float64(tot), true
}

// HeapHitPct is the heap (table) shared-buffer hit ratio; ok is false when no
// heap blocks have been touched. Higher is better.
func (s TableStat) HeapHitPct() (float64, bool) {
	tot := s.HeapBlksRead + s.HeapBlksHit
	if tot <= 0 {
		return 0, false
	}
	return 100 * float64(s.HeapBlksHit) / float64(tot), true
}

// IdxHitPct is the index shared-buffer hit ratio; ok is false when no index
// blocks have been touched.
func (s TableStat) IdxHitPct() (float64, bool) {
	tot := s.IdxBlksRead + s.IdxBlksHit
	if tot <= 0 {
		return 0, false
	}
	return 100 * float64(s.IdxBlksHit) / float64(tot), true
}

// IdxHeapRatio is index bytes / heap bytes (over-indexing signal); ok is false
// for an empty heap.
func (s TableStat) IdxHeapRatio() (float64, bool) {
	if s.HeapBytes <= 0 {
		return 0, false
	}
	return float64(s.IndexesBytes) / float64(s.HeapBytes), true
}

// FillFactor returns the table's effective FILLFACTOR storage parameter, or 100
// (the heap default) when unset.
func (s TableStat) FillFactor() int {
	return int(optInt64(ParseRelOptions(s.RelOptions), "fillfactor", 100))
}

// AutovacuumReloption reports the per-table autovacuum_enabled storage parameter
// when explicitly set: ("on"|"off", true). ok is false when the table inherits
// the cluster default (the common case), so the column can render "—".
func (s TableStat) AutovacuumReloption() (string, bool) {
	opts := ParseRelOptions(s.RelOptions)
	if _, ok := opts["autovacuum_enabled"]; !ok {
		return "", false
	}
	if optBool(opts, "autovacuum_enabled", true) {
		return "on", true
	}
	return "off", true
}

// Persistence renders relpersistence as a word: permanent / unlogged / temp.
func (s TableStat) Persistence() string {
	switch s.RelPersistence {
	case "u":
		return "unlogged"
	case "t":
		return "temp"
	default:
		return "permanent"
	}
}
