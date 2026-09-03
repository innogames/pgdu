package pg

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgbProc is one running pgbouncer as read from /proc.
type pgbProc struct {
	PID  int
	Argv []string
	Cwd  string
}

// pgbDefaultIni is what pgbouncer reads when started without a config path —
// which it never really is, but the argv parser needs a fallback.
const pgbDefaultIni = "/etc/pgbouncer/pgbouncer.ini"

// DiscoverPgBouncers lists every pgbouncer instance we can find, best-effort
// and deduped by Key(). Sources, in priority order (the first sighting owns the
// Reason; later ones only fill in blanks):
//
//  1. explicit --pgbouncer-target values (ini path, socket dir, or host:port);
//  2. running processes from /proc, each parsed from the ini on its command line;
//  3. /etc/pgbouncer/*.ini for configured-but-stopped instances;
//  4. pgdu's own connection when it evidently goes through a pooler.
//
// Nothing here fails: an unreadable ini leaves IniErr set, a missing /proc
// yields nothing from that source, and so on.
func (c *Client) DiscoverPgBouncers(ctx context.Context) []PgBouncerInstance {
	var found []PgBouncerInstance

	for _, t := range c.cfg.PgBouncerTargets {
		if inst, ok := parsePgBouncerTarget(t); ok {
			found = append(found, inst)
		}
	}

	for _, p := range scanPgBouncerProcs() {
		ini := pgbIniFromArgv(p.Argv)
		if !filepath.IsAbs(ini) && p.Cwd != "" {
			ini = filepath.Join(p.Cwd, ini)
		}
		inst := PgBouncerInstance{PID: p.PID, Reason: "/proc"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	for _, ini := range pgbIniGlob() {
		inst := PgBouncerInstance{Reason: "/etc/pgbouncer"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	if pool, err := c.PoolFor(ctx, c.DefaultDB()); err == nil && behindProxy(ctx, pool) {
		found = append(found, PgBouncerInstance{
			Name:   c.cfg.Target(),
			Reason: "connection target",
			DSN:    c.cfg.BuildDSN("pgbouncer"),
		})
	}

	return dedupePgBouncers(found)
}

// dedupePgBouncers merges sightings of the same instance (same Key) and sorts
// the result by name. The first sighting wins for Reason and for every field
// it set; later sightings only fill zero fields — so /proc's PID lands on the
// ini-derived record and an explicit target keeps its "--pgbouncer-target".
func dedupePgBouncers(found []PgBouncerInstance) []PgBouncerInstance {
	byKey := map[string]int{}
	var out []PgBouncerInstance
	for _, inst := range found {
		k := inst.Key()
		i, seen := byKey[k]
		if !seen {
			byKey[k] = len(out)
			out = append(out, inst)
			continue
		}
		cur := &out[i]
		if cur.PID == 0 {
			cur.PID = inst.PID
		}
		if cur.IniPath == "" {
			cur.IniPath = inst.IniPath
			cur.IniErr = inst.IniErr
		}
		if cur.Name == "" {
			cur.Name = inst.Name
		}
		if cur.Logfile == "" {
			cur.Logfile = inst.Logfile
		}
		if cur.SocketDir == "" && cur.DSN == "" {
			cur.SocketDir = inst.SocketDir
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// pgbIniFromArgv extracts the config path from a pgbouncer command line:
// the first non-option argument, skipping the value-taking -u/--user. Missing
// → pgbouncer's default path.
func pgbIniFromArgv(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-u" || a == "--user":
			i++
		case strings.HasPrefix(a, "-"):
			// -d -R -q -v -V -h and --long forms carry no ini.
		default:
			return a
		}
	}
	return pgbDefaultIni
}

// parsePgBouncerTarget interprets one --pgbouncer-target value: an ini file, a
// unix socket directory, or host[:port]. Unrecognisable input is dropped rather
// than surfaced — the instance list explains the accepted forms.
func parsePgBouncerTarget(t string) (PgBouncerInstance, bool) {
	t = strings.TrimSpace(t)
	if t == "" {
		return PgBouncerInstance{}, false
	}
	inst := PgBouncerInstance{Reason: "--pgbouncer-target"}
	if strings.HasSuffix(t, ".ini") {
		instanceFromIni(&inst, t)
		return inst, true
	}
	if fi, err := os.Stat(t); err == nil && fi.IsDir() {
		inst.SocketDir = t
		inst.Name = t
		// Prefer the port the socket file itself advertises: several instances
		// on one host differ only in their socket dir, not their port.
		if socks, _ := filepath.Glob(filepath.Join(t, ".s.PGSQL.*")); len(socks) > 0 {
			if n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(socks[0]), ".s.PGSQL.")); err == nil {
				inst.ListenPort = n
			}
		}
		return inst, true
	}
	host, port := t, pgbDefaultPort
	if h, p, err := net.SplitHostPort(t); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	if host == "" {
		return PgBouncerInstance{}, false
	}
	inst.ListenAddr = host
	inst.ListenPort = port
	inst.Name = net.JoinHostPort(host, strconv.Itoa(port))
	return inst, true
}

// behindProxy reports whether the connections in pool terminate somewhere
// other than the Postgres backend — i.e. a pooler sits in between. It compares
// the peer address of our own socket with the address the backend sees itself
// on (inet_server_addr/port): a direct connection has both equal (TCP) or both
// unix (NULL server side). Any mismatch means a proxy. Errors are treated as
// "direct" so discovery stays silent when in doubt — dialing dbname=pgbouncer
// against a real server logs `FATAL: database "pgbouncer" does not exist`,
// noise in exactly the log the analyzer reads.
func behindProxy(ctx context.Context, pool *pgxpool.Pool) bool {
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return false
	}
	defer pc.Release()

	var srvAddr, srvPort *string
	if err := pc.QueryRow(ctx,
		"SELECT host(inet_server_addr()), inet_server_port()::text").Scan(&srvAddr, &srvPort); err != nil {
		return false
	}
	remote := pc.Conn().PgConn().Conn().RemoteAddr()
	tcp, isTCP := remote.(*net.TCPAddr)
	if srvAddr == nil || srvPort == nil {
		// Backend accepted us on a unix socket. Direct if we opened one too;
		// a TCP client socket ending on a unix backend socket is a proxy.
		return isTCP
	}
	if !isTCP {
		return true
	}
	srvIP := net.ParseIP(*srvAddr)
	return srvIP == nil || !srvIP.Equal(tcp.IP) || *srvPort != strconv.Itoa(tcp.Port)
}
