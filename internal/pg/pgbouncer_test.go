package pg

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"pgdu/internal/cli"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParsePgBouncerIni(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "inc.ini"), "[pgbouncer]\nlogfile = /var/log/inc.log\nstats_users = nagios\n")
	writeFile(t, filepath.Join(dir, "main.ini"), `
; comment
# another
[databases]
shop = host=127.0.0.1 port=5432 dbname=shop
[pgbouncer]
%include inc.ini
listen_addr = *
listen_port = 6432
unix_socket_dir = '/var/run/pgbouncer_1'
admin_users = "root"
stats_users = nagios, postgres
auth_type = scram-sha-256
pool_mode=transaction
[users]
alice = pool_mode=session
`)
	ini, err := parsePgBouncerIni(filepath.Join(dir, "main.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if ini["listen_addr"] != "*" || ini.intOr("listen_port", 0) != 6432 {
		t.Errorf("listen: %q %q", ini["listen_addr"], ini["listen_port"])
	}
	if ini["unix_socket_dir"] != "/var/run/pgbouncer_1" || ini["admin_users"] != "root" {
		t.Errorf("quotes not stripped: %q %q", ini["unix_socket_dir"], ini["admin_users"])
	}
	if got := ini.list("stats_users"); len(got) != 2 || got[0] != "nagios" || got[1] != "postgres" {
		t.Errorf("stats_users = %v (later value must win over the include)", got)
	}
	if ini["logfile"] != "/var/log/inc.log" {
		t.Errorf("%%include not applied: logfile = %q", ini["logfile"])
	}
	if _, ok := ini["shop"]; ok {
		t.Errorf("[databases] key leaked into the pgbouncer section")
	}
	if _, ok := ini["alice"]; ok {
		t.Errorf("[users] key leaked into the pgbouncer section")
	}
	if ini["pool_mode"] != "transaction" {
		t.Errorf("pool_mode = %q", ini["pool_mode"])
	}
	if _, err := parsePgBouncerIni(filepath.Join(dir, "missing.ini")); err == nil {
		t.Errorf("missing file must error")
	}
}

func TestParsePgBouncerIniIncludeLoop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "loop.ini")
	writeFile(t, p, "[pgbouncer]\n%include loop.ini\n")
	if _, err := parsePgBouncerIni(p); err == nil {
		t.Errorf("self-including ini must fail, not recurse forever")
	}
}

func TestInstanceFromIniUnreadable(t *testing.T) {
	inst := PgBouncerInstance{PID: 42, Reason: "/proc"}
	instanceFromIni(&inst, "/nonexistent/pgbouncer_9.ini")
	if inst.IniErr == nil || inst.Name != "pgbouncer_9" || inst.PID != 42 {
		t.Errorf("unreadable ini must keep the /proc facts and set IniErr: %+v", inst)
	}
}

