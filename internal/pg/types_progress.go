package pg

// ProgressRow is one running operation from the unified pg_stat_progress_*
// query (sqlProgressOps): a maintenance/DDL command, its target relation, the
// current phase, and raw done/total counters whose Unit says what they count
// ("blocks" or "bytes").
type ProgressRow struct {
	PID       int32
	Command   string
	Relation  string // empty when the view has no relid (e.g. base backup)
	RelID     uint32 // raw relid; 0 when the view has none
	Database  string // database the operation runs in ("" for base backup)
	Phase     string
	Unit      string // "blocks", "bytes", "indexes" (vacuum index passes), or "rows" (COPY TO estimate)
	Done      int64
	Total     int64
	Approx    bool // total is an estimate (COPY TO vs pg_class.reltuples), not a server counter
	RunningMs float64
	Username  string
}

// Pct returns completion as 0..100, or -1 when the total is unknown (some
// views report total 0 until they have an estimate).
func (r ProgressRow) Pct() float64 {
	if r.Total <= 0 {
		return -1
	}
	return 100 * float64(r.Done) / float64(r.Total)
}
