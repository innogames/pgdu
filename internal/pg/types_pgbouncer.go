package pg

import (
	"net"
	"path/filepath"
	"strconv"
)

// pgbDefaultPort is pgbouncer's compiled-in listen_port.
const pgbDefaultPort = 6432

// PgBouncerInstance is one pgbouncer process (running or merely configured)
// as seen by discovery. Fields sourced from the ini are empty when the ini was
// unreadable — IniErr then says why, and the instance keeps whatever /proc or
// the explicit target told us.
type PgBouncerInstance struct {
	Name       string // ini basename without .ini, else host:port
	IniPath    string
	PID        int // 0 = not seen running
	SocketDir  string
	ListenAddr string
	ListenPort int
	Logfile    string
	Pidfile    string
	PoolMode   string
	AuthType   string
	AuthFile   string
	AdminUsers []string
	StatsUsers []string
	Reason     string // discovery source: --pgbouncer-target, /proc, /etc/pgbouncer, connection target
	IniErr     error
	// DSN, when set, overrides the target derived from the fields above. Only
	// the "connection target" source uses it: that instance is whatever pgdu's
	// own -h/-p points at, so it borrows the very same connection parameters.
	DSN string
}

// Port is ListenPort with the pgbouncer default applied.
func (i PgBouncerInstance) Port() int {
	if i.ListenPort > 0 {
		return i.ListenPort
	}
	return pgbDefaultPort
}

// SocketPath is the unix socket this instance listens on, "" without a
// socket dir.
func (i PgBouncerInstance) SocketPath() string {
	if i.SocketDir == "" {
		return ""
	}
	return filepath.Join(i.SocketDir, ".s.PGSQL."+strconv.Itoa(i.Port()))
}

// Target picks how to reach the instance: the unix socket when it exists
// locally, otherwise TCP on listen_addr. The socket is strongly preferred —
// several instances on one host commonly share a TCP port via so_reuseport,
// and then the kernel hands a TCP connect to *any* of them, so only the
// per-instance socket addresses a specific one.
func (i PgBouncerInstance) Target() (host string, port int, unix bool) {
	port = i.Port()
	if p := i.SocketPath(); p != "" {
		if fi, err := statSocket(p); err == nil && fi {
			return i.SocketDir, port, true
		}
	}
	host = i.ListenAddr
	switch host {
	case "", "*", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	// listen_addr may be a comma list; the first entry is as good as any.
	if h, _, ok := cutComma(host); ok {
		host = h
	}
	return host, port, false
}

// TargetLabel renders the connect target for a table cell.
func (i PgBouncerInstance) TargetLabel() string {
	host, port, unix := i.Target()
	if unix {
		return filepath.Join(host, ".s.PGSQL."+strconv.Itoa(port))
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Key is the dedupe identity of an instance across discovery sources: the
// socket path when the ini names one, else the TCP endpoint, else the ini path.
func (i PgBouncerInstance) Key() string {
	if i.DSN != "" {
		return "dsn:" + i.DSN
	}
	if p := i.SocketPath(); p != "" {
		return "unix:" + p
	}
	if i.ListenAddr != "" || i.IniPath == "" {
		host, port, _ := i.Target()
		return "tcp:" + net.JoinHostPort(host, strconv.Itoa(port))
	}
	return "ini:" + i.IniPath
}

// PgBouncerPoolTotals aggregates SHOW POOLS across every pool except the
// console's own "pgbouncer" pseudo-database.
type PgBouncerPoolTotals struct {
	ClActive   int
	ClWaiting  int
	SvActive   int
	SvIdle     int
	Pools      int
	MaxWaitSec float64
}

// PgBouncerProbe is the cheap health read done for the instance list and the
// triage check: version plus pool totals, or the reason neither was available.
type PgBouncerProbe struct {
	Version string
	Totals  PgBouncerPoolTotals
	Err     error
	AuthErr bool   // the console rejected our login (as opposed to being unreachable)
	User    string // login user we tried, for the hint
}

// PgBouncerOverview backs the per-instance overview screen.
type PgBouncerOverview struct {
	Version string
	State   map[string]string // SHOW STATE (nil when the version lacks it)
	Lists   map[string]int64  // SHOW LISTS
	Totals  PgBouncerPoolTotals
}

func cutComma(s string) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