func TestPgbIniFromArgv(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"/usr/sbin/pgbouncer", "/etc/pgbouncer/pgbouncer_1.ini"}, "/etc/pgbouncer/pgbouncer_1.ini"},
		{[]string{"pgbouncer", "-d", "-u", "postgres", "/etc/x.ini"}, "/etc/x.ini"},
		{[]string{"pgbouncer", "-q", "-R", "/etc/y.ini"}, "/etc/y.ini"},
		{[]string{"pgbouncer", "-v"}, pgbDefaultIni},
		{[]string{"pgbouncer"}, pgbDefaultIni},
	}
	for _, c := range cases {
		if got := pgbIniFromArgv(c.argv); got != c.want {
			t.Errorf("pgbIniFromArgv(%v) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestParsePgBouncerTarget(t *testing.T) {
	dir := t.TempDir()
	// A socket dir is recognised by being a directory; the port comes from the
	// socket file inside it.
	l, err := net.Listen("unix", filepath.Join(dir, ".s.PGSQL.7432"))
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	defer func() { _ = l.Close() }()

	inst, ok := parsePgBouncerTarget(dir)
	if !ok || inst.SocketDir != dir || inst.Port() != 7432 {
		t.Errorf("socket dir target: ok=%v %+v", ok, inst)
	}
	host, port, unix := inst.Target()
	if !unix || host != dir || port != 7432 {
		t.Errorf("Target() should pick the existing socket: %q %d %v", host, port, unix)
	}
	if !strings.HasPrefix(inst.Key(), "unix:") {
		t.Errorf("Key = %q", inst.Key())
	}

	inst, ok = parsePgBouncerTarget("10.0.0.5:6433")
	if !ok || inst.ListenAddr != "10.0.0.5" || inst.ListenPort != 6433 || inst.Name != "10.0.0.5:6433" {
		t.Errorf("host:port target: ok=%v %+v", ok, inst)
	}
	inst, ok = parsePgBouncerTarget("pooler.example")
	if !ok || inst.ListenAddr != "pooler.example" || inst.Port() != pgbDefaultPort {
		t.Errorf("bare host target: ok=%v %+v", ok, inst)
	}
	if _, ok := parsePgBouncerTarget("  "); ok {
		t.Errorf("blank target accepted")
	}
}

func TestInstanceTargetFallsBackToTCP(t *testing.T) {
	inst := PgBouncerInstance{SocketDir: t.TempDir(), ListenAddr: "*", ListenPort: 6432}
	host, port, unix := inst.Target()
	if unix || host != "127.0.0.1" || port != 6432 {
		t.Errorf("no socket file → TCP on loopback, got %q %d unix=%v", host, port, unix)
	}
	inst.ListenAddr = "10.1.1.1,10.1.1.2"
	if host, _, _ = inst.Target(); host != "10.1.1.1" {
		t.Errorf("first listen_addr wanted, got %q", host)
	}
}

func TestDedupePgBouncers(t *testing.T) {
	// The same instance seen via --pgbouncer-target (first), /proc (with PID)
	// and the /etc glob must collapse to one record that keeps the explicit
	// reason and picks up the PID.
	found := []PgBouncerInstance{
		{Name: "b", SocketDir: "/run/b", ListenPort: 6432, Reason: "--pgbouncer-target", IniPath: "/etc/b.ini"},
		{Name: "a", SocketDir: "/run/a", ListenPort: 6432, Reason: "/proc", PID: 7, IniPath: "/etc/a.ini", Logfile: "/var/log/a.log"},
		{Name: "b", SocketDir: "/run/b", ListenPort: 6432, Reason: "/proc", PID: 9, IniPath: "/etc/b.ini"},
		{Name: "a", SocketDir: "/run/a", ListenPort: 6432, Reason: "/etc/pgbouncer", IniPath: "/etc/a.ini"},
	}
	out := dedupePgBouncers(found)
	if len(out) != 2 {
		t.Fatalf("got %d instances, want 2: %+v", len(out), out)
	}
	if out[0].Name != "a" || out[0].PID != 7 || out[0].Reason != "/proc" || out[0].Logfile != "/var/log/a.log" {
		t.Errorf("a: %+v", out[0])
	}
	if out[1].Name != "b" || out[1].PID != 9 || out[1].Reason != "--pgbouncer-target" {
		t.Errorf("b must keep the explicit reason and gain the pid: %+v", out[1])
	}
}

func TestPoolTotalsSkipsConsolePool(t *testing.T) {
	num := func(n float64) DiagCell { return DiagCell{Display: "x", Num: n, HasNum: true} }
	res := &DiagResult{
		Columns: []DiagColumn{{Name: "database"}, {Name: "cl_active"}, {Name: "cl_waiting"}, {Name: "sv_active"}, {Name: "sv_idle"}, {Name: "maxwait"}, {Name: "maxwait_us"}},
		Rows: [][]DiagCell{
			{{Display: "shop"}, num(10), num(2), num(5), num(1), num(1), num(500000)},
			{{Display: "shop"}, num(3), num(0), num(2), num(4), num(0), num(0)},
			{{Display: "pgbouncer"}, num(1), num(0), num(0), num(0), num(0), num(0)},
		},
	}
	tot := poolTotals(res)
	if tot.Pools != 2 || tot.ClActive != 13 || tot.ClWaiting != 2 || tot.SvActive != 7 || tot.SvIdle != 5 {
		t.Errorf("totals = %+v", tot)
	}
	if tot.MaxWaitSec != 1.5 {
		t.Errorf("maxwait = %v, want 1.5 (maxwait + maxwait_us/1e6)", tot.MaxWaitSec)
	}
	if poolTotals(nil).Pools != 0 {
		t.Errorf("nil result must yield zero totals")
	}
}

func TestIsPgbAuthErr(t *testing.T) {
	if !isPgbAuthErr(&pgconn.PgError{Code: "28P01", Message: "password authentication failed"}) {
		t.Errorf("28P01 is an auth error")
	}
	if !isPgbAuthErr(errors.New("FATAL: SASL authentication failed")) {
		t.Errorf("SASL failure text is an auth error")
	}
	if isPgbAuthErr(errors.New("dial unix /run/x: connect: no such file or directory")) {
		t.Errorf("a dial failure is not an auth error")
	}
}

func TestWorstPgBouncerProbe(t *testing.T) {
	insts := []PgBouncerInstance{{Name: "one"}, {Name: "two"}, {Name: "three"}}
	probes := []PgBouncerProbe{
		{Totals: PgBouncerPoolTotals{ClWaiting: 0}},
		{Totals: PgBouncerPoolTotals{ClWaiting: 4, MaxWaitSec: 12}},
		{Err: errors.New("down")},
	}
	sev, detail, err := worstPgBouncerProbe(insts, probes)
	if err != nil || sev != SevCrit || !strings.Contains(detail, "two") {
		t.Errorf("got %v %q %v, want crit on instance two", sev, detail, err)
	}
	sev, detail, err = worstPgBouncerProbe(insts[:1], probes[:1])
	if err != nil || sev != SevOK || !strings.Contains(detail, "1 instance") {
		t.Errorf("all quiet: %v %q %v", sev, detail, err)
	}
	authInst := PgBouncerInstance{Name: "x", IniPath: "/etc/x.ini"}
	_, _, err = worstPgBouncerProbe([]PgBouncerInstance{authInst}, []PgBouncerProbe{{Err: errors.New("auth"), AuthErr: true, User: "postgres"}})
	if err == nil || !strings.Contains(err.Error(), "stats_users") || !strings.Contains(err.Error(), "/etc/x.ini") {
		t.Errorf("all-unreachable with auth failure must carry the hint: %v", err)
	}
}

func TestPgbShowAllowlist(t *testing.T) {
	c := New(cli.Config{User: "x"})
	if _, err := c.PgBouncerShow(t.Context(), PgBouncerInstance{}, "pause"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("non-SHOW verbs must be rejected before any connection attempt: %v", err)
	}
}
