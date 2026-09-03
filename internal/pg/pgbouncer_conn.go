package pg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgpassfile"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgbDialTimeout bounds one console connect. The console answers instantly or
// not at all (a wedged single-threaded pgbouncer), so a few seconds is plenty.
const pgbDialTimeout = 3 * time.Second

// pgbConn is the console connection to one instance. It is kept open for the
// life of the session: pgbouncer logs every console login and logout at LOG
// level, so redialing on each refresh tick would spam exactly the log the
// user then opens in the analyzer. Console connections hold no server slot.
type pgbConn struct {
	mu   sync.Mutex
	conn *pgx.Conn
}

// pgbShowAllowed is the set of SHOW commands the tool may issue. The console
// has no other read-only surface, and keeping this closed rules out any admin
// verb (PAUSE, KILL, …) ever reaching pgbQuery by accident.
var pgbShowAllowed = map[string]bool{
	"version": true, "state": true, "lists": true, "mem": true, "config": true,
	"databases": true, "users": true, "pools": true, "peer_pools": true, "peers": true,
	"stats": true, "stats_totals": true, "stats_averages": true,
	"clients": true, "servers": true, "sockets": true, "active_sockets": true,
	"dns_hosts": true, "dns_zones": true, "fds": true,
}

// PgBouncerUser is the console login: --pgbouncer-user, else pgdu's own user.
func (c *Client) PgBouncerUser() string { return c.pgbUser() }

