package pgbouncer

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pgdu/internal/procfs"
)

// defaultIni is what pgbouncer reads when started without a config path —
// which it never really is, but the argv parser needs a fallback.
const defaultIni = "/etc/pgbouncer/pgbouncer.ini"

// Discover lists every pgbouncer instance we can find, best-effort
// and deduped by Key(). Sources, in priority order (the first sighting owns the
// Reason; later ones only fill in blanks):
//
//  1. explicit --pgbouncer-target values (ini path, socket dir, or host:port);
//  2. running processes from /proc, each parsed from the ini on its command line;
//  3. /etc/pgbouncer/*.ini for configured-but-stopped instances;
//  4. pgdu's own connection when viaPooler says it evidently goes through a
//     pooler (pg.Client judges that from the pool's socket addresses).
//
// Nothing here fails: an unreadable ini leaves IniErr set, a missing /proc
// yields nothing from that source, and so on.
func (c *Client) Discover(viaPooler bool) []Instance {
	var found []Instance

	for _, t := range c.cfg.PgBouncerTargets {
		if inst, ok := parseTarget(t); ok {
			found = append(found, inst)
		}
	}

	for _, p := range procfs.ListByComm("pgbouncer") {
		ini := iniFromArgv(p.Argv)
		if !filepath.IsAbs(ini) && p.Cwd != "" {
			ini = filepath.Join(p.Cwd, ini)
		}
		inst := Instance{PID: p.PID, Reason: "/proc"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	for _, ini := range iniGlob() {
		inst := Instance{Reason: "/etc/pgbouncer"}
		instanceFromIni(&inst, ini)
		found = append(found, inst)
	}

	if viaPooler {
		found = append(found, Instance{
			Name:   c.cfg.Target(),
			Reason: "connection target",
			DSN:    c.cfg.BuildDSN("pgbouncer"),
		})
	}

	return dedupe(found)
}

// dedupe merges sightings of the same instance (same Key) and sorts
// the result by name. The first sighting wins for Reason and for every field
// it set; later sightings only fill zero fields — so /proc's PID lands on the
// ini-derived record and an explicit target keeps its "--pgbouncer-target".
func dedupe(found []Instance) []Instance {
	byKey := map[string]int{}
	var out []Instance
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

// iniFromArgv extracts the config path from a pgbouncer command line:
// the first non-option argument, skipping the value-taking -u/--user. Missing
// → pgbouncer's default path.
func iniFromArgv(argv []string) string {
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
	return defaultIni
}

// parseTarget interprets one --pgbouncer-target value: an ini file, a
// unix socket directory, or host[:port]. Unrecognisable input is dropped rather
// than surfaced — the instance list explains the accepted forms.
func parseTarget(t string) (Instance, bool) {
	t = strings.TrimSpace(t)
	if t == "" {
		return Instance{}, false
	}
	inst := Instance{Reason: "--pgbouncer-target"}
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
	host, port := t, defaultPort
	if h, p, err := net.SplitHostPort(t); err == nil {
		host = h
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	if host == "" {
		return Instance{}, false
	}
	inst.ListenAddr = host
	inst.ListenPort = port
	inst.Name = net.JoinHostPort(host, strconv.Itoa(port))
	return inst, true
}

// iniGlob lists the packaged config directory for instances that are
// configured but not running (or whose /proc entries we could not read).
func iniGlob() []string {
	m, _ := filepath.Glob("/etc/pgbouncer/*.ini")
	return m
}
