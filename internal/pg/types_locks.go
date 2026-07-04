package pg

// LockNode is one backend in the blocking forest: a row of pg_stat_activity
// restricted to backends that block or are blocked, plus the lock it is waiting
// on and the PIDs blocking it. Blockers drives the tree assembly in the TUI.
type LockNode struct {
	PID           int32
	Blockers      []int32 // PIDs directly blocking this backend (pg_blocking_pids)
	Database      string
	Username      string
	AppName       string
	State         string // active | idle in transaction | …
	WaitEventType string
	WaitEvent     string
	XactAgeMs     float64 // now() - xact_start in ms; 0 when not in a transaction
	WaitLockType  string  // locktype of the lock it's waiting to acquire ("" when granted/not waiting)
	WaitMode      string  // requested lock mode
	WaitRelation  string  // relation it's waiting on (regclass name; "" when not a relation lock)
	Query         string  // truncated current/last query
}

// Waiting reports whether this backend is itself blocked (has any blocker).
func (n LockNode) Waiting() bool { return len(n.Blockers) > 0 }