func (c *Client) pgbUser() string {
	if u := c.cfg.PgBouncerUser; u != "" {
		return u
	}
	if c.cfg.User != "" {
		return c.cfg.User
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// pgbPassword resolves the console password the way libpq would, which pgx
// does not for unix sockets: pgx looks every socket up under host "localhost",
// whereas libpq — and therefore every hand-written or puppet-managed .pgpass on
// a pgbouncer host — keys the entry by the socket *directory*. Order:
// PGDU_PGBOUNCER_PASSWORD, PGPASSWORD, .pgpass by socket dir, .pgpass by
// localhost (the pgx convention), none.
func pgbPassword(host string, port int, unix bool, usr string) string {
	if pw := os.Getenv("PGDU_PGBOUNCER_PASSWORD"); pw != "" {
		return pw
	}
	if pw := os.Getenv("PGPASSWORD"); pw != "" {
		return pw
	}
	path := os.Getenv("PGPASSFILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = home + "/.pgpass"
	}
	pf, err := pgpassfile.ReadPassfile(path)
	if err != nil {
		return ""
	}
	p := strconv.Itoa(port)
	if pw := pf.FindPassword(host, p, "pgbouncer", usr); pw != "" {
		return pw
	}
	if unix {
		return pf.FindPassword("localhost", p, "pgbouncer", usr)
	}
	return ""
}

// pgbConfig builds the pgx config for inst's console.
func (c *Client) pgbConfig(inst PgBouncerInstance) (*pgx.ConnConfig, error) {
	var cfg *pgx.ConnConfig
	var err error
	if inst.DSN != "" {
		cfg, err = pgx.ParseConfig(inst.DSN)
	} else {
		host, port, unix := inst.Target()
		usr := c.pgbUser()
		parts := []string{
			"host=" + host,
			"port=" + strconv.Itoa(port),
			"user=" + usr,
			"dbname=pgbouncer",
			"application_name=pgdu",
		}
		if pw := pgbPassword(host, port, unix, usr); pw != "" {
			parts = append(parts, "password="+quoteConnValue(pw))
		}
		cfg, err = pgx.ParseConfig(strings.Join(parts, " "))
	}
	if err != nil {
		return nil, err
	}
	// The console speaks the simple query protocol only, and rejects the SET
	// our pool's AfterConnect issues — hence a raw conn, never PoolFor.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return cfg, nil
}

func quoteConnValue(v string) string {
	if !strings.ContainsAny(v, " \t'\\") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// pgbDial opens and sanity-checks a console connection: a real Postgres that
// happens to own a database named "pgbouncer" answers SHOW VERSION with a
// syntax error, so the check doubles as proxy detection.
func (c *Client) pgbDial(ctx context.Context, inst PgBouncerInstance) (*pgx.Conn, string, error) {
	cfg, err := c.pgbConfig(inst)
	if err != nil {
		return nil, "", err
	}
	dctx, cancel := context.WithTimeout(ctx, pgbDialTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(dctx, cfg)
	if err != nil {
		return nil, "", err
	}
	var version string
	if err := conn.QueryRow(dctx, "SHOW VERSION").Scan(&version); err != nil {
		_ = conn.Close(ctx)
		return nil, "", err
	}
	if !strings.Contains(version, "PgBouncer") {
		_ = conn.Close(ctx)
		return nil, "", fmt.Errorf("%s is not a pgbouncer console (SHOW VERSION: %q)", inst.TargetLabel(), version)
	}
	return conn, version, nil
}

// pgbAcquire returns the cached console conn for inst, dialing on first use.
// The returned pgbConn is locked; the caller must Unlock it.
func (c *Client) pgbAcquire(ctx context.Context, inst PgBouncerInstance) (*pgbConn, error) {
	key := inst.Key()
	c.pgbMu.Lock()
	pc, ok := c.pgbConns[key]
	if !ok {
		pc = &pgbConn{}
		c.pgbConns[key] = pc
	}
	c.pgbMu.Unlock()

	pc.mu.Lock()
	if pc.conn == nil || pc.conn.IsClosed() {
		conn, _, err := c.pgbDial(ctx, inst)
		if err != nil {
			pc.mu.Unlock()
			return nil, err
		}
		pc.conn = conn
	}
	return pc, nil
}

// pgbQuery runs one SHOW on inst's console and returns the generic table. A
// failed query drops the cached conn and retries once on a fresh one — a
// pgbouncer restart between two refresh ticks is the common case.
func (c *Client) pgbQuery(ctx context.Context, inst PgBouncerInstance, sql string) (*DiagResult, error) {
	var lastErr error
	for range 2 {
		pc, err := c.pgbAcquire(ctx, inst)
		if err != nil {
			return nil, fmt.Errorf("pgbouncer %s: %w", inst.Name, err)
		}
		res, err := pgbRun(ctx, pc.conn, sql)
		if err == nil {
			pc.mu.Unlock()
			return res, nil
		}
		lastErr = err
		// A server-side error (e.g. "admin access needed", unknown SHOW on an
		// older version) leaves the conn healthy: no point in redialing.
		if _, ok := errors.AsType[*pgconn.PgError](err); ok {
			pc.mu.Unlock()
			break
		}
		_ = pc.conn.Close(ctx)
		pc.conn = nil
		pc.mu.Unlock()
	}
	return nil, fmt.Errorf("pgbouncer %s: %s: %w", inst.Name, sql, lastErr)
}

func pgbRun(ctx context.Context, conn *pgx.Conn, sql string) (*DiagResult, error) {
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, out, _, err := scanDiagRows(rows, 0)
	if err != nil {
		return nil, err
	}
	return &DiagResult{Columns: cols, Rows: out, BarCol: -1, SortCol: -1}, nil
}

// PgBouncerShow runs `SHOW <what>` on inst. what must be one of the read-only
// SHOW commands (lower-case, e.g. "pools", "dns_hosts").
func (c *Client) PgBouncerShow(ctx context.Context, inst PgBouncerInstance, what string) (*DiagResult, error) {
	what = strings.ToLower(strings.TrimSpace(what))
	if !pgbShowAllowed[what] {
		return nil, fmt.Errorf("pgbouncer: SHOW %s is not supported", what)
	}
	return c.pgbQuery(ctx, inst, "SHOW "+strings.ToUpper(what))
}

// PgBouncerProbe is the cheap health read for the instance list and triage:
// version plus pool totals.
func (c *Client) PgBouncerProbe(ctx context.Context, inst PgBouncerInstance) PgBouncerProbe {
	pr := PgBouncerProbe{User: c.pgbUser()}
	ver, err := c.pgbQuery(ctx, inst, "SHOW VERSION")
	if err != nil {
		pr.Err = err
		pr.AuthErr = isPgbAuthErr(err)
		return pr
	}
	if len(ver.Rows) > 0 && len(ver.Rows[0]) > 0 {
		pr.Version = ver.Rows[0][0].Display
	}
	pools, err := c.pgbQuery(ctx, inst, "SHOW POOLS")
	if err != nil {
		pr.Err = err
		return pr
	}
	pr.Totals = poolTotals(pools)
	return pr
}

// PgBouncerOverview reads the header data for the instance screen. VERSION is
// required; STATE (1.19+), LISTS and POOLS are best-effort.
func (c *Client) PgBouncerOverview(ctx context.Context, inst PgBouncerInstance) (*PgBouncerOverview, error) {
	ver, err := c.pgbQuery(ctx, inst, "SHOW VERSION")
	if err != nil {
		return nil, err
	}
	ov := &PgBouncerOverview{}
	if len(ver.Rows) > 0 && len(ver.Rows[0]) > 0 {
		ov.Version = ver.Rows[0][0].Display
	}
	if st, err := c.pgbQuery(ctx, inst, "SHOW STATE"); err == nil {
		ov.State = map[string]string{}
		for _, r := range st.Rows {
			if len(r) >= 2 {
				ov.State[r[0].Display] = r[1].Display
			}
		}
	}
	if ls, err := c.pgbQuery(ctx, inst, "SHOW LISTS"); err == nil {
		ov.Lists = map[string]int64{}
		for _, r := range ls.Rows {
			if len(r) >= 2 && r[1].HasNum {
				ov.Lists[r[0].Display] = int64(r[1].Num)
			}
		}
	}
	if pools, err := c.pgbQuery(ctx, inst, "SHOW POOLS"); err == nil {
		ov.Totals = poolTotals(pools)
	}
	return ov, nil
}

// poolTotals sums SHOW POOLS, skipping the console's own "pgbouncer" pool.
// maxwait is whole seconds with the sub-second remainder in maxwait_us.
func poolTotals(res *DiagResult) PgBouncerPoolTotals {
	var t PgBouncerPoolTotals
	if res == nil {
		return t
	}
	dbCol := diagColIdx(res, "database")
	clA, clW := diagColIdx(res, "cl_active"), diagColIdx(res, "cl_waiting")
	svA, svI := diagColIdx(res, "sv_active"), diagColIdx(res, "sv_idle")
	mw, mwUS := diagColIdx(res, "maxwait"), diagColIdx(res, "maxwait_us")
	for _, row := range res.Rows {
		if dbCol >= 0 && dbCol < len(row) && row[dbCol].Display == "pgbouncer" {
			continue
		}
		t.Pools++
		n, _ := diagNum(row, clA)
		t.ClActive += int(n)
		n, _ = diagNum(row, clW)
		t.ClWaiting += int(n)
		n, _ = diagNum(row, svA)
		t.SvActive += int(n)
		n, _ = diagNum(row, svI)
		t.SvIdle += int(n)
		secs, _ := diagNum(row, mw)
		us, _ := diagNum(row, mwUS)
		if w := secs + us/1e6; w > t.MaxWaitSec {
			t.MaxWaitSec = w
		}
	}
	return t
}

// PgBouncerAuthHintApplies reports whether err is a rejected console login
// (as opposed to an unreachable console), i.e. whether PgBouncerAuthHint is the
// right thing to show next to it.
func PgBouncerAuthHintApplies(err error) bool { return err != nil && isPgbAuthErr(err) }

// isPgbAuthErr tells a rejected login from an unreachable console. pgbouncer
// reports both password and "user not in admin_users/stats_users" failures as
// SQLSTATE 28000/28P01 (older versions use plain text).
func isPgbAuthErr(err error) bool {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		if pgErr.Code == "28P01" || pgErr.Code == "28000" {
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "password") ||
		strings.Contains(msg, "no such user") ||
		strings.Contains(msg, "not allowed")
}

// PgBouncerAuthHint explains, for one instance, what a rejected console login
// needs — the read-only role is enough for everything this tool does.
func PgBouncerAuthHint(inst PgBouncerInstance, usr string) string {
	ini := inst.IniPath
	if ini == "" {
		ini = "pgbouncer.ini"
	}
	auth := inst.AuthFile
	if auth == "" {
		auth = "auth_file"
	}
	// Name the configured socket dir even when the socket is currently absent
	// (instance stopped): that is the host libpq and .pgpass key on.
	host := inst.SocketDir
	if host == "" {
		host, _, _ = inst.Target()
	}
	return fmt.Sprintf("%q must be in stats_users (read-only) or admin_users in %s (RELOAD after) and have a password in %s; "+
		"pgdu reads it from PGDU_PGBOUNCER_PASSWORD, PGPASSWORD or ~/.pgpass (\"%s:%d:pgbouncer:%s:…\"); --pgbouncer-user picks another login",
		usr, ini, auth, host, inst.Port(), usr)
}

// closePgBouncerConns is Close's share of the console connections.
func (c *Client) closePgBouncerConns() {
	c.pgbMu.Lock()
	defer c.pgbMu.Unlock()
	for _, pc := range c.pgbConns {
		pc.mu.Lock()
		if pc.conn != nil {
			_ = pc.conn.Close(context.Background())
			pc.conn = nil
		}
		pc.mu.Unlock()
	}
	c.pgbConns = map[string]*pgbConn{}
}
