package pg

// ActivityFilter controls which backends are listed in the Activity tool.
type ActivityFilter int

const (
	// ActivityActiveWaiting shows only backends that are actively running a query
	// or blocked on a wait event. Excludes idle and idle-in-transaction.
	ActivityActiveWaiting ActivityFilter = iota
	// ActivityNonIdle shows everything except plain idle backends — includes
	// active, idle-in-transaction, idle-in-transaction (aborted), fastpath function call.
	ActivityNonIdle
	// ActivityAll shows every backend including idle connections.
	ActivityAll
)

// Label returns the human-readable filter name for the status line.
func (f ActivityFilter) Label() string {
	switch f {
	case ActivityActiveWaiting:
		return "active+waiting"
	case ActivityNonIdle:
		return "non-idle"
	case ActivityAll:
		return "all"
	}
	return "?"
}

// Next cycles to the next filter mode.
func (f ActivityFilter) Next() ActivityFilter {
	switch f {
	case ActivityActiveWaiting:
		return ActivityNonIdle
	case ActivityNonIdle:
		return ActivityAll
	default:
		return ActivityActiveWaiting
	}
}

// ActivityRow holds one row from pg_stat_activity.
type ActivityRow struct {
	PID           int32
	Database      string
	Username      string
	AppName       string
	ClientAddr    string // raw inet host string, empty for local/unix-socket
	BackendType   string
	State         string // active | idle | idle in transaction | …
	WaitEventType string
	WaitEvent     string
	BackendXid    string  // nil → empty
	BackendXmin   string  // nil → empty
	QueryAgeMs    float64 // now() - query_start in ms; 0 when query_start is NULL
	XactAgeMs     float64 // now() - xact_start in ms; 0 when xact_start is NULL
	StateAgeMs    float64 // now() - state_change in ms; 0 when state_change is NULL
	QueryID       int64   // pg_stat_statements queryid (PG 14+); 0 when unknown
	Query         string  // truncated normalized query text
}
